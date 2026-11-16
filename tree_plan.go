package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

func sourceManifest(ctx context.Context, j Job) (Manifest, error) {
	m := Manifest{Version: 2, Name: "files", ChunkSize: 16 << 20}
	if err := checkSourcePlan(j.Inputs, j.Sources); err != nil {
		return m, err
	}
	key, _ := json.Marshal(j.Inputs)
	cachePath := filepath.Join(filepath.Dir(j.Dir), ".source-manifests", digest(key)+".json")
	if !j.Recheck {
		if cached, err := loadManifest(cachePath); err == nil && cached.Version == 2 && len(cached.Entries) == len(j.Inputs) {
			valid := true
			for i, s := range j.Inputs {
				e := cached.Entries[i]
				e.SHA256 = ""
				if e != s.Entry {
					valid = false
					break
				}
			}
			if valid {
				fmt.Printf("SOURCE verified metadata cache accepted; files=%d bytes=%d\n", cached.fileCount(), cached.Size)
				return cached, nil
			}
		}
	}
	chunks := &chunkBuilder{size: m.ChunkSize}
	buf := make([]byte, 1<<20)
	for _, s := range j.Inputs {
		if err := ctx.Err(); err != nil {
			return m, err
		}
		e := s.Entry
		if e.Kind == "file" {
			f, err := os.OpenFile(s.Source, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
			if err != nil {
				return m, err
			}
			st, err := f.Stat()
			if err == nil {
				fp, x := sourceFingerprint(st)
				if x != nil {
					err = x
				} else if fp != s.Fingerprint {
					err = errors.New("source changed before hashing")
				}
			}
			h := sha256.New()
			if err == nil {
				var count int64
				count, err = io.CopyBuffer(io.MultiWriter(h, chunks), f, buf)
				if err == nil && count != e.Size {
					err = io.ErrUnexpectedEOF
				}
			}
			after, se := f.Stat()
			ce := f.Close()
			if err != nil {
				return m, err
			}
			if se != nil {
				return m, se
			}
			if ce != nil {
				return m, ce
			}
			fp, err := sourceFingerprint(after)
			if err != nil {
				return m, err
			}
			if fp != s.Fingerprint {
				return m, fmt.Errorf("source changed while hashing: %s", s.Source)
			}
			e.SHA256 = hex.EncodeToString(h.Sum(nil))
			m.Size += e.Size
		}
		m.Entries = append(m.Entries, e)
	}
	if err := checkSourcePlan(j.Inputs, j.Sources); err != nil {
		return m, err
	}
	m.Chunks = chunks.finish()
	m.SHA256 = treeDigest(m.Entries)
	if err := m.validate(); err != nil {
		return m, err
	}
	if err := os.MkdirAll(filepath.Dir(cachePath), 0700); err == nil {
		// Another job may have cached the same immutable source concurrently.
		b, _ := json.Marshal(m)
		_ = atomicWrite(cachePath, b, 0600)
	}
	fmt.Printf("SOURCE hashed files=%d entries=%d bytes=%d tree-sha256=%s\n", m.fileCount(), len(m.Entries), m.Size, m.SHA256)
	return m, nil
}
