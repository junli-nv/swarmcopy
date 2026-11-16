package main

import (
	"bufio"
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
	"strings"
	"sync"
	"syscall"
)

type treeVerified map[string]fileFingerprint

const treeCacheName = ".swarmcopy/cache.json"

type treeCacheRecord struct {
	ManifestID string
	Files      treeVerified
}

func loadTreeVerified(r *os.Root, m Manifest) treeVerified {
	if f, e := r.Open(treeCacheName); e == nil {
		var saved treeCacheRecord
		e = json.NewDecoder(io.LimitReader(f, 32<<20)).Decode(&saved)
		f.Close()
		if e == nil && saved.ManifestID == m.id() && saved.Files != nil {
			return saved.Files
		}
	}
	// Read old cache in place; no bulk migration or copying of media/partials.
	for _, name := range []string{".swarmcopy-trees/" + m.id() + "/verified.json", ".swarmcopy/trees/" + m.id() + "/verified.json"} {
		f, e := r.Open(name)
		if e != nil {
			continue
		}
		var v treeVerified
		e = json.NewDecoder(io.LimitReader(f, 32<<20)).Decode(&v)
		f.Close()
		if e == nil && v != nil {
			return v
		}
	}
	return treeVerified{}
}
func saveTreeVerified(r *os.Root, m Manifest, v treeVerified) error {
	if len(v) == 0 {
		return nil
	}
	if e := strictDirs(r, ".swarmcopy"); e != nil {
		return e
	}
	b, e := json.Marshal(treeCacheRecord{m.id(), v})
	if e != nil {
		return e
	}
	tmp := ".swarmcopy/cache.next-" + randomID()
	f, e := r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	defer r.Remove(tmp)
	_, e = f.Write(b)
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	if e = r.Rename(tmp, treeCacheName); e != nil {
		return e
	}
	return syncRootDir(r, ".swarmcopy")
}
func cachedTreeFile(r *os.Root, e TreeEntry, cache treeVerified) (bool, error) {
	return cachedTreeFileContext(context.Background(), r, e, cache)
}
func cachedTreeFileContext(ctx context.Context, r *os.Root, e TreeEntry, cache treeVerified) (bool, error) {
	st, err := r.Lstat(e.Path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !st.Mode().IsRegular() {
		return false, fmt.Errorf("unsupported destination type at %s; move it aside explicitly", e.Path)
	}
	if st.Size() != e.Size {
		return false, nil
	}
	fp, ok := fingerprint(st)
	if old, exists := cache[e.Path]; exists && ok && old == fp {
		return true, nil
	}
	same, err := sameTreeFileContext(ctx, r, e)
	if same && ok {
		after, x := r.Lstat(e.Path)
		if x != nil {
			return false, x
		}
		next, valid := fingerprint(after)
		if !valid || next != fp {
			return false, errors.New("destination changed during verification")
		}
		cache[e.Path] = fp
	}
	return same, err
}

type treeInventory struct {
	Complete                         bool
	Files, ReusedFiles, MissingFiles int
	ReusedBytes                      int64
	Conflicts                        []string
}

func inspectTree(dest string, m Manifest, recheck bool) (treeInventory, error) {
	return inspectTreeContext(context.Background(), dest, m, recheck)
}
func inspectTreeContext(ctx context.Context, dest string, m Manifest, recheck bool) (treeInventory, error) {
	out := treeInventory{Complete: true, Files: m.fileCount(), Conflicts: []string{}}
	if err := os.MkdirAll(dest, 0755); err != nil {
		return out, err
	}
	r, err := os.OpenRoot(dest)
	if err != nil {
		return out, err
	}
	defer r.Close()
	cache := loadTreeVerified(r, m)
	if recheck {
		cache = treeVerified{}
	}
	for _, e := range m.Entries {
		if x := ctx.Err(); x != nil {
			return out, x
		}
		for p := path.Dir(e.Path); p != "."; p = path.Dir(p) {
			if st, x := r.Lstat(p); x == nil {
				if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
					return out, fmt.Errorf("unsafe destination ancestor: %s", p)
				}
			} else if !os.IsNotExist(x) {
				return out, x
			}
		}
		st, x := r.Lstat(e.Path)
		if x != nil && !os.IsNotExist(x) {
			return out, x
		}
		if e.Kind == "dir" {
			if x == nil && (!st.IsDir() || st.Mode()&os.ModeSymlink != 0) {
				return out, fmt.Errorf("directory type collision: %s", e.Path)
			}
			if x != nil {
				out.Complete = false
			}
			continue
		}
		same, x := cachedTreeFileContext(ctx, r, e, cache)
		if x != nil {
			return out, x
		}
		if same {
			out.ReusedFiles++
			out.ReusedBytes += e.Size
			continue
		}
		out.Complete = false
		if st == nil {
			out.MissingFiles++
		} else {
			out.Conflicts = append(out.Conflicts, e.Path)
		}
	}
	if err = saveTreeVerified(r, m, cache); err != nil {
		return out, err
	}
	return out, nil
}
func treeCheckCommand(args []string) error {
	fs := flag.NewFlagSet("tree-check", flag.ContinueOnError)
	manifest := fs.String("manifest", "", "trusted tree manifest")
	dest := fs.String("dest", "", "destination directory")
	recheck := fs.Bool("recheck", false, "bypass verified metadata cache")
	watchInput := fs.Bool("watch-stdin", false, "cancel inventory when the controller input stream closes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *manifest == "" || *dest == "" || fs.NArg() != 0 {
		return errors.New("tree-check requires --manifest and --dest")
	}
	m, err := loadManifest(*manifest)
	if err != nil {
		return err
	}
	if !m.isTree() {
		return errors.New("tree-check requires a v2 manifest")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	if *watchInput {
		go func() { io.Copy(io.Discard, os.Stdin); stop() }()
	}
	out, err := inspectTreeContext(ctx, *dest, m, *recheck)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(out)
}
func promptExisting(in io.Reader, out io.Writer) (string, error) {
	fmt.Fprintln(out, "Existing files differ from the source. Old files remain intact until verification succeeds.")
	fmt.Fprintln(out, "Choose: [r] resume/repair matching chunks, [p] replace after a fresh transfer, [c] cancel and keep existing files.")
	scanner := bufio.NewScanner(in)
	for {
		fmt.Fprint(out, "Conflict action [r/p/c]: ")
		if !scanner.Scan() {
			return "", errors.New("no conflict choice supplied; use --existing resume, replace or keep in scripts")
		}
		switch strings.ToLower(strings.TrimSpace(scanner.Text())) {
		case "r", "resume":
			return "resume", nil
		case "p", "replace":
			return "replace", nil
		case "c", "cancel", "keep":
			return "", errors.New("cancelled; existing files preserved")
		default:
			fmt.Fprintln(out, "Enter r, p or c.")
		}
	}
}
func preflightTree(ctx context.Context, r sshRunner, j *Job, m Manifest, manifest []byte, selected ...string) (bool, error) {
	var mu sync.Mutex
	anyComplete := false
	var conflicts int
	hosts := selected
	if len(hosts) == 0 {
		hosts = j.Hosts
	}
	_, err := nodeStage(ctx, j, hosts, "checking-existing", func(c context.Context, h string) error {
		mp := j.remoteDir() + "/manifest.json"
		if e := r.write(c, h, mp, bytes.NewReader(manifest), false); e != nil {
			return e
		}
		cmd := shellQuote(j.remoteDir()+"/swarmcopy") + " tree-check --manifest " + shellQuote(mp) + " --dest " + shellQuote(j.Dest) + " --watch-stdin"
		if j.Recheck {
			cmd += " --recheck"
		}
		b, e := r.run(c, h, "check existing files before distribution", cmd, keepaliveReader{c})
		if e != nil {
			return e
		}
		reportNode(c)
		var inv treeInventory
		if e = json.Unmarshal(b, &inv); e != nil {
			return e
		}
		mu.Lock()
		defer mu.Unlock()
		anyComplete = anyComplete || inv.Complete
		conflicts += len(inv.Conflicts)
		fmt.Printf("EXISTING %s: valid=%d missing=%d different=%d total=%d\n", h, inv.ReusedFiles, inv.MissingFiles, len(inv.Conflicts), inv.Files)
		for i, p := range inv.Conflicts {
			if i >= 10 {
				fmt.Printf("  ... %d more\n", len(inv.Conflicts)-i)
				break
			}
			fmt.Printf("  conflict: %s\n", p)
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	if conflicts > 0 {
		switch j.Existing {
		case "ask", "":
			tty, e := os.OpenFile("/dev/tty", os.O_RDWR, 0)
			if e != nil {
				return false, errors.New("destination conflicts need a choice; run interactively or specify --existing resume/replace/keep")
			}
			choice, e := promptExisting(tty, tty)
			tty.Close()
			if e != nil {
				return false, e
			}
			j.Existing = choice
			if e = j.save(); e != nil {
				return false, e
			}
		case "keep":
			return false, errors.New("cancelled by --existing keep; existing files preserved")
		}
	}
	return anyComplete, nil
}
