package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	randv2 "math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/quic-go/quic-go/http3"
)

const benchmarkUsage = `Measure per-node network-interface throughput.
Default MESH: every node sends and receives; no content hashes or payload files.
Live view: current NIC rates. Final table: average NIC rates and link utilization.
RX/TX are separate directions; bond counters include all member ports once.
NIC counters also include other traffic on that interface.

Examples:
  swarmcopy benchmark --hosts 'HGX[603-608,610-618]'
  swarmcopy benchmark --hosts @hosts.txt --duration 60s
  swarmcopy benchmark --hosts @hosts.txt --workers 128
  swarmcopy benchmark --hosts @hosts.txt --output network.json

Ctrl-C stops this benchmark and cleans its temporary deployment.
Use --verbose for deployment details; --help-all for advanced options.
`

type benchmarkConfig struct {
	NUMA                                                    string
	CPUs                                                    int
	ID, Token, Listen, Transport                            string
	Addresses                                               []string
	Index, Workers, Uploads, PerSource, BufferKiB, BlockMiB int
	QUICSockets, QUICMaxPayload, QUICGSOKiB                 int
	Lifetime                                                time.Duration
	Topology                                                string
}

func (c benchmarkConfig) validate() error {
	if !validBenchmarkNUMA(c.NUMA) {
		return errors.New("numa must be system, interleave, or a node number 0..1023")
	}
	if !strings.HasPrefix(c.ID, "benchmark-") || len(c.Token) < 32 || len(c.Token) > 512 || len(c.Addresses) < 2 || len(c.Addresses) > 1024 || c.Index < 0 || c.Index >= len(c.Addresses) {
		return errors.New("invalid benchmark identity/hosts")
	}
	if c.CPUs < 0 || c.CPUs > 1024 || c.Workers < 1 || c.Workers > 1024 || c.Uploads < 1 || c.Uploads > 1024 || c.PerSource < 1 || c.PerSource > 1024 || c.BufferKiB < 16 || c.BufferKiB > 1024 || c.BlockMiB < 1 || c.BlockMiB > 1024 {
		return errors.New("benchmark workers/uploads/per-source must be 1..1024, buffer-kib 16..1024, block-mib 1..1024")
	}
	if int64(c.Workers)*int64(c.BufferKiB) > 1024*1024 {
		return errors.New("receive buffers exceed 1 GiB per node")
	}
	if !validTransport(c.Transport) || c.QUICSockets < 1 || c.QUICSockets > 32 || c.QUICMaxPayload < 1452 || c.QUICMaxPayload > 8952 || c.QUICGSOKiB < 16 || c.QUICGSOKiB > 64 || c.Lifetime <= 0 || c.Lifetime > 3*time.Hour {
		return errors.New("invalid benchmark transport/lifetime")
	}
	if c.Topology != "mesh" && c.Topology != "ring" && c.Topology != "seed" {
		return errors.New("topology must be mesh, ring or seed")
	}
	if _, e := peers(strings.Join(c.Addresses, ",")); e != nil {
		return e
	}
	_, _, e := net.SplitHostPort(c.Listen)
	return e
}
func (c benchmarkConfig) sources() []string {
	if c.Topology == "ring" {
		return []string{c.Addresses[(c.Index+len(c.Addresses)-1)%len(c.Addresses)]}
	}
	if c.Topology == "seed" {
		if c.Index == 0 {
			return nil
		}
		return c.Addresses[:1]
	}
	// Start after this node, wrapping around, so even a partial mesh is balanced.
	out := make([]string, 0, len(c.Addresses)-1)
	for offset := 1; offset < len(c.Addresses); offset++ {
		out = append(out, c.Addresses[(c.Index+offset)%len(c.Addresses)])
	}
	return out
}
func (c benchmarkConfig) activeWorkers() int { return min(c.Workers, len(c.sources())*c.PerSource) }

type benchmarkStatus struct {
	NIC *benchmarkNICStatus
	benchmarkErrors
	Interface                                string
	LinkGbit                                 float64
	NUMA                                     string
	CPUs                                     int
	ID, Transport, Topology, Error           string
	PID, Workers, UploadLimit                int
	Running, Measuring, Frozen               bool
	Seconds                                  float64
	Received, Sent, Requests, Failures, Busy int64
	RXGbit                                   float64 `json:"ApplicationRXGbit"`
	TXGbit                                   float64 `json:"ApplicationTXGbit"`
	From                                     map[string]int64
}
type benchmarkPeer struct {
	nic                                                  *benchmarkNICMonitor
	errorCounts                                          [benchmarkErrorKinds]atomic.Int64
	baseErrorCounts                                      [benchmarkErrorKinds]int64
	lastError                                            string
	iface                                                string
	linkGbit                                             float64
	c                                                    benchmarkConfig
	payload                                              []byte
	rx, tx, requests, failures, busy                     atomic.Int64
	perSource                                            []atomic.Int64
	slots                                                chan struct{}
	client                                               *http.Client
	mu                                                   sync.Mutex
	running, measuring, frozen                           bool
	epoch                                                time.Time
	baseRX, baseTX, baseRequests, baseFailures, baseBusy int64
	baseFrom                                             []int64
	final                                                benchmarkStatus
	workers                                              sync.WaitGroup
	workCtx                                              context.Context
	stopWork                                             context.CancelFunc
}

