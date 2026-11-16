package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNodeFailuresDoNotCancelHealthyHosts(t *testing.T) {
	j := Job{Dir: t.TempDir(), ID: "isolation", Hosts: []string{"bad", "healthy", "slow"}, Fanout: 3, NodeTimeout: 40 * time.Millisecond}
	var mu sync.Mutex
	healthyRan := false
	good, e := nodeStage(context.Background(), &j, j.Hosts, "test", func(ctx context.Context, h string) error {
		switch h {
		case "bad":
			return errors.New("simulated offline")
		case "slow":
			<-ctx.Done()
			return ctx.Err()
		default:
			time.Sleep(10 * time.Millisecond)
			if ctx.Err() != nil {
				return errors.New("healthy sibling was cancelled")
			}
			mu.Lock()
			healthyRan = true
			mu.Unlock()
			return nil
		}
	})
	if e != nil || len(good) != 1 || good[0] != "healthy" || !healthyRan {
		t.Fatalf("%v %v", good, e)
	}
	if len(j.Hosts) != 3 || len(j.NodeIssues) != 2 {
		t.Fatal("targets lost or errors missing")
	}
	saved, e := os.ReadFile(filepath.Join(j.Dir, "job.json"))
	if e != nil || !bytes.Contains(saved, []byte("simulated offline")) {
		t.Fatal("diagnostic not persisted")
	}
}
func TestOriginalSourceViewAndChangeDetection(t *testing.T) {
	m, data, plan, roots := algorithmFixture(t, "blake3", "blake3")
	j := Job{Inputs: plan, Sources: roots}
	v, e := newSourceView(context.Background(), j, m)
	if e != nil {
		t.Fatal(e)
	}
	defer v.Close()
	// Read across file boundaries in reverse order, like peer chunk requests.
	for end := len(data); end > 0; {
		start := max(0, end-73123)
		b := make([]byte, end-start)
		n, e := v.ReadAt(b, int64(start))
		if e != nil || n != len(b) || !bytes.Equal(b, data[start:end]) {
			t.Fatalf("source read %d: %v", start, e)
		}
		end = start
	}
	if _, e := v.WriteAt([]byte("x"), 0); e == nil {
		t.Fatal("source is writable")
	}
	for _, entry := range plan {
		if entry.Entry.Kind == "file" && entry.Entry.Size > 0 {
			f, e := os.OpenFile(entry.Source, os.O_WRONLY, 0)
			if e != nil {
				t.Fatal(e)
			}
			_, e = f.WriteAt([]byte("X"), 0)
			f.Close()
			if e != nil {
				t.Fatal(e)
			}
			break
		}
	}
	b := make([]byte, len(data))
	if _, e = v.ReadAt(b, 0); e == nil || !strings.Contains(e.Error(), "source changed") {
		t.Fatalf("mutation not detected: %v", e)
	}
}
func TestControllerTargetDetection(t *testing.T) {
	h, e := os.Hostname()
	if e != nil {
		t.Fatal(e)
	}
	j := Job{Hosts: []string{"unrelated.invalid", "root@" + strings.ToUpper(h)}}
	if controllerSourceHost(j) != strings.ToUpper(h) {
		t.Fatal("controller not recognized")
	}
	if controllerSourceHost(Job{Hosts: []string{"unrelated.invalid"}}) != "" {
		t.Fatal("head incorrectly selected as direct origin")
	}
}
func TestDirectOriginServesTrustedOriginals(t *testing.T) {
	m, data, plan, roots := algorithmFixture(t, "blake3", "blake3")
	j := Job{Dir: t.TempDir(), ID: "origin", Inputs: plan, Sources: roots, Verify: "chunks", Uploads: 2}
	if e := os.WriteFile(filepath.Join(j.Dir, "job.token"), []byte(strings.Repeat("a", 64)), 0600); e != nil {
		t.Fatal(e)
	}
	close, e := startSourceOrigin(context.Background(), &j, m, "127.0.0.1")
	if e != nil {
		t.Fatal(e)
	}
	defer close()
	c := client()
	defer c.CloseIdleConnections()
	st, e := getStatus(context.Background(), c, strings.Repeat("a", 64), j.SourceAddress)
	if e != nil || !st.Complete || st.ID != m.id() {
		t.Fatalf("origin status: %v %+v", e, st)
	}
	for i := range m.Chunks {
		r, e := request(context.Background(), c, strings.Repeat("a", 64), j.SourceAddress, "/chunk?i="+strconv.Itoa(i))
		if e != nil {
			t.Fatal(e)
		}
		b := make([]byte, m.length(i))
		_, e = io.ReadFull(r.Body, b)
		r.Body.Close()
		if e != nil || !bytes.Equal(b, data[int64(i)*m.ChunkSize:int64(i)*m.ChunkSize+int64(len(b))]) {
			t.Fatal("origin bytes differ")
		}
	}
	addr := j.SourceAddress
	close()
	conn, e := net.DialTimeout("tcp", addr, 100*time.Millisecond)
	if e == nil {
		conn.Close()
		t.Fatal("source listener left running")
	}
}

func TestReopenedSparsePartialDoesNotScanZeros(t *testing.T) {
	const size = int64(1 << 30)
	e := TreeEntry{Path: "large.bin", Kind: "file", Size: size, SHA256: digest([]byte("trusted-file-placeholder"))}
	m := Manifest{Version: 2, Name: "files", Size: size, ChunkSize: 16 << 20, Entries: []TreeEntry{e}}
	for i := int64(0); i < size; i += m.ChunkSize {
		m.Chunks = append(m.Chunks, digest([]byte("trusted-chunk-placeholder")))
	}
	m.SHA256 = treeDigest(m.Entries)
	dest := t.TempDir()
	store, x := openTreeStore(dest, m, "resume")
	if x != nil {
		t.Fatal(x)
	}
	store.Close()
	store, x = openTreeStore(dest, m, "resume")
	if x != nil {
		t.Fatal(x)
	}
	defer store.Close()
	if store.chunkMayExist(0, m.ChunkSize) {
		t.Fatal("reopened empty sparse range treated as received data")
	}
	n := &Node{m: m, f: store, have: make([]bool, len(m.Chunks)), o: Options{Verify: "chunks"}}
	if x = n.scan(); x != nil {
		t.Fatal(x)
	}
	for _, h := range n.have {
		if h {
			t.Fatal("sparse hole incorrectly marked verified")
		}
	}
	if _, x = store.WriteAt([]byte("received"), m.ChunkSize+4096); x != nil {
		t.Fatal(x)
	}
	if !store.chunkMayExist(m.ChunkSize, m.ChunkSize) {
		t.Fatal("allocated received range skipped")
	}
	if store.chunkMayExist(2*m.ChunkSize, m.ChunkSize) {
		t.Fatal("unwritten sparse range scanned")
	}
}
