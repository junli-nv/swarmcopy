package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

func forHosts(ctx context.Context, hosts []string, limit int, fn func(context.Context, string) error) error {
	if limit < 1 {
		limit = 8
	}
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	slots := make(chan struct{}, limit)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error
	for _, host := range hosts {
		wg.Add(1)
		go func(h string) {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
			case <-child.Done():
				return
			}
			defer func() { <-slots }()
			if e := fn(child, h); e != nil {
				mu.Lock()
				errs = append(errs, e)
				mu.Unlock()
				cancel()
			}
		}(host)
	}
	wg.Wait()
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return ctx.Err()
}
func startRemotePeer(ctx context.Context, r sshRunner, j Job, m Manifest, manifest []byte, h string) error {
	root := j.remoteDir()
	if e := r.write(ctx, h, filepath.Join(root, "manifest.json"), bytes.NewReader(manifest), false); e != nil {
		return e
	}
	file := filepath.Join(j.Dest, m.Name)
	if h == j.Seed {
		file = j.SeedFile
	}
	if m.isTree() {
		file = j.Dest
	}
	addresses := []string{}
	for i, v := range j.addresses() {
		if j.Hosts[i] != h {
			addresses = append(addresses, v)
		}
	}
	if j.SourceAddress != "" {
		addresses = append(addresses, j.SourceAddress)
	}
	o := Options{Verify: verifyMode(j.Verify), Existing: j.Existing, QUICGSOKiB: j.QUICGSOKiB, QUICMaxPayload: j.QUICMaxPayload, QUICSockets: j.QUICSockets, Transport: j.Transport, VerifyWindowChunks: j.VerifyWindowChunks, PipelineVerify: j.PipelineVerify, VerifyBufferMiB: j.VerifyBufferMiB, MinWorkers: j.MinWorkers, Adaptive: j.Adaptive, Endgame: j.Endgame, MaxWorkers: j.MaxWorkers, HedgeDelay: j.HedgeDelay, Recheck: j.Recheck, File: file, Manifest: filepath.Join(root, "manifest.json"), Listen: ":" + strconv.Itoa(j.Port),
		Peers: strings.Join(addresses, ","), TokenFile: filepath.Join(root, "job.token"), Workers: j.Workers, Uploads: j.Uploads,
		UploadMiB: j.UploadMiB, DownloadMiB: j.DownloadMiB, Lifetime: j.Lifetime, Stall: 10 * time.Minute}
	if j.SourceAddress != "" && sourceHostKey(h) == sourceHostKey(controllerSourceHost(j)) {
		o.LocalInputs = j.Inputs
		o.LocalRoots = j.Sources
	}
	config, _ := json.Marshal(o)
	if e := r.write(ctx, h, filepath.Join(root, "peer.json"), bytes.NewReader(config), false); e != nil {
		return e
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Let an in-flight launch settle before Ctrl-C teardown so agent stop cannot
	// race with a peer launched after its SSH connection is cancelled.
	startCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), nodeTimeout(j))
	defer cancel()
	stop := context.AfterFunc(ctx, func() {
		if !errors.Is(context.Cause(ctx), errControllerInterrupt) {
			cancel()
		}
	})
	defer stop()
	b, e := r.run(startCtx, h, "start or reuse owned peer", shellQuote(filepath.Join(root, "swarmcopy"))+" agent start --config "+shellQuote(filepath.Join(root, "peer.json")), nil)
	if e != nil {
		return e
	}
	fmt.Printf("%s %s", h, b)
	return nil
}

func stopRemotePeers(ctx context.Context, r sshRunner, j Job) error {
	var mu sync.Mutex
	var errs []error
	root := j.remoteDir()
	config := shellQuote(filepath.Join(root, "peer.json"))
	command := "if [ -f " + config + " ]; then " + shellQuote(filepath.Join(root, "swarmcopy")) + " agent stop --config " + config + "; fi"
	e := forHosts(ctx, j.Hosts, j.Fanout, func(c context.Context, h string) error {
		b, err := r.run(c, h, "stop owned peer", command, nil)
		if err != nil {
			mu.Lock()
			errs = append(errs, err)
			mu.Unlock()
		} else {
			fmt.Printf("%s %s", h, b)
		}
		return nil
	})
	if e != nil {
		errs = append(errs, e)
	}
	return errors.Join(errs...)
}
