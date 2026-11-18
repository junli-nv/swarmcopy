package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"sync"
	"time"
)

type startupSpan struct {
	entry          TreeEntry
	offset         int64
	fp             fileFingerprint
	actualSize     int64
	exists, cached bool
}

// During startup only verified chunks are advertised. Reads use the unchanged
// originals until the ordinary writable store has finished conflict handling.
type startupTree struct {
	mu      sync.RWMutex
	root    *os.Root
	dest    string
	m       Manifest
	spans   []startupSpan
	ends    []int64
	backend *treeStore
	closed  bool
}

func openStartupTree(dest string, m Manifest, recheck bool) (*startupTree, error) {
	if err := m.validateTree(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dest, 0755); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dest)
	if err != nil {
		return nil, err
	}
	t := &startupTree{root: root, dest: dest, m: m}
	cache := loadTreeVerified(root, m)
	var offset int64
	for _, e := range m.Entries {
		for p := path.Dir(e.Path); p != "."; p = path.Dir(p) {
			st, x := root.Lstat(p)
			if x == nil && (!st.IsDir() || st.Mode()&os.ModeSymlink != 0) {
				root.Close()
				return nil, fmt.Errorf("unsafe destination ancestor: %s", p)
			}
			if x != nil && !os.IsNotExist(x) {
				root.Close()
				return nil, x
			}
		}
		st, x := root.Lstat(e.Path)
		if x != nil && !os.IsNotExist(x) {
			root.Close()
			return nil, x
		}
		if e.Kind == "dir" {
			if x == nil && (!st.IsDir() || st.Mode()&os.ModeSymlink != 0) {
				root.Close()
				return nil, fmt.Errorf("directory collision: %s", e.Path)
			}
			continue
		}
		s := startupSpan{entry: e, offset: offset}
		if x == nil {
			if !st.Mode().IsRegular() {
				root.Close()
				return nil, fmt.Errorf("unsupported destination type: %s", e.Path)
			}
			fp, ok := fingerprint(st)
			if !ok {
				root.Close()
				return nil, fmt.Errorf("destination fingerprint unavailable: %s", e.Path)
			}
			s.fp, s.exists, s.actualSize = fp, true, st.Size()
			old, found := cache[e.Path]
			s.cached = !recheck && found && old == fp && st.Size() == e.Size
		}
		offset += e.Size
		t.spans = append(t.spans, s)
		t.ends = append(t.ends, offset)
	}
	return t, nil
}

func (t *startupTree) Name() string            { return t.dest }
func (t *startupTree) Chmod(os.FileMode) error { return nil }
func (t *startupTree) Truncate(int64) error    { return nil }
func (t *startupTree) Stat() (os.FileInfo, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.closed {
		return nil, os.ErrClosed
	}
	return t.root.Stat(".")
}
func (t *startupTree) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	var err error
	if t.backend != nil {
		err = t.backend.Close()
	}
	return errors.Join(err, t.root.Close())
}
func (t *startupTree) store() *treeStore {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.backend
}
func (n *Node) treeStoreFile() *treeStore {
	switch f := n.f.(type) {
	case *treeStore:
		return f
	case *startupTree:
		return f.store()
	}
	return nil
}
func (t *startupTree) ReadAt(p []byte, off int64) (int, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.closed {
		return 0, os.ErrClosed
	}
	if t.backend != nil {
		return t.backend.ReadAt(p, off)
	}
	return t.readOriginal(p, off)
}
func (t *startupTree) readOriginal(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("negative startup offset")
	}
	total := 0
	for len(p) > 0 {
		i := sort.Search(len(t.ends), func(i int) bool { return t.ends[i] > off })
		if i == len(t.spans) {
			return total, io.EOF
		}
		s := t.spans[i]
		if !s.exists {
			return total, io.EOF
		}
		f, err := t.root.Open(s.entry.Path)
		if err != nil {
			return total, err
		}
		st, err := f.Stat()
		if err == nil {
			fp, ok := fingerprint(st)
			if !ok || !st.Mode().IsRegular() || fp != s.fp {
				err = fmt.Errorf("destination changed during startup: %s", s.entry.Path)
			}
		}
		if err != nil {
			f.Close()
			return total, err
		}
		want := int(min(int64(len(p)), s.offset+s.entry.Size-off))
		got, err := f.ReadAt(p[:want], off-s.offset)
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
		if got != want {
			return total, io.ErrUnexpectedEOF
		}
	}
	return total, nil
}
func (t *startupTree) WriteAt(p []byte, off int64) (int, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.closed {
		return 0, os.ErrClosed
	}
	if t.backend == nil {
		return 0, errors.New("local verification has not prepared writable storage")
	}
	return t.backend.WriteAt(p, off)
}
func (t *startupTree) Sync() error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.closed {
		return os.ErrClosed
	}
	if t.backend == nil {
		return errors.New("local verification has not prepared storage")
	}
	return t.backend.Sync()
}

type startupChunks struct {
	n             *Node
	buf           []byte
	index, filled int64
}

