package main

import (
	"context"
	"errors"
	"io"
	"log"
	"strconv"
	"time"
)

func logRetry(i int, addr string, err error) {
	log.Printf("RETRY chunk=%d peer=%s error=%v", i, addr, err)
}
func (n *Node) networkWork(addr string, elapsed time.Duration, success bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.initTransfersLocked()
	seconds := elapsed.Seconds()
	n.perf.NetworkWorkSeconds += seconds
	if success {
		s := n.sourceStats[addr]
		if s == nil {
			s = &sourceState{}
			n.sourceStats[addr] = s
		}
		if s.NetworkSecondsEWMA == 0 {
			s.NetworkSecondsEWMA = seconds
		} else {
			s.NetworkSecondsEWMA = 0.8*s.NetworkSecondsEWMA + 0.2*seconds
		}
	}
}
func (n *Node) download(ctx context.Context, i int, addr string, buffers ...[]byte) error {
	start := time.Now()
	networkDone := false
	defer func() {
		if !networkDone {
			n.networkWork(addr, time.Since(start), false)
		}
	}()
	resp, err := request(ctx, n.chunkClient(), n.token, addr, "/chunk?i="+strconv.Itoa(i))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if transportName(n.o.Transport) == "quic" {
		if resp.ProtoMajor != 3 {
			return errors.New("QUIC chunk response did not use HTTP/3")
		}
		n.quicRequests.Add(1)
	}
	if resp.StatusCode != 200 {
		return transferHTTPError{resp.StatusCode}
	}
	size := n.m.length(i)
	if resp.ContentLength != size {
		return errors.New("chunk length mismatch")
	}
	var buf []byte
	if len(buffers) > 0 && int64(len(buffers[0])) >= size {
		buf = buffers[0][:size]
	} else {
		buf = make([]byte, int(size))
	}
	for pos := 0; pos < len(buf); {
		end := min(pos+(256<<10), len(buf))
		if err = n.down.wait(ctx, int64(end-pos)); err != nil {
			return err
		}
		read, readErr := io.ReadFull(resp.Body, buf[pos:end])
		n.wireReceived.Add(int64(read))
		if readErr != nil {
			return readErr
		}
		pos = end
	}
	elapsed := time.Since(start)
	networkDone = true
	hashStart := time.Now()
	valid := digest(buf) == n.m.Chunks[i]
	hashTime := time.Since(hashStart).Seconds()
	n.mu.Lock()
	n.initTransfersLocked()
	n.perf.ChunkHashWorkSeconds += hashTime
	lock := &n.chunkLocks[i]
	n.mu.Unlock()
	n.networkWork(addr, elapsed, valid)
	if !valid {
		return errors.New("chunk checksum mismatch")
	}
	// Serialize commits only for the SAME chunk; unrelated writes remain concurrent.
	lock.Lock()
	defer lock.Unlock()
	n.mu.Lock()
	if n.have[i] {
		n.perf.DuplicateBytes += size
		n.mu.Unlock()
		return nil
	}
	n.mu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	writeStart := time.Now()
	written, writeErr := n.f.WriteAt(buf, int64(i)*n.m.ChunkSize)
	writeTime := time.Since(writeStart).Seconds()
	n.mu.Lock()
	defer n.mu.Unlock()
	n.perf.WriteWorkSeconds += writeTime
	if writeErr == nil && int64(written) != size {
		writeErr = io.ErrShortWrite
	}
	if writeErr != nil {
		return &localWriteError{writeErr}
	}
	n.have[i] = true
	n.from[addr] += size
	n.lastProgress = time.Now()
	n.downloaded.Add(size)
	if n.verifier != nil {
		n.verifier.signal()
	}
	for _, other := range n.flights[i] {
		other.cancel()
	}
	return nil
}
func (n *Node) setPhase(stage string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.perf.Stage = stage
}
func (n *Node) finalize() error { return n.finalizeContext(context.Background()) }
func (n *Node) finalizeContext(ctx context.Context) (err error) {
	n.mu.Lock()
	n.transferEnded = time.Now()
	n.perf.FileHashBytesAtTransferEnd = n.perf.FileHashBytes
	if !n.transferStarted.IsZero() {
		n.perf.TransferSeconds = n.transferEnded.Sub(n.transferStarted).Seconds()
	}
	n.perf.Stage = "sync"
	n.mu.Unlock()
	defer func() {
		if err != nil {
			n.mu.Lock()
			n.failure = err.Error()
			n.perf.Stage = "error"
			n.mu.Unlock()
		}
	}()
	start := time.Now()
	err = n.f.Sync()
	n.mu.Lock()
	n.perf.FinalSyncSeconds += time.Since(start).Seconds()
	n.mu.Unlock()
	if err != nil {
		return err
	}
	n.setPhase("final-hash")
	start = time.Now()
	var sum string
	if n.verifier != nil {
		sum, err = n.verifier.wait(ctx)
	} else {
		sum, err = n.serialDigest(ctx)
		n.mu.Lock()
		n.perf.FileHashWallSeconds = time.Since(start).Seconds()
		n.mu.Unlock()
	}

	n.mu.Lock()
	n.perf.FinalHashSeconds = time.Since(start).Seconds()
	n.mu.Unlock()
	if err != nil {
		return err
	}
	if sum != n.m.SHA256 {
		return errors.New("final SHA256 mismatch")
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	n.setPhase("publish")
	start = time.Now()
	if err = n.f.Chmod(0644); err != nil {
		return err
	}
	syncStart := time.Now()
	err = n.f.Sync()
	n.mu.Lock()
	syncElapsed := time.Since(syncStart).Seconds()
	n.perf.FinalSyncSeconds += syncElapsed
	n.mu.Unlock()
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if tree, ok := n.f.(*treeStore); ok {
		err = tree.Publish(ctx)
	} else {
		err = publish(n.f.Name(), n.o.File)
	}
	if err != nil {
		return err
	}
	if info, e := n.f.Stat(); e == nil && n.m.Version == 1 {
		rememberVerified(n.o.File, n.m, info)
	}
	n.mu.Lock()
	n.perf.PublishSeconds = time.Since(start).Seconds() - syncElapsed
	n.complete = true
	n.perf.Stage = "complete"
	n.mu.Unlock()
	log.Printf("COMPLETE sha256=%s bytes=%d; continuing to seed", n.m.SHA256, n.m.Size)
	return nil
}
