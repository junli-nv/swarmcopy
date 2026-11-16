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
	"path"
	"strings"
	"sync"
)

type treeVerified map[string]fileFingerprint

func loadTreeVerified(r *os.Root, m Manifest) treeVerified {
	v := treeVerified{}
	f, e := r.Open(".swarmcopy-trees/" + m.id() + "/verified.json")
	if e != nil {
		return v
	}
	defer f.Close()
	if json.NewDecoder(io.LimitReader(f, 32<<20)).Decode(&v) != nil || v == nil {
		return treeVerified{}
	}
	return v
}
func saveTreeVerified(r *os.Root, m Manifest, v treeVerified) error {
	rel := ".swarmcopy-trees/" + m.id()
	if e := strictDirs(r, rel); e != nil {
		return e
	}
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	tmp := rel + "/verified.next-" + randomID()
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
	if e = r.Rename(tmp, rel+"/verified.json"); e != nil {
		return e
	}
	return syncRootDir(r, rel)
}
func cachedTreeFile(r *os.Root, e TreeEntry, cache treeVerified) (bool, error) {
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
	same, err := sameTreeFile(r, e)
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
		same, x := cachedTreeFile(r, e, cache)
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
	if m.Version != 2 {
		return errors.New("tree-check requires a v2 manifest")
	}
	out, err := inspectTree(*dest, m, *recheck)
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
func preflightTree(ctx context.Context, r sshRunner, j *Job, m Manifest, manifest []byte) (bool, error) {
	var mu sync.Mutex
	anyComplete := false
	var conflicts int
	err := forHosts(ctx, j.Hosts, j.Fanout, func(c context.Context, h string) error {
		mp := j.remoteDir() + "/manifest.json"
		if e := r.write(c, h, mp, bytes.NewReader(manifest), false); e != nil {
			return e
		}
		cmd := shellQuote(j.remoteDir()+"/swarmcopy") + " tree-check --manifest " + shellQuote(mp) + " --dest " + shellQuote(j.Dest)
		if j.Recheck {
			cmd += " --recheck"
		}
		b, e := r.run(c, h, "check existing files before distribution", cmd, nil)
		if e != nil {
			return e
		}
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
