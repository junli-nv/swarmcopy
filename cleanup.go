package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// Never delete an unexpected file from retained data, even for a completed job.
func emptyStaging(r *os.Root, name string) error {
	f, e := r.Open(name)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	entries, e := f.ReadDir(-1)
	f.Close()
	if e != nil {
		return e
	}
	for _, v := range entries {
		if !v.IsDir() || v.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("retained data at %s; kept for recovery", path.Join(name, v.Name()))
		}
		if e = emptyStaging(r, path.Join(name, v.Name())); e != nil {
			return e
		}
	}
	return nil
}
func completedTreeCleanup(jobRoot string, o Options, m Manifest) error {
	if filepath.Clean(o.File) != filepath.Dir(filepath.Dir(jobRoot)) {
		return errors.New("completed tree config has foreign destination")
	}
	r, e := os.OpenRoot(o.File)
	if e != nil {
		return e
	}
	defer r.Close()
	if e = strictDirs(r, ".swarmcopy"); e != nil {
		return e
	}
	shared, e := sharedPartial(jobRoot, m)
	if e != nil {
		return e
	}
	if shared {
		fmt.Println("KEEP SHARED STAGING:", m.id())
		return nil
	}
	rel, e := treeNamespace(r, m)
	if e != nil {
		return e
	}
	if st, x := r.Lstat(rel); x == nil {
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return errors.New("unsafe staging namespace")
		}
		if e = emptyStaging(r, rel+"/data"); e != nil {
			return e
		}
		f, x := r.Open(rel)
		if x != nil {
			return x
		}
		entries, x := f.ReadDir(-1)
		f.Close()
		if x != nil {
			return x
		}
		for _, v := range entries {
			if v.Name() != "data" && v.Name() != "complete" && v.Name() != "verified.json" {
				return fmt.Errorf("unknown staging entry %s; retained", v.Name())
			}
			if v.Type()&os.ModeSymlink != 0 {
				return errors.New("symlink in staging namespace")
			}
		}
		cache := loadTreeVerified(r, m)
		if e = saveTreeVerified(r, m, cache); e != nil {
			return e
		}
		if e = r.RemoveAll(rel); e != nil {
			return e
		}
	} else if !os.IsNotExist(x) {
		return x
	}
	// Remove only empty parents, never another job's namespace.
	for _, p := range []string{".swarmcopy/trees", ".swarmcopy-trees"} {
		if e = r.Remove(p); e != nil && !os.IsNotExist(e) && !errors.Is(e, syscall.ENOTEMPTY) && !errors.Is(e, syscall.EEXIST) {
			return e
		}
	}
	return syncRootDir(r, ".swarmcopy")
}
func archiveLastPeerLog(jobRoot string, o Options, m Manifest) error {
	dest := o.File
	if !m.isTree() {
		dest = filepath.Dir(dest)
	}
	r, e := os.OpenRoot(dest)
	if e != nil {
		return e
	}
	defer r.Close()
	if e = strictDirs(r, ".swarmcopy"); e != nil {
		return e
	}
	fd, e := syscall.Open(filepath.Join(jobRoot, "peer.log"), syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	var f *os.File
	if e == nil {
		f = os.NewFile(uintptr(fd), "peer.log")
	}
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return e
	}
	if !st.Mode().IsRegular() {
		return errors.New("peer log is not a regular file")
	}
	const limit = 8 << 20
	if st.Size() > limit {
		if _, e = f.Seek(st.Size()-limit, io.SeekStart); e != nil {
			return e
		}
	}
	tmp := ".swarmcopy/log.next-" + randomID()
	out, e := r.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer r.Remove(tmp)
	_, e = io.Copy(out, io.LimitReader(f, limit))
	if e == nil {
		e = out.Sync()
	}
	ce := out.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	if e = r.Rename(tmp, ".swarmcopy/last-peer.log"); e != nil {
		return e
	}
	return syncRootDir(r, ".swarmcopy")
}
func cleanupCompletedNodes(ctx context.Context, r sshRunner, j *Job, prepare bool) error {
	var manifest []byte
	var m Manifest
	var e error
	if prepare {
		manifest, e = os.ReadFile(filepath.Join(j.Dir, "manifest.json"))
		if e != nil {
			return e
		}
		m, e = loadManifest(filepath.Join(j.Dir, "manifest.json"))
		if e != nil {
			return e
		}
	}
	issues := map[string]string{}
	var mu sync.Mutex
	e = forHosts(ctx, j.Hosts, j.Fanout, func(c context.Context, h string) error {
		one, done := context.WithTimeout(c, nodeTimeout(*j))
		defer done()
		root := j.remoteDir()
		bin := shellQuote(filepath.Join(root, "swarmcopy"))
		config := shellQuote(filepath.Join(root, "peer.json"))
		var x error
		if prepare {
			cleaner := *j
			exe, _ := os.Executable()
			cleaner.BinDir = defaultBinaryDir(exe)
			x = r.bootstrap(one, cleaner, h)
			if x == nil {
				_, x = r.run(one, h, "stop owned peer before completed cleanup", "if [ -f "+config+" ]; then "+bin+" agent stop --config "+config+"; fi", nil)
			}
			if x == nil {
				x = r.write(one, h, filepath.Join(root, "manifest.json"), bytes.NewReader(manifest), false)
			}
			file := filepath.Join(j.Dest, m.Name)
			if h == j.Seed {
				file = j.SeedFile
			}
			if m.isTree() {
				file = j.Dest
			}
			o := Options{File: file, Manifest: filepath.Join(root, "manifest.json"), TokenFile: filepath.Join(root, "job.token")}
			b, _ := json.Marshal(o)
			if x == nil {
				x = r.write(one, h, filepath.Join(root, "peer.json"), bytes.NewReader(b), false)
			}
		}
		if x == nil {
			var b []byte
			b, x = r.run(one, h, "clean completed job deployment and empty staging", bin+" agent cleanup-complete --config "+config, nil)
			if len(b) > 0 {
				fmt.Printf("%s %s", h, b)
			}
		}
		if x != nil {
			mu.Lock()
			issues[h] = x.Error()
			mu.Unlock()
		}
		return nil
	})
	j.CleanupIssues = issues
	if e != nil {
		return e
	}
	if len(issues) > 0 {
		return fmt.Errorf("cleanup pending on %d nodes; retry cleanup --job %s", len(issues), j.ID)
	}
	j.Cleaned = time.Now().UTC().Format(time.RFC3339Nano)
	return nil
}
func cleanupCommand(args []string) error {
	fs := flag.NewFlagSet("cleanup", flag.ContinueOnError)
	configureUsage(fs, "cleanup")
	id := fs.String("job", "", "completed saved job ID or directory")
	ask := fs.Bool("ask-pass", false, "prompt for the shared SSH password")
	if len(args) == 0 {
		fs.Usage()
		return errors.New("missing completed job")
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
	if j.State != "complete" {
		return errors.New("cleanup requires a completed job; incomplete jobs retain recovery data")
	}
	if _, _, e = completedJobRows(j); e != nil {
		return e
	}
	fd, e := syscall.Open(filepath.Join(j.Dir, "controller.lock"), syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if e != nil {
		return e
	}
	defer syscall.Close(fd)
	if e = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return errors.New("controller is still active")
	}
	if j.Cleaned != "" && len(j.CleanupIssues) == 0 {
		fmt.Println("ALREADY CLEANED:", j.ID, "; no remote deployment needed")
		return nil
	}

	sig, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ctx, done := context.WithTimeout(sig, 10*time.Minute)
	defer done()
	env, finish, e := passwordEnvironment(ctx, *ask)
	if e != nil {
		return e
	}
	defer finish()
	r := sshRunner{env: env, user: j.SSHUser, password: *ask, cipher: j.SSHCipher, options: j.SSHOptions}
	e = cleanupCompletedNodes(ctx, r, &j, true)
	if x := j.save(); x != nil {
		return x
	}
	if e != nil {
		return e
	}
	fmt.Println("CLEANUP COMPLETE:", j.ID, "; media and controller job history preserved")
	return nil
}
