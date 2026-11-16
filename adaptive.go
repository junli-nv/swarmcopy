package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"sync"
	"time"
)

// Performance durations are sums of concurrent request work unless named as a phase.
type Performance struct {
	VerifyWindowChunks                                                                       int
	PipelineVerify                                                                           bool
	FileHashBytes, FileHashBytesAtTransferEnd                                                int64
	FileHashReadSeconds, FileHashComputeSeconds, FileHashWaitSeconds, FileHashWallSeconds    float64
	Stage                                                                                    string
	Adaptive                                                                                 bool
	ActiveDownloads, TargetDownloads, MaxDownloads, MinDownloads, ActiveUploads, UploadLimit int
	Requests, Retries, BusyResponses, HedgeRequests, CanceledRequests, WorkerChanges         int64
	ReceivedBytes, DuplicateBytes                                                            int64
	ScanSeconds, TransferSeconds, FinalSyncSeconds, FinalHashSeconds, PublishSeconds         float64
	NetworkWorkSeconds, ChunkHashWorkSeconds, WriteWorkSeconds                               float64
	SourceCount                                                                              int
}
type SourcePerformance struct {
	Requests, Errors   int64
	NetworkSecondsEWMA float64
}
type sourceState struct {
	SourcePerformance
	cooldown time.Time
}
type flight struct {
	addr    string
	started time.Time
	cancel  context.CancelFunc
}
type transferHTTPError struct{ code int }

func (e transferHTTPError) Error() string { return fmt.Sprintf("HTTP %d", e.code) }

func (n *Node) initTransfersLocked() {
	if n.flights == nil {
		n.flights = make(map[int]map[string]*flight)
	}
	if n.sourceStats == nil {
		n.sourceStats = make(map[string]*sourceState)
	}
	if n.chunkLocks == nil {
		n.chunkLocks = make([]sync.Mutex, len(n.have))
	}
	if n.target == 0 {
		n.target = n.o.Workers
		if n.target < 1 {
			n.target = 4
		}
	}
}
func (n *Node) minWorkers() int {
	if n.o.MinWorkers > 0 {
		return n.o.MinWorkers
	}
	return max(1, n.o.Workers)
}
func (n *Node) maxWorkers() int {
	if n.o.Adaptive && n.o.MaxWorkers > 0 {
		return n.o.MaxWorkers
	}
	if n.o.Workers > 0 {
		return n.o.Workers
	}
	return 4
}
func (n *Node) performanceLocked() *Performance {
	p := n.perf
	p.Adaptive = n.o.Adaptive
	p.PipelineVerify = n.o.PipelineVerify
	p.VerifyWindowChunks = n.o.VerifyWindowChunks
	p.ActiveDownloads = n.active
	p.TargetDownloads = n.target
	p.MaxDownloads = n.maxWorkers()
	p.MinDownloads = n.minWorkers()
	p.ActiveUploads = len(n.slots)
	p.UploadLimit = cap(n.slots)
	p.ReceivedBytes = n.wireReceived.Load()
	if !n.transferStarted.IsZero() && n.transferEnded.IsZero() && !n.complete {
		p.TransferSeconds = time.Since(n.transferStarted).Seconds()
	}
	p.SourceCount = len(n.sourceStats)
	return &p
}
func (n *Node) sourceCostLocked(p Remote) float64 {
	if !n.o.Adaptive {
		cost := float64(n.peerBusy[p.addr] * 2)
		if p.st.Origin {
			cost += 10
		}
		return cost
	}
	seconds := 0.25
	if s := n.sourceStats[p.addr]; s != nil && s.NetworkSecondsEWMA > 0 {
		seconds = s.NetworkSecondsEWMA
	}
	cost := seconds * float64(1+n.peerBusy[p.addr])
	if p.st.Origin {
		cost *= 2
	}
	if perf := p.st.Performance; perf != nil && perf.UploadLimit > 0 {
		cost *= 1 + float64(perf.ActiveUploads)/float64(perf.UploadLimit)
	}
	return cost
}
func (n *Node) candidateLocked(i int, exclude map[string]*flight) (int, string) {
	count := 0
	best := math.Inf(1)
	addr := ""
	for _, p := range n.remotes {
		if time.Since(p.seen) > 8*time.Second || i >= len(p.st.Have) || !p.st.Have[i] {
			continue
		}
		count++
		if n.peerBusy[p.addr] >= 2 || exclude[p.addr] != nil {
			continue
		}
		if s := n.sourceStats[p.addr]; n.o.Adaptive && s != nil && time.Now().Before(s.cooldown) {
			continue
		}
		cost := n.sourceCostLocked(p)
		if cost < best || cost == best && rand.IntN(2) == 0 {
			best = cost
			addr = p.addr
		}
	}
	return count, addr
}

