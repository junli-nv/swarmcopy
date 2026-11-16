package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func cleanupFixture(t *testing.T) (string, string, Manifest, []SourceEntry) {
	t.Helper()
	m, data, plan, _ := algorithmFixture(t, "blake3", "blake3")
	dest := t.TempDir()
	store, e := openTreeStore(dest, m, "resume")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = store.WriteAt(data[:len(data)/2], 0); e != nil {
		t.Fatal(e)
	}
	store.Close()
	root := filepath.Join(dest, ".swarmcopy", "job-to-remove")
	if e = os.MkdirAll(root, 0700); e != nil {
		t.Fatal(e)
	}
	if e = saveManifest(filepath.Join(root, "manifest.json"), m); e != nil {
		t.Fatal(e)
	}
	o := Options{File: dest, Manifest: filepath.Join(root, "manifest.json"), TokenFile: filepath.Join(root, "job.token")}
	b, _ := json.Marshal(o)
	if e = os.WriteFile(filepath.Join(root, "peer.json"), b, 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(dest, "published.keep"), []byte("published-original"), 0600); e != nil {
		t.Fatal(e)
	}
	return root, dest, m, plan
}
func TestRemoveExclusivePartialsPreservesSourceAndPublishedFiles(t *testing.T) {
	root, dest, m, plan := cleanupFixture(t)
	source := map[string][]byte{}
	for _, p := range plan {
		if p.Entry.Kind == "file" {
			b, e := os.ReadFile(p.Source)
			if e != nil {
				t.Fatal(e)
			}
			source[p.Source] = b
		}
	}
	if e := cleanupPeer(filepath.Join(root, "peer.json"), true); e != nil {
		t.Fatal(e)
	}
	for _, p := range []string{root, treeStage(dest, m)} {
		if _, e := os.Lstat(p); !os.IsNotExist(e) {
			t.Fatalf("residual %s: %v", p, e)
		}
	}
	for path, want := range source {
		got, e := os.ReadFile(path)
		if e != nil || !bytes.Equal(got, want) {
			t.Fatal("original source changed")
		}
	}
	if b, e := os.ReadFile(filepath.Join(dest, "published.keep")); e != nil || string(b) != "published-original" {
		t.Fatal("published media changed")
	}
}
func TestRemoveKeepsSharedAndUnrequestedPartials(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(map[bool]string{false: "metadata-only", true: "shared"}[shared], func(t *testing.T) {
			root, dest, m, _ := cleanupFixture(t)
			if shared {
				other := filepath.Join(filepath.Dir(root), "another-job")
				if e := os.Mkdir(other, 0700); e != nil {
					t.Fatal(e)
				}
				if e := saveManifest(filepath.Join(other, "manifest.json"), m); e != nil {
					t.Fatal(e)
				}
			}
			if e := cleanupPeer(filepath.Join(root, "peer.json"), shared); e != nil {
				t.Fatal(e)
			}
			if _, e := os.Stat(treeStage(dest, m)); e != nil {
				t.Fatal("partial shared or not requested was deleted")
			}
		})
	}
}
func TestRemoveRejectsForeignAndSymlinkJobRoots(t *testing.T) {
	if cleanupPeer(filepath.Join(t.TempDir(), "peer.json"), true) == nil {
		t.Fatal("foreign directory accepted")
	}
	root, _, _, _ := cleanupFixture(t)
	parent := filepath.Dir(root)
	link := filepath.Join(parent, "linked-job")
	if e := os.Symlink(root, link); e != nil {
		t.Fatal(e)
	}
	if cleanupPeer(filepath.Join(link, "peer.json"), true) == nil {
		t.Fatal("symlink job accepted")
	}
}
