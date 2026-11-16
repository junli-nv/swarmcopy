package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func algorithmFixture(t *testing.T, ch, fh string) (Manifest, []byte, []SourceEntry, []string) {
	t.Helper()
	_, data, plan, roots := treeFixture(t)
	j := Job{Inputs: plan, Sources: roots, ChunkHash: ch, FileHash: fh, Dir: filepath.Join(t.TempDir(), "job")}
	m, e := sourceManifest(context.Background(), j)
	if e != nil {
		t.Fatal(e)
	}
	m.ChunkSize = 65536
	m.Chunks = nil
	for off := 0; off < len(data); off += 65536 {
		m.Chunks = append(m.Chunks, contentDigest(ch, data[off:min(off+65536, len(data))]))
	}
	if e = m.validate(); e != nil {
		t.Fatal(e)
	}
	return m, data, plan, roots
}
func assertAlgorithmTree(t *testing.T, m Manifest, plan []SourceEntry, dest string) {
	t.Helper()
	for _, s := range plan {
		st, e := os.Stat(filepath.Join(dest, s.Entry.Path))
		if e != nil {
			t.Fatal(e)
		}
		if s.Entry.Kind == "dir" {
			if !st.IsDir() {
				t.Fatal("directory missing")
			}
			continue
		}
		a, e := os.ReadFile(s.Source)
		if e != nil {
			t.Fatal(e)
		}
		b, e := os.ReadFile(filepath.Join(dest, s.Entry.Path))
		if e != nil {
			t.Fatal(e)
		}
		if !bytes.Equal(a, b) {
			t.Fatalf("content changed: %s", s.Entry.Path)
		}
	}
}
func TestHashKnownVectors(t *testing.T) {
	for name, want := range map[string]string{
		"sha256":   "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		"blake3":   "af1349b9f5f9a1a6a0404dea36dcc9499bcb25c9adc112b7cc9a93cae41f3262",
		"xxh3-128": "99aa06d3014798d86001c324468d497f",
	} {
		if got := contentDigest(name, nil); got != want {
			t.Fatalf("%s: %s != %s", name, got, want)
		}
	}
}
func TestHashModesAndMixedAlgorithmTransfers(t *testing.T) {
	for _, ch := range []string{"sha256", "blake3", "xxh3-128"} {
		for _, fh := range []string{"sha256", "blake3"} {
			for _, mode := range []string{"full", "chunks"} {
				t.Run(ch+"/"+fh+"/"+mode, func(t *testing.T) {
					m, data, plan, _ := algorithmFixture(t, ch, fh)
					if ch == "xxh3-128" && mode == "chunks" {
						if validateVerification(mode, m) == nil {
							t.Fatal("noncryptographic-only completion allowed")
						}
						return
					}
					if e := validateVerification(mode, m); e != nil {
						t.Fatal(e)
					}
					seed := treeNode(t, m, t.TempDir(), "keep")
					seed.f.WriteAt(data, 0)
					for i := range seed.have {
						seed.have[i] = true
					}
					srv := httptest.NewServer(http.HandlerFunc(seed.serve))
					defer srv.Close()
					dest := t.TempDir()
					n := treeNode(t, m, dest, "keep")
					n.o.Verify = mode
					if mode == "full" {
						startTestVerifier(t, n)
					}
					for i := len(n.have) - 1; i >= 0; i-- {
						if e := n.download(context.Background(), i, strings.TrimPrefix(srv.URL, "http://")); e != nil {
							t.Fatal(e)
						}
					}
					if e := n.finalize(); e != nil {
						t.Fatal(e)
					}
					assertAlgorithmTree(t, m, plan, dest)
					p := n.snapshot().Performance
					if mode == "chunks" && p.FileHashBytes != 0 {
						t.Fatal("chunks unexpectedly reread full files")
					}
					if mode == "full" && p.FileHashBytes != m.Size {
						t.Fatalf("wrong full verification bytes: %d", p.FileHashBytes)
					}
					raw, e := os.ReadFile(filepath.Join(treeStage(dest, m), "verified.json"))
					if e != nil {
						t.Fatal(e)
					}
					var cache treeVerified
					if e = json.Unmarshal(raw, &cache); e != nil {
						t.Fatal(e)
					}
					if mode == "chunks" && len(cache) != 0 {
						t.Fatal("chunks minted full-verification cache")
					}
				})
			}
		}
	}
}
func TestMixedWeakChunkCannotBypassStrongFileDigest(t *testing.T) {
	m, data, _, _ := algorithmFixture(t, "xxh3-128", "blake3")
	corrupted := append([]byte(nil), data...)
	corrupted[0] ^= 1
	// Even a chunk checksum matching corrupt data cannot satisfy the file digest.
	m.Chunks[0] = contentDigest("xxh3-128", corrupted[:m.length(0)])
	n := treeNode(t, m, t.TempDir(), "keep")
	n.f.WriteAt(corrupted, 0)
	for i := range n.have {
		n.have[i] = true
	}
	if e := n.finalize(); e == nil {
		t.Fatal("weak chunks bypassed cryptographic file integrity")
	}
	if _, e := os.Stat(filepath.Join(n.o.File, m.Entries[0].Path)); !os.IsNotExist(e) {
		t.Fatal("bad file published")
	}
}
func TestChunksDiskCorruptionCannotPoisonLaterFullCache(t *testing.T) {
	m, data, _, _ := algorithmFixture(t, "blake3", "blake3")
	dest := t.TempDir()
	n := treeNode(t, m, dest, "keep")
	n.o.Verify = "chunks"
	n.f.WriteAt(data, 0)
	for i := range n.have {
		n.have[i] = true
	}
	// Models corruption after a valid network chunk was checked and written.
	n.f.WriteAt([]byte("post-write damage"), 0)
	if e := n.finalize(); e != nil {
		t.Fatal(e)
	}
	inv, e := inspectTree(dest, m, false)
	if e != nil {
		t.Fatal(e)
	}
	if inv.Complete || len(inv.Conflicts) == 0 {
		t.Fatal("chunks completion poisoned full-file cache")
	}
}
func TestV3SeedStreamAndResume(t *testing.T) {
	for _, ch := range []string{"blake3", "xxh3-128"} {
		t.Run(ch, func(t *testing.T) {
			m, _, plan, roots := algorithmFixture(t, ch, "blake3")
			var stream bytes.Buffer
			if e := streamSourcePlan(context.Background(), &stream, plan, roots); e != nil {
				t.Fatal(e)
			}
			dest := t.TempDir()
			mp := filepath.Join(t.TempDir(), "manifest")
			got, e := receiveTree(context.Background(), &stream, dest, mp, m.ChunkSize, m)
			if e != nil {
				t.Fatal(e)
			}
			if got.id() != m.id() {
				t.Fatal("seed changed algorithm manifest")
			}
			n := treeNode(t, m, dest, "keep")
			if e = n.scan(); e != nil {
				t.Fatal(e)
			}
			for _, have := range n.have {
				if !have {
					t.Fatal("valid retained chunk lost")
				}
			}
			if e = n.finalize(); e != nil {
				t.Fatal(e)
			}
			assertAlgorithmTree(t, m, plan, dest)
		})
	}
}
func TestManifestAlgorithmConfusionRejected(t *testing.T) {
	m, _, _, _ := algorithmFixture(t, "blake3", "blake3")
	bad := m
	bad.ChunkHash = "unknown"
	if bad.validate() == nil {
		t.Fatal("unknown algorithm accepted")
	}
	bad = m
	bad.FileHash = "xxh3-128"
	if bad.validate() == nil {
		t.Fatal("weak final file digest accepted")
	}
	bad = m
	bad.Version = 2
	if bad.validate() == nil {
		t.Fatal("algorithm fields silently accepted as v2")
	}
	bad = m
	bad.Entries = append([]TreeEntry(nil), m.Entries...)
	bad.Entries[0].Digest = "sha256:" + strings.Repeat("0", 64)
	bad.SHA256 = treeDigest(bad.Entries)
	if bad.validate() == nil {
		t.Fatal("per-file/global algorithm conflict accepted")
	}
}
func TestV2TreeEncodingAndIdentityUnchanged(t *testing.T) {
	m, _, _, _ := treeFixture(t)
	type oldEntry struct {
		Path, Kind string
		Size       int64
		Mode       uint32
		Modified   int64
		SHA256     string `json:",omitempty"`
	}
	type oldManifest struct {
		Entries         []oldEntry `json:",omitempty"`
		Version         int
		Name            string
		Size, ChunkSize int64
		SHA256          string
		Chunks          []string
	}
	old := oldManifest{Version: m.Version, Name: m.Name, Size: m.Size, ChunkSize: m.ChunkSize, SHA256: m.SHA256, Chunks: m.Chunks}
	for _, e := range m.Entries {
		old.Entries = append(old.Entries, oldEntry{e.Path, e.Kind, e.Size, e.Mode, e.Modified, e.SHA256})
	}
	a, _ := json.Marshal(old)
	b, _ := json.Marshal(m)
	if !bytes.Equal(a, b) || digest(a) != m.id() {
		t.Fatal("default SHA256 tree wire identity changed")
	}
}
func TestFreshTreeSkipsOnlyUnwrittenRanges(t *testing.T) {
	m, data, _, _ := treeFixture(t)
	n := treeNode(t, m, t.TempDir(), "keep")
	s := n.f.(*treeStore)
	for i := range m.Chunks {
		if s.chunkMayExist(int64(i)*m.ChunkSize, m.length(i)) {
			t.Fatal("fresh sparse data marked retained")
		}
	}
	if _, e := n.f.WriteAt(data, 0); e != nil {
		t.Fatal(e)
	}
	if e := n.scan(); e != nil {
		t.Fatal(e)
	}
	for _, have := range n.have {
		if !have {
			t.Fatal("newly written content was skipped")
		}
	}
}
func TestSSHAdditionalOptionsOverrideAndRoundTrip(t *testing.T) {
	opts, e := mergeSSHOptions([]string{"ConnectTimeout=8", "LogLevel=quiet"}, []string{"connecttimeout=15", "ServerAliveInterval=30"})
	if e != nil {
		t.Fatal(e)
	}
	args := sshRunner{options: opts, cipher: "system"}.arguments("node", "true")
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "ConnectTimeout=8") || !strings.Contains(joined, "connecttimeout=15") || !strings.Contains(joined, "ServerAliveInterval=30") {
		t.Fatal(joined)
	}
	for _, bad := range []string{"NoEquals", "=bad", "Name=bad\noption"} {
		if _, e := mergeSSHOptions(nil, []string{bad}); e == nil {
			t.Fatal(bad)
		}
	}
	j := Job{ID: "opts", Hosts: []string{"node"}, Port: 19700, Dir: t.TempDir(), SSHOptions: opts}
	if e = j.save(); e != nil {
		t.Fatal(e)
	}
	got, e := loadJob(j.Dir)
	if e != nil || fmt.Sprint(got.SSHOptions) != fmt.Sprint(opts) {
		t.Fatal(got, e)
	}
}
func BenchmarkIntegritySourcePipeline(b *testing.B) {
	data := make([]byte, 1<<20)
	for i := range data {
		data[i] = byte(i * 31)
	}
	for _, pair := range [][2]string{{"sha256", "sha256"}, {"blake3", "blake3"}, {"xxh3-128", "blake3"}, {"xxh3-128", "sha256"}, {"sha256", "blake3"}, {"blake3", "sha256"}} {
		b.Run(pair[0]+"+"+pair[1], func(b *testing.B) {
			chunks := &chunkBuilder{size: 16 << 20, algorithm: pair[0]}
			whole := newHash(pair[1])
			w := io.MultiWriter(chunks, whole)
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, e := w.Write(data); e != nil {
					b.Fatal(e)
				}
			}
			b.StopTimer()
			whole.Sum(nil)
			chunks.finish()
		})
	}
}