func (p *benchmarkPeer) snapshotLocked() benchmarkStatus {
	if p.frozen {
		return p.final
	}
	s := benchmarkStatus{NIC: p.nic.snapshot(), benchmarkErrors: p.errorSnapshotLocked(), Interface: p.iface, LinkGbit: p.linkGbit, NUMA: cNUMA(p.c.NUMA), CPUs: runtime.GOMAXPROCS(0), ID: p.c.ID, Transport: p.c.Transport, Topology: p.c.Topology, PID: os.Getpid(), Workers: p.c.activeWorkers(), UploadLimit: p.c.Uploads, Running: p.running, Measuring: p.measuring, Received: p.rx.Load() - p.baseRX, Sent: p.tx.Load() - p.baseTX, Requests: p.requests.Load() - p.baseRequests, Failures: p.failures.Load() - p.baseFailures, Busy: p.busy.Load() - p.baseBusy, From: map[string]int64{}}
	if p.measuring {
		s.Seconds = time.Since(p.epoch).Seconds()
		s.RXGbit = float64(s.Received) * 8 / s.Seconds / 1e9
		s.TXGbit = float64(s.Sent) * 8 / s.Seconds / 1e9
	}
	for i, a := range p.c.sources() {
		s.From[a] = p.perSource[i].Load() - p.baseFrom[i]
	}
	return s
}
func (p *benchmarkPeer) handler(w http.ResponseWriter, r *http.Request) {
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+p.c.Token)) != 1 {
		http.Error(w, "unauthorized", 401)
		return
	}
	if r.Method != "GET" {
		http.Error(w, "GET only", 405)
		return
	}
	w.Header().Set("X-SCDP-Version", "1")
	if r.URL.Path == "/benchmark/data" {
		if p.c.Transport == "quic" && r.ProtoMajor != 3 {
			http.Error(w, "QUIC required", 400)
			return
		}
		select {
		case p.slots <- struct{}{}:
			defer func() { <-p.slots }()
		default:
			p.busy.Add(1)
			http.Error(w, "busy", 429)
			return
		}
		left := int64(p.c.BlockMiB) << 20
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(left, 10))
		w.Header().Set("Content-Encoding", "identity")
		for left > 0 && r.Context().Err() == nil {
			b := p.payload[:min(left, int64(len(p.payload)))]
			n, e := w.Write(b)
			p.tx.Add(int64(n))
			left -= int64(n)
			if e != nil || n == 0 {
				return
			}
		}
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	switch r.URL.Path {
	case "/benchmark/start":
		if !p.running && !p.frozen && p.workCtx.Err() == nil {
			p.running = true
			for i := 0; i < p.c.activeWorkers(); i++ {
				p.workers.Add(1)
				go func(i int) { defer p.workers.Done(); p.receive(i % len(p.c.sources())) }(i)
			}
		}
	case "/benchmark/reset":
		if !p.running || p.frozen {
			http.Error(w, "not running", 409)
			return
		}
		if p.nic != nil {
			p.nic.reset()
		}
		p.baseRX = p.rx.Load()
		p.baseTX = p.tx.Load()
		p.baseRequests = p.requests.Load()
		p.baseFailures = p.failures.Load()
		p.baseBusy = p.busy.Load()
		for i := range p.baseErrorCounts {
			p.baseErrorCounts[i] = p.errorCounts[i].Load()
		}
		p.lastError = ""
		for i := range p.baseFrom {
			p.baseFrom[i] = p.perSource[i].Load()
		}
		p.epoch = time.Now()
		p.measuring = true
	case "/benchmark/stop":
		if !p.frozen {
			p.final = p.snapshotLocked()
			p.final.Frozen = true
			p.frozen = true
		}
	case "/status":
	default:
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(p.snapshotLocked())
}
func (p *benchmarkPeer) receive(source int) {
	buf := make([]byte, p.c.BufferKiB<<10)
	addr := p.c.sources()[source]
	backoff := 20 * time.Millisecond
	for p.workCtx.Err() == nil {
		kind := benchmarkConnect
		resp, e := request(p.workCtx, p.client, p.c.Token, addr, "/benchmark/data")
		if e == nil {
			switch {
			case resp.StatusCode == 429:
				kind = benchmarkBusy
				e = fmt.Errorf("HTTP 429: source upload slots busy")
			case resp.StatusCode != 200:
				kind = benchmarkHTTP
				e = fmt.Errorf("HTTP %d", resp.StatusCode)
			case p.c.Transport == "quic" && resp.ProtoMajor != 3:
				kind = benchmarkProtocol
				e = fmt.Errorf("QUIC response used HTTP/%d", resp.ProtoMajor)
			default:
				kind = benchmarkRead
				var count int64
				for {
					n, err := resp.Body.Read(buf)
					p.rx.Add(int64(n))
					p.perSource[source].Add(int64(n))
					count += int64(n)
					if err != nil {
						if err != io.EOF {
							e = err
						}
						break
					}
				}
				if e == nil && count != int64(p.c.BlockMiB)<<20 {
					e = io.ErrUnexpectedEOF
				}
				if e == nil {
					p.requests.Add(1)
				}
			}
		}
		if resp != nil {
			resp.Body.Close()
		}
		if e != nil && p.workCtx.Err() == nil {
			p.recordFailure(kind, addr, e)
			backoff = min(backoff*2, time.Second)
			delay := backoff/2 + time.Duration(randv2.Int64N(int64(backoff/2)+1))
			select {
			case <-p.workCtx.Done():
				return
			case <-time.After(delay):
			}
		} else {
			backoff = 20 * time.Millisecond
		}
	}
}

