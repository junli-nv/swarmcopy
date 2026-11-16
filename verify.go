package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"hash"
	"io"
	"os"
	"time"
)

type hashWork struct {
	Bytes                       int64
	ReadSeconds, ComputeSeconds float64
}

// hashRange reads only the requested range; callers decide when that range is immutable.
func hashRange(ctx context.Context, f *os.File, h hash.Hash, offset, end int64, buf []byte, progress func(hashWork)) (hashWork, error) {
	var total hashWork
	if len(buf) == 0 || offset < 0 || end < offset {
		return total, errors.New("invalid hash range or empty buffer")
	}
	for offset < end {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		want := int(min(int64(len(buf)), end-offset))
		started := time.Now()
		read, err := f.ReadAt(buf[:want], offset)
		readTime := time.Since(started).Seconds()
		work := hashWork{ReadSeconds: readTime}
		if read > 0 {
			started = time.Now()
			_, writeErr := h.Write(buf[:read])
			work.ComputeSeconds = time.Since(started).Seconds()
			work.Bytes = int64(read)
			offset += int64(read)
			if writeErr != nil {
				return total, writeErr
			}
		}
		total.Bytes += work.Bytes
		total.ReadSeconds += work.ReadSeconds
		total.ComputeSeconds += work.ComputeSeconds
		if progress != nil {
			progress(work)
		}
		if err != nil {
			return total, err
		}
		if read != want {
			return total, io.ErrUnexpectedEOF
		}
	}
	return total, nil
}
func (n *Node) verifyBuffer() []byte {
	size := n.o.VerifyBufferMiB
	if size <= 0 {
		size = 4
	}
	return make([]byte, size<<20)
}
func (n *Node) addHashWork(work hashWork) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.perf.FileHashBytes += work.Bytes
	n.perf.FileHashReadSeconds += work.ReadSeconds
	n.perf.FileHashComputeSeconds += work.ComputeSeconds
}
func (n *Node) serialDigest(ctx context.Context) (string, error) {
	h := sha256.New()
	_, err := hashRange(ctx, n.f, h, 0, n.m.Size, n.verifyBuffer(), n.addHashWork)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type orderedVerifier struct {
	n    *Node
	wake chan struct{}
	done chan struct{}
	sum  string
	err  error
}

func newOrderedVerifier(n *Node) *orderedVerifier {
	return &orderedVerifier{n: n, wake: make(chan struct{}, 1), done: make(chan struct{})}
}
func (v *orderedVerifier) signal() {
	select {
	case v.wake <- struct{}{}:
	default:
	}
}
func (v *orderedVerifier) wait(ctx context.Context) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-v.done:
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return v.sum, v.err
	}
}
func (v *orderedVerifier) run(ctx context.Context) {
	n := v.n
	started := time.Now()
	defer func() {
		n.mu.Lock()
		n.perf.FileHashWallSeconds = time.Since(started).Seconds()
		if v.err != nil && ctx.Err() == nil {
			n.failure = "pipeline verification: " + v.err.Error()
			n.perf.Stage = "error"
		}
		n.mu.Unlock()
		close(v.done)
	}()
	h := sha256.New()
	buf := n.verifyBuffer()
	var offset int64
	next := 0
	for offset < n.m.Size {
		if err := ctx.Err(); err != nil {
			v.err = err
			return
		}
		n.mu.Lock()
		for next < len(n.have) && n.have[next] {
			next++
		}
		end := min(n.m.Size, int64(next)*n.m.ChunkSize)
		n.mu.Unlock()
		if end == offset {
			waitStart := time.Now()
			select {
			case <-ctx.Done():
				v.err = ctx.Err()
				return
			case <-v.wake:
			}
			n.mu.Lock()
			n.perf.FileHashWaitSeconds += time.Since(waitStart).Seconds()
			n.mu.Unlock()
			continue
		}
		work, err := hashRange(ctx, n.f, h, offset, end, buf, n.addHashWork)
		offset += work.Bytes
		if err != nil {
			v.err = err
			return
		}
	}
	v.sum = hex.EncodeToString(h.Sum(nil))
	if v.sum != n.m.SHA256 {
		v.err = errors.New("final SHA256 mismatch")
	}
}

// A pipeline opt-out also disables the implicit window. An explicit incompatible
// window remains an error rather than silently ignoring the user's setting.
func normalizeVerifyFlags(fs *flag.FlagSet, pipeline bool, window *int) {
	explicit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "verify-window-chunks" {
			explicit = true
		}
	})
	if !pipeline && !explicit {
		*window = 0
	}
}
