package main

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"
)

type CompletionSummary struct {
	Started, Finished                                     string
	TotalSeconds, PreparationSeconds, DistributionSeconds float64
	NetworkReceivedBytes, LocalCopiedBytes, ReusedBytes   int64
	AverageNetworkMiBps, AverageLocalMiBps                float64
	MedianNodeNetworkMiBps                                float64
	CompletedNodes, TotalNodes                            int
}

func completionSummary(started, transferring, finished time.Time, rows []observation, initial ...map[string]Status) CompletionSummary {
	s := CompletionSummary{Started: started.UTC().Format(time.RFC3339Nano), Finished: finished.UTC().Format(time.RFC3339Nano), TotalSeconds: math.Max(0, finished.Sub(started).Seconds()), PreparationSeconds: math.Max(0, transferring.Sub(started).Seconds()), DistributionSeconds: math.Max(0, finished.Sub(transferring).Seconds()), TotalNodes: len(rows)}
	speeds := []float64{}
	for _, r := range rows {
		if r.err != nil || !r.st.Complete || r.st.Error != "" {
			continue
		}
		st := r.st
		s.CompletedNodes++
		network, local := st.Downloaded, st.LocalCopied
		duration := 0.0
		if st.Performance != nil {
			duration = st.Performance.TransferSeconds
		}
		if len(initial) > 0 {
			if old, ok := initial[0][r.addr]; ok && st.Instance != "" && st.Instance == old.Instance {
				network = max(int64(0), network-old.Downloaded)
				local = max(int64(0), local-old.LocalCopied)
				if old.Performance != nil {
					duration = math.Max(0, duration-old.Performance.TransferSeconds)
				}
			}
		}
		s.NetworkReceivedBytes += network
		s.LocalCopiedBytes += local
		s.ReusedBytes += max(int64(0), st.VerifiedBytes-network-local)
		if network > 0 && duration > 0 {
			speeds = append(speeds, float64(network)/(1<<20)/duration)
		}
	}
	if s.TotalSeconds > 0 {
		s.AverageNetworkMiBps = float64(s.NetworkReceivedBytes) / (1 << 20) / s.TotalSeconds
		s.AverageLocalMiBps = float64(s.LocalCopiedBytes) / (1 << 20) / s.TotalSeconds
	}
	if len(speeds) > 0 {
		sort.Float64s(speeds)
		i := len(speeds) / 2
		s.MedianNodeNetworkMiBps = speeds[i]
		if len(speeds)%2 == 0 {
			s.MedianNodeNetworkMiBps = (speeds[i-1] + speeds[i]) / 2
		}
	}
	return s
}
func printCompletionSummary(s CompletionSummary) {
	fmt.Println("JOB SUMMARY")
	fmt.Printf("  Completed: %d/%d targets\n", s.CompletedNodes, s.TotalNodes)
	fmt.Printf("  Elapsed this run: %s | preparation %s | distribution/verification/finish %s\n", time.Duration(s.TotalSeconds*float64(time.Second)).Round(time.Millisecond), time.Duration(s.PreparationSeconds*float64(time.Second)).Round(time.Millisecond), time.Duration(s.DistributionSeconds*float64(time.Second)).Round(time.Millisecond))
	fmt.Printf("  Data: network %.2f GiB | local copy %.2f GiB | reused %.2f GiB\n", float64(s.NetworkReceivedBytes)/(1<<30), float64(s.LocalCopiedBytes)/(1<<30), float64(s.ReusedBytes)/(1<<30))
	fmt.Printf("  Average network receive: %.2f MiB/s aggregate across targets (this run, including preparation)\n", s.AverageNetworkMiBps)
	if s.NetworkReceivedBytes > 0 {
		fmt.Printf("  Median receiving-node network average: %.2f MiB/s (each node transfer phase)\n", s.MedianNodeNetworkMiBps)
	}
	fmt.Printf("  Average aggregate local copy: %.2f MiB/s (this run; excluded from network)\n", s.AverageLocalMiBps)
}
func printRecordedSummary(report any) {
	b, e := json.Marshal(report)
	if e != nil {
		return
	}
	var saved struct{ Summary *CompletionSummary }
	if json.Unmarshal(b, &saved) == nil && saved.Summary != nil {
		printCompletionSummary(*saved.Summary)
	}
}
