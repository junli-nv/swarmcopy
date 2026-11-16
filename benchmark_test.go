package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testBenchmarkConfig() benchmarkConfig {
	return benchmarkConfig{ID: "benchmark-test", Token: strings.Repeat("x", 64), Listen: "127.0.0.1:19701", Transport: "tcp", Addresses: []string{"127.0.0.1:19701", "127.0.0.1:19702"}, Workers: 2, Uploads: 4, PerSource: 2, BufferKiB: 16, BlockMiB: 1, QUICSockets: 1, QUICMaxPayload: 1452, QUICGSOKiB: 20, Lifetime: time.Minute, Topology: "mesh"}
}
func TestBenchmarkConfigAndTopology(t *testing.T) {
	c := testBenchmarkConfig()
	if e := c.validate(); e != nil {
		t.Fatal(e)
	}
	c.Workers = 128
	c.PerSource = 8
	if c.activeWorkers() != 8 {
		t.Fatal("per-source cap ignored")
	}
	c.Topology = "seed"
	if c.activeWorkers() != 0 {
		t.Fatal("seed must not download")
	}
	c.Index = 1
	if c.sources()[0] != c.Addresses[0] {
		t.Fatal("seed source")
	}
	c.Topology = "ring"
	if c.sources()[0] != c.Addresses[0] {
		t.Fatal("ring source")
	}
	for _, mutate := range []func(*benchmarkConfig){func(c *benchmarkConfig) { c.Workers = 1025 }, func(c *benchmarkConfig) { c.Uploads = 0 }, func(c *benchmarkConfig) { c.Topology = "bad" }, func(c *benchmarkConfig) { c.BufferKiB = 0 }, func(c *benchmarkConfig) { c.Transport = "udp" }, func(c *benchmarkConfig) { c.Index = 2 }} {
		v := testBenchmarkConfig()
		mutate(&v)
		if v.validate() == nil {
			t.Fatal("invalid config accepted", v)
		}
	}
}
func TestBenchmarkStreamingAuth(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := testBenchmarkConfig()
	p := &benchmarkPeer{c: c, payload: make([]byte, 16<<10), slots: make(chan struct{}, 1), workCtx: ctx, stopWork: cancel}
	s := httptest.NewServer(http.HandlerFunc(p.handler))
	defer s.Close()
	resp, e := http.Get(s.URL + "/benchmark/data")
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal("unauthenticated payload allowed")
	}
	req, _ := http.NewRequest("GET", s.URL+"/benchmark/data", nil)
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, e = http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	n, e := io.CopyBuffer(io.Discard, resp.Body, make([]byte, 32<<10))
	resp.Body.Close()
	if e != nil || n != 1<<20 || len(p.payload) != 16<<10 || p.tx.Load() != n {
		t.Fatal("streaming payload", n, e, p.tx.Load())
	}
}
func TestBenchmarkNetworkAndMeasurement(t *testing.T) {
	for _, transport := range []string{"tcp", "quic"} {
		t.Run(transport, func(t *testing.T) {
			c := testBenchmarkConfig()
			c.Transport = transport
			for i := range c.Addresses {
				ln, e := net.Listen("tcp", "127.0.0.1:0")
				if e != nil {
					t.Fatal(e)
				}
				c.Addresses[i] = ln.Addr().String()
				ln.Close()
			}
			ctx, cancel := context.WithCancel(context.Background())
			exits := make(chan error, 2)
			for i := range c.Addresses {
				cfg := c
				cfg.Index = i
				cfg.Listen = c.Addresses[i]
				go func() { exits <- serveBenchmarkPeer(ctx, cfg) }()
			}
			defer func() {
				cancel()
				for range 2 {
					select {
					case e := <-exits:
						if e != nil {
							t.Error(e)
						}
					case <-time.After(5 * time.Second):
						t.Error("benchmark peer leaked")
					}
				}
			}()
			hc := client()
			defer hc.CloseIdleConnections()
			deadline := time.Now().Add(5 * time.Second)
			for {
				if _, e := benchmarkAll(ctx, hc, c, ""); e == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("listeners not ready")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if transport == "quic" {
				resp, e := request(ctx, hc, c.Token, c.Addresses[0], "/benchmark/data")
				if e != nil {
					t.Fatal(e)
				}
				resp.Body.Close()
				if resp.StatusCode != 400 {
					t.Fatal("QUIC silently fell back to TCP")
				}
			}
			bad := c
			bad.ID = "benchmark-another"
			if _, e := benchmarkCall(ctx, hc, bad, c.Addresses[0], ""); e == nil {
				t.Fatal("accepted unrelated listener")
			}
			if _, e := benchmarkAll(ctx, hc, c, "start"); e != nil {
				t.Fatal(e)
			}
			time.Sleep(120 * time.Millisecond)
			if _, e := benchmarkAll(ctx, hc, c, "reset"); e != nil {
				t.Fatal(e)
			}
			time.Sleep(150 * time.Millisecond)
			nodes, e := benchmarkAll(ctx, hc, c, "stop")
			if e != nil {
				t.Fatal(e)
			}
			for a, s := range nodes {
				if !s.Frozen || !s.Measuring || s.Received == 0 || s.RXGbit <= 0 || s.Seconds < 0.14 || s.Seconds > 2 {
					t.Fatalf("bad measurement %s %+v", a, s)
				}
				again, e := benchmarkCall(ctx, hc, c, a, "")
				if e != nil || again.Received != s.Received || again.Seconds != s.Seconds {
					t.Fatal("frozen window changed", e)
				}
			}
		})
	}
}

func TestBenchmarkNUMALaunch(t *testing.T) {
	for _, name := range []string{"", "system", "interleave", "0", "1", "1023"} {
		c := testBenchmarkConfig()
		c.NUMA = name
		if e := c.validate(); e != nil {
			t.Fatal(name, e)
		}
		cmd := benchmarkLaunch("/tmp/test binary", c)
		if !strings.HasSuffix(cmd, "'/tmp/test binary' benchmark-peer") {
			t.Fatal(cmd)
		}
		if (name == "" || name == "system") && strings.Contains(cmd, "numactl") {
			t.Fatal("unexpected dependency", cmd)
		}
	}
	for _, name := range []string{"-1", "1024", "0;id", "--all", "0 1", "01"} {
		c := testBenchmarkConfig()
		c.NUMA = name
		if c.validate() == nil {
			t.Fatal("accepted invalid NUMA", name)
		}
	}
}
