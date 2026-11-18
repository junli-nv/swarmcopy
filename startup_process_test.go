package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Set SWARMCOPY_BINARY to exercise the built executable, not an in-process peer.
func TestStartupTreePeerProcesses(t *testing.T) {
	binary := os.Getenv("SWARMCOPY_BINARY")
	if binary == "" {
		t.Skip("set SWARMCOPY_BINARY to a built Linux executable")
	}
	for _, mode := range []string{"chunks", "full"} {
		for _, transport := range []string{"tcp", "quic"} {
			t.Run(mode+"/"+transport, func(t *testing.T) {
				m, data, _, _ := treeFixture(t)
				root := t.TempDir()
				manifest := filepath.Join(root, "manifest.json")
				if err := saveManifest(manifest, m); err != nil {
					t.Fatal(err)
				}
				tokenPath := filepath.Join(root, "token")
				secret := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
				if err := os.WriteFile(tokenPath, []byte(secret), 0600); err != nil {
					t.Fatal(err)
				}
				dirs := []string{filepath.Join(root, "a"), filepath.Join(root, "b"), filepath.Join(root, "c")}
				startupOriginals(t, dirs[0], m, data)
				startupOriginals(t, dirs[1], m, data)
				// Same-size corruption must be repaired from the source, never trusted by size.
				badPath := filepath.Join(dirs[1], "z.bin")
				bad, err := os.ReadFile(badPath)
				if err != nil {
					t.Fatal(err)
				}
				bad[len(bad)-1] ^= 255
				if err = os.WriteFile(badPath, bad, 0640); err != nil {
					t.Fatal(err)
				}
				addresses := make([]string, 3)
				for i := range addresses {
					l, err := net.Listen("tcp", "127.0.0.1:0")
					if err != nil {
						t.Fatal(err)
					}
					addresses[i] = l.Addr().String()
					l.Close()
				}
				if addresses[0] == addresses[1] || addresses[0] == addresses[2] || addresses[1] == addresses[2] {
					t.Fatal("port collision")
				}
				for i := range dirs {
					logPath := filepath.Join(root, fmt.Sprintf("peer-%d.log", i))
					f, err := os.Create(logPath)
					if err != nil {
						t.Fatal(err)
					}
					peers := ""
					if i > 0 {
						peers = addresses[i-1]
					}
					cmd := exec.Command(binary, "peer", "--file", dirs[i], "--manifest", manifest,
						"--token-file", tokenPath, "--listen", addresses[i], "--peers", peers,
						"--existing", "resume", "--verify", mode, "--transport", transport,
						"--lifetime", "45s", "--stall-timeout", "20s")
					cmd.Stdout = f
					cmd.Stderr = f
					if err = cmd.Start(); err != nil {
						f.Close()
						t.Fatal(err)
					}
					t.Cleanup(func() {
						cmd.Process.Signal(os.Interrupt)
						done := make(chan error, 1)
						go func() { done <- cmd.Wait() }()
						select {
						case <-done:
						case <-time.After(5 * time.Second):
							cmd.Process.Kill()
							<-done
						}
						f.Close()
						if t.Failed() {
							b, _ := os.ReadFile(logPath)
							t.Log(string(b))
						}
					})
				}
				c := client()
				defer c.CloseIdleConnections()
				deadline := time.Now().Add(25 * time.Second)
				var statuses [3]Status
				complete := false
				for time.Now().Before(deadline) {
					complete = true
					for i, a := range addresses {
						ctx, cancel := context.WithTimeout(context.Background(), time.Second)
						st, err := getStatus(ctx, c, secret, a)
						cancel()
						if err != nil || !st.Complete {
							complete = false
						}
						if err == nil && st.Error != "" {
							t.Fatalf("peer %d: %s", i, st.Error)
						}
						statuses[i] = st
					}
					if complete {
						break
					}
					time.Sleep(100 * time.Millisecond)
				}
				if !complete {
					t.Fatalf("tree peers did not complete: %+v", statuses)
				}
				for _, dir := range dirs {
					assertTree(t, dir, m)
				}
				if statuses[0].Downloaded != 0 {
					t.Fatal("source downloaded media")
				}
				if statuses[1].Downloaded <= 0 || statuses[1].Downloaded >= m.Size {
					t.Fatal("partial existing replica was not reused")
				}
				if statuses[1].Uploaded <= 0 || statuses[2].From[addresses[1]] != m.Size {
					t.Fatal("tree chain bypassed partial replica")
				}
				for _, st := range statuses {
					if st.CheckingExisting || st.ExistingConflict {
						t.Fatal("completed peer retained initialization state")
					}
				}
			})
		}
	}
}
