package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

func nodeTimeout(j Job) time.Duration {
	if j.NodeTimeout <= 0 {
		return time.Minute
	}
	return j.NodeTimeout
}

// Host failures are isolated. Only cancellation of the controller cancels siblings.
func nodeStage(ctx context.Context, j *Job, hosts []string, stage string, fn func(context.Context, string) error) ([]string, error) {
	if j.NodeIssues == nil {
		j.NodeIssues = map[string]string{}
	}
	good := map[string]bool{}
	var mu sync.Mutex
	err := forHosts(ctx, hosts, j.Fanout, func(c context.Context, h string) error {
		one, cancel := context.WithTimeout(c, nodeTimeout(*j))
		defer cancel()
		x := fn(one, h)
		mu.Lock()
		defer mu.Unlock()
		if x != nil {
			j.NodeIssues[h] = stage + ": " + x.Error()
			fmt.Fprintf(os.Stderr, "NODE %s FAILED at %s: %v; other nodes continue\n", h, stage, x)
		} else {
			good[h] = true
			delete(j.NodeIssues, h)
			reportNode(c)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, h := range hosts {
		if good[h] {
			out = append(out, h)
		}
	}
	return out, j.save()
}

func annotateJobRows(j Job, m *Manifest, rows []observation) {
	addresses := j.addresses()
	for i := range rows {
		row := &rows[i]
		if row.err == nil {
			if m != nil && (row.st.ID != m.id() || verifyMode(row.st.Verify) != verifyMode(j.Verify)) {
				row.err = errors.New("peer manifest or verification mode does not match this job")
			}
		}
		if row.err != nil {
			for k, addr := range addresses {
				if addr == row.addr {
					if detail := j.NodeIssues[j.Hosts[k]]; detail != "" {
						row.err = fmt.Errorf("%s; HTTP: %w", detail, row.err)
					}
					break
				}
			}
		}
	}
}

func diagnosePeers(ctx context.Context, r sshRunner, j *Job, rows []observation, previous map[string]observation) error {
	hosts := []string{}
	byHost := map[string]string{}
	for i, addr := range j.addresses() {
		byHost[j.Hosts[i]] = addr
	}
	for _, h := range j.Hosts {
		addr := byHost[h]
		for _, row := range rows {
			if row.addr == addr && row.err != nil && (j.NodeIssues[h] == "" || strings.HasPrefix(j.NodeIssues[h], "STARTING:") || previous[addr].err == nil && !previous[addr].at.IsZero()) {
				hosts = append(hosts, h)
			}
		}
	}
	if len(hosts) == 0 {
		return nil
	}
	var mu sync.Mutex
	err := forHosts(ctx, hosts, j.Fanout, func(c context.Context, h string) error {
		one, cancel := context.WithTimeout(c, 4*time.Second)
		defer cancel()
		root := j.remoteDir()
		b, x := r.run(one, h, "diagnose only this job's unready peer", shellQuote(filepath.Join(root, "swarmcopy"))+" agent inspect --config "+shellQuote(filepath.Join(root, "peer.json")), nil)
		detail := ""
		var st struct {
			Alive        bool
			PID          int
			Log, LastLog string
		}
		if x != nil {
			detail = "UNREACHABLE: SSH inspection failed: " + x.Error()
		} else if x = json.Unmarshal(b, &st); x != nil {
			detail = "FAILED: invalid agent status: " + x.Error()
		} else if st.Alive {
			detail = fmt.Sprintf("STARTING: peer PID %d is alive; %s; log=%s", st.PID, compactLog(st.LastLog), st.Log)
		} else {
			detail = fmt.Sprintf("FAILED: peer exited; %s; log=%s", compactLog(st.LastLog), st.Log)
		}
		mu.Lock()
		j.NodeIssues[h] = detail
		mu.Unlock()
		fmt.Fprintf(os.Stderr, "NODE %s %s; other nodes continue\n", h, detail)
		return nil
	})
	if err != nil {
		return err
	}
	return j.save()
}
func compactLog(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 700 {
		s = s[len(s)-700:]
	}
	if s == "" {
		s = "no startup log yet"
	}
	return fmt.Sprintf("%q", s)
}
func recentPeerLog(path string) string {
	f, e := os.Open(path)
	if e != nil {
		return e.Error()
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return e.Error()
	}
	b := make([]byte, min(int64(2048), st.Size()))
	_, _ = f.ReadAt(b, max(int64(0), st.Size()-int64(len(b))))
	return string(b)
}
func controllerSourceHost(j Job) string {
	local, _ := os.Hostname()

	for _, h := range j.Hosts {
		if sourceHostKey(h) == sourceHostKey(local) {
			if i := strings.LastIndexByte(h, '@'); i >= 0 {
				h = h[i+1:]
			}
			return h
		}
	}
	return ""
}

func sourceHostKey(s string) string {
	if i := strings.LastIndexByte(s, '@'); i >= 0 {
		s = s[i+1:]
	}
	if h, _, e := net.SplitHostPort(s); e == nil {
		s = h
	}
	return strings.ToLower(strings.Split(s, ".")[0])
}
