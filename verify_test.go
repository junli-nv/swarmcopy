package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func startTestVerifier(t *testing.T, n *Node) (context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	n.o.PipelineVerify = true
	n.verifier = newOrderedVerifier(n)
	go n.verifier.run(ctx)
	t.Cleanup(func() {
		cancel()
		select {
		case <-n.verifier.done:
		case <-time.After(time.Second):
			t.Error("verifier goroutine leaked")
		}
	})
	return ctx, cancel
}
func awaitTestVerifier(t *testing.T, n *Node) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return n.verifier.wait(ctx)
}
func TestPipelineWaitsForPrefixAndPublishesSameSHA(t *testing.T) {
	n, data := fixture(t)
	n.o.File = filepath.Join(t.TempDir(), "final")
	n.have[1] = true
	startTestVerifier(t, n)
	select {
	case <-n.verifier.done:
		t.Fatal("hashed missing prefix")
	case <-time.After(20 * time.Millisecond):
	}
	if p := n.snapshot().Performance; p.FileHashBytes != 0 || n.complete {
		t.Fatal("unready data was hashed or published")
	}
	n.mu.Lock()
	n.have[0] = true
	n.verifier.signal()
	n.mu.Unlock()
	sum, err := awaitTestVerifier(t, n)
	if err != nil || sum != digest(data) {
		t.Fatalf("wrong ordered whole hash %s: %v", sum, err)
	}
	if n.complete {
		t.Fatal("digest completion bypassed fsync/publication")
	}
	if _, err := os.Stat(n.o.File); !os.IsNotExist(err) {
		t.Fatal("file published before finalize")
	}
	if err = n.finalize(); err != nil {
		t.Fatal(err)
	}
	p := n.snapshot().Performance
	if !n.complete || p.FileHashBytes != int64(len(data)) || p.FileHashBytesAtTransferEnd != int64(len(data)) {
		t.Fatalf("wrong unique hash progress: %+v", p)
	}
	if got, _ := os.ReadFile(n.o.File); !bytes.Equal(got, data) {
		t.Fatal("published file changed")
	}
}
func TestPipelineRejectsDiskBytesInsteadOfTrustingHave(t *testing.T) {
	n, _ := fixture(t)
	n.o.File = filepath.Join(t.TempDir(), "final")
	n.f.WriteAt([]byte("corruption"), 0)
	n.have[0] = true
	n.have[1] = true
	startTestVerifier(t, n)
	_, err := awaitTestVerifier(t, n)
	if err == nil || !strings.Contains(err.Error(), "SHA256 mismatch") {
		t.Fatalf("disk corruption accepted: %v", err)
	}
	if err = n.finalize(); err == nil {
		t.Fatal("bad disk data published")
	}
	if _, err = os.Stat(n.o.File); !os.IsNotExist(err) {
		t.Fatal("corrupt final exists")
	}
}
func TestPipelineRejectsInconsistentWholeManifestHash(t *testing.T) {
	n, _ := fixture(t)
	n.o.File = filepath.Join(t.TempDir(), "final")
	n.m.SHA256 = strings.Repeat("0", 64)
	n.have[0] = true
	n.have[1] = true
	startTestVerifier(t, n)
	_, err := awaitTestVerifier(t, n)
	if err == nil {
		t.Fatal("chunk validity incorrectly substituted for full SHA256")
	}
	if err = n.finalize(); err == nil {
		t.Fatal("inconsistent whole SHA published")
	}
}
func TestPipelineCancellationRetainsPartial(t *testing.T) {
	n, _ := fixture(t)
	n.o.File = filepath.Join(t.TempDir(), "final")
	_, cancel := startTestVerifier(t, n)
	cancel()
	_, err := awaitTestVerifier(t, n)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("missing cancellation: %v", err)
	}
	if info, err := n.f.Stat(); err != nil || info.Size() != n.m.Size {
		t.Fatal("partial lost after cancellation")
	}
	if _, err = os.Stat(n.o.File); !os.IsNotExist(err) {
		t.Fatal("canceled file published")
	}
}
func TestPipelineCanceledFinalizeNeverPublishes(t *testing.T) {
	n, _ := fixture(t)
	n.o.File = filepath.Join(t.TempDir(), "final")
	n.have[0] = true
	n.have[1] = true
	startTestVerifier(t, n)
	if _, err := awaitTestVerifier(t, n); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := n.finalizeContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled finalize: %v", err)
	}
	if _, err := os.Stat(n.o.File); !os.IsNotExist(err) {
		t.Fatal("published after cancellation")
	}
}
func TestPipelineCoalescedNotificationsAndShortLastChunk(t *testing.T) {
	n, data := fixture(t)
	startTestVerifier(t, n)
	for j := 0; j < 100; j++ {
		n.verifier.signal()
	}
	n.mu.Lock()
	n.have[0] = true
	n.have[1] = true
	n.verifier.signal()
	n.mu.Unlock()
	sum, err := awaitTestVerifier(t, n)
	if err != nil || sum != digest(data) || n.snapshot().Performance.FileHashBytes != n.m.Size {
		t.Fatalf("lost notification or wrong last chunk: %v", err)
	}
}
func TestPipelineReadFailureIsTerminal(t *testing.T) {
	n, _ := fixture(t)
	n.have[0] = true
	n.have[1] = true
	n.f.Close()
	startTestVerifier(t, n)
	_, err := awaitTestVerifier(t, n)
	if err == nil || n.snapshot().Error == "" || n.complete {
		t.Fatal("read failure ignored")
	}
}
func TestSerialHashRangeMatchesReferenceAndBufferBoundaries(t *testing.T) {
	n, data := fixture(t)
	for _, size := range []int{1, 63, 64, 65, 4096, 1 << 20} {
		h := sha256.New()
		work, err := hashRange(context.Background(), n.f, h, 0, n.m.Size, make([]byte, size), nil)
		if err != nil || work.Bytes != n.m.Size || hex.EncodeToString(h.Sum(nil)) != digest(data) {
			t.Fatalf("size=%d work=%+v err=%v", size, work, err)
		}
	}
}

