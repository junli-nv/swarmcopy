package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func completedCleanupFixture(t *testing.T, mode string) (string, string, Manifest, []SourceEntry) {
	t.Helper()
	root, dest, m, plan := cleanupFixture(t)
	var data []byte
	for _, e := range plan {
		if e.Entry.Kind == "file" {
			b, x := os.ReadFile(e.Source)
			if x != nil {
				t.Fatal(x)
			}
			data = append(data, b...)
		}
	}
	n := treeNode(t, m, dest, "resume")
	n.o.Verify = mode
	if _, e := n.f.WriteAt(data, 0); e != nil {
		t.Fatal(e)
	}
	if e := n.scan(); e != nil {
		t.Fatal(e)
	}
	if e := n.finalize(); e != nil {
		t.Fatal(e)
	}
	if e := n.f.Close(); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(root, "peer.log"), []byte("most-recent-peer-log"), 0600); e != nil {
		t.Fatal(e)
	}
	return root, dest, m, plan
}
func TestCompletedCleanupPreservesMediaCacheAndBoundedLog(t *testing.T) {
	for _, mode := range []string{"full", "chunks"} {
		t.Run(mode, func(t *testing.T) {
			root, dest, m, plan := completedCleanupFixture(t, mode)
			namespace := treeStage(dest, m)
			if e := cleanupCompletedPeer(filepath.Join(root, "peer.json")); e != nil {
				t.Fatal(e)
			}
			for _, p := range []string{root, namespace, filepath.Join(dest, ".swarmcopy-trees"), filepath.Join(dest, ".swarmcopy/trees")} {
				if _, e := os.Lstat(p); !os.IsNotExist(e) {
					t.Fatal("uncleaned path", p, e)
				}
			}
			assertAlgorithmTree(t, m, plan, dest)
			r, e := os.OpenRoot(dest)
			if e != nil {
				t.Fatal(e)
			}
			cache := loadTreeVerified(r, m)
			r.Close()
			if mode == "full" && len(cache) != m.fileCount() {
				t.Fatal("full verification cache lost")
			}
			if mode == "chunks" && len(cache) != 0 {
				t.Fatal("chunks invented full cache")
			}
			b, e := os.ReadFile(filepath.Join(dest, ".swarmcopy/last-peer.log"))
			if e != nil || string(b) != "most-recent-peer-log" {
				t.Fatal("recent log lost", e)
			}
			if e = cleanupCompletedPeer(filepath.Join(root, "peer.json")); e != nil {
				t.Fatal("cleanup not idempotent", e)
			}
		})
	}
}
func TestCleanupDoesNotDeleteIncompleteSharedOrLockedData(t *testing.T) {
	t.Run("incomplete", func(t *testing.T) {
		root, dest, m, _ := cleanupFixture(t)
		if cleanupCompletedPeer(filepath.Join(root, "peer.json")) == nil {
			t.Fatal("partial data removed")
		}
		if _, e := os.Stat(treeStage(dest, m)); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("shared", func(t *testing.T) {
		root, dest, m, _ := completedCleanupFixture(t, "chunks")
		other := filepath.Join(filepath.Dir(root), "other-job")
		os.Mkdir(other, 0700)
		if e := saveManifest(filepath.Join(other, "manifest.json"), m); e != nil {
			t.Fatal(e)
		}
		if e := cleanupCompletedPeer(filepath.Join(root, "peer.json")); e != nil {
			t.Fatal(e)
		}
		if _, e := os.Stat(treeStage(dest, m)); e != nil {
			t.Fatal("shared namespace removed", e)
		}
		if _, e := os.Stat(other); e != nil {
			t.Fatal("other job removed")
		}
	})
	t.Run("locked", func(t *testing.T) {
		root, dest, _, _ := completedCleanupFixture(t, "chunks")
		fd, e := syscall.Open(dest+".swarmcopy.lock", syscall.O_CREAT|syscall.O_RDWR, 0600)
		if e != nil {
			t.Fatal(e)
		}
		defer syscall.Close(fd)
		defer os.Remove(dest + ".swarmcopy.lock")
		if e = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
			t.Fatal(e)
		}
		if cleanupCompletedPeer(filepath.Join(root, "peer.json")) == nil {
			t.Fatal("live destination lock ignored")
		}
		if _, e = os.Stat(root); e != nil {
			t.Fatal("locked job removed")
		}
	})
	t.Run("symlink-parent", func(t *testing.T) {
		root, _, _, _ := completedCleanupFixture(t, "chunks")
		outside := t.TempDir()
		link := filepath.Join(outside, ".swarmcopy")
		if e := os.Symlink(filepath.Dir(root), link); e != nil {
			t.Fatal(e)
		}
		if cleanupCompletedPeer(filepath.Join(link, filepath.Base(root), "peer.json")) == nil {
			t.Fatal("symlink parent accepted")
		}
		if _, e := os.Stat(root); e != nil {
			t.Fatal("foreign job removed")
		}
	})
}
func TestLegacyStagingAndCacheAreReadInPlaceAndCleaned(t *testing.T) {
	root, dest, m, plan := completedCleanupFixture(t, "full")
	old := filepath.Join(dest, ".swarmcopy-trees", m.id())
	if e := os.MkdirAll(filepath.Dir(old), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.Rename(treeStage(dest, m), old); e != nil {
		t.Fatal(e)
	}
	r, e := os.OpenRoot(dest)
	if e != nil {
		t.Fatal(e)
	}
	cache := loadTreeVerified(r, m)
	r.Close()
	b, _ := json.Marshal(cache)
	if e = os.WriteFile(filepath.Join(old, "verified.json"), b, 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(filepath.Join(dest, treeCacheName)); e != nil {
		t.Fatal(e)
	}
	store, e := openTreeStore(dest, m, "resume")
	if e != nil {
		t.Fatal(e)
	}
	if store.stagePath != old || !store.complete {
		t.Fatal("old staging not reused in place")
	}
	store.Close()
	if e = cleanupCompletedPeer(filepath.Join(root, "peer.json")); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Dir(old)); !os.IsNotExist(e) {
		t.Fatal("legacy parent remained", e)
	}
	assertAlgorithmTree(t, m, plan, dest)
	r, e = os.OpenRoot(dest)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	if len(loadTreeVerified(r, m)) != m.fileCount() {
		t.Fatal("legacy cache not preserved")
	}
}
func TestLatestCacheAndLogDoNotAccumulatePerJob(t *testing.T) {
	root, dest, m, _ := completedCleanupFixture(t, "full")
	r, e := os.OpenRoot(dest)
	if e != nil {
		t.Fatal(e)
	}
	cache := loadTreeVerified(r, m)
	for i := 0; i < 30; i++ {
		next := m
		next.Name = fmt.Sprint("dataset-", i)
		if e = saveTreeVerified(r, next, cache); e != nil {
			t.Fatal(e)
		}
	}
	r.Close()
	large := bytes.Repeat([]byte("z"), (8<<20)+1024)
	copy(large[len(large)-4:], []byte("LAST"))
	if e = os.WriteFile(filepath.Join(root, "peer.log"), large, 0600); e != nil {
		t.Fatal(e)
	}
	if e = cleanupCompletedPeer(filepath.Join(root, "peer.json")); e != nil {
		t.Fatal(e)
	}
	names, e := os.ReadDir(filepath.Join(dest, ".swarmcopy"))
	if e != nil {
		t.Fatal(e)
	}
	if len(names) != 2 {
		t.Fatalf("per-job state accumulated: %v", names)
	}
	b, e := os.ReadFile(filepath.Join(dest, ".swarmcopy/last-peer.log"))
	if e != nil {
		t.Fatal(e)
	}
	if len(b) != (8<<20) || string(b[len(b)-4:]) != "LAST" {
		t.Fatal("recent log bound broken")
	}
}

func TestCompletedCleanupFastPathDoesNotRedeploy(t *testing.T) {
	dir := t.TempDir()
	j := Job{ID: "already-clean", Dir: dir, State: "complete", Cleaned: "2026-11-16T00:00:00Z", Hosts: []string{"unreachable.invalid"}, Port: 19700}
	if e := j.save(); e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(map[string]any{"Finished": j.Cleaned, "Nodes": map[string]Status{"unreachable.invalid:19700": {Complete: true}}})
	if e := os.WriteFile(filepath.Join(dir, "result.json"), b, 0600); e != nil {
		t.Fatal(e)
	}
	if e := cleanupCommand([]string{"--job", dir}); e != nil {
		t.Fatal("already-cleaned job contacted remote host", e)
	}
}
