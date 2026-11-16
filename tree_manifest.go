package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path"
	"strings"
)

// Entry paths are relative to --dest. Only directories and regular files are supported.
type TreeEntry struct {
	Path     string
	Kind     string
	Size     int64
	Mode     uint32
	Modified int64
	SHA256   string `json:",omitempty"`
}

func safeTreePath(s string) bool {
	return s != "" && s != "." && len(s) <= 4096 && path.Clean(s) == s &&
		!path.IsAbs(s) && s != ".." && !strings.HasPrefix(s, "../") &&
		!strings.ContainsAny(s, "\\\x00") && !strings.HasPrefix(strings.Split(s, "/")[0], ".swarmcopy")
}
func treeDigest(entries []TreeEntry) string {
	b, _ := json.Marshal(entries)
	return digest(b)
}
func (m Manifest) validateTree() error {
	if m.Name != "files" || m.Size < 0 || m.Size > 1<<50 ||
		m.ChunkSize < 65536 || m.ChunkSize > 64<<20 ||
		len(m.Entries) == 0 || len(m.Entries) > 100000 ||
		int64(len(m.Chunks)) != (m.Size+m.ChunkSize-1)/m.ChunkSize || len(m.Chunks) > 1000000 {
		return errors.New("invalid tree manifest dimensions")
	}
	seen := map[string]string{}
	var total int64
	metadataBytes := int64(len(m.Chunks)) * 80
	previous := ""
	for _, e := range m.Entries {
		metadataBytes += int64(len(e.Path))*6 + 512
		if metadataBytes > 64<<20 {
			return errors.New("tree manifest metadata exceeds size limit")
		}
		if !safeTreePath(e.Path) || e.Path <= previous || e.Mode & ^uint32(0777) != 0 {
			return fmt.Errorf("unsafe, duplicate or unsorted tree entry %q", e.Path)
		}
		previous = e.Path
		if parent := path.Dir(e.Path); parent != "." && seen[parent] != "dir" {
			return fmt.Errorf("missing directory parent: %s", e.Path)
		}
		switch e.Kind {
		case "dir":
			if e.Size != 0 || e.SHA256 != "" {
				return errors.New("invalid directory entry")
			}
		case "file":
			if e.Size < 0 || e.Size > 1<<50 || total > 1<<50-e.Size {
				return errors.New("invalid file size")
			}
			b, err := hex.DecodeString(e.SHA256)
			if err != nil || len(b) != 32 {
				return errors.New("invalid file SHA256")
			}
			total += e.Size
		default:
			return errors.New("unsupported tree entry type")
		}
		seen[e.Path] = e.Kind
	}
	if total != m.Size || treeDigest(m.Entries) != m.SHA256 {
		return errors.New("tree digest or size mismatch")
	}
	for _, s := range m.Chunks {
		b, e := hex.DecodeString(s)
		if e != nil || len(b) != 32 {
			return errors.New("invalid chunk SHA256")
		}
	}
	return nil
}

// treeHash verifies per-file SHA256 while consuming the virtual concatenation once.
// Its final digest binds paths, sizes, permissions, timestamps and file digests.
type treeHash struct {
	entries   []TreeEntry
	index     int
	remaining int64
	current   hash.Hash
	err       error
	expected  string
}

func newContentHash(m Manifest) hash.Hash {
	if m.Version != 2 {
		return sha256.New()
	}
	h := &treeHash{entries: m.Entries, expected: m.SHA256}
	h.advance()
	return h
}
func (h *treeHash) advance() {
	for h.index < len(h.entries) {
		e := h.entries[h.index]
		if e.Kind != "file" {
			h.index++
			continue
		}
		h.current = sha256.New()
		h.remaining = e.Size
		if e.Size > 0 {
			return
		}
		if digest(nil) != e.SHA256 && h.err == nil {
			h.err = fmt.Errorf("empty-file SHA256 mismatch: %s", e.Path)
		}
		h.index++
	}
	h.current = nil
}
func (h *treeHash) Write(p []byte) (int, error) {
	size := len(p)
	for len(p) > 0 {
		if h.current == nil {
			if h.err == nil {
				h.err = errors.New("excess tree content")
			}
			break
		}
		n := min(int64(len(p)), h.remaining)
		h.current.Write(p[:n])
		p = p[n:]
		h.remaining -= n
		if h.remaining == 0 {
			if hex.EncodeToString(h.current.Sum(nil)) != h.entries[h.index].SHA256 && h.err == nil {
				h.err = fmt.Errorf("file SHA256 mismatch: %s", h.entries[h.index].Path)
			}
			h.index++
			h.advance()
		}
	}
	return size, nil
}
func (h *treeHash) Sum(b []byte) []byte {
	if h.err != nil || h.current != nil {
		return append(b, make([]byte, 32)...)
	}
	v, _ := hex.DecodeString(h.expected)
	return append(b, v...)
}
func (h *treeHash) Reset()       { h.index = 0; h.err = nil; h.advance() }
func (*treeHash) Size() int      { return 32 }
func (*treeHash) BlockSize() int { return 64 }
func contentHashError(h hash.Hash) error {
	if t, ok := h.(*treeHash); ok {
		if t.err != nil {
			return t.err
		}
		if t.current != nil {
			return io.ErrUnexpectedEOF
		}
	}
	return nil
}

type contentFile interface {
	io.ReaderAt
	io.WriterAt
	Sync() error
	Close() error
	Stat() (os.FileInfo, error)
	Chmod(os.FileMode) error
	Truncate(int64) error
	Name() string
}
