package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

var errPartialCompletion = errors.New("job partially complete; some targets failed")

func failedRemainingTargets(j Job, rows []observation, completed int) map[string]string {
	failed := make(map[string]string)
	for i, row := range rows {
		if row.err == nil {
			continue
		}
		host := j.Hosts[i]
		detail := j.NodeIssues[host]
		stage, _, _ := strings.Cut(detail, ":")
		if stage == "checking-ssh" || stage == "deploying" || stage == "starting-peers" ||
			strings.HasPrefix(detail, "FAILED: peer exited;") {
			failed[host] = detail
		}
	}
	if len(failed) == 0 || completed+len(failed) != len(rows) {
		return nil
	}
	return failed
}

func finishPartialJob(ctx context.Context, r sshRunner, j *Job, completed int, failed map[string]string) error {
	peersMayBeRunning := j.KeepSeeding
	if !j.KeepSeeding {
		completedJob := *j
		completedJob.Hosts = make([]string, 0, completed)
		for _, host := range j.Hosts {
			if _, skipped := failed[host]; !skipped {
				completedJob.Hosts = append(completedJob.Hosts, host)
			}
		}
		stopCtx, cancel := context.WithTimeout(ctx, nodeTimeout(*j))
		err := stopPeers(stopCtx, r, completedJob)
		cancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "WARNING: could not stop all completed peers: %v\n", err)
			peersMayBeRunning = true
		}
	}
	j.State = "partial"
	j.PartialCompleted = completed
	j.NodeIssues = failed
	j.LastError = errPartialCompletion.Error()
	if err := j.save(); err != nil {
		return err
	}
	if peersMayBeRunning {
		fmt.Fprintln(os.Stderr, "To stop any remaining peers from this controller:")
		fmt.Fprintln(os.Stderr, "  "+jobCommand(*j, "stop", r.password))
	}
	fmt.Fprintln(os.Stderr, "To retry the failed targets after recovery, resume the same job:")
	fmt.Fprintln(os.Stderr, "  "+jobCommand(*j, "resume", r.password))
	printPartialResult(os.Stderr, *j)
	return errPartialCompletion
}

func printPartialResult(w io.Writer, j Job) {
	fmt.Fprintf(w, "JOB PARTIALLY COMPLETE: %s | %d/%d targets verified and published\n", j.ID, j.PartialCompleted, len(j.Hosts))
	fmt.Fprintln(w, "Failed targets:")
	for _, host := range j.Hosts {
		if detail := j.NodeIssues[host]; detail != "" {
			fmt.Fprintf(w, "  %s: %s\n", host, detail)
		}
	}
}
