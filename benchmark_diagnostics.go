package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type benchmarkErrorKind int

const (
	benchmarkBusy benchmarkErrorKind = iota
	benchmarkConnect
	benchmarkRead
	benchmarkHTTP
	benchmarkProtocol
	benchmarkErrorKinds
)

type benchmarkErrors struct {
	BusyResponses, ConnectErrors, ReadErrors, HTTPErrors, ProtocolErrors int64
	LastError                                                            string
}

func (p *benchmarkPeer) errorSnapshotLocked() benchmarkErrors {
	values := [benchmarkErrorKinds]int64{}
	for i := range values {
		values[i] = p.errorCounts[i].Load() - p.baseErrorCounts[i]
	}
	return benchmarkErrors{values[benchmarkBusy], values[benchmarkConnect], values[benchmarkRead], values[benchmarkHTTP], values[benchmarkProtocol], p.lastError}
}
func (p *benchmarkPeer) recordFailure(kind benchmarkErrorKind, addr string, err error) {
	p.errorCounts[kind].Add(1)
	p.failures.Add(1)
	message := fmt.Sprintf("%s: %v", addr, err)
	if len(message) > 300 {
		message = message[:300]
	}
	p.mu.Lock()
	p.lastError = message
	p.mu.Unlock()
}

// UDP connect selects a local route without transmitting any datagram.
// A missing/virtual link speed stays unknown; never invent a capacity.
func benchmarkLink(ctx context.Context, addresses []string, index int) (string, float64) {
	if len(addresses) < 2 {
		return "", 0
	}
	target := addresses[(index+1)%len(addresses)]
	child, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(child, "udp", target)
	if err != nil {
		return "", 0
	}
	local, ok := conn.LocalAddr().(*net.UDPAddr)
	conn.Close()
	if !ok {
		return "", 0
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", 0
	}
	for _, iface := range interfaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addrs {
			ip, _, err := net.ParseCIDR(address.String())
			if err != nil || !ip.Equal(local.IP) {
				continue
			}
			if filepath.Base(iface.Name) != iface.Name {
				return "", 0
			}
			data, err := os.ReadFile(filepath.Join("/sys/class/net", iface.Name, "speed"))
			if err != nil {
				return iface.Name, 0
			}
			speed, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
			if err != nil || speed <= 0 {
				return iface.Name, 0
			}
			return iface.Name, speed / 1000
		}
	}
	return "", 0
}