func TestNewJobIntegrityPresets(t *testing.T) {
	cases := []struct {
		name        string
		job         Job
		chunk, file string
	}{
		{"default", Job{}, "sha256", "sha256"},
		{"full", Job{Verify: "full"}, "sha256", "sha256"},
		{"chunks", Job{Verify: "chunks"}, "blake3", "blake3"},
		{"explicit-chunk", Job{Verify: "chunks", ChunkHash: "sha256"}, "sha256", "blake3"},
		{"explicit-file", Job{Verify: "chunks", FileHash: "sha256"}, "blake3", "sha256"},
		{"mixed-full", Job{Verify: "full", ChunkHash: "xxh3-128", FileHash: "blake3"}, "xxh3-128", "blake3"},
		{"legacy-prepared", Job{Verify: "chunks", SeedFile: "/raid/images/seed"}, "sha256", "sha256"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			j := tc.job
			resolveNewJobIntegrity(&j)
			if j.ChunkHash != tc.chunk || j.FileHash != tc.file {
				t.Fatalf("unexpected preset: %+v", j)
			}
			if err := validateVerification(j.Verify, newSourceManifest(j)); err != nil {
				t.Fatal(err)
			}
			m, _, _, _ := algorithmFixture(t, j.ChunkHash, j.FileHash)
			if m.chunkAlgorithm() != tc.chunk || m.fileAlgorithm() != tc.file {
				t.Fatal("manifest did not bind preset algorithms")
			}
		})
	}
	j := Job{Verify: "chunks", ChunkHash: "xxh3-128"}
	resolveNewJobIntegrity(&j)
	if validateVerification(j.Verify, newSourceManifest(j)) == nil {
		t.Fatal("weak chunks accepted")
	}
}