// stdin is an ownership lease: controller exit/SSH disconnect closes it.
func benchmarkPeerCommand() error {
	var c benchmarkConfig
	if e := json.NewDecoder(io.LimitReader(os.Stdin, 1<<20)).Decode(&c); e != nil {
		return e
	}
	if e := c.validate(); e != nil {
		return e
	}
	sig, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(sig, c.Lifetime)
	defer cancel()
	go func() { _, _ = io.Copy(io.Discard, os.Stdin); cancel() }()
	if c.CPUs > 0 {
		runtime.GOMAXPROCS(min(c.CPUs, runtime.NumCPU()))
	}
	return serveBenchmarkPeer(ctx, c)
}
func serveBenchmarkPeer(ctx context.Context, c benchmarkConfig) error {
	data := make([]byte, c.BufferKiB<<10)
	if _, e := rand.Read(data); e != nil {
		return e
	}
	dc, closeData, e := newDataClient(c.Transport, c.Token, c.QUICSockets, c.QUICMaxPayload, c.QUICGSOKiB)
	if e != nil {
		return e
	}
	defer closeData()
	if tr, ok := dc.Transport.(*http.Transport); ok {
		tr.MaxIdleConns = c.Workers * 2
		tr.MaxIdleConnsPerHost = c.PerSource
		tr.DisableCompression = true
	}
	workCtx, stopWork := context.WithCancel(ctx)
	defer stopWork()
	p := &benchmarkPeer{c: c, payload: data, slots: make(chan struct{}, c.Uploads), client: dc, workCtx: workCtx, stopWork: stopWork, perSource: make([]atomic.Int64, len(c.sources())), baseFrom: make([]int64, len(c.sources()))}
	p.iface, p.linkGbit = benchmarkLink(ctx, c.Addresses, c.Index)
	p.nic = newBenchmarkNIC(p.iface, p.linkGbit)
	ln, e := net.Listen("tcp", c.Listen)
	if e != nil {
		return e
	}
	srv := &http.Server{Handler: http.HandlerFunc(p.handler), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192, BaseContext: func(net.Listener) context.Context { return ctx }}
	errs := make(chan error, 2)
	go func() { errs <- srv.Serve(ln) }()
	var closeQUIC = func() {}
	if c.Transport == "quic" {
		tls, e := jobTLS(c.Token)
		if e != nil {
			srv.Close()
			return e
		}
		udp, e := net.ListenPacket("udp", ln.Addr().String())
		if e != nil {
			srv.Close()
			return e
		}
		qs := &http3.Server{TLSConfig: tls, QUICConfig: quicConfig(c.QUICMaxPayload, c.QUICGSOKiB), Handler: http.HandlerFunc(p.handler), MaxHeaderBytes: 8192, IdleTimeout: 30 * time.Second}
		closeQUIC = func() { qs.Close(); udp.Close() }
		go func() { errs <- qs.Serve(udp) }()
	}
	defer func() {
		stopWork()
		p.mu.Lock()
		p.frozen = true
		p.mu.Unlock()
		srv.Close()
		closeQUIC()
		p.workers.Wait()
	}()
	select {
	case <-ctx.Done():
		return nil
	case e := <-errs:
		return e
	}
}

func cNUMA(name string) string {
	if name == "" {
		return "system"
	}
	return name
}
func validBenchmarkNUMA(name string) bool {
	if name == "" || name == "system" || name == "interleave" {
		return true
	}
	n, e := strconv.Atoi(name)
	return e == nil && n >= 0 && n <= 1023 && strconv.Itoa(n) == name
}
func benchmarkLaunch(exe string, c benchmarkConfig) string {
	command := "exec "
	if c.NUMA == "interleave" {
		command += "numactl --interleave=all "
	} else if c.NUMA != "" && c.NUMA != "system" {
		command += "numactl --cpunodebind=" + c.NUMA + " --membind=" + c.NUMA + " "
	}
	return command + shellQuote(exe) + " benchmark-peer"
}
