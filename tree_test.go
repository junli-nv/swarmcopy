package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func treeFixture(t *testing.T) (Manifest, []byte, []SourceEntry, []string) {
	t.Helper()
	dir := t.TempDir()
	paths := []string{"a.bin", "bundle", "bundle/empty", "bundle/nested", "bundle/nested/space name.bin", "z.bin"}
	for _, p := range paths {
		abs := filepath.Join(dir, p)
		if p == "bundle" || p == "bundle/nested" {
			if e := os.Mkdir(abs, 0750); e != nil {
				t.Fatal(e)
			}
			continue
		}
		var data []byte
		if p != "bundle/empty" {
			n := 45001
			if p == "z.bin" {
				n = 150003
			}
			data = make([]byte, n)
			for i := range data {
				data[i] = byte((i*31 + int(p[0])) % 251)
			}
		}
		if e := os.WriteFile(abs, data, 0640); e != nil {
			t.Fatal(e)
		}
	}
	inputs := []string{filepath.Join(dir, "a.bin"), filepath.Join(dir, "bundle"), filepath.Join(dir, "z.bin")}
	plan, roots, e := buildSourcePlan(inputs)
	if e != nil {
		t.Fatal(e)
	}
	j := Job{Inputs: plan, Sources: roots, Dir: filepath.Join(t.TempDir(), "job")}
	m, e := sourceManifest(context.Background(), j)
	if e != nil {
		t.Fatal(e)
	}
	// Small chunks deliberately span multiple files and include an empty file.
	m.ChunkSize = 65536
	m.Chunks = nil
	var data []byte
	for _, s := range plan {
		if s.Entry.Kind == "file" {
			b, e := os.ReadFile(s.Source)
			if e != nil {
				t.Fatal(e)
			}
			data = append(data, b...)
		}
	}
	for off := 0; off < len(data); off += 65536 {
		m.Chunks = append(m.Chunks, digest(data[off:min(off+65536, len(data))]))
	}
	if e = m.validate(); e != nil {
		t.Fatal(e)
	}
	return m, data, plan, roots
}
func treeNode(t *testing.T, m Manifest, dest, policy string) *Node {
	t.Helper()
	store, e := openTreeStore(dest, m, policy)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { store.Close() })
	n := &Node{m: m, f: store, o: Options{File: dest, Existing: policy}, token: strings.Repeat("x", 64),
		have: make([]bool, len(m.Chunks)), busy: make([]bool, len(m.Chunks)), peerBusy: map[string]int{}, from: map[string]int64{}, slots: make(chan struct{}, 4), client: client()}
	t.Cleanup(n.client.CloseIdleConnections)
	return n
}
func assertTree(t *testing.T, dest string, m Manifest) {
	t.Helper()
	for _, e := range m.Entries {
		p := filepath.Join(dest, e.Path)
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if e.Kind == "dir" {
			if !st.IsDir() {
				t.Fatal("directory missing")
			}
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if digest(b) != e.SHA256 {
			t.Fatalf("bad file %s", p)
		}
	}
}
func TestTreeStreamMatchesTrustedManifest(t *testing.T) {
	m, _, plan, roots := treeFixture(t)
	var stream bytes.Buffer
	if e := streamSourcePlan(context.Background(), &stream, plan, roots); e != nil {
		t.Fatal(e)
	}
	dest := t.TempDir()
	side := filepath.Join(t.TempDir(), "manifest")
	got, e := receiveTree(context.Background(), &stream, dest, side, m.ChunkSize)
	if e != nil {
		t.Fatal(e)
	}
	if got.id() != m.id() {
		t.Fatalf("stream changed manifest: %s != %s", got.id(), m.id())
	}
	if _, e = os.Stat(filepath.Join(dest, "a.bin")); !os.IsNotExist(e) {
		t.Fatal("published before verification")
	}
	n := treeNode(t, m, dest, "keep")
	if e = n.scan(); e != nil {
		t.Fatal(e)
	}
	if e = n.finalize(); e != nil {
		t.Fatal(e)
	}
	assertTree(t, dest, m)
	inv, e := inspectTree(dest, m, false)
	if e != nil || !inv.Complete || inv.ReusedFiles != m.fileCount() {
		t.Fatalf("not reusable: %+v %v", inv, e)
	}
}
func TestTreeResumeAcrossFilesAndPipeline(t *testing.T) {
	m, data, _, _ := treeFixture(t)
	dest := t.TempDir()
	store, e := openTreeStore(dest, m, "keep")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = store.WriteAt(data[65536:2*65536], 65536); e != nil {
		t.Fatal(e)
	}
	store.Close()
	n := treeNode(t, m, dest, "keep")
	if e = n.scan(); e != nil {
		t.Fatal(e)
	}
	if n.have[0] || !n.have[1] {
		t.Fatal("did not retain only completed chunk")
	}
	startTestVerifier(t, n)
	for i := len(m.Chunks) - 1; i >= 0; i-- {
		if n.have[i] {
			continue
		}
		off := int64(i) * m.ChunkSize
		if _, e = n.f.WriteAt(data[off:off+m.length(i)], off); e != nil {
			t.Fatal(e)
		}
		n.mu.Lock()
		n.have[i] = true
		n.verifier.signal()
		n.mu.Unlock()
	}
	if _, e = awaitTestVerifier(t, n); e != nil {
		t.Fatal(e)
	}
	if e = n.finalize(); e != nil {
		t.Fatal(e)
	}
	assertTree(t, dest, m)
	if n.snapshot().Performance.FileHashBytes != m.Size {
		t.Fatal("tree data hashed more than once")
	}
}
func TestTreeConflictsResumeReplaceAndPreserveOld(t *testing.T) {
	for _, policy := range []string{"resume", "replace"} {
		t.Run(policy, func(t *testing.T) {
			m, data, _, _ := treeFixture(t)
			dest := t.TempDir()
			old := bytes.Repeat([]byte("old"), 15001)
			first := m.Entries[0]
			original := append([]byte(nil), data[:first.Size]...)
			if policy == "resume" {
				old = append([]byte(nil), original...)
				old[0] ^= 1
			}
			p := filepath.Join(dest, first.Path)
			if e := os.WriteFile(p, old, 0644); e != nil {
				t.Fatal(e)
			}
			n := treeNode(t, m, dest, policy)
			got, _ := os.ReadFile(p)
			if !bytes.Equal(got, old) {
				t.Fatal("old changed before transfer")
			}
			if _, e := n.f.WriteAt(data, 0); e != nil {
				t.Fatal(e)
			}
			got, _ = os.ReadFile(p)
			if !bytes.Equal(got, old) {
				t.Fatal("old changed before verification")
			}
			for i := range n.have {
				n.have[i] = true
			}
			if e := n.finalize(); e != nil {
				t.Fatal(e)
			}
			assertTree(t, dest, m)
		})
	}
	var out bytes.Buffer
	for _, choice := range []string{"r", "p"} {
		got, e := promptExisting(strings.NewReader("bad\n"+choice+"\n"), &out)
		if e != nil || got == "" {
			t.Fatal(e)
		}
	}
	if _, e := promptExisting(strings.NewReader("c\n"), &out); e == nil {
		t.Fatal("cancel ignored")
	}
}
func TestTreePublishedFileMutationAndNoOverwrite(t *testing.T) {
	m, data, _, _ := treeFixture(t)
	dest := t.TempDir()
	n := treeNode(t, m, dest, "replace")
	n.f.WriteAt(data, 0)
	for i := range n.have {
		n.have[i] = true
	}
	p := filepath.Join(dest, m.Entries[0].Path)
	os.WriteFile(p, []byte("concurrent"), 0600)
	if e := n.finalize(); e == nil {
		t.Fatal("concurrent destination overwritten")
	}
	got, _ := os.ReadFile(p)
	if string(got) != "concurrent" {
		t.Fatal("concurrent file changed")
	}
}
func TestTreeReusesCompleteFilesAndInvalidatesCache(t *testing.T) {
	m, data, _, _ := treeFixture(t)
	dest := t.TempDir()
	n := treeNode(t, m, dest, "keep")
	n.f.WriteAt(data, 0)
	for i := range n.have {
		n.have[i] = true
	}
	if e := n.finalize(); e != nil {
		t.Fatal(e)
	}
	inv, e := inspectTree(dest, m, false)
	if e != nil || !inv.Complete {
		t.Fatal(e)
	}
	f, e := os.OpenFile(filepath.Join(dest, m.Entries[0].Path), os.O_WRONLY, 0)
	if e != nil {
		t.Fatal(e)
	}
	f.WriteAt([]byte("bad"), 0)
	f.Close()
	inv, e = inspectTree(dest, m, false)
	if e != nil || inv.Complete || len(inv.Conflicts) != 1 {
		t.Fatalf("stale cache accepted: %+v %v", inv, e)
	}
	if s, e := openTreeStore(dest, m, "keep"); e == nil {
		s.Close()
		t.Fatal("different final accepted without policy")
	}
}
func TestTreeEmptyDataset(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "emptydir"), 0755)
	os.WriteFile(filepath.Join(dir, "emptyfile"), nil, 0600)
	plan, roots, e := buildSourcePlan([]string{filepath.Join(dir, "*")})
	if e != nil {
		t.Fatal(e)
	}
	j := Job{Inputs: plan, Sources: roots, Dir: filepath.Join(t.TempDir(), "j")}
	m, e := sourceManifest(context.Background(), j)
	if e != nil {
		t.Fatal(e)
	}
	if m.Size != 0 || len(m.Chunks) != 0 {
		t.Fatal("not empty")
	}
	dest := t.TempDir()
	n := treeNode(t, m, dest, "keep")
	startTestVerifier(t, n)
	if e = n.finalize(); e != nil {
		t.Fatal(e)
	}
	assertTree(t, dest, m)
	if progressPercent(0, 0, true) != 100 {
		t.Fatal("empty progress")
	}
}
func TestTreeRejectsUnsafeManifestAndStream(t *testing.T) {
	m, _, _, _ := treeFixture(t)
	for _, p := range []string{"../escape", "/abs", "a/../../b", "a//b", ".swarmcopy/x", "a\\b"} {
		bad := m
		bad.Entries = append([]TreeEntry(nil), m.Entries...)
		bad.Entries[0].Path = p
		bad.SHA256 = treeDigest(bad.Entries)
		if bad.validate() == nil {
			t.Fatalf("accepted %q", p)
		}
	}
	for _, kind := range []byte{tar.TypeSymlink, tar.TypeReg} {
		var stream bytes.Buffer
		tw := tar.NewWriter(&stream)
		tw.WriteHeader(&tar.Header{Name: "../escape", Mode: 0600, Typeflag: kind, Linkname: "../../outside"})
		tw.Close()
		dest := t.TempDir()
		if _, e := receiveTree(context.Background(), &stream, dest, filepath.Join(t.TempDir(), "m"), 65536); e == nil {
			t.Fatal("unsafe stream accepted")
		}
	}
	var stream bytes.Buffer
	tw := tar.NewWriter(&stream)
	tw.WriteHeader(&tar.Header{Name: "file", Typeflag: tar.TypeReg, Mode: 0600})
	tw.Close()
	if _, e := receiveTree(context.Background(), &stream, t.TempDir(), filepath.Join(t.TempDir(), "m"), 65536); e == nil {
		t.Fatal("missing completion marker accepted")
	}
}
func TestTreeSourceChangesAndCollisions(t *testing.T) {
	_, _, plan, roots := treeFixture(t)
	if e := os.WriteFile(plan[0].Source, []byte("changed"), 0644); e != nil {
		t.Fatal(e)
	}
	if e := checkSourcePlan(plan, roots); e == nil {
		t.Fatal("source mutation missed")
	}
	a, b := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(a, "same"), nil, 0600)
	os.WriteFile(filepath.Join(b, "same"), nil, 0600)
	if _, _, e := buildSourcePlan([]string{filepath.Join(a, "same"), filepath.Join(b, "same")}); e == nil {
		t.Fatal("basename collision missed")
	}
	os.Symlink(filepath.Join(a, "same"), filepath.Join(a, "link"))
	if _, _, e := buildSourcePlan([]string{filepath.Join(a, "link")}); e == nil {
		t.Fatal("source symlink followed")
	}
}
func TestTreeTCPAndQUICTransfers(t *testing.T) {
	for _, transport := range []string{"tcp", "quic"} {
		t.Run(transport, func(t *testing.T) {
			m, data, _, _ := treeFixture(t)
			seed := treeNode(t, m, t.TempDir(), "keep")
			seed.f.WriteAt(data, 0)
			for i := range seed.have {
				seed.have[i] = true
			}
			seed.o.Transport = transport
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var addr string
			if transport == "tcp" {
				srv := httptest.NewServer(http.HandlerFunc(seed.serve))
				defer srv.Close()
				addr = strings.TrimPrefix(srv.URL, "http://")
			} else {
				sock, e := net.ListenPacket("udp", "127.0.0.1:0")
				if e != nil {
					t.Fatal(e)
				}
				addr = sock.LocalAddr().String()
				sock.Close()
				stop, e := seed.startQUIC(ctx, addr, make(chan error, 1))
				if e != nil {
					t.Fatal(e)
				}
				defer stop()
			}
			dest := t.TempDir()
			n := treeNode(t, m, dest, "keep")
			n.o.Transport = transport
			c, closeClient, e := newDataClient(transport, n.token)
			if e != nil {
				t.Fatal(e)
			}
			defer closeClient()
			n.dataClient = c
			for i := range n.have {
				if e = n.download(ctx, i, addr); e != nil {
					t.Fatal(e)
				}
			}
			if e = n.finalize(); e != nil {
				t.Fatal(e)
			}
			assertTree(t, dest, m)
			if transport == "quic" && n.quicRequests.Load() == 0 {
				t.Fatal("UDP path unused")
			}
		})
	}
}
func TestV1ManifestIdentityUnchanged(t *testing.T) {
	n, _ := fixture(t)
	b, _ := json.Marshal(n.m)
	if bytes.Contains(b, []byte("Entries")) {
		t.Fatal("v1 wire encoding changed")
	}
	var legacy struct {
		Version         int
		Name            string
		Size, ChunkSize int64
		SHA256          string
		Chunks          []string
	}
	if e := json.Unmarshal(b, &legacy); e != nil {
		t.Fatal(e)
	}
	old, _ := json.Marshal(legacy)
	if digest(old) != n.m.id() {
		t.Fatal("v1 identity changed")
	}
	h := newContentHash(n.m)
	if _, e := io.Copy(h, bytes.NewReader(nil)); e != nil {
		t.Fatal(e)
	}
}
