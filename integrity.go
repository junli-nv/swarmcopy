package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"strings"

	"github.com/zeebo/xxh3"
	"lukechampine.com/blake3"
)

func hashName(s string) string {
	if s == "" {
		return "sha256"
	}
	return s
}
func verifyMode(s string) string {
	if s == "" {
		return "full"
	}
	return s
}
func cryptoHash(s string) bool { s = hashName(s); return s == "sha256" || s == "blake3" }
func validHash(s string) bool  { return cryptoHash(s) || s == "xxh3-128" }
func newHash(name string) hash.Hash {
	switch hashName(name) {
	case "sha256":
		return sha256.New()
	case "blake3":
		return blake3.New(32, nil)
	case "xxh3-128":
		return xxh3.New128()
	default:
		panic("unsupported validated hash algorithm: " + name)
	}
}
func contentDigest(name string, b []byte) string {
	h := newHash(name)
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}
func validDigest(name, s string) bool {
	size := 32
	if name == "xxh3-128" {
		size = 16
	}
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == size && s == strings.ToLower(s)
}
func (m Manifest) isTree() bool           { return m.Version == 2 || m.Version == 3 }
func (m Manifest) chunkAlgorithm() string { return hashName(m.ChunkHash) }
func (m Manifest) fileAlgorithm() string  { return hashName(m.FileHash) }
func (e TreeEntry) fileDigest() string {
	if e.Digest != "" {
		_, sum, _ := strings.Cut(e.Digest, ":")
		return sum
	}
	return e.SHA256
}
func (m Manifest) validateAlgorithms() error {
	if !validHash(m.chunkAlgorithm()) || !cryptoHash(m.fileAlgorithm()) {
		return errors.New("chunk hash must be sha256/blake3/xxh3-128; file hash must be sha256/blake3")
	}
	if m.Version < 3 && (m.ChunkHash != "" || m.FileHash != "") {
		return errors.New("algorithm fields require manifest v3")
	}
	if m.Version == 3 && (m.ChunkHash == "" || m.FileHash == "") {
		return errors.New("v3 must explicitly name chunk/file algorithms")
	}
	return nil
}
func validateVerification(mode string, m Manifest) error {
	if mode = verifyMode(mode); mode != "full" && mode != "chunks" {
		return errors.New("verify must be full or chunks")
	}
	if mode == "chunks" && !cryptoHash(m.chunkAlgorithm()) {
		return errors.New("chunks mode requires SHA-256 or BLAKE3 chunks; XXH3-128 requires full cryptographic file verification")
	}
	return nil
}
func newSourceManifest(j Job) Manifest {
	m := Manifest{Version: 2, Name: "files", ChunkSize: 16 << 20}
	if hashName(j.ChunkHash) != "sha256" || hashName(j.FileHash) != "sha256" {
		m.Version = 3
		m.ChunkHash = hashName(j.ChunkHash)
		m.FileHash = hashName(j.FileHash)
	}
	return m
}
func setEntryDigest(e *TreeEntry, m Manifest, sum string) {
	if m.Version == 3 {
		e.Digest = m.fileAlgorithm() + ":" + sum
		e.SHA256 = ""
	} else {
		e.SHA256 = sum
		e.Digest = ""
	}
}
func integrityDescription(mode string, m Manifest) string {
	return fmt.Sprintf("verify=%s chunk-hash=%s file-hash=%s", verifyMode(mode), m.chunkAlgorithm(), m.fileAlgorithm())
}

func (e TreeEntry) fileAlgorithm() string {
	if e.Digest != "" {
		name, _, _ := strings.Cut(e.Digest, ":")
		return name
	}
	return "sha256"
}

const defaultNewJobVerify = "chunks"

// Resolve presets only for new jobs; saved jobs and prepared manifests keep their algorithms.
func resolveNewJobIntegrity(j *Job) {
	if j.Verify == "" {
		j.Verify = defaultNewJobVerify
	}
	algorithm := "sha256"
	if j.Verify == "chunks" && j.SeedFile == "" {
		algorithm = "blake3"
	}
	if j.ChunkHash == "" {
		j.ChunkHash = algorithm
	}
	if j.FileHash == "" {
		j.FileHash = algorithm
	}
}
