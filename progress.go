package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var errJobStop = errors.New("stop requested; verified P2P partials retained")

type progressContextKey struct{}
type PreparationProgress struct {
	JobID, Attempt, Stage, Detail, State, Error  string
	CurrentFile                                  string
	ProcessedBytes, TotalBytes                   int64
	FilesDone, FilesTotal, NodesDone, NodesTotal int
	Started, Updated                             time.Time
	ElapsedSeconds, BytesPerSecond, ETASeconds   float64
	EstimateReady                                bool
	Complete                                     bool // Preparation is never whole-job completion.
}
type jobProgress struct {
	mu, emitMu   sync.Mutex
	value        PreparationProgress
	dir          string
	out          io.Writer
	started      time.Time
	done, exited chan struct{}
	warned       bool
}

func reporter(ctx context.Context) *jobProgress {
	p, _ := ctx.Value(progressContextKey{}).(*jobProgress)
	return p
}
func reportBytes(ctx context.Context, n int) {
	if p := reporter(ctx); p != nil {
		p.mu.Lock()
		p.value.ProcessedBytes += int64(n)
		p.mu.Unlock()
	}
}
func reportFile(ctx context.Context, name string, finished bool) {
	if p := reporter(ctx); p != nil {
		p.mu.Lock()
		p.value.CurrentFile = name
		if finished {
			p.value.FilesDone++
		}
		p.mu.Unlock()
	}
}
func reportNode(ctx context.Context) {
	if p := reporter(ctx); p != nil {
		p.mu.Lock()
		p.value.NodesDone++
		done := p.value.NodesDone == p.value.NodesTotal
		p.mu.Unlock()
		if done {
			p.emit()
		}
	}
}

