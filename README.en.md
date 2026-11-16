# swarmcopy

[UDP audit and all ablations](docs/QUIC-TUNING-RESULTS.en.md) · [Options and maintenance](docs/QUIC-TUNING.en.md)

The optional UDP candidate uses receive-socket sharding, negotiated payload ceilings and bounded GSO batches. TCP stays default, with four fixed workers and adaptive concurrency disabled. Select v0.3.0-dev.4 above for UDP; the regular stable release is v0.2.0.

[UDP build](https://github.com/junli-nv/swarmcopy/releases/tag/v0.3.0-dev.4) · [45-node results](docs/QUIC-RESULTS.en.md)

[UDP/QUIC implementation](docs/QUIC.en.md): select UDP chunk transport with `--transport quic`; TCP remains the default.

[Download v0.2.0](https://github.com/junli-nv/swarmcopy/releases/tag/v0.2.0) · [Verification design](docs/PIPELINE.en.md) · [45-node comparison](docs/PIPELINE-RESULTS.en.md)

New jobs default to pipelined SHA-256 with a 256-chunk prefix window. Adaptive concurrency remains off. Use `--pipeline-verify=false` to disable both or `--verify-window-chunks 0` to disable only the window. Resumed jobs retain saved settings.

Resumable P2P distribution of large files, including NeMo/SquashFS container media, to node-local storage in Linux compute clusters.

[中文（主文档）](README.md) · [Developer guide](docs/DEVELOPMENT.en.md) · [中文开发指南](docs/DEVELOPMENT.zh-CN.md) · [Validation](docs/VALIDATION.en.md)

[Cold-cache comparison protocol](docs/COLD-STUDY.en.md) · [Benchmark](docs/BENCHMARK.en.md)

## Large-cluster comparison

157 nodes (one seed + 156 receivers), a 37.43 GiB file, three pairs with fresh targets and OS caches dropped before each method: SCDP verified median **153.29 s**, rsync-over-SSH copy **362.76 s**, verified **408.73 s**: **2.37× / 2.67×** respectively. A prepared seed excludes initial staging. This compares encrypted SSH and plaintext HTTP solutions. See [all rounds, ranges, and limitations](docs/COLD-RESULTS.en.md).

## Help first

~~~bash
./swarmcopy
./swarmcopy --help
./swarmcopy distribute --help
./swarmcopy jobs --help
./swarmcopy help watch
~~~

English CLI help includes descriptions, flags, defaults, and examples. Running the executable without arguments does not start a transfer.

## Protocol name

**SCDP — SwarmCopy Distribution Protocol**, currently **SCDP/1**. Protocol and application versions are independent.

## Requirements and installation

One static executable per Linux architecture: x86_64 and aarch64. Runtime does not require pdsh, Python, Go, a tracker, Kubernetes, or a permanent agent. The controller needs OpenSSH ssh; targets need an SSH server, POSIX shell, and basic cat/chmod/mv/mkdir/uname utilities.

The controller must reach each target over SSH and TCP 19700; targets must reach one another on that TCP port. Use --port to change it. Hostnames must resolve to mutually reachable IPs everywhere. SSH-only aliases do not automatically become data-plane addresses. Destination storage should be local and have adequate space.

Download [v0.2.0 release](https://github.com/junli-nv/swarmcopy/releases/tag/v0.2.0) binaries for both architectures and SHA256SUMS into one directory:

~~~bash
sha256sum -c SHA256SUMS
chmod +x swarmcopy-linux-*
cp swarmcopy-linux-x86_64 swarmcopy  # use aarch64 on ARM controllers
./swarmcopy version
~~~

Keep both files for mixed-architecture deployment and pass --bin-dir. Same-architecture deployment can reuse the controller executable. ARM64 cross-compilation is verified; hardware-test coverage is stated in the validation notes.


The development-only swarmcopy-race executable is for race detection. It is not a runtime dependency, is not deployed to targets, and is excluded from releases. distribute automatically deploys the ordinary executable.

## First distribution

These are example names and paths. New imports require an unused destination filename.

~~~bash
./swarmcopy distribute \
  --hosts 'node[001-005]' \
  --source /shared/media/nemo.sqsh \
  --dest /raid/images --bin-dir .
~~~

The first target is the default seed. The controller sends one initial copy over SSH and builds a SHA-256 manifest. Verified chunks are then exchanged directly over HTTP/TCP; even incomplete peers can upload their valid chunks. The controller collects state without relaying peer data. This protocol is not BitTorrent-compatible.

Only the controller needs access to --source; targets do not require NFS. Keep the source immutable during import. A persistent job ID and directory are printed before deployment. Monitoring reports global/per-node progress, DL/UL rates, ETA, final verification, and seeding state.

Defaults: unlimited upload/download (0), four download workers, four upload slots, and two-hour --lifetime. Set --upload-mib 256 --download-mib 256 to limit either direction in MiB/s. Once all targets pass whole-file SHA-256, owned peers stop automatically. --keep-seeding leaves them running within their lifetime.

~~~bash
--hosts 'node001,node006,node009'
--hosts 'node[001,006,009-012]'
--hosts 'alice@node[001-005]'
--hosts @hosts.txt
~~~

Host files have one entry per line and support # comments. The 1024-host input limit is not a validated scale claim.

## SSH authentication

Reuse existing keys, ssh-agent, or SSH config. Enroll and verify host keys beforehand. Strict checking remains enabled; authorized_keys is not changed.

~~~bash
./swarmcopy distribute \
  --hosts 'node[001-005]' --ssh-user alice --ask-pass \
  --source /shared/media/nemo.sqsh --dest /raid/images --bin-dir .
~~~

--ask-pass prompts once for the same password across all targets. An in-memory local askpass service supplies it to OpenSSH without saving it in argv, files, job metadata, or logs. It requires an interactive controlling terminal. Re-enter it after restarting/resuming. Use separate batches or keys/config for different passwords. MFA, host-key enrollment, or disabled server-side password authentication require separate handling.

## Jobs and recovery

~~~bash
./swarmcopy jobs
./swarmcopy jobs --json
./swarmcopy inspect --job <ID>
./swarmcopy watch
./swarmcopy status
./swarmcopy watch --job <ID> --interval 2s
./swarmcopy resume --job <ID>
./swarmcopy resume --job <ID> --ask-pass
./swarmcopy stop --job <ID>
~~~

watch/status without arguments choose the latest active job, otherwise the latest saved job. Completed jobs show a clearly marked saved result; --live forces a live query and stopped peers may be unreachable. status emits JSON and returns nonzero for an incomplete/unreachable active job.

distribute and resume include global monitoring. Exiting a separate watch does not stop peers. Interrupted controllers leave peer processes running within their lifetime; resume reconnects and reuses matching live processes. stop signals only recorded peers whose PID, start ticks, executable argv, and token-file identity match. Files and partial data are preserved.

Records default to $XDG_STATE_HOME/swarmcopy/jobs or ~/.local/state/swarmcopy/jobs. IDs are local to the controller user. jobs shows recorded states, which may be stale after crashes; watch/status query live active jobs. Use --job /absolute/new-job-directory for a custom location and pass the same path later. jobs --state-dir /parent lists custom records. Securely migrate the entire job directory when changing controllers; it contains a private task token and must not be committed.

**Why is a completed task missing from jobs?** Listing examines direct child directories of the selected state directory; it does not recursively search disks. A different user, XDG_STATE_HOME, or custom --job location changes the scope. Custom jobs are not registered in the default directory. Pass the original directory to inspect/watch/resume instead of its bare ID.

The cold-cache benchmark stores SCDP jobs under its --out directory:

~~~bash
./swarmcopy jobs --state-dir ./cold-results-new
./swarmcopy watch --job ./cold-results-new/<case-id>-job --count 1
./swarmcopy inspect --job ./cold-results-new/<case-id>-job
~~~

Read <case-id> from study-plan.json; benchmark case IDs differ from the JOB ID shown by jobs. The rsync baseline is not a swarmcopy job: inspect progress.json, results.json, and the final summary.json in the study directory. Benchmark scripts are experiment tools, not runtime dependencies.

Keep the manifest and .part files. Resume rehashes all existing chunks, downloads only missing/corrupt ones, verifies whole-file SHA-256, and atomically publishes the final file. Valid existing final files can seed; mismatching final files are rejected rather than overwritten.

Initial controller-to-seed import is not chunk-resumable yet and may need restarting. If media was published before the manifest, inspect the state first. This limitation is separate from P2P recovery once a manifest exists.

## Prepared seed

~~~bash
# On the seed: --out must not already exist
./swarmcopy prepare --source /shared/media/nemo.sqsh --out /raid/images/nemo.sqsh

# On the controller: reuse the file plus its .manifest.json sidecar
./swarmcopy distribute \
  --hosts 'node[001-005]' --seed node001 \
  --seed-file /raid/images/nemo.sqsh --dest /raid/images --bin-dir .
~~~

## Networking and limits

Listening on all addresses does not select the outbound NIC, aggregate links, or enable native RDMA. DNS and OS routing determine data paths. This version uses plaintext HTTP on a mutually trusted, controlled cluster network. A task token is access control, not TLS; token holders can read that task's media.

--upload-mib 0 --download-mib 0 disables throttling. More workers/uploads increase resource load. Chunks default to 16 MiB; prepare --chunk-mib changes this.

100% verified bytes still requires whole-file verification. Offline nodes contribute their last known bytes or zero and never count as complete. ETA excludes full-disk verification time; totals may initially be unknown.

Low-level peer/resume accepts explicit manifests, tokens, and host:port lists. There is no automatic network/firewall configuration, disk cleanup, node reboot, or training-job scheduling.

## Verification cache and forced recheck

A fully verified final file gets a .swarmcopy.verified.json sidecar binding its manifest ID, device, inode, size, mtime, and ctime. The previous verification result is reused only when all fields match. Fresh sparse partials are not scanned; existing .part files are always rehashed, and newly received files still require whole-file SHA-256.

This cache assumes immutable files and correct OS write metadata. It does not replace periodic full verification for silent disk corruption. Use --recheck to bypass final-file caches:

~~~bash
./swarmcopy distribute --hosts 'node[001-005]' --seed node001 --seed-file /raid/images/nemo.sqsh --dest /raid/images --recheck
./swarmcopy resume --job <ID> --recheck
~~~

New prepare imports create the cache after verification. Legacy media is verified once on first use. Do not copy a cache from another file. SSH startup/stop uses at most eight concurrent operations by default; --fanout configures new jobs.

## Find and stop processes manually

~~~bash
pgrep -af swarmcopy
kill -TERM <confirmed-PID>
~~~

Prefer stop --job for an entire task. Killing the controller does not automatically terminate detached peers. Node PID records/logs are under <dest>/.swarmcopy/<ID>/. Avoid global pkill that could stop unrelated jobs. SIGKILL is a last resort; recovery revalidates retained partials.

This release accepts a single file. Directories, multiple inputs, and file globs are deferred to the next release. Concurrent jobs need different ports and independent destinations; a destination is protected by an exclusive lock.

## Development

Go 1.26+; Python is only for development tests.

~~~bash
bash scripts/build.sh
go test -race ./...
go vet ./...
python3 scripts/integration.py
~~~

The bilingual developer guides cover the protocol, locking, recovery, testing, limitations, and optimization roadmap.


