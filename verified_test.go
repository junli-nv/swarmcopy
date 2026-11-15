package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestVerifiedCacheRejectsMetadataOrManifestChange(t *testing.T) {
	n, _ := fixture(t)
	info, e := n.f.Stat()
	if e != nil {
		t.Fatal(e)
	}
	if e = saveVerified(n.f.Name(), n.m, info); e != nil {
		t.Fatal(e)
	}
	if !cacheMatches(n.f.Name(), n.m, info) {
		t.Fatal("valid verified cache rejected")
	}
	other := n.m
	other.SHA256 = digest([]byte("different"))
	if cacheMatches(n.f.Name(), other, info) {
		t.Fatal("wrong manifest accepted")
	}
	oldTime := info.ModTime()
	time.Sleep(2 * time.Millisecond)
	if _, e = n.f.WriteAt([]byte("changed"), 0); e != nil {
		t.Fatal(e)
	}
	if e = os.Chtimes(n.f.Name(), oldTime, oldTime); e != nil {
		t.Fatal(e)
	}
	changed, _ := n.f.Stat()
	if cacheMatches(n.f.Name(), n.m, changed) {
		t.Fatal("content modification with restored mtime accepted")
	}
}
func TestVerifiedCacheRejectsReplacedInode(t *testing.T) {
	n, data := fixture(t)
	info, _ := n.f.Stat()
	if e := saveVerified(n.f.Name(), n.m, info); e != nil {
		t.Fatal(e)
	}
	replacement := filepath.Join(filepath.Dir(n.f.Name()), "replacement")
	if e := os.WriteFile(replacement, data, 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Rename(replacement, n.f.Name()); e != nil {
		t.Fatal(e)
	}
	replaced, _ := os.Stat(n.f.Name())
	if cacheMatches(n.f.Name(), n.m, replaced) {
		t.Fatal("replacement inode accepted")
	}
}
