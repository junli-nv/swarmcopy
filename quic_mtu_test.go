package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"
)

// This proxy owns both sockets and silently drops oversized UDP packets, modeling
// a path with smaller MTU without requiring namespaces, root or network changes.
func mtuProxy(t *testing.T, target string, ceiling int) (string, *atomic.Int64, *atomic.Int64, func()) {
	t.Helper()
	addr, e := net.ResolveUDPAddr("udp", target)
	if e != nil {
		t.Fatal(e)
	}
	back, e := net.DialUDP("udp", nil, addr)
	if e != nil {
		t.Fatal(e)
	}
	front, e := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if e != nil {
		back.Close()
		t.Fatal(e)
	}
	var peer *net.UDPAddr
	var mu sync.Mutex
	var wg sync.WaitGroup
	maxForwarded, dropped := &atomic.Int64{}, &atomic.Int64{}
	update := func(n int) {
		for {
			old := maxForwarded.Load()
			if int64(n) <= old || maxForwarded.CompareAndSwap(old, int64(n)) {
				return
			}
		}
	}
	wg.Add(2)
	go func() {
		defer wg.Done()
		buf := make([]byte, 65536)
		for {
			n, p, e := front.ReadFromUDP(buf)
			if e != nil {
				return
			}
			mu.Lock()
			peer = p
			mu.Unlock()
			if n > ceiling {
				dropped.Add(1)
				continue
			}
			update(n)
			if _, e = back.Write(buf[:n]); e != nil {
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		buf := make([]byte, 65536)
		for {
			n, e := back.Read(buf)
			if e != nil {
				return
			}
			if n > ceiling {
				dropped.Add(1)
				continue
			}
			update(n)
			mu.Lock()
			p := peer
			mu.Unlock()
			if p != nil {
				if _, e = front.WriteToUDP(buf[:n], p); e != nil {
					return
				}
			}
		}
	}()
	return front.LocalAddr().String(), maxForwarded, dropped, func() { front.Close(); back.Close(); wg.Wait() }
}
func TestQUICPathMTUDiscoveryAndSmallPeerNegotiation(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		serverMax, clientMax, pathMax int
		expectJumbo, expectDrop       bool
	}{
		{"jumbo", 8952, 8952, 9000, true, false},
		{"narrow-path", 8952, 8952, 1472, false, true},
		{"small-peer", 1452, 8952, 9000, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token := strings.Repeat("m", 64)
			conf, e := jobTLS(token)
			if e != nil {
				t.Fatal(e)
			}
			payload := bytes.Repeat([]byte("mtu-safe-full-integrity"), 400000)
			packet, e := net.ListenPacket("udp", "127.0.0.1:0")
			if e != nil {
				t.Fatal(e)
			}
			srv := &http3.Server{TLSConfig: conf, QUICConfig: quicConfig(tc.serverMax), Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
				w.Write(payload)
			})}
			go srv.Serve(packet)
			defer func() { srv.Close(); packet.Close() }()
			addr, largest, drops, stop := mtuProxy(t, packet.LocalAddr().String(), tc.pathMax)
			defer stop()
			c, closeClient, e := newDataClient("quic", token, 1, tc.clientMax)
			if e != nil {
				t.Fatal(e)
			}
			defer closeClient()
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			r, e := request(ctx, c, token, addr, "/payload")
			if e != nil {
				t.Fatal(e)
			}
			got, e := io.ReadAll(io.LimitReader(r.Body, int64(len(payload))+1))
			r.Body.Close()
			if e != nil || !bytes.Equal(got, payload) || r.ProtoMajor != 3 {
				t.Fatalf("MTU transfer failed: %v (%d/%d)", e, len(got), len(payload))
			}
			if tc.expectJumbo && largest.Load() <= 1452 {
				t.Fatalf("jumbo mode never sent larger packets: %d", largest.Load())
			}
			if !tc.expectJumbo && largest.Load() > int64(min(tc.serverMax, tc.pathMax)) {
				t.Fatalf("exceeded peer/path limit: %d", largest.Load())
			}
			if tc.expectDrop && drops.Load() == 0 {
				t.Fatal("narrow-path test did not exercise oversized probe loss")
			}
			t.Logf("largest forwarded UDP payload=%d, oversized probes dropped=%d", largest.Load(), drops.Load())
		})
	}
}
