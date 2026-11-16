package main

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"time"
)

type sourceFlags []string

func (s *sourceFlags) String() string     { return strings.Join(*s, ", ") }
func (s *sourceFlags) Set(v string) error { *s = append(*s, v); return nil }

type SourceEntry struct {
	Entry       TreeEntry
	Source      string
	Fingerprint fileFingerprint
}

func sourceFingerprint(st os.FileInfo) (fileFingerprint, error) {
	x, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return fileFingerprint{}, errors.New("source stat unavailable")
	}
	return fileFingerprint{uint64(x.Dev), x.Ino, st.Size(), x.Mtim.Sec, x.Mtim.Nsec, x.Ctim.Sec, x.Ctim.Nsec}, nil
}
func buildSourcePlan(inputs []string) ([]SourceEntry, []string, error) {
	return buildPlan(inputs, true)
}
func buildPlan(inputs []string, expand bool) ([]SourceEntry, []string, error) {
	unique := map[string]bool{}
	var roots []string
	for _, input := range inputs {
		matches := []string{input}
		if _, literalErr := os.Lstat(input); expand && literalErr != nil {
			var e error
			matches, e = filepath.Glob(input)
			if e != nil {
				return nil, nil, e
			}
		}
		if len(matches) == 0 {
			return nil, nil, fmt.Errorf("source pattern matched nothing: %s", input)
		}
		for _, p := range matches {
			a, e := filepath.Abs(p)
			if e != nil {
				return nil, nil, e
			}
			if !unique[a] {
				roots = append(roots, a)
				unique[a] = true
			}
		}
	}
	if len(roots) == 0 {
		return nil, nil, errors.New("at least one --source is required")
	}
	sort.Strings(roots)
	for _, p := range roots {
		for parent := filepath.Dir(p); parent != filepath.Dir(parent); parent = filepath.Dir(parent) {
			if unique[parent] {
				return nil, nil, fmt.Errorf("overlapping source roots: %s and %s", parent, p)
			}
		}
	}
	names := map[string]bool{}
	var entries []SourceEntry
	for _, p := range roots {
		base := filepath.Base(p)
		if !safeTreePath(base) || names[base] {
			return nil, nil, fmt.Errorf("duplicate or reserved destination basename: %s", base)
		}
		names[base] = true
		err := filepath.WalkDir(p, func(current string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			st, e := d.Info()
			if e != nil {
				return e
			}
			rel, e := filepath.Rel(p, current)
			if e != nil {
				return e
			}
			name := base
			if rel != "." {
				name = filepath.ToSlash(filepath.Join(base, rel))
			}
			if !safeTreePath(name) {
				return fmt.Errorf("unsafe input path: %q", name)
			}
			kind := "file"
			size := st.Size()
			if st.IsDir() {
				kind = "dir"
				size = 0
			} else if !st.Mode().IsRegular() {
				return fmt.Errorf("unsupported source %s: only regular files and directories; symlinks are not followed", current)
			}
			fp, e := sourceFingerprint(st)
			if e != nil {
				return e
			}
			entries = append(entries, SourceEntry{TreeEntry{Path: name, Kind: kind, Size: size, Mode: uint32(st.Mode().Perm()), Modified: st.ModTime().UnixNano()}, current, fp})
			if len(entries) > 100000 {
				return errors.New("maximum 100000 tree entries")
			}
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Entry.Path < entries[j].Entry.Path })
	for i := 1; i < len(entries); i++ {
		if entries[i-1].Entry.Path == entries[i].Entry.Path {
			return nil, nil, errors.New("overlapping source roots")
		}
	}
	return entries, roots, nil
}
func checkSourcePlan(plan []SourceEntry, roots []string) error {
	current, _, err := buildPlan(roots, false)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current, plan) {
		return errors.New("source files or directory membership changed; restore original inputs or create a new job")
	}
	return nil
}

const treeEndMarker = ".swarmcopy-stream-complete"

func streamSourcePlan(ctx context.Context, w io.Writer, plan []SourceEntry, roots []string) error {
	if err := checkSourcePlan(plan, roots); err != nil {
		return err
	}
	tw := tar.NewWriter(w)
	for _, s := range plan {
		if err := ctx.Err(); err != nil {
			return err
		}
		e := s.Entry
		h := &tar.Header{Name: e.Path, Mode: int64(e.Mode), ModTime: time.Unix(0, e.Modified), Size: e.Size, Typeflag: tar.TypeReg, Format: tar.FormatPAX}
		if e.Kind == "dir" {
			h.Typeflag = tar.TypeDir
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if e.Kind != "file" {
			continue
		}
		f, err := os.OpenFile(s.Source, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return err
		}
		st, err := f.Stat()
		if err == nil {
			var fp fileFingerprint
			fp, err = sourceFingerprint(st)
			if fp != s.Fingerprint {
				err = errors.New("source changed before streaming")
			}
		}
		if err == nil {
			_, err = io.CopyN(tw, f, e.Size)
		}
		after, se := f.Stat()
		ce := f.Close()
		if err != nil {
			return err
		}
		if se != nil {
			return se
		}
		if ce != nil {
			return ce
		}
		fp, err := sourceFingerprint(after)
		if err != nil {
			return err
		}
		if fp != s.Fingerprint {
			return fmt.Errorf("source changed while streaming: %s", s.Source)
		}
	}
	// The seed refuses publication unless the final snapshot check succeeded.
	if err := checkSourcePlan(plan, roots); err != nil {
		return err
	}
	b, _ := json.Marshal(len(plan))
	if err := tw.WriteHeader(&tar.Header{Name: treeEndMarker, Mode: 0600, Typeflag: tar.TypeReg, Size: int64(len(b))}); err != nil {
		return err
	}
	if _, err := tw.Write(b); err != nil {
		return err
	}
	return tw.Close()
}
func importTreeJob(ctx context.Context, r sshRunner, j *Job, binary string) error {
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() { e := streamSourcePlan(ctx, pw, j.Inputs, j.Sources); pw.CloseWithError(e); done <- e }()
	b, err := r.run(ctx, j.Seed, "stream files and directories once to seed", binary+" prepare-tree --dest "+shellQuote(j.Dest)+" --manifest "+shellQuote(j.SeedFile+".manifest.json")+" --expected-manifest "+shellQuote(j.remoteDir()+"/manifest.json"), pr)
	pr.Close()
	streamErr := <-done
	if err != nil || streamErr != nil {
		return errors.Join(err, streamErr)
	}
	fmt.Print(string(b))
	return nil
}
