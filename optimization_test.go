package main

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNativeLocalCopiesVerifyAllBytesWithoutNetwork(t *testing.T) {
	for _, mode := range []string{"full", "chunks"} {
		t.Run(mode, func(t *testing.T) {
			m, _, plan, roots := algorithmFixture(t, "blake3", "blake3")
			dest := t.TempDir()
			store, e := openTreeStore(dest, m, "resume")
			if e != nil {
				t.Fatal(e)
			}
			defer store.Close()
			view, e := newSourceView(context.Background(), Job{Inputs: plan, Sources: roots}, m)
			if e != nil {
				t.Fatal(e)
			}
			defer view.Close()
			n := &Node{m: m, f: store, localSource: view, have: make([]bool, len(m.Chunks)), busy: make([]bool, len(m.Chunks)), peerBusy: map[string]int{}, from: map[string]int64{}, o: Options{File: dest, Verify: mode, Workers: 4}, lastProgress: time.Now()}
			for range m.Chunks {
				i, f, ctx := n.reserve(context.Background())
				if i < 0 || f.addr != localSourceName {
					t.Fatal("local data was not preferred")
				}
				e = n.download(ctx, i, f.addr)
				n.finishRequest(context.Background(), i, f, e)
				if e != nil {
					t.Fatal(e)
				}
			}
			if n.downloaded.Load() != 0 || n.wireReceived.Load() != 0 || n.localCopied.Load() != m.Size || n.from[localSourceName] != m.Size {
				t.Fatal("local copy falsely counted as network or missed bytes")
			}
			if e = n.finalize(); e != nil {
				t.Fatal(e)
			}
			assertAlgorithmTree(t, m, plan, dest)
		})
	}
}
func TestLocalSourceDamageNeverPublishesOrBlamesNetwork(t *testing.T) {
	m, _, plan, roots := algorithmFixture(t, "blake3", "blake3")
	store, e := openTreeStore(t.TempDir(), m, "resume")
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	view, e := newSourceView(context.Background(), Job{Inputs: plan, Sources: roots}, m)
	if e != nil {
		t.Fatal(e)
	}
	defer view.Close()
	n := &Node{m: m, f: store, localSource: view, have: make([]bool, len(m.Chunks)), busy: make([]bool, len(m.Chunks)), peerBusy: map[string]int{}, from: map[string]int64{}, o: Options{Verify: "chunks", Workers: 1}}
	for _, p := range plan {
		if p.Entry.Kind == "file" && p.Entry.Size > 0 {
			f, e := os.OpenFile(p.Source, os.O_WRONLY, 0)
			if e != nil {
				t.Fatal(e)
			}
			_, e = f.WriteAt([]byte("bad"), 0)
			f.Close()
			if e != nil {
				t.Fatal(e)
			}
			break
		}
	}
	i, f, ctx := n.reserve(context.Background())
	if i < 0 {
		t.Fatal("missing local request")
	}
	e = n.download(ctx, i, f.addr)
	var local *localSourceError
	if !errors.As(e, &local) {
		t.Fatalf("expected terminal local error: %v", e)
	}
	n.finishRequest(context.Background(), i, f, e)
	if n.have[i] || n.perf.Retries != 0 || n.downloaded.Load() != 0 || n.localCopied.Load() != 0 {
		t.Fatal("damage accepted or counted as network retry")
	}
}
func TestLargeSchedulerSearchIsBoundedAndNotPrefixOnly(t *testing.T) {
	const count = 30966
	n := &Node{have: make([]bool, count), busy: make([]bool, count), peerBusy: map[string]int{}}
	for p := 0; p < 9; p++ {
		have := make([]bool, count)
		for i := range have {
			have[i] = true
		}
		n.remotes = append(n.remotes, remoteFor(string(rune('a'+p)), have))
	}
	outside := false
	for attempt := 0; attempt < 30; attempt++ {
		before := n.perf.SchedulerCandidates
		i, addr := n.choose()
		if i < 0 {
			t.Fatal("valid data unavailable")
		}
		if n.perf.SchedulerCandidates-before > 256 {
			t.Fatal("unbounded candidate search")
		}
		outside = outside || i >= 256
		n.busy[i] = false
		n.peerBusy[addr]--
	}
	if !outside {
		t.Fatal("search starved chunks outside a fixed prefix")
	}
}
func TestCompletionSummarySeparatesDataAndTiming(t *testing.T) {
	start := time.Unix(100, 0)
	transfer := start.Add(20 * time.Second)
	finish := transfer.Add(30 * time.Second)
	rows := []observation{
		{st: Status{Complete: true, VerifiedBytes: 100 << 20, Downloaded: 100 << 20, Performance: &Performance{TransferSeconds: 10}}},
		{st: Status{Complete: true, VerifiedBytes: 200 << 20, LocalCopied: 200 << 20}},
		{st: Status{Complete: true, VerifiedBytes: 50 << 20}},
		{err: errors.New("offline")},
	}
	s := completionSummary(start, transfer, finish, rows)
	if s.TotalSeconds != 50 || s.PreparationSeconds != 20 || s.DistributionSeconds != 30 || s.CompletedNodes != 3 || s.TotalNodes != 4 {
		t.Fatal("incorrect elapsed/coverage")
	}
	if s.NetworkReceivedBytes != 100<<20 || s.LocalCopiedBytes != 200<<20 || s.ReusedBytes != 50<<20 {
		t.Fatal("data classes mixed")
	}
	if math.Abs(s.AverageNetworkMiBps-100.0/50) > 1e-9 || s.MedianNodeNetworkMiBps != 10 {
		t.Fatal("incorrect average-rate denominator")
	}
	z := completionSummary(start, start, start, rows)
	if math.IsNaN(z.AverageNetworkMiBps) || math.IsInf(z.AverageNetworkMiBps, 0) {
		t.Fatal("invalid zero-duration rate")
	}
}