func (p *jobProgress) phase(stage, detail string, total int64, files, nodes int) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.value.Stage = stage
	p.value.Detail = detail
	p.value.State = "active"
	p.value.Error = ""
	p.value.ProcessedBytes = 0
	p.value.TotalBytes = total
	p.value.CurrentFile = ""
	p.value.FilesDone = 0
	p.value.FilesTotal = files
	p.value.NodesDone = 0
	p.value.NodesTotal = nodes
	p.started = time.Now()
	p.value.Started = p.started
	p.mu.Unlock()
	p.emit()
}
func (p *jobProgress) snapshot() PreparationProgress {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.value
	s.Updated = time.Now()
	s.ElapsedSeconds = s.Updated.Sub(p.started).Seconds()
	s.BytesPerSecond = 0
	s.ETASeconds = 0
	s.EstimateReady = false
	if s.ElapsedSeconds >= 2 && s.ProcessedBytes > 0 {
		s.BytesPerSecond = float64(s.ProcessedBytes) / s.ElapsedSeconds
		s.EstimateReady = true
		if s.TotalBytes > s.ProcessedBytes {
			s.ETASeconds = float64(s.TotalBytes-s.ProcessedBytes) / s.BytesPerSecond
		}
	}
	s.Complete = false
	return s
}
func preparationLine(s PreparationProgress) string {
	elapsed := time.Duration(s.ElapsedSeconds * float64(time.Second)).Round(time.Second)
	if s.TotalBytes > 0 {
		eta := "calculating"
		rate := "calculating"
		if s.EstimateReady {
			rate = fmt.Sprintf("%.1f MiB/s", s.BytesPerSecond/(1<<20))
			eta = "~" + (time.Duration(s.ETASeconds * float64(time.Second))).Round(time.Second).String()
		}
		if s.ProcessedBytes >= s.TotalBytes {
			eta = "finalizing (time unknown)"
		}
		return fmt.Sprintf("%s %5.1f%% | %.2f / %.2f GiB | %s | ETA %s | elapsed %s | %s",
			s.Stage, progressPercent(s.ProcessedBytes, s.TotalBytes, false), float64(s.ProcessedBytes)/(1<<30), float64(s.TotalBytes)/(1<<30), rate, eta, elapsed, s.CurrentFile)
	}
	if s.NodesTotal > 0 {
		return fmt.Sprintf("%s | nodes %d/%d | elapsed %s | %s", s.Stage, s.NodesDone, s.NodesTotal, elapsed, s.Detail)
	}
	return fmt.Sprintf("%s | elapsed %s | %s", s.Stage, elapsed, s.Detail)
}
func (p *jobProgress) emit() {
	p.emitMu.Lock()
	defer p.emitMu.Unlock()
	s := p.snapshot()
	if p.out != nil && s.Stage != "running" {
		fmt.Fprintln(p.out, "PREPARING "+preparationLine(s))
		if s.TotalBytes > 0 && s.ProcessedBytes >= s.TotalBytes {
			fmt.Fprintln(p.out, s.Detail+"; bytes complete, waiting for phase finalization")
		}
		if s.Error != "" {
			fmt.Fprintln(p.out, "PREPARATION "+s.State+": "+s.Error)
		}
	}
	if p.dir == "" {
		return
	}
	b, _ := json.Marshal(s)
	// Telemetry is atomic but need not fsync every heartbeat; durable job/manifest
	// records remain unchanged. Failure to save telemetry must not invalidate data.
	f, e := os.CreateTemp(p.dir, ".progress-")
	if e == nil {
		name := f.Name()
		if e = f.Chmod(0600); e == nil {
			_, e = f.Write(b)
		}
		ce := f.Close()
		if e == nil {
			e = ce
		}
		if e == nil {
			e = os.Rename(name, filepath.Join(p.dir, "progress.json"))
		}
		os.Remove(name)
	}
	if e != nil && !p.warned {
		p.warned = true
		if p.out != nil {
			fmt.Fprintf(p.out, "Progress record unavailable: %v\n", e)
		}
	}
}
func startJobProgress(ctx context.Context, j Job, out io.Writer) (context.Context, func(error)) {
	child, cancel := context.WithCancelCause(ctx)
	p := &jobProgress{dir: j.Dir, out: out, done: make(chan struct{}), exited: make(chan struct{}), value: PreparationProgress{JobID: j.ID, Attempt: j.Attempt}}
	child = context.WithValue(child, progressContextKey{}, p)
	p.phase("starting", "initializing controller", 0, 0, 0)
	go func() {
		defer close(p.exited)
		tick := time.NewTicker(500 * time.Millisecond)
		defer tick.Stop()
		last := time.Now()
		for {
			select {
			case <-p.done:
				return
			case <-child.Done():
				p.mu.Lock()
				p.value.State = "cancelling"
				p.value.Error = "Cancellation requested; finishing the current I/O operation."
				p.mu.Unlock()
				p.emit()
				return
			case <-tick.C:
				if _, e := os.Stat(filepath.Join(j.Dir, "stop.request")); e == nil {
					cancel(errJobStop)
					continue
				}
				if time.Since(last) >= 2*time.Second {
					p.emit()
					last = time.Now()
				}
			}
		}
	}()
	finish := func(result error) {
		close(p.done)
		<-p.exited
		p.mu.Lock()
		if result != nil {
			p.value.State = "interrupted"
			p.value.Error = result.Error()
			if errors.Is(context.Cause(child), errJobStop) {
				p.value.State = "stopped"
			}
		}
		if result == nil {
			p.value.State = "complete"
		}
		p.mu.Unlock()
		p.emit()
		cancel(nil)
	}
	return child, finish
}

type contextReader struct {
	ctx context.Context
	in  io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.in.Read(p)
}

type progressWriter struct {
	ctx context.Context
	out io.Writer
}

func (w progressWriter) Write(b []byte) (int, error) {
	n, e := w.out.Write(b)
	reportBytes(w.ctx, n)
	return n, e
}

