package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

type failingSyncFile struct {
	contentFile
	failOn, calls int
}

func (f *failingSyncFile) Sync() error {
	f.calls++
	if f.calls == f.failOn {
		return syscall.EIO
	}
	return f.contentFile.Sync()
}
func TestChunksSyncErrorRetainsRecheckablePartials(t *testing.T) {
	for _, failOn := range []int{1, 2} {
		for _, damage := range []bool{false, true} {
			t.Run(fmt.Sprintf("sync%d/damage%t", failOn, damage), func(t *testing.T) {
				m, data, plan, _ := algorithmFixture(t, "blake3", "blake3")
				dest := t.TempDir()
				n := treeNode(t, m, dest, "resume")
				n.o.Verify = "chunks"
				if _, e := n.f.WriteAt(data, 0); e != nil {
					t.Fatal(e)
				}
				if e := n.scan(); e != nil {
					t.Fatal(e)
				}
				real := n.f
				n.f = &failingSyncFile{contentFile: real, failOn: failOn}
				if e := n.finalize(); !errors.Is(e, syscall.EIO) {
					t.Fatalf("sync error lost: %v", e)
				}
				if n.complete || n.failure == "" || n.perf.Stage != "error" {
					t.Fatal("failed fsync reported success")
				}
				for _, entry := range m.Entries {
					if entry.Kind == "file" {
						if _, e := os.Stat(filepath.Join(dest, entry.Path)); !os.IsNotExist(e) {
							t.Fatal("file published after failed fsync", e)
						}
					}
				}
				if damage {
					if _, e := real.WriteAt([]byte{data[0] ^ 0xff}, 0); e != nil {
						t.Fatal(e)
					}
				}
				if e := real.Close(); e != nil {
					t.Fatal(e)
				}
				resumed := treeNode(t, m, dest, "resume")
				resumed.o.Verify = "chunks"
				if e := resumed.scan(); e != nil {
					t.Fatal(e)
				}
				for i, valid := range resumed.have {
					want := !(damage && i == 0)
					if valid != want {
						t.Fatalf("chunk %d valid=%t want=%t", i, valid, want)
					}
				}
				if resumed.complete || resumed.downloaded.Load() != 0 {
					t.Fatal("resume bypassed finalization or counted old bytes as download")
				}
				if damage {
					if _, e := resumed.f.WriteAt(data[:m.length(0)], 0); e != nil {
						t.Fatal(e)
					}
					if e := resumed.scan(); e != nil {
						t.Fatal(e)
					}
				}
				if e := resumed.finalize(); e != nil {
					t.Fatal(e)
				}
				if !resumed.complete {
					t.Fatal("recovered fsync never completed")
				}
				assertAlgorithmTree(t, m, plan, dest)
			})
		}
	}
}
