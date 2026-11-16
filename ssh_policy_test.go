package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSSHDefaultsMatchClusterPolicy(t *testing.T) {
	args := sshRunner{cipher: "system"}.arguments("node001", "true")
	joined := strings.Join(args, " ")
	for _, s := range []string{"StrictHostKeyChecking=no", "UserKnownHostsFile=/dev/null", "LogLevel=quiet", "CheckHostIP=no", "BatchMode=yes"} {
		if strings.Count(joined, s) != 1 {
			t.Fatalf("missing or duplicate %s in %v", s, args)
		}
	}
	if strings.Contains(joined, "StrictHostKeyChecking=yes") {
		t.Fatal("strict checking still enabled")
	}
	for _, s := range []string{"aes128-gcm@openssh.com", "aes256-gcm@openssh.com", "chacha20-poly1305@openssh.com"} {
		args = sshRunner{cipher: s}.arguments("node001", "true")
		joined = strings.Join(args, " ")
		if !strings.Contains(joined, "-c "+s) {
			t.Fatal(args)
		}
	}
	if validSSHCipher("unsupported") {
		t.Fatal("unsupported cipher accepted")
	}
}
func TestSSHFailurePrecedesSourceHash(t *testing.T) {
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "ssh"), []byte("#!/bin/sh\nexit 23\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", d+string(os.PathListSeparator)+os.Getenv("PATH"))
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	source := filepath.Join(t.TempDir(), "media")
	os.WriteFile(source, []byte("source must not be hashed"), 0600)
	err := clusterCommand("distribute", []string{"--hosts", "node001", "--source", source, "--dest", "/raid/images/test", "--lifetime", "3s"})
	if err == nil {
		t.Fatal("failed SSH login accepted")
	}
	jobs, _ := filepath.Glob(filepath.Join(state, "swarmcopy/jobs", "*", "job.json"))
	if len(jobs) != 1 {
		t.Fatal(jobs)
	}
	job, err := loadJob(filepath.Dir(jobs[0]))
	if err != nil {
		t.Fatal(err)
	}
	progress, ok := preparationState(job)
	if !ok || progress.Stage != "checking-ssh" {
		t.Fatalf("hashing started before SSH login: %+v", progress)
	}
	if _, err = os.Stat(filepath.Join(job.Dir, "manifest.json")); !os.IsNotExist(err) {
		t.Fatal("manifest created after failed SSH check")
	}
	if _, err = os.Stat(filepath.Join(filepath.Dir(job.Dir), ".source-manifests")); !os.IsNotExist(err) {
		t.Fatal("source hashed before failed SSH check")
	}
}
