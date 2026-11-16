package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const version = "0.5.0"
const protocol = "SCDP/1"

type Manifest struct {
	ChunkHash string      `json:",omitempty"`
	FileHash  string      `json:",omitempty"`
	Entries   []TreeEntry `json:",omitempty"`
	Version   int
	Name      string
	Size      int64
	ChunkSize int64
	SHA256    string
	Chunks    []string
}

func digest(b []byte) string  { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func (m Manifest) id() string { b, _ := json.Marshal(m); return digest(b) }
func (m Manifest) length(i int) int64 {
	n := m.Size - int64(i)*m.ChunkSize
	if n > m.ChunkSize {
		n = m.ChunkSize
	}
	return n
}
func (m Manifest) validate() error {
	if err := m.validateAlgorithms(); err != nil {
		return err
	}
	if m.isTree() {
		return m.validateTree()
	}
	if len(m.Entries) != 0 {
		return errors.New("v1 manifest contains tree entries")
	}
	if m.Version != 1 || m.Size <= 0 || m.ChunkSize < 65536 || m.ChunkSize > 64<<20 ||
		m.Size > 1<<50 || m.Name == "" || filepath.Base(m.Name) != m.Name ||
		m.Name == "." || m.Name == ".." || int64(len(m.Chunks)) != (m.Size-1)/m.ChunkSize+1 ||
		len(m.Chunks) > 1000000 {
		return errors.New("invalid manifest dimensions")
	}
	for _, s := range append([]string{m.SHA256}, m.Chunks...) {
		b, e := hex.DecodeString(s)
		if e != nil || len(b) != 32 {
			return errors.New("invalid SHA256")
		}
	}
	return nil
}
func loadManifest(path string) (Manifest, error) {
	var m Manifest
	f, e := os.Open(path)
	if e != nil {
		return m, e
	}
	defer f.Close()
	e = json.NewDecoder(io.LimitReader(f, 70<<20)).Decode(&m)
	if e == nil {
		e = m.validate()
	}
	return m, e
}
func publish(tmp, dst string) error {
	if e := os.Link(tmp, dst); e != nil {
		return e
	}
	if e := os.Remove(tmp); e != nil {
		return e
	}
	d, e := os.Open(filepath.Dir(dst))
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
func saveManifest(path string, m Manifest) error {
	f, e := os.CreateTemp(filepath.Dir(path), ".manifest-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = json.NewEncoder(f).Encode(m); e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	return publish(f.Name(), path)
}
func prepare(args []string) error {
	fs := flag.NewFlagSet("prepare", flag.ContinueOnError)
	configureUsage(fs, "prepare")
	src := fs.String("source", "", "immutable source file")
	dst := fs.String("out", "", "new local destination file")
	chunk := fs.Int64("chunk-mib", 16, "chunk size MiB (1..64)")
	streamSize := fs.Int64("size", 0, "declared stream size when --source -")
	if len(args) == 0 {
		fs.Usage()
		return errors.New("missing arguments; see examples above")
	}
	if e := fs.Parse(args); e != nil {
		return e
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return errors.New("unexpected positional arguments; this release accepts a single source file")
	}
	if *src == "" || *dst == "" || *chunk < 1 || *chunk > 64 {
		return errors.New("require --source --out and chunk-mib 1..64")
	}
	if _, e := os.Lstat(*dst); !os.IsNotExist(e) {
		return errors.New("destination exists or cannot be inspected")
	}
	if _, e := os.Lstat(*dst + ".manifest.json"); !os.IsNotExist(e) {
		return errors.New("manifest exists or cannot be inspected")
	}
	var in *os.File
	var before os.FileInfo
	var size int64
	var e error
	if *src == "-" {
		in = os.Stdin
		size = *streamSize
		if size <= 0 || size > 1<<50 {
			return errors.New("stdin requires --size in 1..1PiB")
		}
	} else {
		in, e = os.Open(*src)
		if e != nil {
			return e
		}
		defer in.Close()
		before, e = in.Stat()
		if e != nil {
			return e
		}
		if !before.Mode().IsRegular() || before.Size() == 0 {
			return errors.New("source must be nonempty regular file")
		}
		size = before.Size()
	}
	if e = os.MkdirAll(filepath.Dir(*dst), 0755); e != nil {
		return e
	}
	out, e := os.CreateTemp(filepath.Dir(*dst), ".swarmcopy-import-")
	if e != nil {
		return e
	}
	defer os.Remove(out.Name())
	defer out.Close()
	m := Manifest{Version: 1, Name: filepath.Base(*dst), Size: size, ChunkSize: *chunk << 20}
	whole := sha256.New()
	buf := make([]byte, int(m.ChunkSize))
	start := time.Now()
	for remaining := m.Size; remaining > 0; {
		n := m.ChunkSize
		if remaining < n {
			n = remaining
		}
		if _, e = io.ReadFull(in, buf[:n]); e != nil {
			return e
		}
		if _, e = out.Write(buf[:n]); e != nil {
			return e
		}
		whole.Write(buf[:n])
		m.Chunks = append(m.Chunks, digest(buf[:n]))
		remaining -= n
		if len(m.Chunks)%256 == 0 {
			log.Printf("IMPORT bytes=%d/%d elapsed=%s", m.Size-remaining, m.Size, time.Since(start))
		}
	}
	if before != nil {
		after, e := in.Stat()
		if e != nil {
			return e
		}
		if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
			return errors.New("source changed during import")
		}
	}
	m.SHA256 = hex.EncodeToString(whole.Sum(nil))
	if e = out.Chmod(0644); e != nil {
		return e
	}
	if e = out.Sync(); e != nil {
		return e
	}
	if e = out.Close(); e != nil {
		return e
	}
	if e = publish(out.Name(), *dst); e != nil {
		return e
	}
	if e = saveManifest(*dst+".manifest.json", m); e != nil {
		return e
	}
	if info, err := os.Stat(*dst); err == nil {
		rememberVerified(*dst, m, info)
	}
	log.Printf("IMPORTED bytes=%d chunks=%d sha256=%s elapsed=%s", m.Size, len(m.Chunks), m.SHA256, time.Since(start))
	return nil
}

type Options struct {
	Verify                                   string
	Existing                                 string
	QUICGSOKiB                               int
	QUICMaxPayload                           int
	QUICSockets                              int
	Transport                                string
	VerifyWindowChunks                       int
	PipelineVerify                           bool
	VerifyBufferMiB                          int
	Adaptive, Endgame                        bool
	MinWorkers                               int
	MaxWorkers                               int
	HedgeDelay                               time.Duration
	Recheck                                  bool
	File, Manifest, Listen, Peers, TokenFile string
	Workers, Uploads                         int
	DownloadMiB, UploadMiB                   int64
	Lifetime, Stall                          time.Duration
}
type Status struct {
	Verify, ChunkHash, FileHash string
	Files                       int
	Entries                     int
	Transport                   string
	Performance                 *Performance `json:",omitempty"`
	Protocol                    string
	Size, VerifiedBytes         int64
	Recorded                    bool
	ID                          string
	Version                     string
	Have                        []bool
	Complete                    bool
	Origin                      bool
	Downloaded, Uploaded        int64
	From                        map[string]int64
	Error                       string
}
type pacer struct {
	mu   sync.Mutex
	next time.Time
	rate int64
}

func (p *pacer) wait(ctx context.Context, n int64) error {
	if p.rate <= 0 {
		return nil
	}
	p.mu.Lock()
	now := time.Now()
	if p.next.Before(now) {
		p.next = now
	}
	delay := time.Until(p.next)
	p.next = p.next.Add(time.Duration(float64(n) / float64(p.rate) * float64(time.Second)))
	p.mu.Unlock()
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

type Remote struct {
	addr string
	st   Status
	seen time.Time
}
type Node struct {
	dataClient                     *http.Client
	quicRequests                   atomic.Int64
	verifier                       *orderedVerifier
	m                              Manifest
	o                              Options
	token                          string
	f                              contentFile
	mu                             sync.Mutex
	have, busy                     []bool
	remotes                        []Remote
	peerBusy                       map[string]int
	from                           map[string]int64
	complete, origin               bool
	failure                        string
	lastProgress                   time.Time
	downloaded, uploaded           atomic.Int64
	up, down                       pacer
	slots                          chan struct{}
	client                         *http.Client
	flights                        map[int]map[string]*flight
	chunkLocks                     []sync.Mutex
	sourceStats                    map[string]*sourceState
	active, target, cooldownTicks  int
	probing                        bool
	previousRate                   float64
	perf                           Performance
	wireReceived                   atomic.Int64
	transferStarted, transferEnded time.Time
}

func token(path string) (string, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return "", e
	}
	s := strings.TrimSpace(string(b))
	if len(s) < 32 || len(s) > 512 {
		return "", errors.New("token must be 32..512 characters")
	}
	return s, nil
}
func client() *http.Client {
	return &http.Client{Timeout: 60 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			MaxIdleConns: 128, MaxIdleConnsPerHost: 8, ResponseHeaderTimeout: 5 * time.Second}}
}
func peers(s string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, v := range strings.Split(s, ",") {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, _, e := net.SplitHostPort(v); e != nil {
			return nil, fmt.Errorf("peer %q must be host:port", v)
		}
		if !seen[v] {
			out = append(out, v)
			seen[v] = true
		}
	}
	if len(out) > 1024 {
		return nil, errors.New("too many peers")
	}
	return out, nil
}
func request(ctx context.Context, c *http.Client, token, addr, path string) (*http.Response, error) {
	r, e := http.NewRequestWithContext(ctx, "GET", "http://"+addr+path, nil)
	if e != nil {
		return nil, e
	}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("X-SCDP-Version", "1")
	resp, e := c.Do(r)
	if e == nil {
		if v := resp.Header.Get("X-SCDP-Version"); v != "" && v != "1" {
			resp.Body.Close()
			return nil, errors.New("unsupported SCDP response version")
		}
	}
	return resp, e
}
func getStatus(ctx context.Context, c *http.Client, t, addr string) (Status, error) {
	var st Status
	r, e := request(ctx, c, t, addr, "/status")
	if e != nil {
		return st, e
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return st, fmt.Errorf("HTTP %d", r.StatusCode)
	}
	e = json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&st)
	if e == nil && st.Protocol != "" && st.Protocol != protocol {
		return st, errors.New("unsupported SCDP status version")
	}
	return st, e
}
func main() {
	if os.Getenv("SWARMCOPY_ASKPASS_MODE") == "1" {
		if askpassHelper() != nil {
			os.Exit(1)
		}
		return
	}
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	var e error
	if len(os.Args) < 2 {
		rootHelp()
		return
	}
	if e == nil {
		switch os.Args[1] {
		case "prepare-tree":
			e = prepareTreeCommand(os.Args[2:])
		case "tree-check":
			e = treeCheckCommand(os.Args[2:])
		case "hashbench":
			e = hashbenchCommand(os.Args[2:])
		case "prepare":
			e = prepare(os.Args[2:])
		case "peer":
			e = peer(os.Args[2:])
		case "resume":
			if !hasFlag(os.Args[2:], "file") && !hasFlag(os.Args[2:], "manifest") {
				e = clusterCommand("resume", os.Args[2:])
			} else {
				e = peer(os.Args[2:])
			}
		case "distribute", "stop":
			e = clusterCommand(os.Args[1], os.Args[2:])
		case "jobs":
			e = jobsCommand(os.Args[2:])
		case "inspect":
			e = inspectCommand(os.Args[2:])
		case "agent":
			e = agentCommand(os.Args[2:])
		case "help", "--help", "-h":
			if len(os.Args) > 2 {
				e = commandHelp(os.Args[2])
			} else {
				rootHelp()
			}
		case "watch":
			e = watchCommand(os.Args[2:])
		case "status":
			e = statusCommand(os.Args[2:])
		case "version", "--version":
			fmt.Printf("%s (%s)\n", version, protocol)
		default:
			rootHelp()
			e = errors.New("unknown command; see command list above")
		}
	}
	if e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return
		}
		log.Print(e)
		os.Exit(1)
	}
}

