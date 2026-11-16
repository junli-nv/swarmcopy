package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestSourceArgumentsInterspersed(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	var sources sourceFlags
	fs.Var(&sources, "source", "")
	dest := fs.String("dest", "", "")
	adaptive := fs.Bool("adaptive", false, "")
	args := sourceArguments(fs, []string{"--source", "a", "b", "--dest", "out", "--adaptive", "c"})
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	if *dest != "out" || !*adaptive || !reflect.DeepEqual([]string(sources), []string{"a"}) || !reflect.DeepEqual(fs.Args(), []string{"b", "c"}) {
		t.Fatalf("bad args %v", args)
	}
}
func TestTreePerFileHashCannotBeBypassedByValidChunks(t *testing.T) {
	m, data, _, _ := treeFixture(t)
	m.Entries[0].SHA256 = digest([]byte("different file"))
	m.SHA256 = treeDigest(m.Entries)
	n := treeNode(t, m, t.TempDir(), "keep")
	n.f.WriteAt(data, 0)
	for i := range n.have {
		n.have[i] = true
	}
	if err := n.finalize(); err == nil {
		t.Fatal("valid chunks bypassed file hashes")
	}
	if _, err := os.Stat(filepath.Join(n.o.File, m.Entries[0].Path)); !os.IsNotExist(err) {
		t.Fatal("corrupt file published")
	}
}

type cancelAfter struct{ calls int }

func (*cancelAfter) Deadline() (time.Time, bool) { return time.Time{}, false }
func (*cancelAfter) Done() <-chan struct{}       { return nil }
func (c *cancelAfter) Err() error {
	c.calls++
	if c.calls > 1 {
		return context.Canceled
	}
	return nil
}
func (*cancelAfter) Value(any) any { return nil }
func TestTreeResumeAfterPartialPublication(t *testing.T) {
	m, data, _, _ := treeFixture(t)
	dest := t.TempDir()
	s, err := openTreeStore(dest, m, "keep")
	if err != nil {
		t.Fatal(err)
	}
	s.WriteAt(data, 0)
	if err = s.Publish(&cancelAfter{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	s.Close()
	n := treeNode(t, m, dest, "keep")
	if err = n.scan(); err != nil {
		t.Fatal(err)
	}
	for _, have := range n.have {
		if !have {
			t.Fatal("partial publication lost data")
		}
	}
	if err = n.finalize(); err != nil {
		t.Fatal(err)
	}
	assertTree(t, dest, m)
}
func TestTreeRepeatedSeedImportRepairsOldStage(t *testing.T) {
	m, _, plan, roots := treeFixture(t)
	var b bytes.Buffer
	if e := streamSourcePlan(context.Background(), &b, plan, roots); e != nil {
		t.Fatal(e)
	}
	stream := append([]byte(nil), b.Bytes()...)
	dest := t.TempDir()
	mp := filepath.Join(t.TempDir(), "manifest")
	if _, e := receiveTree(context.Background(), bytes.NewReader(stream), dest, mp, m.ChunkSize); e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(treeStage(dest, m), "data", m.Entries[0].Path)
	if e := os.WriteFile(p, []byte("old partial"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := receiveTree(context.Background(), bytes.NewReader(stream), dest, mp, m.ChunkSize); e != nil {
		t.Fatal(e)
	}
	n := treeNode(t, m, dest, "keep")
	if e := n.scan(); e != nil {
		t.Fatal(e)
	}
	for _, have := range n.have {
		if !have {
			t.Fatal("new seed import kept corrupt partial")
		}
	}
}
func TestTreeLiteralBracketsAndEmptyDirectory(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "file[1].bin")
	os.WriteFile(p, []byte("literal"), 0600)
	plan, roots, e := buildSourcePlan([]string{p})
	if e != nil {
		t.Fatal(e)
	}
	if e = checkSourcePlan(plan, roots); e != nil {
		t.Fatal(e)
	}
	empty := filepath.Join(t.TempDir(), "only-empty-dir")
	os.Mkdir(empty, 0755)
	plan, roots, e = buildSourcePlan([]string{empty})
	if e != nil {
		t.Fatal(e)
	}
	m, e := sourceManifest(context.Background(), Job{Inputs: plan, Sources: roots, Dir: filepath.Join(t.TempDir(), "j")})
	if e != nil {
		t.Fatal(e)
	}
	n := treeNode(t, m, t.TempDir(), "keep")
	if e = n.finalize(); e != nil {
		t.Fatal(e)
	}
	assertTree(t, n.o.File, m)
}
func TestTreeHardlinkCrashDoesNotRewriteOldFinal(t *testing.T) {
	m, data, _, _ := treeFixture(t)
	dest := t.TempDir()
	s, e := openTreeStore(dest, m, "keep")
	if e != nil {
		t.Fatal(e)
	}
	s.WriteAt(data, 0)
	s.Close()
	name := m.Entries[0].Path
	part := filepath.Join(treeStage(dest, m), "data", name)
	final := filepath.Join(dest, name)
	if e = os.Link(part, final); e != nil {
		t.Fatal(e)
	}
	// Simulate an old published file later modified after the link/unlink crash.
	f, e := os.OpenFile(final, os.O_WRONLY, 0)
	if e != nil {
		t.Fatal(e)
	}
	f.WriteAt([]byte("BROKEN"), 0)
	f.Close()
	old, e := os.ReadFile(final)
	if e != nil {
		t.Fatal(e)
	}
	n := treeNode(t, m, dest, "resume")
	if _, e = n.f.WriteAt(data, 0); e != nil {
		t.Fatal(e)
	}
	after, _ := os.ReadFile(final)
	if !bytes.Equal(after, old) {
		t.Fatal("stage hardlink rewrote old final before verification")
	}
	for i := range n.have {
		n.have[i] = true
	}
	if e = n.finalize(); e != nil {
		t.Fatal(e)
	}
	assertTree(t, dest, m)
}
func TestTreeImportRejectsTrustedManifestMismatch(t *testing.T) {
	m, _, plan, roots := treeFixture(t)
	var b bytes.Buffer
	if e := streamSourcePlan(context.Background(), &b, plan, roots); e != nil {
		t.Fatal(e)
	}
	m.Entries[0].Mode ^= 0100
	m.SHA256 = treeDigest(m.Entries)
	dest := t.TempDir()
	mp := filepath.Join(t.TempDir(), "manifest")
	if _, e := receiveTree(context.Background(), &b, dest, mp, m.ChunkSize, m); e == nil {
		t.Fatal("mismatched trusted tree accepted")
	}
	if _, e := os.Stat(mp); !os.IsNotExist(e) {
		t.Fatal("mismatched manifest persisted")
	}
}