// A verification window is a priority, not a hard transfer boundary.
// If its data is unavailable/in flight, global rarity keeps remaining capacity useful.
func (n *Node) selectChunkLocked() (int, string) {
	if len(n.have) == 0 || n.complete || n.failure != "" {
		return -1, ""
	}
	if n.o.PipelineVerify && n.o.VerifyWindowChunks > 0 && n.m.ChunkSize > 0 {
		first := int(n.perf.FileHashBytes / n.m.ChunkSize)
		if i, addr := n.selectRangeLocked(first, min(first+n.o.VerifyWindowChunks, len(n.have))); i >= 0 {
			return i, addr
		}
	}
	return n.selectRangeLocked(0, len(n.have))
}
func (n *Node) selectRangeLocked(first, last int) (int, string) {
	if first >= last {
		return -1, ""
	}
	chosen := -1
	best := int(^uint(0) >> 1)
	addr := ""
	start := rand.IntN(last - first)
	for j := 0; j < last-first; j++ {
		i := first + (start+j)%(last-first)
		if n.have[i] || n.busy[i] {
			continue
		}
		count, candidate := n.candidateLocked(i, nil)
		if count > 0 && candidate != "" && count < best {
			chosen = i
			best = count
			addr = candidate
		}
	}
	return chosen, addr
}

