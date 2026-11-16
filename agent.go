package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type processRecord struct {
	PID        int
	StartTicks string
}

func startTicks(pid int) (string, error) {
	b, e := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if e != nil {
		return "", e
	}
	pos := strings.LastIndexByte(string(b), ')')
	if pos < 0 {
		return "", errors.New("invalid process record")
	}
	fields := strings.Fields(string(b[pos+1:]))
	if len(fields) < 20 || fields[0] == "Z" {
		return "", errors.New("process exited")
	}
	return fields[19], nil
}
func ownedProcess(p processRecord, exe string, o Options) bool {
	ticks, e := startTicks(p.PID)
	if e != nil || ticks != p.StartTicks {
		return false
	}
	b, e := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", p.PID))
	if e != nil {
		return false
	}
	args := bytes.Split(b, []byte{0})
	if len(args) < 2 || string(args[0]) != exe || string(args[1]) != "peer" {
		return false
	}
	for i := 2; i+1 < len(args); i++ {
		if string(args[i]) == "--token-file" && string(args[i+1]) == o.TokenFile {
			return true
		}
	}
	return false
}
func agentCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("agent start|stop|inspect --config path")
	}
	mode := args[0]
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	configureUsage(fs, "agent")
	config := fs.String("config", "", "trusted local peer configuration")
	if e := fs.Parse(args[1:]); e != nil {
		return e
	}
	data, e := os.ReadFile(*config)
	if e != nil {
		return e
	}
	var o Options
	if e = json.Unmarshal(data, &o); e != nil {
		return e
	}
	root := filepath.Dir(*config)
	exe, e := os.Executable()
	if e != nil {
		return e
	}
	fd, e := syscall.Open(filepath.Join(root, "agent.lock"), syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if e != nil {
		return e
	}
	defer syscall.Close(fd)
	if e = syscall.Flock(fd, syscall.LOCK_EX); e != nil {
		return e
	}
	recordPath := filepath.Join(root, "process.json")
	var p processRecord
	if b, e := os.ReadFile(recordPath); e == nil {
		if e = json.Unmarshal(b, &p); e != nil {
			return e
		}
	}
	alive := ownedProcess(p, exe, o)
	switch mode {
	case "inspect":
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"Alive": alive, "PID": p.PID, "Log": filepath.Join(root, "peer.log")})
	case "stop":
		if !alive {
			fmt.Println("already stopped")
			return nil
		}
		if e = syscall.Kill(p.PID, syscall.SIGTERM); e != nil {
			return e
		}
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			if !ownedProcess(p, exe, o) {
				fmt.Println("stopped", p.PID)
				return nil
			}
			time.Sleep(100 * time.Millisecond)
		}
		return errors.New("peer did not stop in 30s; no forced kill performed")
	case "start":
		if alive {
			if !o.Recheck {
				fmt.Println("already running", p.PID)
				return nil
			}
			if e = syscall.Kill(p.PID, syscall.SIGTERM); e != nil {
				return e
			}
			deadline := time.Now().Add(30 * time.Second)
			for ownedProcess(p, exe, o) && time.Now().Before(deadline) {
				time.Sleep(100 * time.Millisecond)
			}
			if ownedProcess(p, exe, o) {
				return errors.New("peer still running; cannot force recheck")
			}
		}
		argv := []string{"peer", "--transport", transportName(o.Transport), "--file", o.File, "--manifest", o.Manifest, "--listen", o.Listen,
			"--peers", o.Peers, "--token-file", o.TokenFile, "--workers", strconv.Itoa(o.Workers),
			"--uploads", strconv.Itoa(o.Uploads), "--upload-mib", strconv.FormatInt(o.UploadMiB, 10),
			"--download-mib", strconv.FormatInt(o.DownloadMiB, 10), "--lifetime", o.Lifetime.String(),
			"--stall-timeout", o.Stall.String()}
		// Always pass saved values: old jobs and explicit opt-outs must not inherit new CLI defaults.
		argv = append(argv, "--verify-window-chunks", strconv.Itoa(o.VerifyWindowChunks),
			"--pipeline-verify="+strconv.FormatBool(o.PipelineVerify))
		if o.VerifyBufferMiB > 0 {
			argv = append(argv, "--verify-buffer-mib", strconv.Itoa(o.VerifyBufferMiB))
		}
		if o.MinWorkers > 0 {
			argv = append(argv, "--min-workers", strconv.Itoa(o.MinWorkers))
		}
		if o.MaxWorkers > 0 {
			argv = append(argv, "--max-workers", strconv.Itoa(o.MaxWorkers))
		}
		if o.HedgeDelay > 0 {
			argv = append(argv, "--hedge-delay", o.HedgeDelay.String())
		}
		if o.Adaptive {
			argv = append(argv, "--adaptive")
		}
		if o.Endgame {
			argv = append(argv, "--endgame")
		}
		if o.Recheck {
			argv = append(argv, "--recheck")
		}
		f, e := os.OpenFile(filepath.Join(root, "peer.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		defer f.Close()
		cmd := exec.Command(exe, argv...)
		cmd.Stdout = f
		cmd.Stderr = f
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if e = cmd.Start(); e != nil {
			return e
		}
		ticks, e := startTicks(cmd.Process.Pid)
		if e != nil {
			cmd.Process.Kill()
			cmd.Wait()
			return e
		}
		p = processRecord{PID: cmd.Process.Pid, StartTicks: ticks}
		b, _ := json.Marshal(p)
		if e = atomicWrite(recordPath, b, 0600); e != nil {
			cmd.Process.Kill()
			cmd.Wait()
			return e
		}
		cmd.Process.Release()
		fmt.Println("started", p.PID)
		return nil
	default:
		return errors.New("unknown agent operation")
	}
}
