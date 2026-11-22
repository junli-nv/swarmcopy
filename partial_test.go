package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestControllerExitListsCopyableCommands(t *testing.T) {
	job := Job{ID: "sample", Dir: filepath.Join(t.TempDir(), "custom job"), Hosts: []string{"ok", "failed"}}
	var output bytes.Buffer
	printControllerExit(&output, job, 1, true)
	for _, want := range []string{
		"Last observed completion: 1/2 targets.",
		"\n  " + shellQuote(os.Args[0]) + " stop --job " + shellQuote(job.Dir) + " --ask-pass\n",
		"\n  " + shellQuote(os.Args[0]) + " resume --job " + shellQuote(job.Dir) + " --ask-pass\n",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("missing %q in %s", want, output.String())
		}
	}
}

func TestPartialCompletionWaitsForUnresolvedTargets(t *testing.T) {
	job := Job{Hosts: []string{"done", "broken", "other"}, NodeIssues: map[string]string{
		"broken": "deploying: Read-only file system",
	}}
	offline := errors.New("offline")
	rows := []observation{{st: Status{Complete: true}}, {err: offline}, {err: offline}}
	for _, detail := range []string{"", "STARTING: peer PID 12 is alive", "UNREACHABLE: SSH inspection failed"} {
		job.NodeIssues["other"] = detail
		if failed := failedRemainingTargets(job, rows, 1); failed != nil {
			t.Fatalf("ended with unresolved target %q: %v", detail, failed)
		}
	}
	job.NodeIssues["other"] = "FAILED: peer exited; disk error"
	if failed := failedRemainingTargets(job, rows, 1); len(failed) != 2 {
		t.Fatalf("confirmed failures not recognized: %v", failed)
	}
	rows[2] = observation{st: Status{Size: 100, VerifiedBytes: 100, CheckingExisting: true}}
	if failed := failedRemainingTargets(job, rows, 1); failed != nil {
		t.Fatalf("ended while a target is verifying: %v", failed)
	}
}

func TestRunJobEndsPartiallyAfterDeploymentFailure(t *testing.T) {
	script := `#!/bin/sh
for arg do
    host=$command
    command=$arg
done
case "$host:$command" in
    127.0.0.2:*mkdir*) echo 'Read-only file system' >&2; exit 1 ;;
    *:uname\ -m) echo aarch64 ;;
    *:*agent*stop*) printf '%s\n' "$host" >> "$SWARMCOPY_TEST_STOPS" ;;
    *) cat >/dev/null ;;
esac
`
	job, manifest := mockControllerJob(t, script)
	job.Hosts = append(job.Hosts, "127.0.0.2")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(Status{ID: manifest.id(), Size: 1, VerifiedBytes: 1, Complete: true})
	}))
	defer server.Close()
	_, portText, _ := strings.Cut(server.Listener.Addr().String(), ":")
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	job.Port = port
	stopLog := filepath.Join(job.Dir, "stopped")
	t.Setenv("SWARMCOPY_TEST_STOPS", stopLog)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runJob(ctx, sshRunner{quiet: true}, &job); !errors.Is(err, errPartialCompletion) {
		t.Fatalf("job did not finish partially: %v", err)
	}
	recorded, err := loadJob(job.Dir)
	if err != nil || recorded.State != "partial" || recorded.PartialCompleted != 1 || len(recorded.Hosts) != 2 ||
		!strings.Contains(recorded.NodeIssues["127.0.0.2"], "Read-only file system") {
		t.Fatalf("partial result was not saved: %+v, %v", recorded, err)
	}
	stopped, err := os.ReadFile(stopLog)
	if err != nil || string(stopped) != "127.0.0.1\n" {
		t.Fatalf("stopped the wrong targets: %q, %v", stopped, err)
	}
}

func TestCtrlCStopsPeerDuringLaunch(t *testing.T) {
	script := `#!/bin/sh
for command do :; done
case "$command" in
    'uname -m') echo aarch64 ;;
    *'agent start'*) touch "$SWARMCOPY_TEST_EVENTS/launching"; sleep 0.3; touch "$SWARMCOPY_TEST_EVENTS/launched" ;;
    *'agent stop'*) test -f "$SWARMCOPY_TEST_EVENTS/launched" && touch "$SWARMCOPY_TEST_EVENTS/stopped" ;;
    *) cat >/dev/null ;;
esac
`
	job, _ := mockControllerJob(t, script)
	t.Setenv("SWARMCOPY_TEST_EVENTS", job.Dir)
	sig, stop := jobSignalContext()
	defer stop()
	ctx, cancel := context.WithTimeout(sig, 5*time.Second)
	defer cancel()
	signaled := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				signaled <- ctx.Err()
				return
			case <-ticker.C:
				if _, err := os.Stat(filepath.Join(job.Dir, "launching")); err == nil {
					process, err := os.FindProcess(os.Getpid())
					if err == nil {
						err = process.Signal(os.Interrupt)
					}
					signaled <- err
					return
				}
			}
		}
	}()
	if err := runJob(ctx, sshRunner{quiet: true}, &job); !errors.Is(err, errControllerInterrupt) || errors.Is(err, errPeerStopFailed) {
		t.Fatalf("interrupt did not stop the job: %v", err)
	}
	if err := <-signaled; err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"stopped", "manifest.json", "job.token"} {
		if _, err := os.Stat(filepath.Join(job.Dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	recorded, err := loadJob(job.Dir)
	if err != nil || recorded.State != "stopped" || recorded.LastError != "" {
		t.Fatalf("stopped state was not saved: %+v, %v", recorded, err)
	}
}

func TestCtrlCReportsPeersThatCouldNotStop(t *testing.T) {
	job, _ := mockControllerJob(t, "#!/bin/sh\necho 'Network is unreachable' >&2\nexit 255\n")
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(errControllerInterrupt)
	err := stopInterruptedJob(ctx, sshRunner{quiet: true}, &job, job.Hosts)
	if !errors.Is(err, errControllerInterrupt) || !errors.Is(err, errPeerStopFailed) ||
		job.State != "stop_failed" || !strings.Contains(job.LastError, "Network is unreachable") {
		t.Fatalf("unreachable peer was not reported: state=%s, err=%v", job.State, err)
	}
}

func TestCtrlCBeforePeerLaunchNeedsNoRemoteStop(t *testing.T) {
	job, _ := mockControllerJob(t, "#!/bin/sh\necho 'Unexpected SSH call' >&2\nexit 1\n")
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(errControllerInterrupt)
	err := runJob(ctx, sshRunner{quiet: true}, &job)
	if !errors.Is(err, errControllerInterrupt) || errors.Is(err, errPeerStopFailed) || job.State != "stopped" {
		t.Fatalf("interrupt before launch should stop locally: state=%s, err=%v", job.State, err)
	}
}

func mockControllerJob(t *testing.T, script string) (Job, Manifest) {
	t.Helper()
	hash := digest([]byte("x"))
	manifest := Manifest{Version: 1, Name: "media", Size: 1, ChunkSize: 65536, SHA256: hash, Chunks: []string{hash}}
	dir := t.TempDir()
	job := Job{ID: "test-job", Dir: dir, Hosts: []string{"127.0.0.1"}, Port: 19700,
		State: "created", Dest: dir, BinDir: dir, Seed: "127.0.0.1", Imported: true,
		Fanout: 2, NodeTimeout: time.Second}
	for name, content := range map[string]string{"ssh": script, "job.token": strings.Repeat("a", 64), "swarmcopy-linux-aarch64": "binary"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := saveManifest(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return job, manifest
}