// Kept as a small reservation primitive for scheduler compatibility tests.
func (n *Node) choose() (int, string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.initTransfersLocked()
	i, addr := n.selectChunkLocked()
	if i >= 0 {
		n.busy[i] = true
		n.peerBusy[addr]++
	}
	return i, addr
}
func (n *Node) reserve(ctx context.Context) (int, *flight, context.Context) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.initTransfersLocked()
	if n.active >= n.target || ctx.Err() != nil {
		return -1, nil, nil
	}
	i, addr := n.selectChunkLocked()
	if i < 0 && n.o.Endgame && n.failure == "" && !n.complete {
		remaining := 0
		for _, have := range n.have {
			if !have {
				remaining++
			}
		}
		if remaining > 0 && remaining <= n.target {
			oldest := time.Now()
			for index, requests := range n.flights {
				if n.have[index] || len(requests) != 1 {
					continue
				}
				for _, request := range requests {
					delay := n.o.HedgeDelay
					if delay <= 0 {
						delay = 2 * time.Second
					}
					if s := n.sourceStats[request.addr]; s != nil {
						measured := time.Duration(2 * s.NetworkSecondsEWMA * float64(time.Second))
						if measured > delay {
							delay = measured
						}
					}
					if time.Since(request.started) < delay || !request.started.Before(oldest) {
						continue
					}
					_, other := n.candidateLocked(index, requests)
					if other != "" {
						i = index
						addr = other
						oldest = request.started
					}
				}
			}
		}
	}
	if i < 0 {
		return -1, nil, nil
	}
	child, cancel := context.WithCancel(ctx)
	f := &flight{addr: addr, started: time.Now(), cancel: cancel}
	if n.flights[i] == nil {
		n.flights[i] = make(map[string]*flight)
	}
	if len(n.flights[i]) > 0 {
		n.perf.HedgeRequests++
	}
	n.flights[i][addr] = f
	n.busy[i] = true
	n.peerBusy[addr]++
	n.active++
	n.perf.Requests++
	if n.sourceStats[addr] == nil {
		n.sourceStats[addr] = &sourceState{}
	}
	n.sourceStats[addr].Requests++
	return i, f, child
}
func (n *Node) finishRequest(ctx context.Context, i int, f *flight, err error) {
	f.cancel()
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.flights[i], f.addr)
	if len(n.flights[i]) == 0 {
		delete(n.flights, i)
		n.busy[i] = false
	}
	n.peerBusy[f.addr]--
	n.active--
	if err == nil {
		return
	}
	if errors.Is(err, context.Canceled) {
		n.perf.CanceledRequests++
		return
	}
	if ctx.Err() != nil {
		return
	}
	var disk *localWriteError
	if errors.As(err, &disk) {
		return
	}
	n.perf.Retries++
	s := n.sourceStats[f.addr]
	s.Errors++
	delay := time.Duration(1<<min(s.Errors, 3)) * 250 * time.Millisecond
	var status transferHTTPError
	if errors.As(err, &status) && status.code == 429 {
		n.perf.BusyResponses++
	}
	s.cooldown = time.Now().Add(delay)
	if !n.o.Adaptive {
		for j := range n.remotes {
			if n.remotes[j].addr == f.addr {
				n.remotes[j].seen = time.Time{}
			}
		}
	}
}
func (n *Node) worker(ctx context.Context) {
	var buffer []byte
	for ctx.Err() == nil {
		i, f, child := n.reserve(ctx)
		if i < 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
			continue
		}
		if buffer == nil {
			buffer = make([]byte, int(n.m.ChunkSize))
		}
		err := n.download(child, i, f.addr, buffer)
		n.finishRequest(ctx, i, f, err)
		if err != nil && ctx.Err() == nil && !errors.Is(err, context.Canceled) {
			// Local write errors are terminal, never attributed to a remote source.
			var disk *localWriteError
			if errors.As(err, &disk) {
				n.mu.Lock()
				n.failure = err.Error()
				n.perf.Stage = "error"
				n.mu.Unlock()
				return
			}
			logRetry(i, f.addr, err)
		}
	}
}

type localWriteError struct{ err error }

func (e *localWriteError) Error() string { return "local write: " + e.err.Error() }
func (e *localWriteError) Unwrap() error { return e.err }

// adjustLocked uses measured application throughput and write-call backpressure.
// It does not claim to measure physical disk throughput or classify network loss.
func (n *Node) adjustLocked(rate, writeShare float64) {
	if n.complete || n.failure != "" {
		return
	}
	before := n.target
	switch {
	case writeShare > 0.35:
		n.target = max(n.minWorkers(), n.target-1)
		n.probing = false
		n.cooldownTicks = 2
	case n.cooldownTicks > 0:
		n.cooldownTicks--
	case n.probing && n.previousRate > 0 && rate < n.previousRate*1.05:
		n.target = max(n.minWorkers(), n.target-1)
		n.probing = false
		n.cooldownTicks = 2
	case rate > 0 && n.target < n.maxWorkers():
		n.previousRate = rate
		n.target++
		n.probing = true
	}
	if n.target != before {
		n.perf.WorkerChanges++
	}
}
func (n *Node) control(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	last := time.Now()
	bytes := n.wireReceived.Load()
	var oldWrite, oldNetwork, oldHash float64
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			current := n.wireReceived.Load()
			n.mu.Lock()
			write := n.perf.WriteWorkSeconds
			network := n.perf.NetworkWorkSeconds
			hash := n.perf.ChunkHashWorkSeconds
			work := write - oldWrite + network - oldNetwork + hash - oldHash
			share := 0.0
			if work > 0 {
				share = (write - oldWrite) / work
			}
			remaining := 0
			for _, have := range n.have {
				if !have {
					remaining++
				}
			}
			if remaining > n.target*2 {
				n.adjustLocked(float64(current-bytes)/now.Sub(last).Seconds(), share)
			}
			n.mu.Unlock()
			oldWrite = write
			oldNetwork = network
			oldHash = hash
			last = now
			bytes = current
		}
	}
}
