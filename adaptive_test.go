package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func remoteFor(addr string, have []bool) Remote {
	return Remote{addr: addr, seen: time.Now(), st: Status{Have: have}}
}
func serverAddr(s *httptest.Server) string { return strings.TrimPrefix(s.URL, "http://") }
func chunkServer(data []byte) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.Write(data)
	}))
}
func TestAdaptiveConcurrencyCapAndBackpressure(t *testing.T) {
	n, _ := fixture(t)
	n.o = Options{Workers: 4, MinWorkers: 1, MaxWorkers: 8, Adaptive: true}
	n.have = make([]bool, 32)
	n.busy = make([]bool, 32)
	for j := 0; j < 16; j++ {
		have := make([]bool, 32)
		for i := range have {
			have[i] = true
		}
		n.remotes = append(n.remotes, remoteFor(strconv.Itoa(j)+":1", have))
	}
	type reserved struct {
		i int
		f *flight
	}
	var mu sync.Mutex
	var requests []reserved
	var wg sync.WaitGroup
	for j := 0; j < 64; j++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			i, f, _ := n.reserve(context.Background())
			if i >= 0 {
				mu.Lock()
				requests = append(requests, reserved{i, f})
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(requests) != 4 {
		t.Fatalf("admitted %d requests, want exactly 4", len(requests))
	}
	n.mu.Lock()
	n.adjustLocked(100, .8)
	target := n.target
	n.mu.Unlock()
	if target != 3 {
		t.Fatalf("write pressure did not reduce target: %d", target)
	}
	if i, _, _ := n.reserve(context.Background()); i >= 0 {
		t.Fatal("admitted more work above reduced target")
	}
	for _, r := range requests {
		n.finishRequest(context.Background(), r.i, r.f, nil)
	}
	n.mu.Lock()
	for j := 0; j < 100; j++ {
		n.adjustLocked(float64(100+j*100), 0)
	}
	if n.target > 8 || n.target < 1 {
		t.Fatal("adaptive target outside bounds")
	}
	for j := 0; j < 100; j++ {
		n.adjustLocked(1, 1)
	}
	if n.target != 1 {
		t.Fatal("backpressure must stop at one worker")
	}
	n.mu.Unlock()
}
func TestAdaptiveSourceAndRarestFirst(t *testing.T) {
	n, _ := fixture(t)
	n.o = Options{Workers: 4, MaxWorkers: 8, Adaptive: true}
	n.initTransfersLocked()
	n.sourceStats["fast:1"] = &sourceState{SourcePerformance: SourcePerformance{NetworkSecondsEWMA: .01}}
	n.sourceStats["slow:1"] = &sourceState{SourcePerformance: SourcePerformance{NetworkSecondsEWMA: 1}}
	n.remotes = []Remote{remoteFor("fast:1", []bool{true, true}), remoteFor("slow:1", []bool{true, true})}
	_, addr := n.choose()
	if addr != "fast:1" {
		t.Fatal("did not use measured faster source")
	}
	n.busy = []bool{false, false}
	n.peerBusy = map[string]int{}
	n.remotes[0].st.Have = []bool{false, true}
	i, addr := n.choose()
	if i != 0 || addr != "slow:1" {
		t.Fatal("speed preference overrode chunk rarity")
	}
}
func TestBusyCooldownSurvivesRediscovery(t *testing.T) {
	n, _ := fixture(t)
	n.o = Options{Workers: 2, MaxWorkers: 4, Adaptive: true}
	n.remotes = []Remote{remoteFor("busy:1", []bool{true, true})}
	i, f, _ := n.reserve(context.Background())
	n.finishRequest(context.Background(), i, f, transferHTTPError{429})
	n.remotes = []Remote{remoteFor("busy:1", []bool{true, true})}
	if i, _ := n.choose(); i >= 0 {
		t.Fatal("fresh status erased busy cooldown")
	}
	if n.perf.BusyResponses != 1 || n.sourceStats["busy:1"].Errors != 1 {
		t.Fatal("missing retry evidence")
	}
	n.sourceStats["busy:1"].cooldown = time.Time{}
	if i, _ := n.choose(); i < 0 {
		t.Fatal("source did not become eligible after cooldown")
	}
}
func TestEndgameCancelsSlowRequestAndCountsOnce(t *testing.T) {
	n, data := fixture(t)
	n.have[1] = true
	n.o = Options{Workers: 2, Endgame: true, HedgeDelay: time.Millisecond}
	entered := make(chan struct{})
	canceled := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(canceled)
	}))
	defer slow.Close()
	fast := chunkServer(data[:65536])
	defer fast.Close()
	n.remotes = []Remote{remoteFor(serverAddr(slow), []bool{true, true})}
	i, original, originalCtx := n.reserve(context.Background())
	done := make(chan error, 1)
	go func() { done <- n.download(originalCtx, i, original.addr) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("slow request not started")
	}
	n.mu.Lock()
	original.started = time.Now().Add(-time.Second)
	n.remotes = append(n.remotes, remoteFor(serverAddr(fast), []bool{true, true}))
	n.mu.Unlock()
	j, hedge, hedgeCtx := n.reserve(context.Background())
	if j != i || hedge == nil || hedge.addr == original.addr {
		t.Fatal("missing distinct-source hedge")
	}
	if k, _, _ := n.reserve(context.Background()); k >= 0 {
		t.Fatal("more than two requests for final chunk")
	}
	if err := n.download(hedgeCtx, j, hedge.addr); err != nil {
		t.Fatal(err)
	}
	n.finishRequest(context.Background(), j, hedge, nil)
	var err error
	select {
	case err = <-done:
	case <-time.After(time.Second):
		t.Fatal("losing request was not canceled")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("losing result %v", err)
	}
	n.finishRequest(context.Background(), i, original, err)
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("cancel did not reach source")
	}
	if n.downloaded.Load() != 65536 || n.from[hedge.addr] != 65536 || n.from[original.addr] != 0 {
		t.Fatal("unique bytes double counted")
	}
	if n.active != 0 || n.busy[i] || len(n.flights) != 0 || n.perf.HedgeRequests != 1 {
		t.Fatal("request state leaked")
	}
}
func TestSimultaneousValidChunksCommitOnce(t *testing.T) {
	n, data := fixture(t)
	s := chunkServer(data[:65536])
	defer s.Close()
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for j := 0; j < 2; j++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- n.download(context.Background(), 0, serverAddr(s)) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n.downloaded.Load() != 65536 || n.perf.DuplicateBytes != 65536 || n.wireReceived.Load() != 131072 {
		t.Fatal("duplicate receive/commit accounting incorrect")
	}
	b := make([]byte, len(data))
	n.f.ReadAt(b, 0)
	if !bytes.Equal(b, data) {
		t.Fatal("duplicate corrupted file")
	}
}
func TestInvalidHedgeDoesNotCancelGoodOriginal(t *testing.T) {
	n, data := fixture(t)
	n.o = Options{Workers: 2, Endgame: true, HedgeDelay: time.Millisecond}
	n.have[1] = true
	bad := chunkServer(bytes.Repeat([]byte("!"), 65536))
	defer bad.Close()
	good := chunkServer(data[:65536])
	defer good.Close()
	n.remotes = []Remote{remoteFor(serverAddr(good), []bool{true, true})}
	i, original, ctx := n.reserve(context.Background())
	n.mu.Lock()
	original.started = time.Now().Add(-time.Second)
	n.remotes = append(n.remotes, remoteFor(serverAddr(bad), []bool{true, true}))
	n.mu.Unlock()
	j, hedge, hctx := n.reserve(context.Background())
	if hedge == nil {
		t.Fatal("missing hedge")
	}
	err := n.download(hctx, j, hedge.addr)
	if err == nil {
		t.Fatal("corrupt hedge accepted")
	}
	n.finishRequest(context.Background(), j, hedge, err)
	if ctx.Err() != nil || n.have[i] {
		t.Fatal("corrupt hedge canceled good source or marked chunk valid")
	}
	err = n.download(ctx, i, original.addr)
	if err != nil {
		t.Fatal(err)
	}
	n.finishRequest(context.Background(), i, original, err)
	if n.downloaded.Load() != 65536 {
		t.Fatal("original failed to repair invalid hedge")
	}
}
func TestLocalWriteErrorNotBlamedOnSource(t *testing.T) {
	n, data := fixture(t)
	n.have[1] = true // Force the full first chunk, not the randomly selected short tail.
	s := chunkServer(data[:65536])
	defer s.Close()
	n.remotes = []Remote{remoteFor(serverAddr(s), []bool{true, true})}
	i, f, ctx := n.reserve(context.Background())
	n.f.Close()
	err := n.download(ctx, i, f.addr)
	var disk *localWriteError
	if !errors.As(err, &disk) {
		t.Fatalf("expected write error, got %v", err)
	}
	n.finishRequest(context.Background(), i, f, err)
	if n.have[i] || n.downloaded.Load() != 0 || n.sourceStats[f.addr].Errors != 0 || n.perf.Retries != 0 {
		t.Fatal("disk error attributed to source or committed")
	}
}
func TestFinalizeStillChecksWholeFile(t *testing.T) {
	n, _ := fixture(t)
	n.o.File = filepath.Join(t.TempDir(), "final")
	n.f.WriteAt([]byte("damage"), 0)
	if err := n.finalize(); err == nil {
		t.Fatal("final hash check bypassed")
	}
	if _, err := os.Stat(n.o.File); !os.IsNotExist(err) {
		t.Fatal("corrupt final file published")
	}
	if n.perf.Stage != "error" || n.perf.FinalHashSeconds <= 0 {
		t.Fatal("missing final hash failure phase")
	}
}
func TestFinalizePerformanceAndPublication(t *testing.T) {
	n, data := fixture(t)
	n.o.File = filepath.Join(t.TempDir(), "final")
	n.transferStarted = time.Now().Add(-time.Second)
	if err := n.scan(); err != nil {
		t.Fatal(err)
	}
	if err := n.finalize(); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(n.o.File)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, _ := io.ReadAll(f)
	if !bytes.Equal(got, data) {
		t.Fatal("final content changed")
	}
	p := n.snapshot().Performance
	if p.Stage != "complete" || p.TransferSeconds < 1 || p.FinalHashSeconds <= 0 || p.PublishSeconds < 0 || !n.complete {
		t.Fatalf("invalid phase metrics: %+v", p)
	}
}

func TestDefaultFloorPreventsWriteShareCollapse(t *testing.T) {
	n, _ := fixture(t)
	n.o = Options{Workers: 4, MaxWorkers: 16, Adaptive: true}
	n.initTransfersLocked()
	for i := 0; i < 100; i++ {
		n.adjustLocked(100, .9)
	}
	if n.target != 4 {
		t.Fatalf("default write feedback collapsed configured floor to %d", n.target)
	}
	n.o.MinWorkers = 2
	for i := 0; i < 100; i++ {
		n.adjustLocked(100, .9)
	}
	if n.target != 2 {
		t.Fatalf("explicit floor not respected: %d", n.target)
	}
}
