package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) (*Node, []byte) {
	t.Helper()
	data := bytes.Repeat([]byte("verified-data"), 6000)
	m := Manifest{Version: 1, Name: "image", Size: int64(len(data)), ChunkSize: 65536, SHA256: digest(data)}
	for i := 0; i < len(data); i += 65536 {
		end := i + 65536
		if end > len(data) {
			end = len(data)
		}
		m.Chunks = append(m.Chunks, digest(data[i:end]))
	}
	f, e := os.CreateTemp(t.TempDir(), "file")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { f.Close() })
	if _, e = f.Write(data); e != nil {
		t.Fatal(e)
	}
	n := &Node{m: m, f: f, token: strings.Repeat("x", 32), have: make([]bool, len(m.Chunks)),
		busy: make([]bool, len(m.Chunks)), peerBusy: map[string]int{}, from: map[string]int64{},
		slots: make(chan struct{}, 2), client: client()}
	return n, data
}
func TestResumeRechecksCorruptChunks(t *testing.T) {
	n, _ := fixture(t)
	if _, e := n.f.WriteAt([]byte("corruption"), 0); e != nil {
		t.Fatal(e)
	}
	if e := n.scan(); e != nil {
		t.Fatal(e)
	}
	if n.have[0] || !n.have[1] {
		t.Fatal("corrupt chunk must be redownloaded; valid chunk must be retained")
	}
	n.origin = true
	if e := n.scan(); e == nil {
		t.Fatal("corrupt final file accepted")
	}
}
func TestChunkAuthenticationAndAvailability(t *testing.T) {
	n, _ := fixture(t)
	for _, tc := range []struct {
		auth string
		code int
	}{{"", 401}, {"Bearer " + n.token, 404}} {
		r := httptest.NewRequest("GET", "/chunk?i=0", nil)
		r.Header.Set("Authorization", tc.auth)
		w := httptest.NewRecorder()
		n.serve(w, r)
		if w.Code != tc.code {
			t.Fatalf("status %d", w.Code)
		}
	}
}
func TestRejectCorruptDownload(t *testing.T) {
	n, data := fixture(t)
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "65536")
		w.Write(bytes.Repeat([]byte("!"), 65536))
	}))
	defer bad.Close()
	if e := n.download(context.Background(), 0, strings.TrimPrefix(bad.URL, "http://")); e == nil {
		t.Fatal("bad checksum accepted")
	}
	b := make([]byte, len(data))
	n.f.ReadAt(b, 0)
	if !bytes.Equal(b, data) || n.have[0] {
		t.Fatal("invalid chunk modified destination")
	}
}
func TestPublishNeverOverwrites(t *testing.T) {
	d := t.TempDir()
	a, b := filepath.Join(d, "a"), filepath.Join(d, "b")
	os.WriteFile(a, []byte("new"), 0600)
	os.WriteFile(b, []byte("existing"), 0600)
	if e := publish(a, b); e == nil {
		t.Fatal("overwrote final file")
	}
	got, _ := os.ReadFile(b)
	if string(got) != "existing" {
		t.Fatal("changed existing destination")
	}
}
func TestSchedulerExchangesPartialChunks(t *testing.T) {
	n, _ := fixture(t)
	n.remotes = []Remote{{addr: "seed:1", seen: time.Now(), st: Status{Origin: true, Have: []bool{true, true}}},
		{addr: "peer:1", seen: time.Now(), st: Status{Have: []bool{true, true}}}}
	_, a := n.choose()
	if a != "peer:1" {
		t.Fatal("should prefer peer when same pieces available")
	}
}
func TestManifestRejectsInvalidDimensions(t *testing.T) {
	n, _ := fixture(t)
	m := n.m
	m.ChunkSize = 0
	if m.validate() == nil {
		t.Fatal("zero chunk accepted")
	}
	b, _ := json.Marshal(n.m)
	var copy Manifest
	json.Unmarshal(b, &copy)
	if copy.id() != n.m.id() {
		t.Fatal("unstable manifest identity")
	}
}