func TestResumedSummaryDoesNotCountOldPeerTrafficAsNewSpeed(t *testing.T) {
	start := time.Unix(1, 0)
	old := Status{Instance: "same-peer", Downloaded: 90 << 20, Performance: &Performance{TransferSeconds: 90}}
	now := Status{Instance: "same-peer", Complete: true, VerifiedBytes: 100 << 20, Downloaded: 100 << 20, Performance: &Performance{TransferSeconds: 100}}
	rows := []observation{{addr: "node", st: now}}
	s := completionSummary(start, start, start.Add(10*time.Second), rows, map[string]Status{"node": old})
	if s.NetworkReceivedBytes != 10<<20 || s.ReusedBytes != 90<<20 || s.MedianNodeNetworkMiBps != 1 {
		t.Fatal("previous run inflated current rate")
	}
	rows[0].st.Instance = "restarted-peer"
	s = completionSummary(start, start, start.Add(10*time.Second), rows, map[string]Status{"node": old})
	if s.NetworkReceivedBytes != 100<<20 {
		t.Fatal("restarted peer counters were incorrectly subtracted")
	}
}

func TestSparseSourceAvailabilityStillUsesIdleWorker(t *testing.T) {
	const count = 30966
	n := &Node{have: make([]bool, count), busy: make([]bool, count), peerBusy: map[string]int{}}
	sparse := make([]bool, count)
	sparse[count-3] = true
	n.remotes = []Remote{remoteFor("sparse", sparse)}
	for attempt := 0; attempt < 20; attempt++ {
		before := n.perf.SchedulerCandidates
		i, addr := n.choose()
		if i != count-3 || addr != "sparse" {
			t.Fatal("bounded sample missed the only available data")
		}
		if n.perf.SchedulerCandidates-before > 256 {
			t.Fatal("fallback did unbounded peer scoring")
		}
		n.busy[i] = false
		n.peerBusy[addr]--
	}
}

func TestLivePeerTrafficDuringPreparationUsesWholeRunDenominator(t *testing.T) {
	start := time.Unix(1, 0)
	old := Status{Instance: "live", Downloaded: 90 << 20, Performance: &Performance{TransferSeconds: 90}}
	now := Status{Instance: "live", Complete: true, VerifiedBytes: 100 << 20, Downloaded: 100 << 20, Performance: &Performance{TransferSeconds: 190}}
	s := completionSummary(start, start.Add(90*time.Second), start.Add(100*time.Second), []observation{{addr: "node", st: now}}, map[string]Status{"node": old})
	if math.Abs(s.AverageNetworkMiBps-.1) > 1e-9 {
		t.Fatal("traffic during preparation inflated the average")
	}
}

func TestCompletedRecordRetainsOptionalSummaryAndLocalCounters(t *testing.T) {
	j := Job{ID: "saved", Dir: t.TempDir(), Hosts: []string{"node"}, Port: 19700}
	for _, current := range []bool{false, true} {
		record := map[string]any{"Finished": "2026-11-16T00:00:00Z", "Nodes": map[string]Status{"node:19700": {Complete: true, LocalCopied: 123}}}
		if current {
			record["Summary"] = CompletionSummary{TotalSeconds: 12, LocalCopiedBytes: 123}
		}
		b, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(j.Dir, "result.json"), b, 0600); err != nil {
			t.Fatal(err)
		}
		rows, report, err := completedJobRows(j)
		if err != nil {
			t.Fatal(err)
		}
		b, err = json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Summary *CompletionSummary
			Nodes   map[string]Status
		}
		if err = json.Unmarshal(b, &got); err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || got.Nodes["node:19700"].LocalCopied != 123 {
			t.Fatal("saved local counters lost")
		}
		if current && (got.Summary == nil || got.Summary.TotalSeconds != 12 || got.Summary.LocalCopiedBytes != 123) {
			t.Fatal("saved summary lost")
		}
		if !current && got.Summary != nil {
			t.Fatal("fabricated summary for old record")
		}
	}
}
