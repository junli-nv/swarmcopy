package main

import (
	"archive/tar"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"hash"
	"io"
	"os"
	"path"
	"path/filepath"
	"syscall"
)

type chunkBuilder struct {
	algorithm  string
	size, used int64
	h          hash.Hash
	chunks     []string
}

func (b *chunkBuilder) Write(p []byte) (int, error) {
	count := len(p)
	for len(p) > 0 {
		if b.h == nil {
			b.h = newHash(b.algorithm)
		}
		n := min(int64(len(p)), b.size-b.used)
		b.h.Write(p[:n])
		p = p[n:]
		b.used += n
		if b.used == b.size {
			b.chunks = append(b.chunks, hex.EncodeToString(b.h.Sum(nil)))
			b.h = nil
			b.used = 0
		}
	}
	return count, nil
}
func (b *chunkBuilder) finish() []string {
	if b.used > 0 {
		b.chunks = append(b.chunks, hex.EncodeToString(b.h.Sum(nil)))
		b.used = 0
	}
	return b.chunks
}
func receiveTree(ctx context.Context, in io.Reader, dest, manifestPath string, chunkSize int64, expected ...Manifest) (Manifest, error) {
	m := Manifest{Version: 2, Name: "files", ChunkSize: chunkSize}
	if len(expected) > 0 {
		m.Version = expected[0].Version
		m.ChunkHash = expected[0].ChunkHash
		m.FileHash = expected[0].FileHash
	}
	if err := m.validateAlgorithms(); err != nil {
		return m, err
	}
	if err := os.MkdirAll(dest, 0755); err != nil {
		return m, err
	}
	lockfd, err := syscall.Open(filepath.Clean(dest)+".swarmcopy.lock", syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return m, err
	}
	defer syscall.Close(lockfd)
	if err = syscall.Flock(lockfd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return m, errors.New("destination is locked by another peer")
	}
	tmp, err := os.MkdirTemp(dest, ".swarmcopy-import-")
	if err != nil {
		return m, err
	}
	defer os.RemoveAll(tmp)
	r, err := os.OpenRoot(tmp)
	if err != nil {
		return m, err
	}
	defer r.Close()
	tr := tar.NewReader(in)
	chunks := &chunkBuilder{size: chunkSize, algorithm: m.chunkAlgorithm()}
	complete := false
	previous := ""
	for {
		if err = ctx.Err(); err != nil {
			return m, err
		}
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return m, e
		}
		if complete {
			return m, errors.New("unexpected data after tree completion marker")
		}
		if h.Name == treeEndMarker {
			if h.Size > 32 || h.Typeflag != tar.TypeReg {
				return m, errors.New("bad completion marker")
			}
			var count int
			if e = json.NewDecoder(tr).Decode(&count); e != nil || count != len(m.Entries) {
				return m, errors.New("bad entry count")
			}
			complete = true
			continue
		}
		if !safeTreePath(h.Name) || h.Name <= previous || len(m.Entries) >= 100000 || h.Size < 0 || h.Size > 1<<50 || m.Size > 1<<50-h.Size {
			return m, errors.New("unsafe or oversized source stream")
		}
		previous = h.Name
		e2 := TreeEntry{Path: h.Name, Mode: uint32(h.Mode) & 0777, Modified: h.ModTime.UnixNano(), Kind: "dir"}
		if e = strictDirs(r, path.Dir(h.Name)); e != nil {
			return m, e
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if h.Size != 0 {
				return m, errors.New("directory has payload")
			}
			if e = r.Mkdir(h.Name, 0700); e != nil {
				return m, e
			}
		case tar.TypeReg:
			e2.Kind = "file"
			e2.Size = h.Size
			f, e := r.OpenFile(h.Name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if e != nil {
				return m, e
			}
			whole := newHash(m.fileAlgorithm())
			_, e = io.CopyBuffer(io.MultiWriter(f, whole, chunks), tr, make([]byte, 256<<10))
			if e == nil {
				e = f.Sync()
			}
			ce := f.Close()
			if e != nil {
				return m, e
			}
			if ce != nil {
				return m, ce
			}
			setEntryDigest(&e2, m, hex.EncodeToString(whole.Sum(nil)))
			m.Size += h.Size
		default:
			return m, errors.New("symlinks, hardlink entries and special files are unsupported")
		}
		m.Entries = append(m.Entries, e2)
	}
	if !complete {
		return m, errors.New("incomplete source stream; no files published")
	}
	// Drain the SSH stream so a source writer failure cannot be silently discarded.
	if _, err = io.Copy(io.Discard, in); err != nil {
		return m, err
	}
	m.Chunks = chunks.finish()
	m.SHA256 = treeDigest(m.Entries)
	if err = m.validate(); err != nil {
		return m, err
	}
	if len(expected) > 0 && expected[0].id() != m.id() {
		return m, errors.New("imported tree does not match trusted controller manifest")
	}
	root, err := os.OpenRoot(dest)
	if err != nil {
		return m, err
	}
	defer root.Close()
	rel := ".swarmcopy-trees/" + m.id()
	if err = strictDirs(root, rel); err != nil {
		return m, err
	}
	if err = strictDirs(root, rel+"/data"); err != nil {
		return m, err
	}
	for _, e := range m.Entries {
		target := rel + "/data/" + e.Path
		if e.Kind == "dir" {
			if err = strictDirs(root, target); err != nil {
				return m, err
			}
			continue
		}
		if st, x := root.Lstat(target); x == nil && !st.Mode().IsRegular() {
			return m, errors.New("unsafe retained partial file")
		} else if x != nil && !os.IsNotExist(x) {
			return m, x
		}
		if err = root.Rename(filepath.Base(tmp)+"/"+e.Path, target); err != nil {
			return m, err
		}
		if err = syncRootDir(root, path.Dir(target)); err != nil {
			return m, err
		}
	}

	// On an interrupted import, never discard an already retained dataset.
	if err = saveManifest(manifestPath, m); os.IsExist(err) {
		old, e := loadManifest(manifestPath)
		if e != nil {
			return m, e
		}
		if old.id() != m.id() {
			return m, errors.New("seed manifest differs from source stream")
		}
		err = nil
	}
	return m, err
}
func prepareTreeCommand(args []string) error {
	fs := flag.NewFlagSet("prepare-tree", flag.ContinueOnError)
	dest := fs.String("dest", "", "destination directory")
	manifest := fs.String("manifest", "", "new manifest sidecar path")
	expectedPath := fs.String("expected-manifest", "", "trusted controller manifest to match before retaining imported data")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dest == "" || *manifest == "" || fs.NArg() != 0 {
		return errors.New("prepare-tree requires --dest and --manifest; input is the controller tree stream")
	}
	var expected []Manifest
	if *expectedPath != "" {
		m, e := loadManifest(*expectedPath)
		if e != nil {
			return e
		}
		expected = append(expected, m)
	}
	m, err := receiveTree(context.Background(), os.Stdin, *dest, *manifest, 16<<20, expected...)
	if err == nil {
		fmt.Printf("TREE IMPORTED files=%d entries=%d bytes=%d digest=%s\n", m.fileCount(), len(m.Entries), m.Size, m.SHA256)
	}
	return err
}
func (m Manifest) fileCount() int {
	if !m.isTree() {
		return 1
	}
	n := 0
	for _, e := range m.Entries {
		if e.Kind == "file" {
			n++
		}
	}
	return n
}
