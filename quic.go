package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"net/http"
	"os"
	"runtime/pprof"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

func transportName(mode string) string {
	if mode == "" {
		return "tcp"
	}
	return mode
}
func validTransport(mode string) bool { return transportName(mode) == "tcp" || mode == "quic" }

// The high-entropy job token authenticates a shared job identity, not an individual
// host. No public CA, certificate files, or persistent private keys are required.
// All job-token holders already have permission to read this job's media.
func jobTLS(token string) (*tls.Config, error) {
	if len(token) < 32 {
		return nil, errors.New("QUIC requires a job token of at least 32 characters")
	}
	seed := sha256.Sum256([]byte("swarmcopy QUIC job identity v1\x00" + token))
	key := ed25519.NewKeyFromSeed(seed[:])
	pub := key.Public().(ed25519.PublicKey)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "swarmcopy job"},
		NotBefore: time.Unix(0, 0), NotAfter: time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, pub, key)
	if err != nil {
		return nil, err
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	verify := func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) != 1 {
			return errors.New("QUIC peer identity is missing")
		}
		c := cs.PeerCertificates[0]
		got, ok := c.PublicKey.(ed25519.PublicKey)
		if !ok || subtle.ConstantTimeCompare(got, pub) != 1 {
			return errors.New("QUIC peer belongs to a different job")
		}
		if time.Now().Before(c.NotBefore) || time.Now().After(c.NotAfter) {
			return errors.New("QUIC peer identity expired")
		}
		return c.CheckSignature(c.SignatureAlgorithm, c.RawTBSCertificate, c.Signature)
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, NextProtos: []string{http3.NextProtoH3},
		Certificates: []tls.Certificate{cert}, ClientAuth: tls.RequireAnyClientCert,
		// Standard DNS/CA verification is replaced by mandatory job-key pinning above.
		InsecureSkipVerify: true, VerifyConnection: verify}, nil
}
func quicConfig(maxPayloads ...int) *quic.Config {
	maxPayload := 1452
	if len(maxPayloads) > 0 && maxPayloads[0] != 0 {
		maxPayload = maxPayloads[0]
	}
	gsoKiB := 20
	if len(maxPayloads) > 1 && maxPayloads[1] != 0 {
		gsoKiB = maxPayloads[1]
	}
	return &quic.Config{MaxGSOBatchSize: uint32(min(gsoKiB<<10, 65527)), MaxUDPPayloadSize: uint16(maxPayload), HandshakeIdleTimeout: 3 * time.Second, MaxIdleTimeout: 45 * time.Second, KeepAlivePeriod: 15 * time.Second,
		InitialStreamReceiveWindow: 2 << 20, MaxStreamReceiveWindow: 32 << 20,
		InitialConnectionReceiveWindow: 8 << 20, MaxConnectionReceiveWindow: 128 << 20, MaxIncomingStreams: 64}
}

type quicRoundTripper struct{ shards []*http3.Transport }

func (q *quicRoundTripper) index(host string) int {
	var h uint64 = 14695981039346656037
	for i := 0; i < len(host); i++ {
		h ^= uint64(host[i])
		h *= 1099511628211
	}
	return int(h % uint64(len(q.shards)))
}

func (q *quicRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	cloned := r.Clone(r.Context())
	url := *r.URL
	url.Scheme = "https"
	cloned.URL = &url
	return q.shards[q.index(r.URL.Host)].RoundTrip(cloned)
}
func newDataClient(mode, token string, socketCounts ...int) (*http.Client, func(), error) {
	if !validTransport(mode) {
		return nil, nil, errors.New("transport must be tcp or quic")
	}
	c := client()
	if transportName(mode) == "tcp" {
		return c, c.CloseIdleConnections, nil
	}
	conf, err := jobTLS(token)
	if err != nil {
		return nil, nil, err
	}
	count := 1
	if len(socketCounts) > 0 && socketCounts[0] != 0 {
		count = socketCounts[0]
	}
	if count < 1 || count > 32 {
		return nil, nil, errors.New("quic-sockets must be 1..32")
	}
	maxPayload := 1452
	if len(socketCounts) > 1 && socketCounts[1] != 0 {
		maxPayload = socketCounts[1]
	}
	if maxPayload < 1452 || maxPayload > 8952 {
		return nil, nil, errors.New("quic-max-payload must be 1452..8952")
	}
	gsoKiB := 20
	if len(socketCounts) > 2 && socketCounts[2] != 0 {
		gsoKiB = socketCounts[2]
	}
	if gsoKiB < 16 || gsoKiB > 64 {
		return nil, nil, errors.New("quic-gso-kib must be 16..64")
	}
	q := &quicRoundTripper{}
	for range count {
		q.shards = append(q.shards, &http3.Transport{TLSClientConfig: conf.Clone(), QUICConfig: quicConfig(maxPayload, gsoKiB), DisableCompression: true, MaxResponseHeaderBytes: 8192})
	}
	c.Transport = q
	return c, func() {
		for _, tr := range q.shards {
			_ = tr.Close()
		}
	}, nil
}
func (n *Node) chunkClient() *http.Client {
	if n.dataClient != nil {
		return n.dataClient
	}
	return n.client
}

// TCP status/control remains reachable in both modes. QUIC mode refuses chunk
// payload on TCP, so blocked UDP cannot silently turn a UDP benchmark into TCP.
func (n *Node) startQUIC(ctx context.Context, addr string, serverErr chan<- error) (func(), error) {
	if transportName(n.o.Transport) != "quic" {
		return func() {}, nil
	}
	conf, err := jobTLS(n.token)
	if err != nil {
		return nil, err
	}
	packet, err := net.ListenPacket("udp", addr)
	if err != nil {
		return nil, err
	}
	srv := &http3.Server{Addr: addr, TLSConfig: conf, QUICConfig: quicConfig(n.o.QUICMaxPayload, n.o.QUICGSOKiB), MaxHeaderBytes: 8192, IdleTimeout: 30 * time.Second,
		Handler: http.HandlerFunc(n.serve), ConnContext: func(_ context.Context, _ *quic.Conn) context.Context { return ctx }}
	go func() { serverErr <- srv.Serve(packet) }()
	return func() { _ = srv.Close(); _ = packet.Close() }, nil
}

// Developer-only opt-in; capture real peer CPU work without an exposed pprof server.
// The launcher controls this environment value, profiles are private new files.
func peerCPUProfile() (func(), error) {
	path := os.Getenv("SWARMCOPY_PEER_CPU_PROFILE")
	if path == "" {
		return func() {}, nil
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	if err = pprof.StartCPUProfile(f); err != nil {
		f.Close()
		return nil, err
	}
	return func() { pprof.StopCPUProfile(); f.Close() }, nil
}

func quicGSOKiB(value int) int {
	if value == 0 {
		return 20
	}
	return value
}
