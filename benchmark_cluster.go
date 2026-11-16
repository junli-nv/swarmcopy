package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type benchmarkReport struct {
	Metric                                                                   string
	Summary                                                                  benchmarkSummary
	NUMA                                                                     string
	CPUs                                                                     int
	ID, Version, Transport, Topology, State, Error                           string
	ContentVerification                                                      bool
	DurationSeconds, WarmupSeconds, TotalSeconds, ApplicationAggregateRXGbit float64
	Workers, Uploads, PerSource, BufferKiB, BlockMiB                         int
	Nodes                                                                    map[string]benchmarkStatus
}

func benchmarkCall(ctx context.Context, hc *http.Client, c benchmarkConfig, address, action string) (benchmarkStatus, error) {
	var s benchmarkStatus
	path := "/status"
	if action != "" {
		path = "/benchmark/" + action
	}
	child, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	r, e := request(child, hc, c.Token, address, path)
	if e != nil {
		return s, e
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return s, fmt.Errorf("%s %s: HTTP %d", address, action, r.StatusCode)
	}
	if e = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&s); e != nil {
		return s, e
	}
	if s.ID != c.ID || s.Transport != c.Transport || s.Topology != c.Topology {
		return s, fmt.Errorf("%s: benchmark identity/transport mismatch", address)
	}
	return s, nil
}
func benchmarkAll(ctx context.Context, hc *http.Client, c benchmarkConfig, action string) (map[string]benchmarkStatus, error) {
	var mu sync.Mutex
	out := map[string]benchmarkStatus{}
	var failures []error
	_ = forHosts(ctx, c.Addresses, 32, func(ctx context.Context, a string) error {
		s, e := benchmarkCall(ctx, hc, c, a, action)
		mu.Lock()
		defer mu.Unlock()
		if e != nil {
			s.Error = e.Error()
			failures = append(failures, e)
		}
		out[a] = s
		return nil
	})
	if ctx.Err() != nil {
		failures = append(failures, ctx.Err())
	}
	return out, errors.Join(failures...)
}
func benchmarkCommand(args []string) (result error) {
	fs := flag.NewFlagSet("benchmark", flag.ContinueOnError)
	configureUsage(fs, "benchmark")
	var c benchmarkConfig
	verbose := fs.Bool("verbose", false, "show SSH deployment and detailed configuration")
	plain := fs.Bool("plain", false, "disable the live terminal dashboard")
	allHelp := fs.Bool("help-all", false, "show all advanced options")
	fs.Usage = func() { benchmarkHelp(fs, false) }
	hosts := fs.String("hosts", "", "compute hosts, bracket expression or @file; at least two distinct hosts")
	port := fs.Int("port", 19701, "dedicated benchmark TCP port; QUIC also uses same-number UDP")
	fs.StringVar(&c.Transport, "transport", "tcp", "tcp or quic; no fallback")
	fs.StringVar(&c.Topology, "topology", "mesh", "mesh (all send/receive), ring, or seed (first host sends)")
	duration := fs.Duration("duration", 30*time.Second, "measured traffic duration, excluding deployment and warmup (1s..2h)")
	warmup := fs.Duration("warmup", 3*time.Second, "untimed warmup (0..1m)")
	fs.StringVar(&c.NUMA, "numa", "system", "system, interleave, or NUMA node number; non-system requires numactl on targets")
	fs.IntVar(&c.CPUs, "cpus", 32, "Go CPU parallelism per benchmark peer; 0=runtime default, capped to host CPU count")
	fs.IntVar(&c.Workers, "workers", 64, "download streams per node (1..1024)")
	fs.IntVar(&c.Uploads, "uploads", 256, "maximum active upload streams per node (1..1024)")
	fs.IntVar(&c.PerSource, "per-source", 64, "maximum download streams to each source (1..1024)")
	fs.IntVar(&c.BufferKiB, "buffer-kib", 1024, "receive buffer per stream and one shared send buffer (16..1024 KiB)")
	fs.IntVar(&c.BlockMiB, "block-mib", 64, "bytes per streaming response; does not allocate a response-sized buffer (1..1024 MiB)")
	fs.IntVar(&c.QUICSockets, "quic-sockets", 4, "QUIC client socket shards (1..32)")
	fs.IntVar(&c.QUICMaxPayload, "quic-max-payload", 8952, "QUIC payload ceiling; requires PMTU confirmation (1452..8952)")
	fs.IntVar(&c.QUICGSOKiB, "quic-gso-kib", 64, "QUIC GSO batch ceiling (16..64 KiB)")
	binDir := fs.String("bin-dir", "dist", "directory containing matching benchmark-enabled architecture binaries")
	user := fs.String("ssh-user", "", "SSH login username")
	ask := fs.Bool("ask-pass", false, "prompt once for the same SSH password on all nodes")
	cipher := fs.String("ssh-cipher", "auto", "SSH control/deployment cipher preference")
	var opts sourceFlags
	fs.Var(&opts, "ssh-opt", "additional SSH KEY=VALUE, repeatable")
	output := fs.String("output", "", "optional NEW JSON result path; no token/config in report")
	nodeTimeout := fs.Duration("node-timeout", time.Minute, "per-node deployment/readiness timeout (1s..5m)")
	if len(args) == 0 {
		fs.Usage()
		return errors.New("--hosts is required; see examples above")
	}
	if e := fs.Parse(args); e != nil {
		return e
	}
	if *allHelp {
		benchmarkHelp(fs, true)
		return nil
	}
	if fs.NArg() != 0 || *duration < time.Second || *duration > 2*time.Hour || *warmup < 0 || *warmup > time.Minute || *port < 1 || *port > 65535 || *nodeTimeout < time.Second || *nodeTimeout > 5*time.Minute || !validSSHCipher(*cipher) {
		return errors.New("invalid benchmark arguments; see benchmark --help")
	}
	hs, e := hostList(*hosts)
	if e != nil {
		return e
	}
	seen := map[string]bool{}
	for _, h := range hs {
		key := sourceHostKey(h)
		if seen[key] {
			return fmt.Errorf("duplicate compute host %s", h)
		}
		seen[key] = true
	}
	c.ID = "benchmark-" + time.Now().UTC().Format("20060102T150405") + "-" + randomID()
	c.Token = randomID() + randomID()
	c.Listen = fmt.Sprintf(":%d", *port)
	c.Lifetime = *duration + *warmup + 2*(*nodeTimeout) + time.Minute
	if c.Lifetime > 3*time.Hour {
		return errors.New("benchmark lifetime exceeds limit")
	}
	j := Job{ID: c.ID, Hosts: hs, Port: *port, Dest: "/tmp", BinDir: *binDir}
	c.Addresses = j.addresses()
	if e = c.validate(); e != nil {
		return e
	}
	if _, e = mergeSSHOptions(nil, opts); e != nil {
		return e
	}
	var reportFile *os.File
	if *output != "" {
		reportFile, e = os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return e
		}
		defer reportFile.Close()
	}
	report := benchmarkReport{Metric: "network-interface counters", NUMA: c.NUMA, CPUs: c.CPUs, ID: c.ID, Version: version, Transport: c.Transport, Topology: c.Topology, State: "preparing", DurationSeconds: duration.Seconds(), WarmupSeconds: warmup.Seconds(), Workers: c.Workers, Uploads: c.Uploads, PerSource: c.PerSource, BufferKiB: c.BufferKiB, BlockMiB: c.BlockMiB, Nodes: map[string]benchmarkStatus{}}
	overall := time.Now()
	defer func() {
		report.TotalSeconds = time.Since(overall).Seconds()
		if result != nil {
			report.State = "failed"
			report.Error = result.Error()
		}
		if reportFile != nil {
			enc := json.NewEncoder(reportFile)
			enc.SetIndent("", "  ")
			if e := enc.Encode(report); e != nil {
				result = errors.Join(result, e)
			}
			if e := reportFile.Sync(); e != nil {
				result = errors.Join(result, e)
			}
		}
	}()
	sig, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(sig, c.Lifetime)
	defer cancel()
	env, closePassword, e := passwordEnvironment(ctx, *ask)
	if e != nil {
		return e
	}
	defer closePassword()
	runner := sshRunner{quiet: !*verbose, env: env, user: *user, password: *ask, cipher: *cipher, options: opts}
	dir, e := os.MkdirTemp("", "swarmcopy-benchmark-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(dir)
	j.Dir = dir
	if e = os.WriteFile(filepath.Join(dir, "job.token"), []byte(c.Token), 0600); e != nil {
		return e
	}
	fmt.Printf("%s / %s | %d nodes | %s measurement + %s warmup\n", strings.ToUpper(c.Topology), strings.ToUpper(c.Transport), len(hs), *duration, *warmup)
	fmt.Println("Measuring each node's routed NIC; RX and TX are separate directions.")
	if *verbose {
		fmt.Printf("BENCHMARK ID: %s\nMODE: %s/%s | content verification OFF | payload disk I/O OFF\n", c.ID, c.Transport, c.Topology)
		fmt.Printf("Per-node receive buffers <= %.2f MiB + shared send %.2f MiB; protocol/socket memory additional.\n", float64(c.Workers*c.BufferKiB)/1024, float64(c.BufferKiB)/1024)
		fmt.Printf("Requested workers=%d uploads=%d per-source=%d | warmup=%s measurement=%s | port=%d\n", c.Workers, c.Uploads, c.PerSource, *warmup, *duration, *port)
		fmt.Printf("DEPLOYING: SSH transfers executables/configuration only; temporary path %s\n", j.remoteDir())
	}
	fmt.Printf("Preparing %d nodes...\n", len(hs))
	peerCtx, cancelPeers := context.WithTimeout(context.Background(), c.Lifetime+20*time.Second)
	defer cancelPeers()
	var leases []*os.File
	var peersWG sync.WaitGroup
	var peerExitMu sync.Mutex
	peerExits := map[string]error{}
	exits := make(chan error, len(hs))
	defer func() {
		for _, w := range leases {
			w.Close()
		}
		done := make(chan struct{})
		go func() { peersWG.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			result = errors.Join(result, fmt.Errorf("benchmark peer shutdown unconfirmed; temporary path retained: %s", j.remoteDir()))
			return
		}
		clean, finish := context.WithTimeout(context.Background(), 30*time.Second)
		defer finish()
		err := forHosts(clean, hs, 8, func(child context.Context, h string) error {
			peerExitMu.Lock()
			exitErr := peerExits[h]
			peerExitMu.Unlock()
			if exitErr != nil {
				return fmt.Errorf("%s shutdown unconfirmed; retained %s: %w", h, j.remoteDir(), exitErr)
			}
			_, err := runner.run(child, h, "remove this benchmark's temporary executable/token only", "rm -rf -- "+shellQuote(j.remoteDir()), nil)
			return err
		})
		if err != nil {
			result = errors.Join(result, fmt.Errorf("cleanup %s: %w", j.remoteDir(), err))
		}
	}()
	if e = forHosts(ctx, hs, 8, func(child context.Context, h string) error {
		one, finish := context.WithTimeout(child, *nodeTimeout)
		defer finish()
		return runner.bootstrap(one, j, h)
	}); e != nil {
		return e
	}
	for i, h := range hs {
		cfg := c
		cfg.Index = i
		in, w, pipeErr := os.Pipe()
		if pipeErr != nil {
			return pipeErr
		}
		leases = append(leases, w)
		peersWG.Add(1)
		go func(h string, cfg benchmarkConfig, in *os.File, w *os.File) {
			defer peersWG.Done()
			defer in.Close()
			written := make(chan error, 1)
			go func() { written <- json.NewEncoder(w).Encode(cfg) }()
			_, err := runner.run(peerCtx, h, "run benchmark peer; stdin EOF ends this owned process", benchmarkLaunch(filepath.Join(j.remoteDir(), "swarmcopy"), cfg), in)
			peerExitMu.Lock()
			peerExits[h] = err
			peerExitMu.Unlock()
			w.Close()
			writeErr := <-written
			if err == nil {
				err = writeErr
			}
			if err == nil {
				err = fmt.Errorf("%s benchmark peer exited", h)
			}
			exits <- err
		}(h, cfg, in, w)
	}
	hc := client()
	defer hc.CloseIdleConnections()
	readyDeadline := time.Now().Add(*nodeTimeout)
	for {
		nodes, err := benchmarkAll(ctx, hc, c, "")
		if err == nil {
			report.Nodes = nodes
			break
		}
		if time.Now().After(readyDeadline) {
			return fmt.Errorf("benchmark readiness: %w", err)
		}
		select {
		case err := <-exits:
			return err
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	for address, row := range report.Nodes {
		if row.NIC == nil || row.NIC.Error != "" {
			return fmt.Errorf("%s: routed NIC counters unavailable; benchmark cannot report physical throughput", address)
		}
	}
	if _, e = benchmarkAll(ctx, hc, c, "start"); e != nil {
		return e
	}
	fmt.Printf("Ready %d/%d. Warming up %s...\n", len(hs), len(hs), *warmup)
	select {
	case e := <-exits:
		return e
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(*warmup):
	}
	initial, e := benchmarkAll(ctx, hc, c, "reset")
	if e != nil {
		return e
	}
	previous := initial
	live := benchmarkTerminal() && !*plain && !*verbose
	lastProgress := time.Time{}
	report.State = "measuring"
	measured := time.Now()
	end := time.NewTimer(*duration)
	defer end.Stop()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case e := <-exits:
			return e
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			nodes, err := benchmarkAll(ctx, hc, c, "")
			report.Nodes = nodes
			if live {
				fmt.Print("\033[H\033[2J")
				renderBenchmark(os.Stdout, c, nodes, previous, *duration-time.Since(measured), false, benchmarkDisplayRows())
			} else if lastProgress.IsZero() || time.Since(lastProgress) >= 5*time.Second {
				summary := summarizeBenchmark(nodes, c.Addresses)
				fmt.Printf("Measuring %s: %.0f/%.0fs | NIC counters %d/%d | busy %d | errors %d\n", strings.ToUpper(c.Topology), time.Since(measured).Seconds(), duration.Seconds(), summary.NICAvailable, summary.Nodes, summary.BusyResponses, summary.OtherErrors)
				lastProgress = time.Now()
			}
			previous = nodes
			if err != nil {
				return err
			}
		case <-end.C:
			nodes, err := benchmarkAll(ctx, hc, c, "stop")
			report.Nodes = nodes
			if err != nil {
				return err
			}
			report.Summary = summarizeBenchmark(nodes, c.Addresses)
			if live {
				fmt.Print("\033[H\033[2J")
			}
			renderBenchmark(os.Stdout, c, nodes, nil, 0, true)
			for address, s := range nodes {
				if s.NIC == nil || s.NIC.Error != "" || s.NIC.Seconds <= 0 {
					return fmt.Errorf("%s: NIC counters unavailable/invalid; no valid network result", address)
				}
				if c.Topology == "mesh" && s.Sent == 0 {
					return fmt.Errorf("%s did not send mesh payload", address)
				}
				if !s.Frozen || !s.Measuring || s.Seconds <= 0 || s.Workers > 0 && s.Received == 0 {
					return errors.New("benchmark did not measure traffic on every receiver")
				}
				report.ApplicationAggregateRXGbit += s.RXGbit
			}
			report.State = "complete"

			fmt.Printf("Finished %.1fs.\n", time.Since(measured).Seconds())
			if *output != "" {
				fmt.Println("JSON report:", *output)
			}
			return nil
		}
	}
}