func (n *Node) startupHave(i int) {
	n.mu.Lock()
	n.have[i] = true
	n.lastProgress = time.Now()
	n.mu.Unlock()
}
func (c *startupChunks) feed(off int64, data []byte) {
	for len(data) > 0 {
		index, pos := off/c.n.m.ChunkSize, off%c.n.m.ChunkSize
		if index != c.index {
			c.index, c.filled = index, 0
		}
		want := min(int64(len(data)), c.n.m.length(int(index))-pos)
		if pos != c.filled {
			c.filled = -1
		}
		copy(c.buf[pos:pos+want], data[:want])
		if c.filled >= 0 {
			c.filled += want
		}
		if c.filled == c.n.m.length(int(index)) && contentDigest(c.n.m.chunkAlgorithm(), c.buf[:c.filled]) == c.n.m.Chunks[index] {
			c.n.startupHave(int(index))
		}
		off += want
		data = data[want:]
	}
}
func (t *startupTree) cachedChunks(ctx context.Context, c *startupChunks, s startupSpan) error {
	start, end := s.offset, s.offset+s.entry.Size
	first := (start + t.m.ChunkSize - 1) / t.m.ChunkSize
	last := end / t.m.ChunkSize
	for i := first; i < last; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		c.n.startupHave(int(i))
	}
	fragments := [][2]int64{}
	if start%t.m.ChunkSize != 0 {
		fragments = append(fragments, [2]int64{start, min(end, first*t.m.ChunkSize)})
	}
	tail := max(start, last*t.m.ChunkSize)
	if len(fragments) > 0 {
		tail = max(tail, fragments[0][1])
	}
	if tail < end {
		fragments = append(fragments, [2]int64{tail, end})
	}
	var largest int64
	for _, fragment := range fragments {
		largest = max(largest, fragment[1]-fragment[0])
	}
	if largest == 0 {
		return nil
	}
	buf := make([]byte, min(largest, t.m.ChunkSize, 1<<20))
	for _, fragment := range fragments {
		for off := fragment[0]; off < fragment[1]; {
			if err := ctx.Err(); err != nil {
				return err
			}
			want := min(int64(len(buf)), fragment[1]-off)
			if _, err := t.readOriginal(buf[:want], off); err != nil {
				return err
			}
			c.feed(off, buf[:want])
			off += want
		}
	}
	return nil
}

// Verify file digests and chunk digests in the same sequential read. Cache
// checkpoints contain only complete, fingerprint-stable file verifications.
func (t *startupTree) verifyOriginals(ctx context.Context, n *Node) error {
	return t.verifyOriginalsProgress(ctx, n, func(count int64) {
		n.mu.Lock()
		n.existingChecked += count
		n.lastProgress = time.Now()
		n.mu.Unlock()
	})
}
func (t *startupTree) totalBytes() int64 {
	var total int64
	for _, s := range t.spans {
		if s.exists {
			total += min(s.actualSize, s.entry.Size)
		}
	}
	return total
}
func (t *startupTree) verifyOriginalsProgress(ctx context.Context, n *Node, progress func(int64)) (err error) {
	chunks := &startupChunks{n: n, buf: make([]byte, t.m.ChunkSize), index: -1}
	buf := make([]byte, min(t.m.ChunkSize, 1<<20))
	cache := loadTreeVerified(t.root, t.m)
	dirty := false
	lastSave := time.Time{}
	checkpoint := func(force bool) error {
		if !dirty || !force && time.Since(lastSave) < time.Second {
			return nil
		}
		if e := saveTreeVerified(t.root, t.m, cache); e != nil {
			return e
		}
		dirty, lastSave = false, time.Now()
		return nil
	}
	defer func() {
		if e := checkpoint(true); err == nil {
			err = e
		}
	}()
	for _, s := range t.spans {
		if e := ctx.Err(); e != nil {
			return e
		}
		if !s.exists {
			continue
		}
		if s.cached {
			if e := t.cachedChunks(ctx, chunks, s); e != nil {
				return e
			}
			if progress != nil {
				progress(s.entry.Size)
			}
			continue
		}
		st, e := t.root.Lstat(s.entry.Path)
		if e != nil {
			return e
		}
		h := newHash(s.entry.fileAlgorithm())
		limit := min(st.Size(), s.entry.Size)
		for off := int64(0); off < limit; {
			if e := ctx.Err(); e != nil {
				return e
			}
			want := min(int64(len(buf)), limit-off)
			if _, e := t.readOriginal(buf[:want], s.offset+off); e != nil {
				return e
			}
			h.Write(buf[:want])
			// replace must not advertise data that will be discarded for a
			// conflicting file. It publishes after the whole file matches.
			if n.o.Existing != "replace" {
				chunks.feed(s.offset+off, buf[:want])
			}
			if progress != nil {
				progress(want)
			}
			off += want
		}
		after, e := t.root.Lstat(s.entry.Path)
		if e != nil {
			return e
		}
		fp, ok := fingerprint(after)
		if !ok || fp != s.fp {
			return fmt.Errorf("destination changed during verification: %s", s.entry.Path)
		}
		if st.Size() == s.entry.Size && hex.EncodeToString(h.Sum(nil)) == s.entry.fileDigest() {
			cache[s.entry.Path] = fp
			dirty = true
			if n.o.Existing == "replace" {
				if e := t.cachedChunks(ctx, chunks, s); e != nil {
					return e
				}
			}
			if e := checkpoint(false); e != nil {
				return e
			}
		}
	}
	for _, s := range t.spans {
		if !s.exists {
			continue
		}
		st, e := t.root.Lstat(s.entry.Path)
		if e != nil {
			return e
		}
		fp, ok := fingerprint(st)
		if !ok || fp != s.fp {
			return fmt.Errorf("destination changed during startup: %s", s.entry.Path)
		}
	}
	return ctx.Err()
}

func (t *startupTree) prepare(ctx context.Context, n *Node) error {
	if err := t.verifyOriginals(ctx, n); err != nil {
		return err
	}
	store, err := openTreeStoreContext(ctx, t.dest, t.m, n.o.Existing, false)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		store.Close()
		return err
	}
	t.mu.Lock()
	t.backend = store
	t.mu.Unlock()
	n.mu.Lock()
	n.origin = store.complete
	if n.origin {
		for i := range n.have {
			n.have[i] = true
		}
	}
	n.mu.Unlock()
	if !store.complete {
		return n.scanContext(ctx)
	}
	return nil
}