func (n *Node) scan() error {
	buf := make([]byte, int(n.m.ChunkSize))
	whole := newContentHash(n.m)
	count := 0
	for i, h := range n.m.Chunks {
		b := buf[:n.m.length(i)]
		if tree, ok := n.f.(*treeStore); ok && !tree.chunkMayExist(int64(i)*n.m.ChunkSize, n.m.length(i)) {
			continue
		}
		_, e := n.f.ReadAt(b, int64(i)*n.m.ChunkSize)
		if e == nil && contentDigest(n.m.chunkAlgorithm(), b) == h {
			n.have[i] = true
			count++
			if verifyMode(n.o.Verify) == "full" {
				whole.Write(b)
			}
		}
	}
	if verifyMode(n.o.Verify) == "full" && count == len(n.have) && (contentHashError(whole) != nil || hex.EncodeToString(whole.Sum(nil)) != n.m.SHA256) {
		return errors.New("whole-file hash contradicts manifest")
	}
	if n.origin && count != len(n.have) {
		return errors.New("existing final file does not match manifest; refusing overwrite")
	}
	n.complete = n.origin
	log.Printf("VERIFIED existing_chunks=%d/%d", count, len(n.have))
	return nil
}
func (n *Node) snapshot() Status {
	n.mu.Lock()
	defer n.mu.Unlock()
	from := map[string]int64{}
	for k, v := range n.from {
		from[k] = v
	}
	return Status{Verify: verifyMode(n.o.Verify), ChunkHash: n.m.chunkAlgorithm(), FileHash: n.m.fileAlgorithm(), Files: n.m.fileCount(), Entries: len(n.m.Entries), Transport: transportName(n.o.Transport), Performance: n.performanceLocked(), Protocol: protocol, Size: n.m.Size, VerifiedBytes: verifiedBytes(n.m, n.have), ID: n.m.id(), Version: version, Have: append([]bool(nil), n.have...), Complete: n.complete,
		Origin: n.origin, Downloaded: n.downloaded.Load(), Uploaded: n.uploaded.Load(), From: from, Error: n.failure}
}
func (n *Node) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-SCDP-Version", "1")
	if transportName(n.o.Transport) == "quic" && r.URL.Path == "/chunk" && r.ProtoMajor != 3 {
		http.Error(w, "chunk payload requires QUIC/UDP", 400)
		return
	}
	if v := r.Header.Get("X-SCDP-Version"); v != "" && v != "1" {
		http.Error(w, "unsupported SCDP version", 400)
		return
	}

	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+n.token)) != 1 {
		http.Error(w, "unauthorized", 401)
		return
	}
	if r.Method != "GET" {
		http.Error(w, "GET only", 405)
		return
	}
	switch r.URL.Path {
	case "/status":
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(n.snapshot())
	case "/chunk":
		i, e := strconv.Atoi(r.URL.Query().Get("i"))
		if e != nil || i < 0 || i >= len(n.have) {
			http.Error(w, "bad chunk", 400)
			return
		}
		n.mu.Lock()
		ok := n.have[i]
		n.mu.Unlock()
		if !ok {
			http.Error(w, "chunk unavailable", 404)
			return
		}
		select {
		case n.slots <- struct{}{}:
			defer func() { <-n.slots }()
		default:
			http.Error(w, "busy", 429)
			return
		}
		size := n.m.length(i)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		reader := io.NewSectionReader(n.f, int64(i)*n.m.ChunkSize, size)
		buf := make([]byte, 256<<10)
		for {
			k, er := reader.Read(buf)
			if k > 0 {
				if n.up.wait(r.Context(), int64(k)) != nil {
					return
				}
				sent, ew := w.Write(buf[:k])
				n.uploaded.Add(int64(sent))
				if ew != nil {
					return
				}
			}
			if er != nil {
				return
			}
		}
	default:
		http.NotFound(w, r)
	}
}
func (n *Node) discover(ctx context.Context, addresses []string) {
	for ctx.Err() == nil {
		var wg sync.WaitGroup
		var mu sync.Mutex
		fresh := []Remote{}
		for _, a := range addresses {
			wg.Add(1)
			go func(addr string) {
				defer wg.Done()
				c, cancel := context.WithTimeout(ctx, 4*time.Second)
				defer cancel()
				st, e := getStatus(c, n.client, n.token, addr)
				if e == nil && st.ID == n.m.id() && len(st.Have) == len(n.have) && st.Error == "" {
					mu.Lock()
					fresh = append(fresh, Remote{addr: addr, st: st, seen: time.Now()})
					mu.Unlock()
				}
			}(a)
		}
		wg.Wait()
		n.mu.Lock()
		n.remotes = fresh
		n.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

func peer(args []string) error {
	var o Options
	fs := flag.NewFlagSet("peer", flag.ContinueOnError)
	configureUsage(fs, "peer")
	fs.StringVar(&o.Verify, "verify", "full", "verification mode: full (disk reread) or chunks (cryptographic chunk hashes)")
	fs.IntVar(&o.QUICGSOKiB, "quic-gso-kib", 64, "maximum QUIC GSO batch KiB (16..64), still congestion/pacing limited")
	fs.IntVar(&o.QUICMaxPayload, "quic-max-payload", 8952, "QUIC UDP payload ceiling (1452..8952); larger packets require PMTU confirmation")
	fs.IntVar(&o.QUICSockets, "quic-sockets", 4, "QUIC client UDP socket shards (1..32); distribute sources across receive loops")
	fs.StringVar(&o.Transport, "transport", "tcp", "chunk data transport: tcp or quic (UDP); control stays TCP")
	fs.StringVar(&o.File, "file", "", "local destination file; destination directory for v2/v3 tree manifests")
	fs.StringVar(&o.Existing, "existing", "keep", "tree conflicts: keep, resume or replace (controller can prompt)")
	fs.IntVar(&o.VerifyWindowChunks, "verify-window-chunks", 256, "prefer this many upcoming hash chunks; 0=global rarity, requires pipeline-verify")
	fs.BoolVar(&o.PipelineVerify, "pipeline-verify", true, "overlap ordered whole-file SHA256 with chunk transfer")
	fs.IntVar(&o.VerifyBufferMiB, "verify-buffer-mib", 4, "whole-file verification read buffer (1..64 MiB)")
	fs.BoolVar(&o.Recheck, "recheck", false, "ignore final-file verification cache; rehash all existing data")
	fs.StringVar(&o.Manifest, "manifest", "", "trusted manifest JSON")
	fs.StringVar(&o.Listen, "listen", ":19700", "listening address")
	fs.StringVar(&o.Peers, "peers", "", "comma-separated host:port list; exclude self")
	fs.StringVar(&o.TokenFile, "token-file", "", "shared job token file")
	fs.IntVar(&o.Workers, "workers", 4, "fixed or initial concurrent downloads (1..32)")
	fs.IntVar(&o.MinWorkers, "min-workers", 0, "adaptive minimum downloads; 0=initial workers")
	fs.BoolVar(&o.Adaptive, "adaptive", false, "experimental adaptive concurrency and source selection")
	fs.BoolVar(&o.Endgame, "endgame", false, "hedge delayed final chunks using one alternate source")
	fs.IntVar(&o.MaxWorkers, "max-workers", 16, "adaptive maximum downloads (workers..32)")
	fs.DurationVar(&o.HedgeDelay, "hedge-delay", 2*time.Second, "minimum age before an endgame duplicate request")
	fs.IntVar(&o.Uploads, "uploads", 4, "concurrent uploads (1..32)")
	fs.Int64Var(&o.DownloadMiB, "download-mib", 0, "aggregate download MiB/s; 0=unlimited")
	fs.Int64Var(&o.UploadMiB, "upload-mib", 0, "aggregate upload MiB/s; 0=unlimited")
	fs.DurationVar(&o.Lifetime, "lifetime", 2*time.Hour, "maximum process lifetime, including seeding")
	fs.DurationVar(&o.Stall, "stall-timeout", 10*time.Minute, "maximum time without download progress")
	if len(args) == 0 {
		fs.Usage()
		return errors.New("missing arguments; see examples above")
	}
	if e := fs.Parse(args); e != nil {
		return e
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return errors.New("unexpected positional arguments; this release accepts a single source file")
	}
	if o.QUICGSOKiB < 16 || o.QUICGSOKiB > 64 {
		return errors.New("quic-gso-kib must be 16..64")
	}
	if o.QUICMaxPayload < 1452 || o.QUICMaxPayload > 8952 {
		return errors.New("quic-max-payload must be 1452..8952")
	}
	if o.QUICSockets < 1 || o.QUICSockets > 32 {
		return errors.New("quic-sockets must be 1..32")
	}
	if !validTransport(o.Transport) {
		return errors.New("transport must be tcp or quic")
	}
	if o.File == "" || o.Manifest == "" || o.TokenFile == "" || o.Workers < 1 || o.Workers > 32 ||
		o.Uploads < 1 || o.Uploads > 32 || o.DownloadMiB < 0 || o.UploadMiB < 0 ||
		o.DownloadMiB > 100000 || o.UploadMiB > 100000 || o.Lifetime <= 0 || o.Stall <= 0 {
		return errors.New("invalid peer arguments")
	}
	if o.MinWorkers == 0 {
		o.MinWorkers = o.Workers
	}
	if o.MinWorkers < 1 || o.MinWorkers > o.Workers {
		return errors.New("min-workers must be 1..workers, or 0 for initial workers")
	}
	if o.MaxWorkers < o.Workers && !o.Adaptive {
		o.MaxWorkers = o.Workers
	}
	if o.MaxWorkers < o.Workers || o.MaxWorkers > 32 || o.HedgeDelay <= 0 {
		return errors.New("invalid max-workers or hedge-delay")
	}
	if o.Verify == "chunks" {
		o.PipelineVerify = false
		o.VerifyWindowChunks = 0
	}
	normalizeVerifyFlags(fs, o.PipelineVerify, &o.VerifyWindowChunks)
	if o.VerifyWindowChunks < 0 || o.VerifyWindowChunks > 4096 || o.VerifyWindowChunks > 0 && !o.PipelineVerify {
		return errors.New("verify-window-chunks must be 0..4096 and requires pipeline-verify")
	}
	if o.VerifyBufferMiB < 1 || o.VerifyBufferMiB > 64 {
		return errors.New("verify-buffer-mib must be 1..64")
	}
	if o.Verify == "chunks" {
		o.PipelineVerify = false
		o.VerifyWindowChunks = 0
	}
	finishProfile, profileErr := peerCPUProfile()
	if profileErr != nil {
		return profileErr
	}
	defer finishProfile()
	m, e := loadManifest(o.Manifest)
	if e != nil {
		return e
	}
	if e = validateVerification(o.Verify, m); e != nil {
		return e
	}
	if m.isTree() {
		o.File, e = filepath.Abs(o.File)
		if e != nil {
			return e
		}
	}
	t, e := token(o.TokenFile)
	if e != nil {
		return e
	}
	addresses, e := peers(o.Peers)
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(o.File), 0755); e != nil {
		return e
	}
	lockfd, e := syscall.Open(o.File+".swarmcopy.lock", syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if e != nil {
		return e
	}
	defer syscall.Close(lockfd)
	if e = syscall.Flock(lockfd, syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return errors.New("destination is locked by another peer")
	}
	n := &Node{m: m, o: o, token: t, have: make([]bool, len(m.Chunks)), busy: make([]bool, len(m.Chunks)),
		peerBusy: map[string]int{}, from: map[string]int64{}, slots: make(chan struct{}, o.Uploads),
		client: client(), lastProgress: time.Now(), up: pacer{rate: o.UploadMiB << 20}, down: pacer{rate: o.DownloadMiB << 20}}
	defer n.client.CloseIdleConnections()
	dataClient, closeData, err := newDataClient(o.Transport, t, o.QUICSockets, o.QUICMaxPayload, o.QUICGSOKiB)
	if err != nil {
		return err
	}
	n.dataClient = dataClient
	defer closeData()
	scanStart := time.Now()
	if m.isTree() {
		if o.Existing != "" && o.Existing != "keep" && o.Existing != "resume" && o.Existing != "replace" {
			return errors.New("invalid existing-file policy")
		}
		store, err := openTreeStore(o.File, m, o.Existing, o.Recheck)
		if err != nil {
			return err
		}
		n.f = store
		n.origin = store.complete
		if n.origin {
			for i := range n.have {
				n.have[i] = true
			}
			n.complete = true
			log.Printf("VERIFIED tree files=%d; all existing content matches", m.fileCount())
		} else if err = n.scan(); err != nil {
			store.Close()
			return err
		}
	} else {
		path := o.File
		info, err := os.Lstat(path)
		if err == nil {
			if !info.Mode().IsRegular() || info.Size() != m.Size {
				return errors.New("existing destination has wrong type or size")
			}
			n.origin = true
		} else if os.IsNotExist(err) {
			path = o.File + "." + m.id()[:16] + ".part"
		} else {
			return err
		}
		flags := syscall.O_RDWR | syscall.O_NOFOLLOW
		if !n.origin {
			flags |= syscall.O_CREAT
		}
		fresh := false
		var fd int
		if n.origin {
			fd, e = syscall.Open(path, flags, 0600)
		} else {
			fd, e = syscall.Open(path, flags|syscall.O_EXCL, 0600)
			if e == syscall.EEXIST {
				fd, e = syscall.Open(path, flags, 0600)
			} else if e == nil {
				fresh = true
			}
		}
		if e != nil {
			return e
		}
		n.f = os.NewFile(uintptr(fd), path)

		st, e := n.f.Stat()
		if e != nil {
			return e
		}
		if !st.Mode().IsRegular() {
			return errors.New("not a regular file")
		}
		if !n.origin {
			if e = n.f.Truncate(m.Size); e != nil {
				return e
			}
		}

		if fresh {
			log.Printf("NEW partial file: no existing chunks to scan")
		} else if n.origin && !o.Recheck && cacheMatches(o.File, m, st) {
			for i := range n.have {
				n.have[i] = true
			}
			n.complete = true
			log.Printf("VERIFIED cache accepted (unchanged inode/size/mtime/ctime); use --recheck to rescan")
		} else {
			if e = n.scan(); e != nil {
				return e
			}
			if n.origin && verifyMode(o.Verify) == "full" {
				if info, err := n.f.Stat(); err == nil {
					rememberVerified(o.File, m, info)
				}
			}
		}
	}
	defer n.f.Close()

	n.perf.ScanSeconds = time.Since(scanStart).Seconds()
	n.initTransfersLocked()
	n.perf.Stage = "transfer"
	if n.complete {
		n.perf.Stage = "complete"
	} else {
		n.transferStarted = time.Now()
	}
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(signalCtx, o.Lifetime)
	defer cancel()
	ln, e := net.Listen("tcp", o.Listen)
	if e != nil {
		return e
	}
	srv := &http.Server{Handler: http.HandlerFunc(n.serve), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 2 * time.Minute, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192, BaseContext: func(net.Listener) context.Context { return ctx }}
	serverErr := make(chan error, 2)
	closeQUIC, err := n.startQUIC(ctx, ln.Addr().String(), serverErr)
	if err != nil {
		ln.Close()
		return err
	}
	defer closeQUIC()
	go func() { serverErr <- srv.Serve(ln) }()
	var wg sync.WaitGroup
	if o.PipelineVerify && !n.complete {
		n.verifier = newOrderedVerifier(n)
		wg.Add(1)
		go func() { defer wg.Done(); n.verifier.run(ctx) }()
	}
	wg.Add(1)
	go func() { defer wg.Done(); n.discover(ctx, addresses) }()
	if o.Adaptive {
		wg.Add(1)
		go func() { defer wg.Done(); n.control(ctx) }()
	}
	for j := 0; j < n.maxWorkers(); j++ {
		wg.Add(1)
		go func() { defer wg.Done(); n.worker(ctx) }()
	}
	defer func() { cancel(); srv.Close(); closeQUIC(); wg.Wait() }()
	log.Printf("READY listen=%s transport=%s bytes=%d chunks=%d origin=%t", ln.Addr(), transportName(o.Transport), m.Size, len(m.Chunks), n.origin)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	ticks := 0
	for {
		select {
		case <-ctx.Done():
			if !n.snapshot().Complete {
				return errors.New("stopped before completion; partial file retained")
			}
			return nil
		case e := <-serverErr:
			return e
		case <-ticker.C:
			st := n.snapshot()
			if st.Error != "" {
				return errors.New(st.Error)
			}
			count := 0
			for _, h := range st.Have {
				if h {
					count++
				}
			}
			if !st.Complete && count == len(st.Have) {
				if e = n.finalizeContext(ctx); e != nil {
					return e
				}
				st = n.snapshot()
			}
			n.mu.Lock()
			stalled := !n.complete && time.Since(n.lastProgress) > o.Stall
			n.mu.Unlock()
			if stalled {
				return errors.New("download stalled; partial file retained")
			}
			ticks++
			if ticks%10 == 0 {
				log.Printf("PROGRESS chunks=%d/%d complete=%t downloaded=%d uploaded=%d", count, len(st.Have), st.Complete, st.Downloaded, st.Uploaded)
			}
		}
	}
}
func statusCommand(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	configureUsage(fs, "status")
	list := fs.String("peers", "", "comma-separated host:port")
	tf := fs.String("token-file", "", "job token file")
	job := fs.String("job", "", "saved ID or directory")
	live := fs.Bool("live", false, "query peers even for a completed saved job")
	if e := fs.Parse(args); e != nil {
		return e
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return errors.New("unexpected positional arguments; this release accepts a single source file")
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
		if preparation, ok := preparationState(j); ok {
			return json.NewEncoder(os.Stdout).Encode(map[string]any{"JobID": j.ID, "State": preparation.State, "Stage": preparation.Stage, "Complete": false, "Preparation": preparation})
		}
		if j.State == "complete" && !*live {
			_, report, e := completedJobRows(j)
			if e != nil {
				return e
			}
			return json.NewEncoder(os.Stdout).Encode(report)
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
	result := map[string]any{}
	ok := true
	for _, a := range addresses {
		st, err := getStatus(context.Background(), c, t, a)
		if err != nil {
			result[a] = map[string]string{"Error": err.Error()}
			ok = false
		} else {
			count := 0
			for _, h := range st.Have {
				if h {
					count++
				}
			}
			result[a] = map[string]any{"Verify": verifyMode(st.Verify), "ChunkHash": hashName(st.ChunkHash), "FileHash": hashName(st.FileHash), "Protocol": st.Protocol, "Size": st.Size, "VerifiedBytes": st.VerifiedBytes, "Complete": st.Complete, "Chunks": count, "TotalChunks": len(st.Have),
				"Downloaded": st.Downloaded, "Uploaded": st.Uploaded, "From": st.From, "Error": st.Error, "ID": st.ID}
			if !st.Complete || st.Error != "" {
				ok = false
			}
		}
	}
	if e = json.NewEncoder(os.Stdout).Encode(result); e != nil {
		return e
	}
	if !ok {
		return errors.New("one or more peers incomplete/unreachable")
	}
	return nil
}
