package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

func verifiedBytes(m Manifest, have []bool) int64 {
	var total int64
	for i, h := range have {
		if h {
			total += m.length(i)
		}
	}
	return total
}

type observation struct {
	addr string
	st   Status
	err  error
	at   time.Time
}

func observe(ctx context.Context, c *http.Client, t string, addresses []string) []observation {
	out := make([]observation, len(addresses))
	var wg sync.WaitGroup
	slots := make(chan struct{}, 16)
	for i, a := range addresses {
		wg.Add(1)
		go func(i int, a string) {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				out[i] = observation{addr: a, err: ctx.Err()}
				return
			}
			defer func() { <-slots }()
			one, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			s, e := getStatus(one, c, t, a)
			out[i] = observation{addr: a, st: s, err: e, at: time.Now()}
		}(i, a)
	}
	wg.Wait()
	id := ""
	for _, o := range out {
		if o.err == nil {
			id = o.st.ID
			break
		}
	}
	for i := range out {
		s := out[i].st
		if out[i].err == nil && (s.ID != id || s.Size < 0 || s.Size == 0 && s.Entries <= 0 || s.VerifiedBytes < 0 || s.VerifiedBytes > s.Size) {
			out[i].err = errors.New("manifest mismatch or invalid status")
		}
	}
	return out
}
func rates(cur, prev observation) (float64, float64) {
	if cur.err != nil || prev.err != nil || prev.at.IsZero() || cur.st.ID != prev.st.ID {
		return 0, 0
	}
	seconds := cur.at.Sub(prev.at).Seconds()
	if seconds <= 0 {
		return 0, 0
	}
	return math.Max(0, float64(cur.st.Downloaded-prev.st.Downloaded)/seconds),
		math.Max(0, float64(cur.st.Uploaded-prev.st.Uploaded)/seconds)
}
func progressBar(ratio float64) string {
	n := int(math.Max(0, math.Min(1, ratio)) * 30)
	return "[" + strings.Repeat("#", n) + strings.Repeat("-", 30-n) + "]"
}
func render(observations []observation, previous map[string]observation, clear bool) bool {
	if clear {
		fmt.Print("\033[H\033[2J")
	}
	fmt.Printf("swarmcopy %s | %s\n", version+" / "+protocol, time.Now().Format("2006-01-02 15:04:05"))
	for _, o := range observations {
		if o.err == nil && o.st.Entries > 0 {
			fmt.Printf("Dataset: %d files / %d entries per node; bytes aggregate across all files\n", o.st.Files, o.st.Entries)
			fmt.Printf("Integrity: verify=%s chunk-hash=%s file-hash=%s\n", verifyMode(o.st.Verify), hashName(o.st.ChunkHash), hashName(o.st.FileHash))
			break
		}
	}
	fmt.Printf("%-25s %7s %20s %11s %11s  %s\n", "NODE", "DONE", "VERIFIED GiB / TOTAL", "DL MiB/s", "UL MiB/s", "STATE")
	var verified, total int64
	var expected int64
	for _, o := range observations {
		if o.err == nil {
			expected = o.st.Size
			break
		}
	}
	if expected == 0 {
		for _, o := range previous {
			if o.err == nil && o.st.Size > 0 {
				expected = o.st.Size
				break
			}
		}
	}
	total = expected * int64(len(observations))
	var down, up float64
	var localCopied int64
	complete, online := 0, 0
	recorded := len(observations) > 0
	for _, o := range observations {
		if !o.st.Recorded {
			recorded = false
		}
	}
	coverage := "online"
	if recorded {
		coverage = "recorded"
	}
	all := len(observations) > 0
	for _, o := range observations {
		if o.err != nil {
			if old, ok := previous[o.addr]; ok && old.err == nil && old.st.Size == expected {
				verified += old.st.VerifiedBytes
			}
			state := "OFFLINE"
			if strings.Contains(o.err.Error(), "STARTING:") {
				state = "STARTING"
			} else if strings.Contains(o.err.Error(), "FAILED:") {
				state = "FAILED"
			}
			fmt.Printf("%-25s %7s %20s %11s %11s  %s: %v\n", o.addr, "?", "?", "-", "-", state, o.err)
			all = false
			continue
		}
		online++
		s := o.st
		localCopied += s.LocalCopied
		d, u := rates(o, previous[o.addr])
		down += d
		up += u
		verified += s.VerifiedBytes
		state := "DOWNLOADING"
		if s.Error != "" {
			state = "ERROR: " + s.Error
			all = false
		} else if s.Complete {
			state = "COMPLETE / SEEDING"
			if s.Recorded {
				state = "COMPLETE (RECORDED)"
			}
			complete++
		} else if s.VerifiedBytes == s.Size {
			state = "FINAL VERIFY"
		} else {
			all = false
		}
		if !s.Complete {
			all = false
		}
		if p := s.Performance; p != nil {
			if !s.Complete && s.Error == "" && (p.Stage == "sync" || p.Stage == "final-hash" || p.Stage == "publish") {
				state = strings.ToUpper(p.Stage)
			}
			if p.PipelineVerify && !s.Origin {
				state += fmt.Sprintf(" hash=%.1f%%", progressPercent(p.FileHashBytes, s.Size, s.Complete))
			}
			if p.Adaptive {
				state += fmt.Sprintf(" workers=%d/%d", p.ActiveDownloads, p.TargetDownloads)
			}
		}
		fmt.Printf("%-25s %6.1f%% %8.2f / %8.2f %11.2f %11.2f  %s\n", o.addr,
			progressPercent(s.VerifiedBytes, s.Size, s.Complete), float64(s.VerifiedBytes)/(1<<30),
			float64(s.Size)/(1<<30), d/(1<<20), u/(1<<20), state)
	}
	var metrics, adaptive int
	var retry, hedge, canceled, received int64
	var syncTime, hashTime, transferTime float64
	for _, o := range observations {
		if o.err != nil || o.st.Performance == nil {
			continue
		}
		p := o.st.Performance
		metrics++
		if p.Adaptive {
			adaptive++
		}
		retry += p.Retries
		hedge += p.HedgeRequests
		canceled += p.CanceledRequests
		received += p.ReceivedBytes
		syncTime = math.Max(syncTime, p.FinalSyncSeconds)
		hashTime = math.Max(hashTime, p.FinalHashSeconds)
		transferTime = math.Max(transferTime, p.TransferSeconds)
	}
	if localCopied > 0 {
		fmt.Printf("Local source copied: %.2f GiB (excluded from network DL)\n", float64(localCopied)/(1<<30))
	}
	if metrics > 0 {
		fmt.Printf("Diagnostics: adaptive %d/%d | retries %d | hedges %d | canceled %d | received %.2f GiB\n", adaptive, metrics, retry, hedge, canceled, float64(received)/(1<<30))
		fmt.Printf("Max per-node phases (overlap, do not sum): transfer %.2fs | fsync %.2fs | final hash %.2fs\n", transferTime, syncTime, hashTime)
	}
	ratio := 0.0
	if total > 0 {
		ratio = float64(verified) / float64(total)
	}
	if total == 0 && all {
		ratio = 1
	}
	eta := "--"
	if total > verified && down > 0 {
		eta = (time.Duration(float64(total-verified)/down) * time.Second).Round(time.Second).String()
	}
	if total == verified && total > 0 {
		eta = "waiting for verification"
	}
	if all {
		eta = "done"
	}
	fmt.Printf("\n%s %.1f%% of ALL targets | complete %d/%d | %s %d/%d\n",
		progressBar(ratio), ratio*100, complete, len(observations), coverage, online, len(observations))
	fmt.Printf("Verified %.2f / %.2f GiB | network DL %.2f MiB/s UL %.2f MiB/s | ETA %s\n",
		float64(verified)/(1<<30), float64(total)/(1<<30), down/(1<<20), up/(1<<20), eta)
	if online != len(observations) {
		fmt.Println("Offline progress is last known (or zero); offline nodes are NOT counted as completed.")
	}
	if recorded {
		fmt.Println("Historical verified results; use --live to query current peers.")
	} else {
		fmt.Println("100% bytes still requires final SHA256; Ctrl-C exits monitor only.")
	}
	return all
}
func watchCommand(args []string) error {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	configureUsage(fs, "watch")
	list := fs.String("peers", "", "comma-separated host:port")
	tf := fs.String("token-file", "", "job token file")
	job := fs.String("job", "", "saved ID or directory")
	live := fs.Bool("live", false, "query peers even for a completed saved job")
	interval := fs.Duration("interval", 2*time.Second, "refresh interval; at least 500ms")
	count := fs.Int("count", 0, "number of snapshots; 0=until timeout or interrupted")
	timeout := fs.Duration("timeout", 30*time.Minute, "maximum monitoring duration")
	plain := fs.Bool("plain", false, "append text without ANSI screen clearing")
	until := fs.Bool("until-complete", false, "exit successfully only when every listed node completes")
	if e := fs.Parse(args); e != nil {
		return e
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return errors.New("unexpected positional arguments; this release accepts a single source file")
	}
	if *interval < 500*time.Millisecond || *count < 0 || *timeout <= 0 {
		return errors.New("invalid monitoring limits")
	}
	if e := selectMonitorJob(list, tf, job); e != nil {
		fs.Usage()
		return e
	}
	if *job != "" {
		if *list != "" || *tf != "" {
			return errors.New("--job cannot be combined with --peers or --token-file")
		}
		j, e := loadJob(*job)
		if e != nil {
			return e
		}
		if j.State == "complete" && !*live {
			rows, report, e := completedJobRows(j)
			if e != nil {
				return e
			}
			fmt.Printf("Saved completion result for %s; verified at %s. No live query.\n", j.ID, rows[0].at.Format(time.RFC3339))
			render(rows, map[string]observation{}, false)
			printRecordedSummary(report)
			return nil
		}
		*list = strings.Join(j.addresses(), ",")
		*tf = filepath.Join(j.Dir, "job.token")
	}
	addresses, e := peers(*list)
	if e != nil {
		return e
	}
	if len(addresses) == 0 {
		return errors.New("no peers")
	}
	t, e := token(*tf)
	if e != nil {
		return e
	}
	c := client()
	defer c.CloseIdleConnections()
	sig, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(sig, *timeout)
	defer cancel()
	clear := false
	if st, e := os.Stdout.Stat(); e == nil {
		clear = st.Mode()&os.ModeCharDevice != 0 && !*plain
	}
	previous := map[string]observation{}
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	for sample := 1; ; sample++ {
		all, handled := false, false
		if *job != "" {
			current, x := loadJob(*job)
			if x != nil {
				return x
			}
			handled, all, x = renderSavedJob(current, *live, clear)
			if x != nil {
				return x
			}
		}
		if !handled {
			rows := observe(ctx, c, t, addresses)
			description := ""
			if *job != "" {
				current, x := loadJob(*job)
				if x != nil {
					return x
				}
				m, x := loadManifest(filepath.Join(current.Dir, "manifest.json"))
				if x == nil {
					annotateJobRows(current, &m, rows)
				} else {
					annotateJobRows(current, nil, rows)
				}
				description = current.SourceDescription
			}
			all = render(rows, previous, clear)
			if description != "" {
				fmt.Println(description)
			}
			for _, o := range rows {
				if o.err == nil {
					previous[o.addr] = o
				}
			}
		}
		if *until && all {
			return nil
		}
		if *count > 0 && sample >= *count {
			if *until {
				return errors.New("snapshot limit reached before all nodes completed")
			}
			return nil
		}
		select {
		case <-ctx.Done():
			if *until {
				return errors.New("monitor stopped before all nodes completed")
			}
			if sig.Err() != nil {
				return nil
			}
			return errors.New("monitor duration elapsed")
		case <-ticker.C:
		}
	}
}

func progressPercent(done, total int64, complete bool) float64 {
	if total > 0 {
		return 100 * float64(done) / float64(total)
	}
	if complete {
		return 100
	}
	return 0
}
