package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func rootHelp() {
	fmt.Print(`swarmcopy - resumable P2P file distribution for Linux clusters
Protocol: SCDP/1 (SwarmCopy Distribution Protocol)

QUICK START
  swarmcopy distribute --hosts 'node[001-005]' --source /shared/nemo.sqsh --dest /raid/images
  swarmcopy jobs
  swarmcopy watch
  swarmcopy resume --job <ID>

COMMANDS
  distribute  Deploy peers through SSH, stage one seed, distribute and monitor
  jobs        List saved job IDs and recorded states on this controller
  inspect     Show a saved job's configuration and directory
  status      Print JSON; default: latest active job, otherwise latest job
  watch       Show global progress; default: latest active/latest saved job
  resume      Reconnect a saved job and recover missing/corrupt chunks
  stop        Stop only the selected job's recorded peer processes
  prepare     Copy an immutable source into a seed file and build its manifest
  peer        Run one peer directly with a manifest and token
  version     Print the executable version

HELP
  swarmcopy <command> --help
  swarmcopy help <command>

REQUIREMENTS
  Linux x86_64/aarch64; controller ssh client; target SSH services and basic
  shell utilities; mutually reachable peer hostnames and TCP port 19700.
  Existing SSH keys/agent/config or --ask-pass; host keys must be enrolled.
  No pdsh, Python, Go installation, tracker, or permanent agent is required.

DOCS
  README.md (Chinese), README.en.md (English), docs/DEVELOPMENT.*.md
  Jobs default to ~/.local/state/swarmcopy/jobs (honors XDG_STATE_HOME).
  --ask-pass prompts once for the SAME SSH password on every selected host.
  Peer HTTP is plaintext: use a trusted, controlled cluster network.
`)
}
func usageText(name string) string {
	switch name {
	case "distribute":
		return `Create a saved job, bootstrap peers over SSH, and distribute one file.
The controller prints a job ID and monitors until all files pass SHA256.
Completed peers stop automatically unless --keep-seeding is set.

Examples:
  swarmcopy distribute --hosts 'node[001-005]' --source /shared/nemo.sqsh --dest /raid/images
  swarmcopy distribute --hosts 'node[001,006,009-012]' --ssh-user alice --ask-pass --source /shared/nemo.sqsh
  swarmcopy distribute --hosts @hosts.txt --seed node001 --seed-file /raid/images/nemo.sqsh --bin-dir ./dist

Use exactly one of --source (controller-local file) or --seed-file
(prepared file on --seed, plus its .manifest.json sidecar).
New source imports never overwrite existing destination files.
`
	case "resume":
		return `Resume a saved job; valid partial chunks are rehashed and retained.
Existing live peers are reused. SSH passwords are requested again if needed.

Examples:
  swarmcopy jobs
  swarmcopy resume --job <ID>
  swarmcopy resume --job /absolute/job-directory --ask-pass

Keep the job directory, token, manifest, and node .part files.
Initial controller-to-seed import is not yet chunk-resumable.
Use --recheck to bypass unchanged final-file verification caches.
`
	case "watch":
		return `Monitor all nodes; no arguments selects the latest active/latest saved job.
Shows progress, DL/UL rates, final verification, offline nodes, and ETA.
Exiting this monitor does not stop peer processes.

Examples:
  swarmcopy watch
  swarmcopy watch --job <ID>
  swarmcopy watch --job <ID> --interval 2s --count 3 --plain
  swarmcopy watch --job <ID> --until-complete --timeout 30m

Completed jobs show saved results; use --live to explicitly query stopped peers.
`
	case "status":
		return `Print machine-readable JSON for a saved job or explicit peer list.
No arguments selects the latest active/latest saved job.
An incomplete/unreachable active job returns a nonzero exit code.
Completed saved jobs return their completion record unless --live is requested.

Examples:
  swarmcopy status
  swarmcopy status --job <ID>
  swarmcopy status --job <ID> --live
`
	case "jobs":
		return `List local saved job IDs, media, target counts, and recorded states.
Recorded states can be stale after a controller crash; watch queries live nodes.

Examples:
  swarmcopy jobs
  swarmcopy jobs --json
  swarmcopy jobs --state-dir /path/to/custom/jobs
`
	case "inspect":
		return "Show saved metadata (never password/token contents).\n\nExample:\n  swarmcopy inspect --job <ID>\n"
	case "stop":
		return `Stop only the selected job's owned processes; preserve all media/partial files.
A job ID is required. Resume it later using the same record.

Examples:
  swarmcopy stop --job <ID>
  swarmcopy stop --job <ID> --ask-pass
`
	case "prepare":
		return `Import one immutable file and create <out>.manifest.json.
The output and manifest must not already exist. Default chunk size: 16 MiB.

Example (on the seed):
  swarmcopy prepare --source /shared/nemo.sqsh --out /raid/images/nemo.sqsh
`
	case "peer":
		return `Run one peer directly. It serves verified chunks while downloading.
Keep serving until SIGTERM/SIGINT or --lifetime. Resume by rerunning the same
command, or use "resume" with these same low-level flags (without --job).

Example:
  swarmcopy peer --file /raid/images/nemo.sqsh --manifest ./manifest.json --token-file ./job.token --peers node001:19700,node003:19700

For automated SSH deployment, prefer "distribute" instead.
`
	}
	return "Internal command; prefer distribute/jobs/watch/status/resume/stop.\n"
}
func configureUsage(fs *flag.FlagSet, name string) {
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), usageText(name))
		fmt.Fprintf(fs.Output(), "Usage: swarmcopy %s [flags]\n\nFlags:\n", name)
		fs.PrintDefaults()
	}
}
func latestJob() (Job, error) {
	entries, e := os.ReadDir(stateDir())
	if e != nil {
		return Job{}, errors.New("no saved jobs; run 'swarmcopy distribute --help' to create one")
	}
	jobs := []Job{}
	for _, v := range entries {
		if v.IsDir() {
			if j, e := loadJob(filepath.Join(stateDir(), v.Name())); e == nil {
				jobs = append(jobs, j)
			}
		}
	}
	sort.Slice(jobs, func(a, b int) bool { return jobs[a].Created > jobs[b].Created })
	for _, j := range jobs {
		if j.State != "complete" && j.State != "stopped" {
			return j, nil
		}
	}
	if len(jobs) > 0 {
		return jobs[0], nil
	}
	return Job{}, errors.New("no saved jobs; run 'swarmcopy distribute --help' to create one")
}
func selectMonitorJob(list, tf, id *string) error {
	if *list == "" && *tf == "" && *id == "" {
		j, e := latestJob()
		if e != nil {
			return e
		}
		*id = j.Dir
		fmt.Fprintf(os.Stderr, "Selected job %s (%s). Override with --job <ID>.\n", j.ID, j.State)
	}
	return nil
}
func completedJobRows(j Job) ([]observation, any, error) {
	b, e := os.ReadFile(filepath.Join(j.Dir, "result.json"))
	if e != nil {
		return nil, nil, e
	}
	var result struct {
		Finished string
		Nodes    map[string]Status
	}
	if e = json.Unmarshal(b, &result); e != nil {
		return nil, nil, e
	}
	at, _ := time.Parse(time.RFC3339, result.Finished)
	rows := []observation{}
	for _, a := range j.addresses() {
		s, ok := result.Nodes[a]
		if !ok || !s.Complete {
			return nil, nil, errors.New("incomplete saved result")
		}
		s.Recorded = true
		if s.Protocol == "" {
			s.Protocol = protocol
		}
		rows = append(rows, observation{addr: a, st: s, at: at})
	}
	summary := map[string]any{}
	for _, row := range rows {
		s := row.st
		summary[row.addr] = map[string]any{"Complete": s.Complete, "Size": s.Size, "VerifiedBytes": s.VerifiedBytes, "Downloaded": s.Downloaded, "Uploaded": s.Uploaded, "From": s.From}
	}
	return rows, map[string]any{"Protocol": protocol, "Recorded": true, "JobID": j.ID, "Finished": result.Finished, "Nodes": summary}, nil
}
func commandHelp(name string) error {
	args := []string{"--help"}
	switch strings.TrimSpace(name) {
	case "distribute", "stop":
		return clusterCommand(name, args)
	case "resume":
		return clusterCommand(name, args)
	case "watch":
		return watchCommand(args)
	case "status":
		return statusCommand(args)
	case "jobs":
		return jobsCommand(args)
	case "inspect":
		_, e := jobSelector(args)
		return e
	case "prepare":
		return prepare(args)
	case "peer":
		return peer(args)
	default:
		rootHelp()
		return nil
	}
}
