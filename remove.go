package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

func stopJobController(ctx context.Context, j Job) error {
	local, _ := os.Hostname()
	if j.ControllerHost != local || j.ControllerPID <= 0 || j.ControllerPID == os.Getpid() {
		return nil
	}
	ticks, e := startTicks(j.ControllerPID)
	if e != nil || ticks != j.ControllerStartTicks {
		return nil
	}
	// PID identity and its start time must both match the saved association.
	b, e := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", j.ControllerPID))
	if e != nil {
		return e
	}
	if !strings.Contains(string(b), "swarmcopy") {
		return errors.New("recorded controller executable is not swarmcopy")
	}
	if e = syscall.Kill(j.ControllerPID, syscall.SIGINT); e != nil {
		return e
	}
	until := time.NewTimer(10 * time.Second)
	defer until.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-until.C:
			return errors.New("controller did not stop; no forced kill performed")
		case <-tick.C:
			ticks, e = startTicks(j.ControllerPID)
			if e != nil || ticks != j.ControllerStartTicks {
				return nil
			}
		}
	}
}
func removeCommand(args []string) error {
	fs := flag.NewFlagSet("remove", flag.ContinueOnError)
	configureUsage(fs, "remove")
	id := fs.String("job", "", "saved job ID or directory")
	purge := fs.Bool("purge-partials", false, "also remove exclusive partial datasets; published media and source files are preserved")
	ask := fs.Bool("ask-pass", false, "prompt once for the shared SSH password")
	if len(args) == 0 {
		fs.Usage()
		return errors.New("missing job; see examples above")
	}
	if e := fs.Parse(args); e != nil {
		return e
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	j, e := loadJob(*id)
	if e != nil {
		return e
	}
	sig, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ctx, stop := context.WithTimeout(sig, 10*time.Minute)
	defer stop()
	if e = stopJobController(ctx, j); e != nil {
		return e
	}
	fd, e := syscall.Open(filepath.Join(j.Dir, "controller.lock"), syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if e != nil {
		return e
	}
	defer syscall.Close(fd)
	if e = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return errors.New("controller is still active; stop it before removing this job")
	}
	env, finish, e := passwordEnvironment(ctx, *ask)
	if e != nil {
		return e
	}
	defer finish()
	r := sshRunner{env: env, user: j.SSHUser, password: *ask, cipher: j.SSHCipher, options: j.SSHOptions}
	exe, e := os.Executable()
	if e != nil {
		return e
	}
	cleaner := j
	cleaner.BinDir = defaultBinaryDir(exe)
	var mu sync.Mutex
	var failures []error
	e = forHosts(ctx, j.Hosts, j.Fanout, func(c context.Context, h string) error {
		one, done := context.WithTimeout(c, max(time.Minute, nodeTimeout(j)))
		defer done()
		x := r.bootstrap(one, cleaner, h)
		if x == nil {
			root := j.remoteDir()
			bin := shellQuote(filepath.Join(root, "swarmcopy"))
			config := shellQuote(filepath.Join(root, "peer.json"))
			cmd := "if [ -f " + config + " ]; then " + bin + " agent stop --config " + config + "; fi"
			_, x = r.run(one, h, "stop only the job being removed", cmd, nil)
			if x == nil {
				cmd = bin + " agent cleanup --config " + config
				if *purge {
					cmd += " --purge-partials"
				}
				var out []byte
				out, x = r.run(one, h, "remove this job's metadata and optional exclusive partials", cmd, nil)
				if len(out) > 0 {
					fmt.Printf("%s %s", h, out)
				}
			}
		}
		if x != nil {
			mu.Lock()
			failures = append(failures, fmt.Errorf("%s: %w", h, x))
			mu.Unlock()
		}
		return nil
	})
	if e != nil {
		failures = append(failures, e)
	}
	if len(failures) > 0 {
		j.State = "cleanup-incomplete"
		j.LastError = errors.Join(failures...).Error()
		_ = j.save()
		return fmt.Errorf("cleanup incomplete; local record retained for retry: %w", errors.Join(failures...))
	}
	if e = os.RemoveAll(j.Dir); e != nil {
		return e
	}
	fmt.Printf("JOB REMOVED: %s; source and published files preserved\n", j.ID)
	return nil
}

func sharedPartial(root string, m Manifest) (bool, error) {
	dirs, e := os.ReadDir(filepath.Dir(root))
	if e != nil {
		return true, e
	}
	for _, d := range dirs {
		if !d.IsDir() || d.Name() == filepath.Base(root) {
			continue
		}
		p := filepath.Join(filepath.Dir(root), d.Name(), "manifest.json")
		other, e := loadManifest(p)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return true, fmt.Errorf("cannot inspect other job %s: %w", d.Name(), e)
		}
		if other.id() == m.id() {
			return true, nil
		}
	}
	return false, nil
}
func cleanupPeer(config string, purge bool) error { return cleanupPeerMode(config, purge, false) }
func cleanupCompletedPeer(config string) error    { return cleanupPeerMode(config, false, true) }
func cleanupPeerMode(config string, purge, completed bool) error {
	root := filepath.Clean(filepath.Dir(config))
	if filepath.Base(config) != "peer.json" || filepath.Base(filepath.Dir(root)) != ".swarmcopy" || filepath.Base(root) == "." {
		return errors.New("cleanup requires a job-specific .swarmcopy/ID/peer.json")
	}
	parent, e := os.Lstat(filepath.Dir(root))
	if e != nil && !os.IsNotExist(e) {
		return e
	}
	if e == nil && (!parent.IsDir() || parent.Mode()&os.ModeSymlink != 0) {
		return errors.New("unsafe job parent directory")
	}
	st, e := os.Lstat(root)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return errors.New("unsafe job directory")
	}
	fd, e := syscall.Open(filepath.Join(root, "agent.lock"), syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if e != nil {
		return e
	}
	defer syscall.Close(fd)
	if e = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return errors.New("agent operation still active")
	}
	var o Options
	b, e := os.ReadFile(config)
	if e == nil {
		if e = json.Unmarshal(b, &o); e != nil {
			return e
		}
		if filepath.Clean(o.TokenFile) != filepath.Join(root, "job.token") || filepath.Clean(o.Manifest) != filepath.Join(root, "manifest.json") {
			return errors.New("peer configuration does not belong to this job")
		}
		var p processRecord
		if b, x := os.ReadFile(filepath.Join(root, "process.json")); x == nil {
			if x = json.Unmarshal(b, &p); x != nil {
				return x
			}
			if ownedProcess(p, filepath.Join(root, "swarmcopy"), o) {
				return errors.New("owned peer is still running; stop it before cleanup")
			}
		}
		if completed {
			m, x := loadManifest(o.Manifest)
			if x != nil {
				return x
			}
			// Hold the exact same persistent lock as peer; never unlink a live lock.
			lock, e := syscall.Open(o.File+".swarmcopy.lock", syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
			if e == nil {
				defer syscall.Close(lock)
				if e = syscall.Flock(lock, syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
					return errors.New("destination peer still active; cleanup deferred")
				}
			} else if !os.IsNotExist(e) {
				return e
			}
			if m.isTree() {
				if x = completedTreeCleanup(root, o, m); x != nil {
					return x
				}
			}
			if x = archiveLastPeerLog(root, o, m); x != nil {
				return x
			}
		}
		if purge {
			m, x := loadManifest(o.Manifest)
			if x != nil {
				return x
			}
			shared, x := sharedPartial(root, m)
			if shared {
				fmt.Printf("KEEP SHARED PARTIALS: %s (%v)\n", m.id(), x)
			} else if m.isTree() {
				dest, x := os.OpenRoot(o.File)
				if x != nil {
					return x
				}
				rel, x := treeNamespace(dest, m)
				if x != nil {
					dest.Close()
					return x
				}
				info, x := dest.Lstat(rel)
				if x == nil && info.Mode()&os.ModeSymlink != 0 {
					dest.Close()
					return errors.New("unsafe partial directory")
				}
				if x == nil {
					x = dest.RemoveAll(rel)
				} else if os.IsNotExist(x) {
					x = nil
				}
				dest.Close()
				if x != nil {
					return x
				}
				fmt.Printf("REMOVED PARTIALS: %s\n", rel)
			} else {
				partial := o.File + "." + m.id()[:16] + ".part"
				if x = os.Remove(partial); x != nil && !os.IsNotExist(x) {
					return x
				}
				fmt.Printf("REMOVED PARTIAL: %s\n", partial)
			}
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	if e = os.RemoveAll(root); e != nil {
		return e
	}
	fmt.Printf("REMOVED JOB DIRECTORY: %s; published files preserved\n", root)
	return nil
}