func preparationState(j Job) (PreparationProgress, bool) {
	preparing := false
	switch j.State {
	case "created", "starting", "checking-ssh", "hashing-source", "deploying", "checking-existing", "importing", "starting-peers", "waiting-peers":
		preparing = true
	}
	s := PreparationProgress{JobID: j.ID, Attempt: j.Attempt, Stage: j.State, State: "recorded", Detail: "Waiting for controller progress record"}
	b, e := os.ReadFile(filepath.Join(j.Dir, "progress.json"))
	if e == nil {
		var recorded PreparationProgress
		if json.Unmarshal(b, &recorded) == nil && recorded.JobID == j.ID && recorded.Attempt == j.Attempt && (recorded.Stage == j.State || j.State == "interrupted" || j.State == "stopped") {
			s = recorded
			if (j.State == "interrupted" || j.State == "stopped") && recorded.Stage != "running" {
				preparing = true
			}
		}
	}
	if !preparing {
		return s, false
	}
	if s.TotalBytes == 0 && j.State == "hashing-source" {
		for _, entry := range j.Inputs {
			if entry.Entry.Kind == "file" {
				s.TotalBytes += entry.Entry.Size
				s.FilesTotal++
			}
		}
	}
	host, _ := os.Hostname()
	if j.ControllerHost == host && j.ControllerPID > 0 {
		ticks, err := startTicks(j.ControllerPID)
		if err != nil || ticks != j.ControllerStartTicks {
			s.State = "controller-stopped"
			s.Detail = "Controller exited; saved preparation progress may be stale. Use resume --job " + j.ID
		}
	}
	return s, true
}
func renderPreparation(s PreparationProgress, clear bool) {
	if clear {
		fmt.Print("\033[H\033[2J")
	}
	fmt.Printf("swarmcopy %s / %s | job %s\n", version, protocol, s.JobID)
	fmt.Println("PREPARING " + preparationLine(s))
	fmt.Printf("State: %s | %s\n", s.State, s.Detail)
	if s.FilesTotal > 0 {
		fmt.Printf("Files hashed/streamed: %d/%d\n", s.FilesDone, s.FilesTotal)
	}
	if !s.Updated.IsZero() {
		fmt.Printf("Progress updated %s ago\n", time.Since(s.Updated).Round(time.Second))
	}
	if s.Error != "" {
		fmt.Println("Detail:", s.Error)
	}
	fmt.Println("Peer data transfer has not started; unopened peer ports are expected during preparation.")
}

func sourceBytes(entries []SourceEntry) int64 {
	var total int64
	for _, s := range entries {
		if s.Entry.Kind == "file" {
			total += s.Entry.Size
		}
	}
	return total
}
func (j *Job) preparing(ctx context.Context, stage, detail string, total int64, files, nodes int) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	j.State = stage
	if e := j.save(); e != nil {
		return e
	}
	reporter(ctx).phase(stage, detail, total, files, nodes)
	return nil
}
func renderSavedJob(j Job, live, clear bool) (bool, bool, error) {
	if s, ok := preparationState(j); ok {
		renderPreparation(s, clear)
		return true, false, nil
	}
	if j.State == "complete" && !live {
		rows, _, err := completedJobRows(j)
		if err != nil {
			return true, false, err
		}
		fmt.Printf("Saved completion result for %s. No live query.\n", j.ID)
		return true, render(rows, map[string]observation{}, clear), nil
	}
	return false, false, nil
}
func waitPeerReadiness(ctx context.Context, j Job, t string) error {
	c := client()
	defer c.CloseIdleConnections()
	addresses := j.addresses()
	ready := map[string]bool{}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		for _, addr := range addresses {
			if ready[addr] {
				continue
			}
			one, cancel := context.WithTimeout(ctx, time.Second)
			st, err := getStatus(one, c, t, addr)
			cancel()
			if err == nil && st.Error != "" {
				return fmt.Errorf("peer %s failed during startup: %s", addr, st.Error)
			}
			if err == nil && st.Error == "" {
				ready[addr] = true
				reportNode(ctx)
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
		if len(ready) == len(addresses) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

// A bounded heartbeat keeps SSH stdin open until cancellation. It also lets
// os/exec finish its stdin copier promptly when the remote command exits.
type keepaliveReader struct{ ctx context.Context }

func (r keepaliveReader) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-r.ctx.Done():
		return 0, io.EOF
	case <-timer.C:
		b[0] = '\n'
		return 1, nil
	}
}
