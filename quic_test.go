package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestQUICJobIdentityPinning(t *testing.T) {
	a, err := jobTLS(strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := jobTLS(strings.Repeat("b", 64))
	parse := func(c *tls.Config) *x509.Certificate {
		p, e := x509.ParseCertificate(c.Certificates[0].Certificate[0])
		if e != nil {
			t.Fatal(e)
		}
		return p
	}
	if err = a.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{parse(a)}}); err != nil {
		t.Fatal(err)
	}
	if a.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{parse(b)}}) == nil {
		t.Fatal("accepted another job")
	}
	if a.VerifyConnection(tls.ConnectionState{}) == nil {
		t.Fatal("accepted missing identity")
	}
	if _, e := jobTLS("weak"); e == nil {
		t.Fatal("short token accepted")
	}
}
func udpRelay(t *testing.T, target string) (string, *atomic.Int64, func()) {
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
		t.Fatal(e)
	}
	var mu sync.Mutex
	var peer *net.UDPAddr
	var wg sync.WaitGroup
	dropped := &atomic.Int64{}
	wg.Add(2)
	go func() {
		defer wg.Done()
		buf := make([]byte, 65536)
		count := 0
		for {
			n, from, e := front.ReadFromUDP(buf)
			if e != nil {
				return
			}
			mu.Lock()
			peer = from
			mu.Unlock()
			count++
			if count%17 == 0 {
				dropped.Add(1)
				continue
			}
			if _, e = back.Write(buf[:n]); e != nil {
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		buf := make([]byte, 65536)
		count := 0
		for {
			n, e := back.Read(buf)
			if e != nil {
				return
			}
			count++
			if count%13 == 0 {
				dropped.Add(1)
				continue
			}
			mu.Lock()
			to := peer
			mu.Unlock()
			if to != nil {
				if _, e = front.WriteToUDP(buf[:n], to); e != nil {
					return
				}
			}
		}
	}()
	return front.LocalAddr().String(), dropped, func() { front.Close(); back.Close(); wg.Wait() }
}
func TestQUICRealUDPRecoversPacketLossAndAuthenticates(t *testing.T) {
	seed, data := fixture(t)
	seed.o.Transport = "quic"
	for i := range seed.have {
		seed.have[i] = true
	}
	socket, e := net.ListenPacket("udp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	addr := socket.LocalAddr().String()
	socket.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	errs := make(chan error, 1)
	stop, e := seed.startQUIC(ctx, addr, errs)
	if e != nil {
		t.Fatal(e)
	}
	defer stop()
	relay, drops, closeRelay := udpRelay(t, addr)
	defer closeRelay()
	c, closeClient, e := newDataClient("quic", seed.token)
	if e != nil {
		t.Fatal(e)
	}
	defer closeClient()
	var got []byte
	for _, path := range []string{"/chunk?i=0", "/chunk?i=1"} {
		resp, e := request(ctx, c, seed.token, relay, path)
		if e != nil {
			t.Fatal(e)
		}
		body, e := io.ReadAll(resp.Body)
		resp.Body.Close()
		if e != nil {
			t.Fatal(e)
		}
		if resp.StatusCode != 200 || resp.ProtoMajor != 3 {
			t.Fatalf("not HTTP/3: %s %d", resp.Proto, resp.StatusCode)
		}
		got = append(got, body...)
	}
	if !bytes.Equal(got, data) || drops.Load() == 0 {
		t.Fatalf("loss recovery failed: dropped %d", drops.Load())
	}
	bad, closeBad, _ := newDataClient("quic", strings.Repeat("b", 64))
	defer closeBad()
	short, end := context.WithTimeout(ctx, 2*time.Second)
	defer end()
	if resp, e := request(short, bad, seed.token, addr, "/chunk?i=0"); e == nil {
		resp.Body.Close()
		t.Fatal("foreign job TLS accepted")
	}
	resp, e := request(ctx, c, "wrong bearer", relay, "/chunk?i=0")
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal("bearer authentication bypassed")
	}
}
func TestQUICRefusesTCPChunkPayload(t *testing.T) {
	n, _ := fixture(t)
	n.o.Transport = "quic"
	n.have[0] = true
	tcp := httptest.NewServer(http.HandlerFunc(n.serve))
	defer tcp.Close()
	c := client()
	defer c.CloseIdleConnections()
	resp, e := request(context.Background(), c, n.token, strings.TrimPrefix(tcp.URL, "http://"), "/chunk?i=0")
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatal("QUIC peer served TCP payload")
	}
	status, e := getStatus(context.Background(), c, n.token, strings.TrimPrefix(tcp.URL, "http://"))
	if e != nil {
		t.Fatal(e)
	}
	if status.Transport != "quic" {
		t.Fatal("control status lost transport")
	}
}
func TestQUICCancellationNeverFallsBackToTCP(t *testing.T) {
	var hits atomic.Int64
	tcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer tcp.Close()
	c, closeClient, e := newDataClient("quic", strings.Repeat("a", 64))
	if e != nil {
		t.Fatal(e)
	}
	defer closeClient()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if resp, e := request(ctx, c, strings.Repeat("a", 64), strings.TrimPrefix(tcp.URL, "http://"), "/chunk?i=0"); e == nil {
		resp.Body.Close()
		t.Fatal("UDP to TCP-only server unexpectedly succeeded")
	}
	if hits.Load() != 0 {
		t.Fatal("silently used TCP")
	}
}

func TestQUICShardsKeepPeerAffinityAndDistributeHosts(t *testing.T) {
	c, closeClient, e := newDataClient("quic", strings.Repeat("a", 64), 4)
	if e != nil {
		t.Fatal(e)
	}
	defer closeClient()
	q := c.Transport.(*quicRoundTripper)
	seen := map[int]bool{}
	for _, host := range []string{"node001:1", "node002:1", "node003:1", "node004:1", "node005:1", "node006:1", "node007:1", "node008:1"} {
		a, b := q.index(host), q.index(host)
		if a != b {
			t.Fatal("peer moved between receive sockets")
		}
		seen[a] = true
	}
	if len(seen) != 4 {
		t.Fatalf("hosts did not spread: %v", seen)
	}
	if _, _, e := newDataClient("quic", strings.Repeat("a", 64), 33); e == nil {
		t.Fatal("unbounded shard allocation")
	}
}

func TestQUICGSOBatchBoundPreservesLegacyAndRejectsInvalid(t *testing.T) {
	if quicGSOKiB(0) != 20 || quicGSOKiB(16) != 16 {
		t.Fatal("legacy or explicit setting changed")
	}
	conf := quicConfig(8952, 64)
	if conf.MaxGSOBatchSize != 65527 {
		t.Fatal("GSO batch exceeds UDP payload bound")
	}
	if _, _, e := newDataClient("quic", strings.Repeat("a", 64), 4, 8952, 65); e == nil {
		t.Fatal("unbounded GSO accepted")
	}
}
