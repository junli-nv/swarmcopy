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
	fmt.Print(`swarmcopy - resumable P2P file and directory distribution for Linux clusters
Protocol: SCDP/1 (SwarmCopy Distribution Protocol)

QUICK START
  swarmcopy distribute --hosts 'node[001-005]' --source /shared/nemo.sqsh --dest /raid/images
  swarmcopy distribute --hosts @hosts.txt --dest /raid/images --source /shared/nemo --source /shared/config.json
  swarmcopy jobs
  swarmcopy watch
  swarmcopy distribute --hosts @hosts.txt --source /shared/media.sqsh --dest /raid/images --workers 8 --uploads 8
  swarmcopy distribute --hosts @hosts.txt --source /shared/media.sqsh --dest /raid/images --adaptive --workers 4 --min-workers 2 --max-workers 16 --uploads 8
  swarmcopy resume --job <ID>

COMMANDS
  distribute  Deploy peers through SSH, select direct/staged source, distribute and monitor
  jobs        List saved job IDs and recorded states on this controller
  inspect     Show a saved job's configuration and directory
  status      Print JSON; default: latest active job, otherwise latest job
  watch       Show global progress; default: latest active/latest saved job
  resume      Reconnect a saved job and recover missing/corrupt chunks
  cleanup     Clean completed deployment/staging; preserve media and job history
  remove      Stop a job and remove records/logs; alias: delete; optional partial cleanup
  stop        Stop only the selected job's recorded peer processes
  prepare     Copy an immutable source into a seed file and build its manifest
  peer        Run one peer directly with a manifest and token
  benchmark   Measure compute-node P2P network throughput using memory only
  hashbench   Measure memory/file SHA256 and optionally record a CPU profile
  version     Print the executable version

HELP
  swarmcopy <command> --help
  swarmcopy help <command>

REQUIREMENTS
  Linux x86_64/aarch64; controller ssh client; target SSH services and basic
  shell utilities; mutually reachable peer hostnames and TCP port 19700.
  Existing SSH keys/agent/config or --ask-pass. SSH host-key checks are disabled.
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
	case "benchmark":
		return benchmarkUsage
	case "hashbench":
		return "Measure SHA256 without changing input files. CPU profiles require a NEW path.\n\nExamples:\n  swarmcopy hashbench --bytes-mib 2048\n  swarmcopy hashbench --file /raid/images/media.sqsh --cpu-profile /tmp/new-sha.pprof\n"
	case "cleanup":
		return `Clean only a completed job's remote deployment and empty staging.
Media and controller records stay available to jobs/watch/status.
One reusable .swarmcopy/cache.json and last-peer.log remain per destination.
Incomplete/shared datasets and live peer locks are protected; errors are retryable.

Examples:
  swarmcopy cleanup --job <ID>
  swarmcopy cleanup --job /absolute/completed/job --ask-pass
`
	case "distribute":
		return `Create a saved job and distribute files or directory trees through SSH/P2P.
Source selection is automatic and printed before preparation:
  Controller is a target: serve original local files directly (recommended).
  Controller is not a target: copy once to a compute seed through SSH.
Direct mode creates the local destination by reading/verifying original files
locally; local copies are excluded from network download statistics.
The direct source listens on a separate dynamic TCP port (also UDP for QUIC);
compute nodes must reach it. Keep the source controller running while needed.

SSH cipher defaults to auto: prefer AES-GCM on AES/GCM-capable controllers,
with negotiation fallback; --ssh-cipher system leaves OpenSSH selection alone.
SSH login is checked before source hashing. SSH uses StrictHostKeyChecking=no,
UserKnownHostsFile=/dev/null, LogLevel=quiet and CheckHostIP=no.
Repeat --ssh-opt KEY=VALUE to extend these defaults; the last same-key value wins.
Options are saved for resume. Explicit --ssh-cipher overrides --ssh-opt Ciphers.
Source hashing and staged seed copying show byte progress, rate and estimated
time remaining every two seconds. Other preparation stages show node counts
and elapsed time. Ctrl-C cancels source hashing between read buffers.
Multiple files go directly under --dest; directories retain their basename.
Repeat --source, use a quoted glob, or pass source operands.
Matching existing files are reused and can seed missing nodes. If a complete
replica already exists, controller-to-seed media transfer is skipped.
Conflicting regular files prompt once with --existing ask (default).
Use --existing resume to reuse matching chunks, replace to receive conflicting
files afresh, or keep to cancel safely. Old files stay intact until verification.
Publication is atomic per file, not across the whole dataset.
Healthy nodes continue when other nodes are unavailable. Monitoring starts
without waiting for every listener. Failed/pending nodes remain incomplete.
Per-node diagnostics identify the host, stage, peer process and recent log.
--node-timeout bounds each SSH/deployment/inventory attempt (default 1m);
slow existing-file inventory can continue inside the peer after startup.
Resume the same job after recovery; this does not automatically redeploy offline hosts.

Examples:
  swarmcopy distribute --hosts 'node[001-005]' --source /shared/nemo.sqsh --dest /raid/images
  swarmcopy distribute --hosts @hosts.txt --dest /raid/images --source /shared/nemo --source /shared/config.json
  swarmcopy distribute --hosts @hosts.txt --dest /raid/images --source '/shared/*.sqsh'
  swarmcopy distribute --hosts @hosts.txt --dest /raid/images /shared/*.sqsh
  swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh --existing resume
  swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh --workers 8 --uploads 8
  swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh --adaptive --workers 4 --min-workers 2 --max-workers 16 --uploads 8
  swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh --verify full --chunk-hash xxh3-128 --file-hash blake3
  swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh --verify chunks
  swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh --ssh-opt ServerAliveInterval=15 --ssh-opt ConnectTimeout=20
  swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh --transport quic
  swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh --ssh-user alice --ask-pass --bin-dir ./dist
  swarmcopy distribute --hosts @hosts.txt --seed node001 --seed-file /raid/images/nemo.sqsh

Workers/uploads are per-node chunk-request concurrency, each 1..32; defaults 4/4.
Adaptive changes downloads only and stays OFF unless requested.
Minimum defaults to initial workers; maximum defaults to 16.
Concurrency is saved at startup; no live-edit command is provided.
Bandwidth defaults to unlimited; upload-mib/download-mib set per-node MiB/s.
TCP stays default; optional QUIC needs UDP as well as TCP on the chosen port.
Choose one preset for a NEW source job:
  --verify chunks  BLAKE3 chunks + BLAKE3 files, without final disk reread (default)
  --verify full    SHA256 chunks + SHA256 files, with final disk verification
No hash flags are needed; --chunk-hash/--file-hash are advanced overrides.
Resume keeps the saved mode (legacy empty mode means full); it never adopts
the new-job default. Prepared seed manifests keep their original algorithms.
--verify chunks validates every cryptographic chunk, then syncs/publishes without
a final disk reread. It cannot detect damage after a successful chunk write.
--chunk-hash sha256|blake3|xxh3-128; --file-hash sha256|blake3.
XXH3-128 requires full mode and a cryptographic file hash.
Pipelined full-file verification and a 256-chunk window default ON in full mode.
--pipeline-verify=false disables both; --verify-window-chunks 0 disables the window.
SHA256 jobs use v2 manifests; custom hash jobs use v3 and require v0.5+ peers.
Legacy prepared --seed-file workflows remain supported.
Symbolic links/special files and overlapping roots are rejected.
Unmatched globs and duplicate top-level basenames are errors.
Completed peers stop and their deployment/empty staging is cleaned automatically,
unless --keep-seeding is set. One reusable .swarmcopy cache/log area remains.
Success prints elapsed/preparation/distribution time and average network rate.
The saved summary separates network, local-copy and reused bytes; aggregate
cluster throughput is labeled separately from receiving-node averages.
Use exactly one source group or --seed-file, never both.
See docs/EXAMPLES.en.md and README.md (Chinese).
`
	case "resume":
		return `Resume a saved job; valid partial chunks are rehashed and retained.
Existing live peers are reused. SSH passwords are requested again if needed.

Examples:
  swarmcopy jobs
  swarmcopy resume --job <ID>
  swarmcopy resume --job /absolute/job-directory --ask-pass
  swarmcopy resume --job <ID> --existing resume

Keep the job directory, token, manifest, and node .part files or
--dest/.swarmcopy/trees/<manifest-id>/data (legacy .swarmcopy-trees stays readable).
Saved input selections do not expand again during resume.
Initial controller-to-seed import is not yet chunk-resumable.
Use --recheck to bypass unchanged final-file verification caches.
--verify full|chunks changes verification mode and forces a peer restart/recheck.
Hash algorithms are bound to the saved manifest; changing them needs a new job.
The new-job BLAKE3 preset does not rewrite existing SHA256 manifests on resume.
Completion averages count bytes added in this invocation, not old live-peer counters.
Repeat --ssh-opt KEY=VALUE to extend or override saved SSH options.
`
	case "watch":
		return `Monitor all nodes; no arguments selects the latest active/latest saved job.
Shows preparation phases, source hash/import bytes and estimated remaining time,
then per-node P2P progress, rates, verification, offline nodes and ETA.
Preparation does not query unopened peer ports; it refreshes saved job state.
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
	case "remove", "delete":
		return `Stop this job controller/owned peers, then remove its records and remote job directories.
Source files and published destination files are preserved.
--purge-partials also removes exclusive unpublished partials; shared datasets stay.
Any unreachable/failed cleanup retains the local job record for retry.

Examples:
  swarmcopy remove --job <ID>
  swarmcopy remove --job <ID> --purge-partials
  swarmcopy delete --job <ID> --purge-partials --ask-pass
`
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

--verify full|chunks selects final verification; algorithms come from the manifest.
For automated SSH deployment, prefer "distribute" instead.
`
	}
	return "Internal command; prefer distribute/jobs/watch/status/resume/stop.\n"
}
func configureUsage(fs *flag.FlagSet, name string) {
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), usageText(name))
		suffix := ""
		if name == "distribute" {
			suffix = " [sources...]"
		}
		fmt.Fprintf(fs.Output(), "Usage: swarmcopy %s [flags]%s\n\nFlags:\n", name, suffix)
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
		Summary  *CompletionSummary
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
		summary[row.addr] = map[string]any{"Complete": s.Complete, "Size": s.Size, "VerifiedBytes": s.VerifiedBytes, "Downloaded": s.Downloaded, "Uploaded": s.Uploaded, "LocalCopied": s.LocalCopied, "From": s.From, "Transport": s.Transport, "Performance": s.Performance}
	}
	report := map[string]any{"Protocol": protocol, "Recorded": true, "JobID": j.ID, "Finished": result.Finished, "Nodes": summary}
	if result.Summary != nil {
		report["Summary"] = result.Summary
	}
	return rows, report, nil
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
	case "cleanup":
		return cleanupCommand(args)
	case "remove", "delete":
		return removeCommand(args)
	case "jobs":
		return jobsCommand(args)
	case "inspect":
		_, e := jobSelector(args)
		return e
	case "prepare":
		return prepare(args)
	case "benchmark":
		return benchmarkCommand(args)
	case "hashbench":
		return hashbenchCommand(args)
	case "peer":
		return peer(args)
	default:
		rootHelp()
		return nil
	}
}
