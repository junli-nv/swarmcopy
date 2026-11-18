package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func startupOriginals(t *testing.T, dest string, m Manifest, data []byte) {
	t.Helper()
	var offset int64
	for _, e := range m.Entries {
		p := filepath.Join(dest, e.Path)
		if e.Kind == "dir" {
			if err := os.MkdirAll(p, 0750); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data[offset:offset+e.Size], 0640); err != nil {
			t.Fatal(err)
		}
		offset += e.Size
	}
}
func startupFixture(t *testing.T, dest string, m Manifest, policy string, recheck bool) (*startupTree, *Node) {
	t.Helper()
	view, err := openStartupTree(dest, m, recheck)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { view.Close() })
	n := &Node{
		m: m, f: view, o: Options{Verify: "chunks", Existing: policy, File: dest, Workers: 2, Uploads: 2},
		checkingExisting: true, existingTotal: view.totalBytes(),
		have: make([]bool, len(m.Chunks)), busy: make([]bool, len(m.Chunks)),
		token: strings.Repeat("x", 64), from: map[string]int64{}, peerBusy: map[string]int{},
		slots: make(chan struct{}, 2), client: client(),
	}
	t.Cleanup(n.client.CloseIdleConnections)
	return view, n
}
func startupFingerprints(t *testing.T, dest string, m Manifest) map[string]fileFingerprint {
	t.Helper()
	out := map[string]fileFingerprint{}
	for _, e := range m.Entries {
		if e.Kind != "file" {
			continue
		}
		st, err := os.Stat(filepath.Join(dest, e.Path))
		if err != nil {
			t.Fatal(err)
		}
		fp, ok := fingerprint(st)
		if !ok {
			t.Fatal("fingerprint unavailable")
		}
		out[e.Path] = fp
	}
	return out
}
func TestStartupMetadataDoesNotCertifySameSizeCorruption(t *testing.T) {
	m, data, _, _ := treeFixture(t)
	dest := t.TempDir()
	startupOriginals(t, dest, m, data)
	if err := os.WriteFile(filepath.Join(dest, "a.bin"), bytes.Repeat([]byte("!"), 45001), 0640); err != nil {
		t.Fatal(err)
	}
	inv, err := inspectTreeMetadata(context.Background(), dest, m, false)
	if err != nil || inv.Complete || len(inv.Conflicts) != 0 || inv.UnverifiedFiles != m.fileCount() {
		t.Fatalf("metadata falsely certified contents: %+v %v", inv, err)
	}
	if _, err := os.Stat(filepath.Join(dest, treeCacheName)); !os.IsNotExist(err) {
		t.Fatal("metadata-only inventory wrote a verification cache")
	}
}
func TestStartupServesVerifiedCrossFileChunkBeforeScanFinishes(t *testing.T) {
	m, data, _, _ := treeFixture(t)
	dest := t.TempDir()
	startupOriginals(t, dest, m, data)
	before := startupFingerprints(t, dest, m)
	view, n := startupFixture(t, dest, m, "keep", false)
	server := httptest.NewServer(http.HandlerFunc(n.serve))
	defer server.Close()
	address := strings.TrimPrefix(server.URL, "http://")
	paused, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		var processed int64
		waited := false
		done <- view.verifyOriginalsProgress(context.Background(), n, func(count int64) {
			processed += count
			if !waited && processed >= m.ChunkSize {
				waited = true
				close(paused)
				<-release
			}
		})
	}()
	defer func() {
		close(release)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-paused:
	case <-time.After(3 * time.Second):
		t.Fatal("scan never published its first chunk")
	}
	st, err := getStatus(context.Background(), n.client, n.token, address)
	if err != nil || !st.CheckingExisting || st.Complete || !st.Have[0] || st.Have[1] {
		t.Fatalf("wrong startup status: %+v %v", st, err)
	}
	response, err := request(context.Background(), n.client, n.token, address, "/chunk?i=0")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || !bytes.Equal(got, data[:m.ChunkSize]) {
		t.Fatalf("verified chunk unavailable: %d %v", response.StatusCode, err)
	}
	response, err = request(context.Background(), n.client, n.token, address, "/chunk?i=1")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 404 {
		t.Fatal("unverified chunk was advertised")
	}
	after := startupFingerprints(t, dest, m)
	for p, fp := range before {
		if after[p] != fp {
			t.Fatal("original file changed during startup", p)
		}
	}
}
func TestStartupCachedAndUncachedFileBoundaries(t *testing.T) {
	m, data, _, _ := treeFixture(t)
	for mask := 0; mask < 16; mask++ {
		dest := t.TempDir()
		startupOriginals(t, dest, m, data)
		root, err := os.OpenRoot(dest)
		if err != nil {
			t.Fatal(err)
		}
		cache := treeVerified{}
		index := 0
		for _, e := range m.Entries {
			if e.Kind != "file" {
				continue
			}
			if mask&(1<<index) != 0 {
				st, _ := root.Lstat(e.Path)
				fp, _ := fingerprint(st)
				cache[e.Path] = fp
			}
			index++
		}
		if err = saveTreeVerified(root, m, cache); err != nil {
			t.Fatal(err)
		}
		root.Close()
		view, n := startupFixture(t, dest, m, "keep", false)
		if err = view.verifyOriginals(context.Background(), n); err != nil {
			t.Fatal(mask, err)
		}
		for i, have := range n.snapshot().Have {
			if !have {
				t.Fatalf("mask=%d lost boundary chunk %d", mask, i)
			}
		}
		if n.snapshot().Complete {
			t.Fatal("scan claimed publication complete")
		}
	}
}
func TestStartupMissingFileNeverAdvertisesCrossingChunks(t *testing.T) {
	m, data, _, _ := treeFixture(t)
	dest := t.TempDir()
	startupOriginals(t, dest, m, data)
	if err := os.Remove(filepath.Join(dest, "bundle/nested/space name.bin")); err != nil {
		t.Fatal(err)
	}
	view, n := startupFixture(t, dest, m, "keep", false)
	if err := view.prepare(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	have := n.snapshot().Have
	if len(have) != 4 || have[0] || have[1] || !have[2] || !have[3] {
		t.Fatalf("missing-file range escaped verification: %v", have)
	}
}
func TestStartupCancellationRetainsOnlyCompletedFileCache(t *testing.T) {
	m, data, _, _ := treeFixture(t)
	dest := t.TempDir()
	startupOriginals(t, dest, m, data)
	view, n := startupFixture(t, dest, m, "keep", false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := view.verifyOriginalsProgress(ctx, n, func(int64) { cancel() })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	cache := loadTreeVerified(view.root, m)
	if len(cache) != 1 {
		t.Fatalf("partial/incomplete files were cached: %v", cache)
	}
	if _, ok := cache["a.bin"]; !ok {
		t.Fatal("completed file checkpoint was lost")
	}
	if n.snapshot().Complete {
		t.Fatal("cancelled startup claimed completion")
	}
	assertTree(t, dest, m)
}

func TestStartupConflictPoliciesPreserveOriginalsUntilPublication(t *testing.T) {
	for _, policy := range []string{"ask", "keep", "resume", "replace"} {
		t.Run(policy, func(t *testing.T) {
			m, data, _, _ := treeFixture(t)
			dest := t.TempDir()
			startupOriginals(t, dest, m, data)
			file := filepath.Join(dest, "z.bin")
			bad, _ := os.ReadFile(file)
			bad[len(bad)-1] = 255
			if err := os.WriteFile(file, bad, 0640); err != nil {
				t.Fatal(err)
			}
			view, n := startupFixture(t, dest, m, policy, false)
			err := view.prepare(context.Background(), n)
			still, _ := os.ReadFile(file)
			if !bytes.Equal(still, bad) {
				t.Fatal("original overwritten during startup")
			}
			if policy == "ask" || policy == "keep" {
				if !errors.Is(err, errExistingConflict) {
					t.Fatalf("conflict lost: %v", err)
				}
				if n.snapshot().Complete {
					t.Fatal("conflict reported complete")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			st := n.snapshot()
			if policy == "replace" {
				for i := 1; i < len(st.Have); i++ {
					if st.Have[i] {
						t.Fatalf("replace reused conflicting-file chunk %d", i)
					}
				}
			} else if !st.Have[2] || st.Have[3] {
				t.Fatalf("resume lost valid chunk or trusted corrupt tail: %v", st.Have)
			}
			for i, have := range st.Have {
				if have {
					continue
				}
				off := int64(i) * m.ChunkSize
				b := data[off : off+m.length(i)]
				if _, err := n.f.WriteAt(b, off); err != nil {
					t.Fatal(err)
				}
				n.startupHave(i)
			}
			n.mu.Lock()
			n.checkingExisting = false
			n.mu.Unlock()
			if err := n.finalize(); err != nil {
				t.Fatal(err)
			}
			assertTree(t, dest, m)
			if _, ok := loadTreeVerified(view.root, m)["z.bin"]; ok {
				t.Fatal("chunks-only repair fabricated a full-file verification cache")
			}
		})
	}
}
func TestStartupCacheFastPathAndExplicitRecheck(t *testing.T) {
	m, data, _, _ := treeFixture(t)
	dest := t.TempDir()
	startupOriginals(t, dest, m, data)
	view, n := startupFixture(t, dest, m, "keep", false)
	if err := view.verifyOriginals(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	if n.snapshot().ExistingCheckedBytes != m.Size {
		t.Fatal("local check byte count is wrong")
	}
	inv, err := inspectTreeMetadata(context.Background(), dest, m, false)
	if err != nil || !inv.Complete || inv.ReusedFiles != m.fileCount() {
		t.Fatalf("%+v %v", inv, err)
	}
	inv, err = inspectTreeMetadata(context.Background(), dest, m, true)
	if err != nil || inv.Complete || inv.UnverifiedFiles != m.fileCount() {
		t.Fatalf("recheck trusted cached content: %+v %v", inv, err)
	}
}
func TestStartupRejectsMutationAfterMetadataSnapshot(t *testing.T) {
	m, data, _, _ := treeFixture(t)
	dest := t.TempDir()
	startupOriginals(t, dest, m, data)
	view, n := startupFixture(t, dest, m, "resume", false)
	file := filepath.Join(dest, "a.bin")
	bad := bytes.Repeat([]byte("!"), 45001)
	if err := os.WriteFile(file, bad, 0640); err != nil {
		t.Fatal(err)
	}
	if err := view.prepare(context.Background(), n); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("mutation not detected: %v", err)
	}
	got, _ := os.ReadFile(file)
	if !bytes.Equal(got, bad) {
		t.Fatal("concurrent writer's file was overwritten")
	}
}
func TestStartupRejectsSymlinkBeforeServing(t *testing.T) {
	m, data, _, _ := treeFixture(t)
	dest := t.TempDir()
	startupOriginals(t, dest, m, data)
	if err := os.Remove(filepath.Join(dest, "a.bin")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("z.bin", filepath.Join(dest, "a.bin")); err != nil {
		t.Fatal(err)
	}
	if view, err := openStartupTree(dest, m, false); err == nil {
		view.Close()
		t.Fatal("symlink accepted as an original")
	}
}
func TestPreparationNodeInventoryCountedOnce(t *testing.T) {
	m, _, _, _ := treeFixture(t)
	j, ctx, p, finish := progressFixture(t)
	defer finish(nil)
	j.Hosts, j.Fanout, j.Existing = []string{"one", "two"}, 2, "keep"
	bin := t.TempDir()
	script := "#!/bin/sh\ncase \"$*\" in\n *tree-check*) case \"$*\" in *--metadata-only*) echo '{\"Complete\":true,\"Files\":4,\"ReusedFiles\":4}' ;; *) exit 9 ;; esac ;;\n *) cat >/dev/null ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	p.phase("checking-existing", "metadata", 0, 0, len(j.Hosts))
	manifest, _ := json.Marshal(m)
	complete, err := preflightTreeMode(ctx, sshRunner{quiet: true}, &j, m, manifest, true)
	if err != nil || !complete {
		t.Fatalf("%t %v", complete, err)
	}
	progress := p.snapshot()
	if progress.NodesDone != 2 || progress.NodesTotal != 2 {
		t.Fatalf("nodes counted more than once: %+v", progress)
	}
}
func TestSSHDeadlineIsNotReportedAsControllerCancellation(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, err := (sshRunner{quiet: true}).run(ctx, "unused.invalid", "expired operation", "true", nil)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "operation deadline") || strings.Contains(err.Error(), "controller") {
		t.Fatalf("misleading timeout: %v", err)
	}
}
func TestStartupDisplayNeverCountsCheckingNodeAsComplete(t *testing.T) {
	output, err := os.CreateTemp(t.TempDir(), "display")
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = output
	all := render([]observation{{addr: "test:1", st: Status{
		Size: 100, VerifiedBytes: 100, Complete: true, CheckingExisting: true,
		ExistingCheckedBytes: 50, ExistingTotalBytes: 100,
	}}}, nil, false)
	os.Stdout = saved
	if _, err := output.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(output)
	output.Close()
	if err != nil || all || !bytes.Contains(b, []byte("VERIFYING LOCAL")) {
		t.Fatalf("local check falsely counted complete: %t %s %v", all, b, err)
	}
}

func TestStartupLateConflictsUseSavedDecision(t *testing.T) {
	for _, policy := range []string{"resume", "replace"} {
		t.Run(policy, func(t *testing.T) {
			m, _, _, _ := treeFixture(t)
			bin := t.TempDir()
			captured := filepath.Join(bin, "peer-config.json")
			script := "#!/bin/sh\ncase \"$*\" in\n *peer.json.next*) cat > " + shellQuote(captured) + " ;;\n *agent*start*) echo started ;;\n *) cat >/dev/null ;;\nesac\n"
			if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			j := Job{Dir: t.TempDir(), ID: "late-conflict", Hosts: []string{"later"}, Port: 19700, Existing: policy}
			rows := []observation{{addr: "later:19700", st: Status{ExistingConflict: true, Error: "destination conflict: z"}}}
			manifest, _ := json.Marshal(m)
			if err := resolveExistingDecisions(context.Background(), sshRunner{quiet: true}, &j, m, manifest, rows); err != nil {
				t.Fatal(err)
			}
			var options Options
			b, err := os.ReadFile(captured)
			if err != nil {
				t.Fatal("late conflict was never restarted", err)
			}
			if err := json.Unmarshal(b, &options); err != nil || options.Existing != policy {
				t.Fatalf("%+v %v", options, err)
			}
		})
	}
}
func TestStartupFailedConflictRestartDoesNotBusyRetry(t *testing.T) {
	j := Job{Hosts: []string{"later"}, Existing: "resume", NodeIssues: map[string]string{"later": "resolving-existing: unavailable"}}
	rows := []observation{{st: Status{ExistingConflict: true}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := resolveExistingDecisions(ctx, sshRunner{}, &j, Manifest{}, nil, rows); err != nil {
		t.Fatal("failed restart was retried", err)
	}
}