func TestVerifyWindowPreservesLocalRarityAndGlobalFallback(t *testing.T) {
	n, _ := fixture(t)
	n.o = Options{PipelineVerify: true, VerifyWindowChunks: 2, Workers: 4}
	n.have = make([]bool, 8)
	n.busy = make([]bool, 8)
	n.m.Size = 8 * n.m.ChunkSize
	n.m.Chunks = make([]string, 8)
	for i := 0; i < 3; i++ {
		n.have[i] = true
	}
	n.perf.FileHashBytes = 3 * n.m.ChunkSize
	all := []bool{true, true, true, true, true, true, true, true}
	n.remotes = []Remote{remoteFor("all:1", all), remoteFor("front:1", []bool{false, false, false, true, true, false, false, false}), remoteFor("common:1", []bool{false, false, false, false, true, false, false, false})}
	i, _ := n.choose()
	if i != 3 {
		t.Fatalf("did not prefer rarest within upcoming window: %d", i)
	}
	n.busy[3] = false
	n.have[3] = true
	n.have[4] = true
	n.peerBusy = map[string]int{}
	i, _ = n.choose()
	if i < 5 {
		t.Fatalf("ready window stalled global fallback: %d", i)
	}
	n.busy = make([]bool, 8)
	n.peerBusy = map[string]int{}
	n.perf.FileHashBytes = 6 * n.m.ChunkSize
	i, _ = n.choose()
	if i < 6 {
		t.Fatalf("window did not follow whole-hash frontier: %d", i)
	}
}
func TestVerifyWindowDisabledKeepsGlobalRarity(t *testing.T) {
	n, _ := fixture(t)
	n.o = Options{PipelineVerify: true, Workers: 4}
	n.remotes = []Remote{remoteFor("all:1", []bool{true, true}), remoteFor("common:1", []bool{true, false})}
	i, _ := n.choose()
	if i != 1 {
		t.Fatalf("disabled window changed rarity: %d", i)
	}
}

func TestVerifyDefaultAndOptOutFlags(t *testing.T) {
	for _, tc := range []struct {
		args     []string
		pipeline bool
		window   int
	}{
		{nil, true, 256}, {[]string{"--pipeline-verify=false"}, false, 0},
		{[]string{"--verify-window-chunks=0"}, true, 0},
		{[]string{"--pipeline-verify=false", "--verify-window-chunks=8"}, false, 8},
	} {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		pipeline := fs.Bool("pipeline-verify", true, "")
		window := fs.Int("verify-window-chunks", 256, "")
		if err := fs.Parse(tc.args); err != nil {
			t.Fatal(err)
		}
		normalizeVerifyFlags(fs, *pipeline, window)
		if *pipeline != tc.pipeline || *window != tc.window {
			t.Fatalf("%v: %v/%d", tc.args, *pipeline, *window)
		}
	}
}
