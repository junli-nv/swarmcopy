package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"
)

type sourceSpan struct {
	start, end int64
	source     SourceEntry
}
type sourceView struct {
	mu      sync.Mutex
	spans   []sourceSpan
	file    *os.File
	current string
	closed  bool
}

func newSourceView(ctx context.Context, j Job, m Manifest) (*sourceView, error) {
	if err := checkSourcePlanContext(ctx, j.Inputs, j.Sources); err != nil {
		return nil, err
	}
	if !m.isTree() || len(j.Inputs) != len(m.Entries) {
		return nil, errors.New("source plan does not match manifest")
	}
	v := &sourceView{}
	var offset int64
	for i, s := range j.Inputs {
		e := m.Entries[i]
		if s.Entry.Path != e.Path || s.Entry.Kind != e.Kind || s.Entry.Size != e.Size {
			return nil, errors.New("source layout does not match manifest")
		}
		if e.Kind == "file" {
			v.spans = append(v.spans, sourceSpan{offset, offset + e.Size, s})
			offset += e.Size
		}
	}
	if offset != m.Size {
		return nil, errors.New("source size does not match manifest")
	}
	return v, nil
}
func (v *sourceView) ReadAt(p []byte, off int64) (int, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return 0, os.ErrClosed
	}
	if off < 0 {
		return 0, errors.New("negative source offset")
	}
	total := 0
	for len(p) > 0 {
		i := sort.Search(len(v.spans), func(i int) bool { return v.spans[i].end > off })
		if i == len(v.spans) {
			return total, io.EOF
		}
		s := v.spans[i]
		if v.current != s.source.Source {
			if v.file != nil {
				v.file.Close()
				v.file = nil
				v.current = ""
			}
			f, e := os.Open(s.source.Source)
			if e != nil {
				return total, e
			}
			v.file = f
			v.current = s.source.Source
		}
		st, e := v.file.Stat()
		if e != nil {
			return total, e
		}
		fp, e := sourceFingerprint(st)
		if e != nil {
			return total, e
		}
		if fp != s.source.Fingerprint {
			return total, fmt.Errorf("source changed while serving: %s", s.source.Source)
		}
		count := int(min(int64(len(p)), s.end-off))
		n, e := v.file.ReadAt(p[:count], off-s.start)
		total += n
		off += int64(n)
		p = p[n:]
		if e != nil {
			return total, e
		}
		if n != count {
			return total, io.ErrUnexpectedEOF
		}
	}
	return total, nil
}
func (v *sourceView) WriteAt([]byte, int64) (int, error) { return 0, errors.New("source is read-only") }
func (v *sourceView) Truncate(int64) error               { return errors.New("source is read-only") }
func (v *sourceView) Chmod(os.FileMode) error            { return errors.New("source is read-only") }
func (v *sourceView) Sync() error                        { return nil }
func (v *sourceView) Name() string                       { return "original source files" }
func (v *sourceView) Stat() (os.FileInfo, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.spans) == 0 {
		return os.Stat(".")
	}
	return os.Stat(v.spans[0].source.Source)
}
func (v *sourceView) Close() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.closed = true
	if v.file != nil {
		return v.file.Close()
	}
	return nil
}

func startSourceOrigin(ctx context.Context, j *Job, m Manifest, host string) (func(), error) {
	view, e := newSourceView(ctx, *j, m)
	if e != nil {
		return nil, e
	}
	bind := ":0"
	if j.SourceAddress != "" {
		_, port, x := net.SplitHostPort(j.SourceAddress)
		if x == nil {
			bind = ":" + port
		}
	}
	ln, e := net.Listen("tcp", bind)
	if e != nil && bind != ":0" {
		ln, e = net.Listen("tcp", ":0")
	}
	if e != nil {
		view.Close()
		return nil, e
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	j.SourceAddress = net.JoinHostPort(host, port)
	t, e := token(filepath.Join(j.Dir, "job.token"))
	if e != nil {
		ln.Close()
		view.Close()
		return nil, e
	}
	n := &Node{instance: randomID(), manifestID: m.id(), m: m, token: t, f: view, have: make([]bool, len(m.Chunks)), origin: true, complete: true, from: map[string]int64{}, slots: make(chan struct{}, max(1, j.Uploads)), up: pacer{rate: j.UploadMiB << 20},
		o: Options{Verify: verifyMode(j.Verify), Transport: j.Transport, QUICGSOKiB: j.QUICGSOKiB, QUICMaxPayload: j.QUICMaxPayload, QUICSockets: j.QUICSockets}}
	for i := range n.have {
		n.have[i] = true
	}
	n.perf.Stage = "complete"
	sourceCtx, cancel := context.WithCancel(ctx)
	serverErr := make(chan error, 2)
	closeQUIC, e := n.startQUIC(sourceCtx, ln.Addr().String(), serverErr)
	if e != nil {
		cancel()
		ln.Close()
		view.Close()
		return nil, e
	}
	srv := &http.Server{Handler: http.HandlerFunc(n.serve), ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 2 * time.Minute, IdleTimeout: 30 * time.Second, BaseContext: func(net.Listener) context.Context { return sourceCtx }}
	go func() { serverErr <- srv.Serve(ln) }()
	go func() {
		tick := time.NewTicker(2 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-sourceCtx.Done():
				return
			case err := <-serverErr:
				if err != nil && !errors.Is(err, http.ErrServerClosed) {
					n.mu.Lock()
					n.failure = err.Error()
					n.mu.Unlock()
				}
			case <-tick.C:
				if err := checkSourcePlanContext(sourceCtx, j.Inputs, j.Sources); err != nil && sourceCtx.Err() == nil {
					n.mu.Lock()
					n.failure = err.Error()
					n.mu.Unlock()
				}
			}
		}
	}()
	close := func() { cancel(); srv.Close(); closeQUIC(); view.Close() }
	if e = j.save(); e != nil {
		close()
		return nil, e
	}
	fmt.Printf("SOURCE READY: %s | original files, no initial seed copy | %s\n", j.SourceAddress, strconv.FormatInt(m.Size, 10)+" bytes")
	return close, nil
}
