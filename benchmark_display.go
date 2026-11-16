package main

import (
	"flag"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"net"
	"os"
	"sort"
	"strings"
	"time"
)

type benchmarkSummary struct {
	Nodes, NICAvailable, BothDirections, NearLineRX, NearLineTX int
	BusyResponses, UploadRejected, OtherErrors                  int64
	Assessment                                                  string
}

func summarizeBenchmark(nodes map[string]benchmarkStatus, addresses []string) benchmarkSummary {
	s := benchmarkSummary{Nodes: len(addresses), Assessment: "MEASURED"}
	for _, a := range addresses {
		row, ok := nodes[a]
		if !ok || row.Error != "" {
			continue
		}
		s.BusyResponses += row.BusyResponses
		s.UploadRejected += row.Busy
		s.OtherErrors += max(0, row.Failures-row.BusyResponses)
		if row.NIC == nil || row.NIC.Error != "" || row.NIC.Seconds <= 0 {
			continue
		}
		s.NICAvailable++
		if row.Received > 0 && row.Sent > 0 {
			s.BothDirections++
		}
		if row.NIC.LinkGbit > 0 {
			if row.NIC.RXGbit >= row.NIC.LinkGbit*0.9 {
				s.NearLineRX++
			}
			if row.NIC.TXGbit >= row.NIC.LinkGbit*0.9 {
				s.NearLineTX++
			}
		}
	}
	if s.BusyResponses > 0 || s.UploadRejected > 0 {
		s.Assessment = "BUSY"
	}
	if s.OtherErrors > 0 {
		s.Assessment = "ERRORS"
	}
	if s.NICAvailable != s.Nodes {
		s.Assessment = "INCOMPLETE NIC COUNTERS"
	}
	return s
}
func benchmarkPercent(value, capacity float64) string {
	if capacity <= 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f%%", value/capacity*100)
}
func renderBenchmark(w io.Writer, c benchmarkConfig, nodes, previous map[string]benchmarkStatus, remaining time.Duration, final bool, limits ...int) {
	label := "CURRENT"
	if final {
		label = "AVERAGE"
	}
	fmt.Fprintf(w, "%s / %s | per-node NIC %s | %d nodes", strings.ToUpper(c.Topology), strings.ToUpper(c.Transport), label, len(c.Addresses))
	if !final {
		fmt.Fprintf(w, " | %.0fs remaining", max(0, remaining.Seconds()))
	}
	fmt.Fprintln(w)
	if c.Topology == "mesh" {
		fmt.Fprintln(w, "All nodes send and receive. RX and TX are separate directions; not added together.")
	} else if c.Topology == "seed" {
		fmt.Fprintln(w, "SINGLE SOURCE: first node sends; receivers do not relay.")
	}
	fmt.Fprintf(w, "%-18s %-17s %12s %12s %7s %7s %s\n", "NODE", "INTERFACE/LINK", "RX Gbit/s", "TX Gbit/s", "RX/LINK", "TX/LINK", "STATE")
	summary := summarizeBenchmark(nodes, c.Addresses)
	visible := c.Addresses
	if !final && len(limits) > 0 && limits[0] > 0 && len(visible) > limits[0] {
		visible = append([]string(nil), visible...)
		score := func(a string) float64 {
			row := nodes[a]
			if row.Error != "" || row.NIC == nil || row.NIC.Error != "" {
				return -2
			}
			var prev *benchmarkNICStatus
			if old, ok := previous[a]; ok {
				prev = old.NIC
			}
			rx, tx, err := benchmarkNICRate(row.NIC, prev)
			if err != nil || row.NIC.LinkGbit <= 0 {
				return -1
			}
			if rx > row.NIC.LinkGbit*1.05 || tx > row.NIC.LinkGbit*1.05 {
				return -3
			}
			return min(rx, tx) / row.NIC.LinkGbit
		}
		sort.SliceStable(visible, func(i, j int) bool { return score(visible[i]) < score(visible[j]) })
		visible = visible[:limits[0]]
	}
	for _, a := range visible {
		row, ok := nodes[a]
		host, _, err := net.SplitHostPort(a)
		if err != nil {
			host = a
		}
		nic := row.NIC
		if !ok || row.Error != "" || nic == nil || nic.Error != "" {
			fmt.Fprintf(w, "%-18s %-17s %12s %12s %7s %7s NO COUNTERS\n", host, "-", "-", "-", "-", "-")
			continue
		}
		var prev *benchmarkNICStatus
		if !final {
			if old, ok := previous[a]; ok {
				prev = old.NIC
			}
		}
		rx, tx, err := benchmarkNICRate(nic, prev)
		if err != nil {
			fmt.Fprintf(w, "%-18s %-17s %12s %12s %7s %7s INVALID SAMPLE\n", host, nic.Interface, "-", "-", "-", "-")
			continue
		}
		link := nic.Interface + "/?"
		if nic.LinkGbit > 0 {
			link = fmt.Sprintf("%s/%.0fG", nic.Interface, nic.LinkGbit)
		}
		state := "ACTIVE"
		if c.Topology == "mesh" {
			if row.Received == 0 {
				state = "NO DOWNLOAD"
			}
			if row.Sent == 0 {
				state = "NO UPLOAD"
			}
			if row.Received == 0 && row.Sent == 0 {
				state = "NO DATA"
			}
		} else if c.Topology == "seed" && a == c.Addresses[0] {
			state = "SEND ONLY"
		} else if c.Topology == "seed" {
			state = "RECEIVE ONLY"
		}
		if nic.LinkGbit > 0 && rx >= nic.LinkGbit*0.9 && tx >= nic.LinkGbit*0.9 {
			state = "NEAR LINE"
		}
		if row.BusyResponses > 0 || row.Busy > 0 {
			state = "BUSY"
		}
		if row.Failures > row.BusyResponses {
			state = "ERRORS"
		}
		if nic.LinkGbit > 0 && (rx > nic.LinkGbit*1.05 || tx > nic.LinkGbit*1.05) {
			state = "CHECK RATE"
		}
		fmt.Fprintf(w, "%-18s %-17s %12.2f %12.2f %7s %7s %s\n", host, link, rx, tx, benchmarkPercent(rx, nic.LinkGbit), benchmarkPercent(tx, nic.LinkGbit), state)
	}
	if len(visible) < len(c.Addresses) {
		fmt.Fprintf(w, "Showing %d lowest-utilization/problem nodes; final table includes all %d.\n", len(visible), len(c.Addresses))
	}
	fmt.Fprintf(w, "NIC counters: %d/%d | average >=90%% of link: RX %d/%d, TX %d/%d", summary.NICAvailable, summary.Nodes, summary.NearLineRX, summary.Nodes, summary.NearLineTX, summary.Nodes)
	if c.Topology == "mesh" {
		fmt.Fprintf(w, " | data in both directions: %d/%d", summary.BothDirections, summary.Nodes)
	}
	fmt.Fprintln(w)
	if summary.BusyResponses > 0 || summary.UploadRejected > 0 {
		fmt.Fprintf(w, "BUSY: %d received HTTP 429, %d uploads rejected. Source slots are full; not packet-loss counts.\n", summary.BusyResponses, summary.UploadRejected)
	}
	if summary.OtherErrors > 0 {
		fmt.Fprintf(w, "ERRORS: %d connect/read/HTTP/protocol failures; see details below or JSON.\n", summary.OtherErrors)
	}
	if final {
		shown := 0
		for _, a := range c.Addresses {
			row := nodes[a]
			detail := row.LastError
			if row.Error != "" {
				detail = row.Error
			}
			if row.NIC != nil && row.NIC.Error != "" {
				detail = row.NIC.Error
			}
			if detail != "" {
				fmt.Fprintf(w, "  %s: %s\n", a, detail)
				shown++
				if shown == 3 {
					break
				}
			}
		}
		fmt.Fprintln(w, "NIC = routed interface counters, including other traffic. Bond totals include its member ports once.")
	}
}
func benchmarkTerminal() bool {
	s, err := os.Stdout.Stat()
	return err == nil && s.Mode()&os.ModeCharDevice != 0 && os.Getenv("TERM") != "dumb"
}
func benchmarkHelp(fs *flag.FlagSet, all bool) {
	fmt.Fprint(fs.Output(), benchmarkUsage)
	fmt.Fprintln(fs.Output(), "\nUsage: swarmcopy benchmark --hosts HOSTS [options]\n\nOptions:")
	common := map[string]bool{"hosts": true, "duration": true, "workers": true, "output": true, "help-all": true}
	fs.VisitAll(func(f *flag.Flag) {
		if !all && !common[f.Name] {
			return
		}
		name, usage := flag.UnquoteUsage(f)
		fmt.Fprintf(fs.Output(), "  -%s %s\n      %s", f.Name, name, usage)
		if f.DefValue != "" && f.DefValue != "false" {
			fmt.Fprintf(fs.Output(), " (default %s)", f.DefValue)
		}
		fmt.Fprintln(fs.Output())
	})
	if !all {
		fmt.Fprintln(fs.Output(), "\nAdvanced options (SSH, topology, CPU, NUMA, buffers): benchmark --help-all")
	}
}

func benchmarkDisplayRows() int {
	ws, err := unix.IoctlGetWinsize(int(os.Stdout.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws.Row == 0 {
		return 16
	}
	return max(1, int(ws.Row)-9)
}
