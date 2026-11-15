package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestHostExpansion(t *testing.T) {
	got, e := hostList("HGX[061,066,082,138,169]")
	if e != nil || !reflect.DeepEqual(got, []string{"HGX061", "HGX066", "HGX082", "HGX138", "HGX169"}) {
		t.Fatal(got, e)
	}
	got, e = hostList("user@node[001-003],user@node002")
	if e != nil || len(got) != 3 || got[2] != "user@node003" {
		t.Fatal(got, e)
	}
	for _, s := range []string{"-evil", "host;id", "HGX[003-001]", "HGX[1-99999]", "HGX[1", "a[b]"} {
		if _, e = hostList(s); e == nil {
			t.Fatalf("accepted %q", s)
		}
	}
}
func TestMonitorRejectsMixedManifest(t *testing.T) {
	servers := []*httptest.Server{}
	addresses := []string{}
	for _, id := range []string{"a", "b"} {
		s := Status{ID: id, Size: 100, VerifiedBytes: 20}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(s) }))
		servers = append(servers, server)
		addresses = append(addresses, strings.TrimPrefix(server.URL, "http://"))
	}
	defer func() {
		for _, s := range servers {
			s.Close()
		}
	}()
	c := client()
	defer c.CloseIdleConnections()
	rows := observe(context.Background(), c, "test", addresses)
	if rows[0].err != nil || rows[1].err == nil {
		t.Fatal("mixed media accepted")
	}
}
func TestRatesSurviveProcessRestart(t *testing.T) {
	old := observation{st: Status{ID: "a", Downloaded: 100}, at: time.Now()}
	now := observation{st: Status{ID: "a", Downloaded: 5}, at: old.at.Add(time.Second)}
	d, u := rates(now, old)
	if d != 0 || u != 0 {
		t.Fatal("negative rate after restart")
	}
}
func TestShellQuoting(t *testing.T) {
	if shellQuote("a'b") != "'a'\"'\"'b'" {
		t.Fatal("incorrect POSIX quoting")
	}
}

func TestLatestJobPrefersUnfinished(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := stateDir()
	if e := os.MkdirAll(root, 0700); e != nil {
		t.Fatal(e)
	}
	for _, j := range []Job{
		{ID: "older", Created: "2026-01-01T00:00:00Z", State: "running", Hosts: []string{"node"}, Port: 19700},
		{ID: "newer", Created: "2026-01-02T00:00:00Z", State: "complete", Hosts: []string{"node"}, Port: 19700},
	} {
		j.Dir = filepath.Join(root, j.ID)
		os.Mkdir(j.Dir, 0700)
		if e := j.save(); e != nil {
			t.Fatal(e)
		}
	}
	j, e := latestJob()
	if e != nil || j.ID != "older" {
		t.Fatal(j.ID, e)
	}
}
