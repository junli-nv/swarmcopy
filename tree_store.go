package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type treeSpan struct {
	available bool
	entry     TreeEntry
	offset    int64
	final     bool
	old       *fileFingerprint
}
type treeStore struct {
	mu              sync.RWMutex
	root, stage     *os.Root
	dest, stagePath string
	stageRel        string
	m               Manifest
	spans           []treeSpan
	starts          []int64
	complete        bool
}

func treeStage(dest string, m Manifest) string {
	legacy := filepath.Join(dest, ".swarmcopy-trees", m.id())
	if _, e := os.Lstat(legacy); e == nil {
		return legacy
	}
	return filepath.Join(dest, ".swarmcopy", "trees", m.id())
}
func treeNamespace(r *os.Root, m Manifest) (string, error) {
	legacy := ".swarmcopy-trees/" + m.id()
	if _, e := r.Lstat(legacy); e == nil {
		if e = strictDirs(r, legacy); e != nil {
			return "", e
		}
		return legacy, nil
	} else if !os.IsNotExist(e) {
		return "", e
	}
	return ".swarmcopy/trees/" + m.id(), nil
}
func strictDirs(r *os.Root, p string) error {
	if p == "." {
		return nil
	}
	parent := path.Dir(p)
	if err := strictDirs(r, parent); err != nil {
		return err
	}
	st, err := r.Lstat(p)
	if os.IsNotExist(err) {
		err = r.Mkdir(p, 0700)
		if err == nil {
			return nil
		}
		st, err = r.Lstat(p)
	}
	if err != nil {
		return err
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("not a real directory: %s", p)
	}
	return nil
}
func fileSHA(r *os.Root, p string) (string, error) { return fileSHAContext(context.Background(), r, p) }
func fileSHAContext(ctx context.Context, r *os.Root, p string) (string, error) {
	return fileHashContext(ctx, r, p, "sha256")
}
func fileHashContext(ctx context.Context, r *os.Root, p, algorithm string) (string, error) {
	f, err := r.OpenFile(p, os.O_RDONLY, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", errors.New("not a regular file")
	}
	h := newHash(algorithm)
	if _, err = io.CopyBuffer(h, contextReader{ctx, f}, make([]byte, 1<<20)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func sameTreeFile(r *os.Root, e TreeEntry) (bool, error) {
	return sameTreeFileContext(context.Background(), r, e)
}
func sameTreeFileContext(ctx context.Context, r *os.Root, e TreeEntry) (bool, error) {
	st, err := r.Lstat(e.Path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !st.Mode().IsRegular() {
		return false, fmt.Errorf("unsupported destination type: %s", e.Path)
	}
	if st.Size() != e.Size {
		return false, nil
	}
	sum, err := fileHashContext(ctx, r, e.Path, e.fileAlgorithm())
	return sum == e.fileDigest(), err
}
func openTreeStore(dest string, m Manifest, policy string, rechecks ...bool) (*treeStore, error) {
	return openTreeStoreContext(context.Background(), dest, m, policy, rechecks...)
}
func openTreeStoreContext(ctx context.Context, dest string, m Manifest, policy string, rechecks ...bool) (t *treeStore, err error) {
	if err = m.validateTree(); err != nil {
		return nil, err
	}
	if err = os.MkdirAll(dest, 0755); err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(dest)
	if err != nil {
		return nil, err
	}
	t = &treeStore{root: r, dest: dest, m: m, stagePath: treeStage(dest, m)}
	opened := t
	defer func() {
		if err != nil {
			opened.Close()
		}
	}()
	t.stageRel, err = treeNamespace(r, m)
	if err != nil {
		return nil, err
	}
	t.stagePath = filepath.Join(dest, filepath.FromSlash(t.stageRel))
	if err = strictDirs(r, t.stageRel+"/data"); err != nil {
		return nil, err
	}
	t.stage, err = r.OpenRoot(t.stageRel + "/data")
	if err != nil {
		return nil, err
	}
	cache := loadTreeVerified(r, m)
	if len(rechecks) > 0 && rechecks[0] {
		cache = treeVerified{}
	}
	all := true
	var offset int64
	for _, e := range m.Entries {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if e.Kind == "dir" {
			// Validate final ancestors without creating/changing final directories yet.
			if st, x := r.Lstat(e.Path); x == nil {
				if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
					return nil, fmt.Errorf("directory collision: %s", e.Path)
				}
			} else if !os.IsNotExist(x) {
				return nil, x
			} else {
				all = false
			}
			if err = strictDirs(t.stage, e.Path); err != nil {
				return nil, err
			}
			continue
		}
		for p := path.Dir(e.Path); p != "."; p = path.Dir(p) {
			if st, x := r.Lstat(p); x == nil {
				if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
					return nil, fmt.Errorf("unsafe destination ancestor: %s", p)
				}
			} else if !os.IsNotExist(x) {
				return nil, x
			}
		}
		s := treeSpan{entry: e, offset: offset, available: true}
		offset += e.Size
		var same bool
		same, err = cachedTreeFileContext(ctx, r, e, cache)
		if err != nil {
			return nil, err
		}
		if same {
			s.final = true
		} else {
			all = false
			if st, x := r.Lstat(e.Path); x == nil {
				if policy != "replace" && policy != "resume" {
					return nil, fmt.Errorf("%w: %s; choose --existing resume or replace", errExistingConflict, e.Path)
				}
				fp, _ := fingerprint(st)
				s.old = &fp
			} else if !os.IsNotExist(x) {
				return nil, x
			}
			if err = strictDirs(t.stage, path.Dir(e.Path)); err != nil {
				return nil, err
			}
			if st, x := t.stage.Lstat(e.Path); x == nil && !st.Mode().IsRegular() {
				return nil, fmt.Errorf("unsafe partial file: %s", e.Path)
			} else if x != nil && !os.IsNotExist(x) {
				return nil, x
			}
			linkedOld := false
			var linkedMode os.FileMode
			if st, x := t.stage.Lstat(e.Path); x == nil && s.old != nil {
				fp, ok := fingerprint(st)
				linkedOld = ok && fp.Device == s.old.Device && fp.Inode == s.old.Inode
				linkedMode = st.Mode()
			}
			if err = detachTreePartialContext(ctx, t.stage, e.Path); err != nil {
				return nil, err
			}
			if linkedOld {
				// Removing our own retained hardlink changes the old inode's ctime.
				st, x := r.Lstat(e.Path)
				if x != nil {
					return nil, x
				}
				fp, ok := fingerprint(st)
				compare := fp
				compare.ChangedSec = s.old.ChangedSec
				compare.ChangedNsec = s.old.ChangedNsec
				if !ok || compare != *s.old || st.Mode() != linkedMode {
					return nil, errors.New("old file changed while detaching retained hardlink")
				}
				s.old = &fp
			}
			if st, x := t.stage.Lstat(e.Path); x == nil && st.Mode().Perm()&0200 == 0 {
				if x = t.stage.Chmod(e.Path, 0600); x != nil {
					return nil, x
				}
			}
			f, x := t.stage.OpenFile(e.Path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
			fresh := x == nil
			if fresh && !(s.old != nil && policy == "resume") {
				s.available = false
			}
			if os.IsExist(x) {
				f, x = t.stage.OpenFile(e.Path, os.O_RDWR, 0)
			}
			if x != nil {
				return nil, x
			}
			st, x := f.Stat()
			if x == nil && !st.Mode().IsRegular() {
				x = errors.New("unsafe partial file")
			}
			if x == nil && fresh && s.old != nil && policy == "resume" {
				var old *os.File
				old, x = r.Open(e.Path)
				if x == nil {
					_, x = io.CopyBuffer(f, io.LimitReader(contextReader{ctx, old}, e.Size), make([]byte, 256<<10))
					old.Close()
				}
			}
			if x == nil {
				x = f.Truncate(e.Size)
			}
			ce := f.Close()
			if x != nil {
				return nil, x
			}
			if ce != nil {
				return nil, ce
			}
		}
		t.spans = append(t.spans, s)
		t.starts = append(t.starts, s.offset+e.Size)
	}
	if err = saveTreeVerified(r, m, cache); err != nil {
		return nil, err
	}
	t.complete = all
	return t, nil
}
func (t *treeStore) Name() string               { return t.dest }
func (t *treeStore) Stat() (os.FileInfo, error) { return t.root.Stat(".") }
func (t *treeStore) Truncate(int64) error       { return nil }
func (t *treeStore) Chmod(os.FileMode) error    { return nil }
func (t *treeStore) Close() error {
	var a, b error
	if t.stage != nil {
		a = t.stage.Close()
	}
	if t.root != nil {
		b = t.root.Close()
	}
	return errors.Join(a, b)
}
func (t *treeStore) ioAt(p []byte, off int64, write bool) (int, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if off < 0 {
		return 0, errors.New("negative tree offset")
	}
	total := 0
	for len(p) > 0 {
		i := sort.Search(len(t.spans), func(i int) bool { return t.starts[i] > off })
		if i == len(t.spans) {
			return total, io.EOF
		}
		s := t.spans[i]
		n := int(min(int64(len(p)), s.offset+s.entry.Size-off))
		r := t.stage
		flags := os.O_RDONLY
		if write {
			flags = os.O_RDWR
		}
		if s.final {
			r = t.root
			flags = os.O_RDONLY
		}
		f, err := r.OpenFile(s.entry.Path, flags, 0)
		if err != nil {
			return total, err
		}
		var got int
		if write && s.final {
			// Chunks can cross an already published file: never rewrite it.
			buf := make([]byte, n)
			got, err = f.ReadAt(buf, off-s.offset)
			if err == nil && !bytes.Equal(buf, p[:n]) {
				err = errors.New("published content changed")
			}
		} else if write {
			got, err = f.WriteAt(p[:n], off-s.offset)
		} else {
			got, err = f.ReadAt(p[:n], off-s.offset)
		}
		ce := f.Close()
		total += got
		off += int64(got)
		p = p[got:]
		if err != nil {
			return total, err
		}
		if ce != nil {
			return total, ce
		}
		if got != n {
			return total, io.ErrUnexpectedEOF
		}
	}
	return total, nil
}
func (t *treeStore) ReadAt(p []byte, off int64) (int, error) { return t.ioAt(p, off, false) }
func (t *treeStore) WriteAt(p []byte, off int64) (int, error) {
	t.mu.Lock()
	for i := sort.Search(len(t.spans), func(i int) bool { return t.starts[i] > off }); i < len(t.spans) && t.spans[i].offset < off+int64(len(p)); i++ {
		t.spans[i].available = true
	}
	t.mu.Unlock()
	return t.ioAt(p, off, true)
}
func (t *treeStore) Sync() error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, s := range t.spans {
		r := t.stage
		if s.final {
			continue
		}
		f, e := r.OpenFile(s.entry.Path, os.O_RDWR, 0)
		if e != nil {
			return e
		}
		e = f.Sync()
		ce := f.Close()
		if e != nil {
			return e
		}
		if ce != nil {
			return ce
		}
	}
	return nil
}
func syncRootDir(r *os.Root, p string) error {
	f, e := r.Open(p)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func (t *treeStore) Publish(ctx context.Context, fullVerification ...bool) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, e := range t.m.Entries {
		if e.Kind == "dir" {
			if err := strictDirs(t.root, e.Path); err != nil {
				return err
			}
		}
	}
	for i := range t.spans {
		if err := ctx.Err(); err != nil {
			return err
		}
		s := &t.spans[i]
		if s.final {
			continue
		}
		f, err := t.stage.OpenFile(s.entry.Path, os.O_RDWR, 0)
		if err != nil {
			return err
		}
		err = f.Chmod(os.FileMode(s.entry.Mode))
		tm := time.Unix(0, s.entry.Modified)
		if err == nil {
			err = t.stage.Chtimes(s.entry.Path, tm, tm)
		}
		if err == nil {
			err = f.Sync()
		}
		ce := f.Close()
		if err != nil {
			return err
		}
		if ce != nil {
			return ce
		}
		if s.old != nil {
			st, x := t.root.Lstat(s.entry.Path)
			if x != nil {
				return x
			}
			fp, ok := fingerprint(st)
			if !ok || fp != *s.old {
				return fmt.Errorf("destination changed since conflict check: %s", s.entry.Path)
			}
			err = t.root.Rename(t.stageRel+"/data/"+s.entry.Path, s.entry.Path)
		} else {
			// Link publishes without replacing a concurrently created destination.
			err = t.root.Link(t.stageRel+"/data/"+s.entry.Path, s.entry.Path)
			if err == nil {
				err = t.stage.Remove(s.entry.Path)
			}
		}
		if err != nil {
			return err
		}
		s.final = true
		if err = syncRootDir(t.root, path.Dir(s.entry.Path)); err != nil {
			return err
		}
	}
	for i := len(t.m.Entries) - 1; i >= 0; i-- {
		e := t.m.Entries[i]
		if e.Kind != "dir" {
			continue
		}
		tm := time.Unix(0, e.Modified)
		if err := t.root.Chtimes(e.Path, tm, tm); err != nil {
			return err
		}
		if err := t.root.Chmod(e.Path, os.FileMode(e.Mode)); err != nil {
			return err
		}
		if err := syncRootDir(t.root, e.Path); err != nil {
			return err
		}
	}
	b, _ := json.Marshal(map[string]string{"ManifestID": t.m.id()})
	marker := t.stageRel + "/complete"
	f, err := t.root.OpenFile(marker, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err != nil {
		return err
	}
	if ce != nil {
		return ce
	}
	if err = syncRootDir(t.root, path.Dir(marker)); err != nil {
		return err
	}
	if len(fullVerification) > 0 && !fullVerification[0] {
		t.complete = true
		return nil
	}
	cache := treeVerified{}
	for _, s := range t.spans {
		st, e := t.root.Lstat(s.entry.Path)
		if e != nil {
			return e
		}
		fp, ok := fingerprint(st)
		if ok {
			cache[s.entry.Path] = fp
		}
	}
	if err = saveTreeVerified(t.root, t.m, cache); err != nil {
		return err
	}
	t.complete = true
	return nil
}

func (t *treeStore) chunkMayExist(off, size int64) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for i := sort.Search(len(t.spans), func(i int) bool { return t.starts[i] > off }); i < len(t.spans) && t.spans[i].offset < off+size; i++ {
		span := t.spans[i]
		if span.entry.Size == 0 {
			continue
		}
		if !span.available {
			return false
		}
		// A previously created sparse partial may still contain no received bytes.
		// SEEK_DATA proves holes without reading/hashing gigabytes of zeros.
		if !span.final {
			f, e := t.stage.OpenFile(span.entry.Path, os.O_RDONLY, 0)
			if e == nil {
				start := max(off, span.offset) - span.offset
				end := min(off+size, span.offset+span.entry.Size) - span.offset
				data, x := unix.Seek(int(f.Fd()), start, unix.SEEK_DATA)
				f.Close()
				if errors.Is(x, unix.ENXIO) || x == nil && data >= end {
					return false
				}
				// Unsupported filesystems fall back to the ordinary content scan.
			}
		}
	}
	return true
}
