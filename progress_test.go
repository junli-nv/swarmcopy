package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func progressFixture(t *testing.T) (Job, context.Context, *jobProgress, func(error)) {
	t.Helper()
	host, _ := os.Hostname()
	ticks, _ := startTicks(os.Getpid())
	j := Job{ID: "progress-test", Attempt: "attempt-a", Dir: t.TempDir(), State: "hashing-source", Hosts: []string{"127.0.0.1"}, Port: 19700, ControllerHost: host, ControllerPID: os.Getpid(), ControllerStartTicks: ticks}
	if e := j.save(); e != nil {
		t.Fatal(e)
	}
	ctx, finish := startJobProgress(context.Background(), j, io.Discard)
	return j, ctx, reporter(ctx), finish
}
func TestPreparationProgressETAAndAttemptIsolation(t *testing.T) {
	j, ctx, p, finish := progressFixture(t)
	defer finish(nil)
	p.phase("hashing-source", "hash", 1000, 1, 0)
	reportBytes(ctx, 250)
	p.mu.Lock()
	p.started = time.Now().Add(-10 * time.Second)
	p.mu.Unlock()
	p.emit()
	s, ok := preparationState(j)
	if !ok || !s.EstimateReady || s.ProcessedBytes != 250 || s.BytesPerSecond < 24 || s.BytesPerSecond > 26 || s.ETASeconds < 29 || s.ETASeconds > 31 || s.Complete {
		t.Fatalf("bad progress: %+v", s)
	}
	if !strings.Contains(preparationLine(s), "ETA ~") {
		t.Fatal("ETA missing")
	}
	j.Attempt = "attempt-b"
	s, ok = preparationState(j)
	if !ok || s.ProcessedBytes != 0 {
		t.Fatal("previous attempt leaked into current progress")
	}
}
func TestContextReaderCancelsBeforeNextRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := contextReader{ctx, strings.NewReader("data")}
	cancel()
	n, e := r.Read(make([]byte, 4))
	if n != 0 || !errors.Is(e, context.Canceled) {
		t.Fatalf("%d %v", n, e)
	}
}
func TestSourceHashCancellationDoesNotSaveManifestOrCache(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "data")
	os.WriteFile(src, bytes.Repeat([]byte("source"), 1000), 0600)
	plan, roots, e := buildSourcePlan([]string{src})
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	j := Job{Inputs: plan, Sources: roots, Dir: filepath.Join(t.TempDir(), "job")}
	if _, e = sourceManifest(ctx, j); !errors.Is(e, context.Canceled) {
		t.Fatalf("hash ignored cancellation: %v", e)
	}
	if _, e = os.Stat(filepath.Join(filepath.Dir(j.Dir), ".source-manifests")); !os.IsNotExist(e) {
		t.Fatal("cancelled source was cached")
	}
}
func TestStopRequestCancelsPreparation(t *testing.T) {
	j, ctx, p, finish := progressFixture(t)
	p.phase("hashing-source", "hash", 1000, 1, 0)
	if e := os.WriteFile(filepath.Join(j.Dir, "stop.request"), []byte("stop"), 0600); e != nil {
		t.Fatal(e)
	}
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("stop request ignored")
	}
	if !errors.Is(context.Cause(ctx), errJobStop) {
		t.Fatal(context.Cause(ctx))
	}
	finish(ctx.Err())
	var s PreparationProgress
	b, _ := os.ReadFile(filepath.Join(j.Dir, "progress.json"))
	json.Unmarshal(b, &s)
	if s.State != "stopped" {
		t.Fatalf("wrong terminal progress: %+v", s)
	}
}
func TestPreparationDoesNotClaimDeadControllerIsActive(t *testing.T) {
	j, _, p, finish := progressFixture(t)
	defer finish(nil)
	p.phase("hashing-source", "hash", 1000, 1, 0)
	j.ControllerStartTicks = "not-the-current-process"
	s, ok := preparationState(j)
	if !ok || s.State != "controller-stopped" {
		t.Fatalf("stale PID accepted: %+v", s)
	}
}
func TestWatchAndStatusDuringPreparationNeverQueryPeer(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); http.Error(w, "should not query", 500) }))
	defer server.Close()
	j, ctx, p, finish := progressFixture(t)
	defer finish(nil)
	host, port, e := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if e != nil {
		t.Fatal(e)
	}
	j.Hosts = []string{host}
	j.Port, e = strconv.Atoi(port)
	if e != nil {
		t.Fatal(e)
	}

	if e := j.save(); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(j.Dir, "job.token"), []byte(strings.Repeat("x", 64)), 0600)
	p.phase("hashing-source", "hash", 1000, 1, 0)
	reportBytes(ctx, 123)
	p.emit()
	if e := watchCommand([]string{"--job", j.Dir, "--plain", "--count", "1"}); e != nil {
		t.Fatal(e)
	}
	if e := statusCommand([]string{"--job", j.Dir}); e != nil {
		t.Fatal(e)
	}
	if calls.Load() != 0 {
		t.Fatal("preparation queried a peer")
	}
	if _, complete, e := renderSavedJob(j, false, false); e != nil || complete {
		t.Fatal("preparation treated as complete")
	}
}
func TestStreamingCountsPayloadNotTarHeaders(t *testing.T) {
	m, _, plan, roots := treeFixture(t)
	_, ctx, p, finish := progressFixture(t)
	defer finish(nil)
	p.phase("importing", "stream", m.Size, m.fileCount(), 0)
	var out bytes.Buffer
	if e := streamSourcePlan(ctx, &out, plan, roots); e != nil {
		t.Fatal(e)
	}
	s := p.snapshot()
	if s.ProcessedBytes != m.Size || s.FilesDone != m.fileCount() {
		t.Fatalf("counted metadata as payload: %+v", s)
	}
}
