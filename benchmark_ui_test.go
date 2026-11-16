package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBenchmarkMeshBalancesEveryNode(t *testing.T) {
	for _, count := range []int{3, 60, 160} {
		c := testBenchmarkConfig()
		c.Workers = 64
		c.PerSource = 64
		c.Addresses = nil
		for i := 0; i < count; i++ {
			c.Addresses = append(c.Addresses, fmt.Sprintf("node%03d:19701", i))
		}
		incoming := map[string]int{}
		for i := range c.Addresses {
			c.Index = i
			sources := c.sources()
			for worker := 0; worker < c.activeWorkers(); worker++ {
				a := sources[worker%len(sources)]
				if a == c.Addresses[i] {
					t.Fatal("self source")
				}
				incoming[a]++
			}
		}
		for _, a := range c.Addresses {
			if incoming[a] != 64 {
				t.Fatalf("%d nodes: %s receives %d streams, wanted 64", count, a, incoming[a])
			}
		}
	}
}
func TestBenchmarkNICMonotonicRatesAndReset(t *testing.T) {
	start := time.Now()
	value := benchmarkNICSample{1000, 2000, start}
	monitor := &benchmarkNICMonitor{iface: "bond0", capacity: 400, read: func(string) (benchmarkNICSample, error) { return value, nil }}
	monitor.reset()
	value = benchmarkNICSample{1000 + 100_000_000_000, 2000 + 50_000_000_000, start.Add(2 * time.Second)}
	first := monitor.snapshot()
	if first.RXGbit != 400 || first.TXGbit != 200 {
		t.Fatal(first)
	}
	value = benchmarkNICSample{1000 + 150_000_000_000, 2000 + 150_000_000_000, start.Add(4 * time.Second)}
	second := monitor.snapshot()
	rx, tx, err := benchmarkNICRate(second, first)
	if err != nil || rx != 200 || tx != 400 || second.RXGbit != 300 {
		t.Fatal(rx, tx, err, second)
	}
	value.rx = 1
	if monitor.snapshot().Error == "" {
		t.Fatal("counter reset accepted")
	}
	if _, _, err = benchmarkNICRate(nil, first); err == nil {
		t.Fatal("missing NIC silently accepted")
	}
}
func TestBenchmarkDisplayUsesNICNotApplication(t *testing.T) {
	c := testBenchmarkConfig()
	nodes := map[string]benchmarkStatus{}
	for _, a := range c.Addresses {
		nodes[a] = benchmarkStatus{Received: 1, Sent: 1, RXGbit: 9999, TXGbit: 8888, NIC: &benchmarkNICStatus{benchmarkNICCounter: benchmarkNICCounter{Interface: "bond0", LinkGbit: 400, Seconds: 2, RXGbit: 250, TXGbit: 300}}}
	}
	var out bytes.Buffer
	renderBenchmark(&out, c, nodes, nil, 0, true)
	s := out.String()
	for _, want := range []string{"MESH", "AVERAGE", "bond0/400G", "250.00", "300.00"} {
		if !strings.Contains(s, want) {
			t.Fatal(want, s)
		}
	}
	for _, bad := range []string{"9999", "8888", "Aggregate"} {
		if strings.Contains(s, bad) {
			t.Fatal(bad, s)
		}
	}
	row := nodes[c.Addresses[0]]
	row.BusyResponses = 3
	row.Failures = 3
	row.LastError = "HTTP 429"
	nodes[c.Addresses[0]] = row
	summary := summarizeBenchmark(nodes, c.Addresses)
	if summary.BusyResponses != 3 || summary.OtherErrors != 0 || summary.Assessment != "BUSY" {
		t.Fatal(summary)
	}
	out.Reset()
	renderBenchmark(&out, c, nodes, nil, 0, true)
	if !strings.Contains(out.String(), "not packet-loss") {
		t.Fatal(out.String())
	}
}
func TestBenchmarkReceiveClassifiesBusyAndReadError(t *testing.T) {
	for _, kind := range []benchmarkErrorKind{benchmarkBusy, benchmarkRead} {
		t.Run(fmt.Sprint(kind), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if kind == benchmarkBusy {
					http.Error(w, "busy", 429)
					return
				}
				w.Header().Set("Content-Length", "1048576")
				io.WriteString(w, "short")
			}))
			defer server.Close()
			c := testBenchmarkConfig()
			c.Addresses[1] = strings.TrimPrefix(server.URL, "http://")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			hc := client()
			defer hc.CloseIdleConnections()
			p := &benchmarkPeer{c: c, client: hc, workCtx: ctx, perSource: make([]atomic.Int64, 1), baseFrom: make([]int64, 1)}
			done := make(chan struct{})
			go func() { p.receive(0); close(done) }()
			deadline := time.Now().Add(time.Second)
			for p.failures.Load() == 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("receive did not stop")
			}
			if p.failures.Load() == 0 || p.errorCounts[kind].Load() != p.failures.Load() {
				t.Fatal("wrong error category")
			}
		})
	}
}

func TestBenchmarkBondAndMembersNotDoubleCounted(t *testing.T) {
	start := time.Now()
	delta := uint64(0)
	n := &benchmarkNICMonitor{iface: "bond0", capacity: 400, members: []string{"eth0", "eth1", "eth2", "eth3"}, capacities: map[string]float64{"eth0": 100, "eth1": 100, "eth2": 100, "eth3": 100}}
	n.read = func(name string) (benchmarkNICSample, error) {
		v := delta
		if name != "bond0" {
			v /= 4
		}
		return benchmarkNICSample{rx: 1000 + v, tx: 1000 + v, at: start.Add(time.Duration(delta/50_000_000_000) * time.Second)}, nil
	}
	n.reset()
	delta = 100_000_000_000
	s := n.snapshot()
	if s.RXGbit != 400 || s.TXGbit != 400 || len(s.Members) != 4 {
		t.Fatal(s)
	}
	for _, member := range s.Members {
		if member.RXGbit != 100 || member.TXGbit != 100 {
			t.Fatal(member)
		}
	}
}

func TestBenchmarkLiveViewFitsAndPrioritizesSlowNodes(t *testing.T) {
	c := testBenchmarkConfig()
	c.Addresses = append(c.Addresses, "third:19701")
	nodes := map[string]benchmarkStatus{}
	for i, a := range c.Addresses {
		rate := 390.0
		if i == 2 {
			rate = 100
		}
		nodes[a] = benchmarkStatus{Received: 1, Sent: 1, NIC: &benchmarkNICStatus{benchmarkNICCounter: benchmarkNICCounter{Interface: "bond0", LinkGbit: 400, Seconds: 2, RXGbit: rate, TXGbit: rate}}}
	}
	var out bytes.Buffer
	renderBenchmark(&out, c, nodes, nil, time.Second, false, 1)
	if !strings.Contains(out.String(), "third") || !strings.Contains(out.String(), "Showing 1") || !strings.Contains(out.String(), "3 nodes") {
		t.Fatal(out.String())
	}
	if strings.Contains(out.String(), "390.00") {
		t.Fatal("live view overflowed its row limit", out.String())
	}
	out.Reset()
	renderBenchmark(&out, c, nodes, nil, 0, true, 1)
	if !strings.Contains(out.String(), "390.00") || strings.Contains(out.String(), "Showing 1") {
		t.Fatal("final view omitted nodes", out.String())
	}
}
