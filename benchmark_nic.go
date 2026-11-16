package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type benchmarkNICSample struct {
	rx, tx uint64
	at     time.Time
}
type benchmarkNICCounter struct {
	Interface                                string
	LinkGbit                                 float64
	RawRXBytes, RawTXBytes, RXBytes, TXBytes uint64
	Seconds, RXGbit, TXGbit                  float64
	Error                                    string `json:",omitempty"`
}
type benchmarkNICStatus struct {
	benchmarkNICCounter
	Members []benchmarkNICCounter `json:",omitempty"`
}
type benchmarkNICMonitor struct {
	iface      string
	capacity   float64
	members    []string
	capacities map[string]float64
	base       map[string]benchmarkNICSample
	read       func(string) (benchmarkNICSample, error)
}

func readBenchmarkNIC(name string) (benchmarkNICSample, error) {
	var s benchmarkNICSample
	if name == "" || filepath.Base(name) != name {
		return s, errors.New("no routed network interface")
	}
	for _, entry := range []struct {
		name   string
		target *uint64
	}{{"rx_bytes", &s.rx}, {"tx_bytes", &s.tx}} {
		b, err := os.ReadFile(filepath.Join("/sys/class/net", name, "statistics", entry.name))
		if err != nil {
			return s, err
		}
		*entry.target, err = strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
		if err != nil {
			return s, err
		}
	}
	s.at = time.Now()
	return s, nil
}
func newBenchmarkNIC(name string, capacity float64) *benchmarkNICMonitor {
	n := &benchmarkNICMonitor{iface: name, capacity: capacity, capacities: map[string]float64{}, read: readBenchmarkNIC}
	if b, err := os.ReadFile(filepath.Join("/sys/class/net", name, "bonding/slaves")); err == nil {
		n.members = strings.Fields(string(b))
		for _, member := range n.members {
			if b, err := os.ReadFile(filepath.Join("/sys/class/net", member, "speed")); err == nil {
				if speed, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64); err == nil && speed > 0 {
					n.capacities[member] = speed / 1000
				}
			}
		}
	}
	return n
}
func (n *benchmarkNICMonitor) reset() {
	n.base = map[string]benchmarkNICSample{}
	for _, name := range append([]string{n.iface}, n.members...) {
		if s, err := n.read(name); err == nil {
			n.base[name] = s
		}
	}
}
func (n *benchmarkNICMonitor) counter(name string, capacity float64) benchmarkNICCounter {
	out := benchmarkNICCounter{Interface: name, LinkGbit: capacity}
	now, err := n.read(name)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	out.RawRXBytes = now.rx
	out.RawTXBytes = now.tx
	if n.base == nil {
		return out
	}
	before, ok := n.base[name]
	if !ok {
		out.Error = "no valid counter baseline"
		return out
	}
	if now.rx < before.rx || now.tx < before.tx {
		out.Error = "NIC counter reset/wrap; measurement invalid"
		return out
	}
	out.Seconds = now.at.Sub(before.at).Seconds()
	if out.Seconds <= 0 {
		out.Error = "invalid monotonic NIC measurement interval"
		return out
	}
	out.RXBytes = now.rx - before.rx
	out.TXBytes = now.tx - before.tx
	out.RXGbit = float64(out.RXBytes) * 8 / out.Seconds / 1e9
	out.TXGbit = float64(out.TXBytes) * 8 / out.Seconds / 1e9
	return out
}
func (n *benchmarkNICMonitor) snapshot() *benchmarkNICStatus {
	if n == nil {
		return &benchmarkNICStatus{benchmarkNICCounter: benchmarkNICCounter{Error: "network interface could not be identified"}}
	}
	out := &benchmarkNICStatus{benchmarkNICCounter: n.counter(n.iface, n.capacity)}
	if n.iface == "lo" {
		out.Error = "loopback route; this is not physical-network throughput"
	}
	for _, name := range n.members {
		out.Members = append(out.Members, n.counter(name, n.capacities[name]))
	}
	return out
}
func benchmarkNICRate(current, previous *benchmarkNICStatus) (rx, tx float64, err error) {
	if current == nil {
		return 0, 0, errors.New("NIC counters unavailable")
	}
	if current.Error != "" {
		return 0, 0, errors.New(current.Error)
	}
	if previous == nil {
		return current.RXGbit, current.TXGbit, nil
	}
	if previous.Interface != current.Interface || previous.Error != "" {
		return 0, 0, errors.New("NIC sampling continuity lost")
	}
	seconds := current.Seconds - previous.Seconds
	if seconds <= 0 || current.RXBytes < previous.RXBytes || current.TXBytes < previous.TXBytes {
		return 0, 0, fmt.Errorf("invalid NIC interval: %.6fs", seconds)
	}
	return float64(current.RXBytes-previous.RXBytes) * 8 / seconds / 1e9, float64(current.TXBytes-previous.TXBytes) * 8 / seconds / 1e9, nil
}
