# swarmcopy

Resumable P2P file and directory distribution for Linux clusters, using **SCDP/1**. Distribute NeMo/SquashFS media from shared storage to node-local RAID while reducing head-node and single-seed pressure.

[Unified release](https://github.com/junli-nv/swarmcopy/releases/latest) · [中文（主文档）](README.md) · [Examples](docs/EXAMPLES.en.md) · [Development](docs/DEVELOPMENT.en.md)

**main is the only long-lived branch.** One binary includes TCP, optional UDP/QUIC, pipelined verification, optional adaptive concurrency, multiple files and directories. No separate UDP or race executable is needed. Historical tags and measurements remain available.

## v0.7.1 defaults to chunks

New distribution jobs omit --verify to use chunks/BLAKE3: every chunk is validated, then fsync and publication, without final whole-file verification. Add `--verify full` for that final check. Saved jobs retain their mode/algorithms on resume; adaptive stays off. Fsync is not content validation and depends on correct underlying storage flush behavior.

## v0.7.0: native copies and completion summaries

Scheduling avoids repeated expensive scoring of every missing chunk. The source-host destination reads and verifies local source data instead of receiving it back over the network. Successful runs print elapsed/preparation/distribution time, network/local/reused bytes and average rates; completed watch shows the saved summary, while status retains JSON. Adaptive stays off; fixed concurrency remains `--workers 4 --uploads 4`, with no rate cap by default.

Nine fresh 32 GiB destinations: mean distribution phase 32.39 → 25.10 seconds (22.50% less time), controller wall 39.68 → 31.61 seconds (20.34% less). Each round used fresh targets, cleared page cache and independent SHA256 checks, without preexisting transfer data. [Method, guarantees and maintenance](docs/OPTIMIZATION-0.7.0.en.md).

## Recommended: seed directly from a compute node

If media already resides on a compute node, run there and include it in `--hosts`. v0.7.0 serves original files directly, skipping initial seed copying; the local destination uses a native copy while remote targets exchange data over the network. A BCM controller outside the target list instead stages a compute seed through SSH. Startup and watch explicitly show the selected mode and endpoints. [Scenarios, network requirements and failures](docs/SOURCE-MODES.en.md).

```bash
# Run on HGX167; include this host as a destination
./swarmcopy distribute --hosts 'HGX[061,066,082,138,169,167]' \
  --source /raid/local-ai/dist/local-ai.tar --dest /raid/images --verify chunks
```

New jobs default to chunks (BLAKE3/BLAKE3); explicitly choose `--verify full` (SHA256/SHA256) for final whole-file verification. Failed targets do not cancel healthy siblings and remain incomplete; resume the same ID after recovery. Resumed sparse holes are skipped without treating unwritten bytes as verified.

Remove a job and exclusive partials with `./swarmcopy remove --job <ID> --purge-partials` (`delete` is an alias). It stops owned controller/peers, then removes records/remote job metadata and exclusive partials. Source files, published media and shared data remain. Failed cleanup keeps the local record for retry. Use `stop` when you only want to pause.

## Two integrity modes (v0.7.1)

For new `--source` jobs, choose `--verify full` or `--verify chunks`. Full defaults to SHA256 chunks/files with final file verification; chunks defaults to BLAKE3 chunks/files and publishes after all chunk checks and fsync, omitting the final full-file reread. From v0.7.1, omitting `--verify` selects chunks; explicitly use `--verify full` for final whole-file verification. No hash flags are needed.

```bash
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh --dest /raid/images --verify chunks
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh --dest /raid/images --verify full
```

`--chunk-hash` / `--file-hash` remain advanced overrides. Resume/prepared seed manifests retain their algorithms; changing mode never rewrites a manifest. An existing SHA256 job still uses SHA256 chunks after switching to chunks mode.

## Integrity algorithms and SSH options

Defaults are chunks + BLAKE3, omitting the final disk reread. Choose `--verify full` for final file verification, or `--verify full --chunk-hash xxh3-128 --file-hash blake3` for fast chunks plus a cryptographic file digest. XXH3 is rejected in chunks mode. Initial source hashing still reads every byte. See [guarantees, combinations and measured hardware results](docs/INTEGRITY.en.md).

SSH defaults include `StrictHostKeyChecking=no`, `UserKnownHostsFile=/dev/null`, `LogLevel=quiet`, `CheckHostIP=no`. Repeat `--ssh-opt KEY=VALUE` to extend/override saved defaults, e.g. `--ssh-opt ConnectTimeout=20 --ssh-opt ServerAliveInterval=15`. Select AES-GCM with `--ssh-cipher aes128-gcm@openssh.com`.

## Large-source progress and Ctrl+C

From v0.4.1, source hashing and initial seed import report bytes, percent, rate and estimated remaining phase time about every two seconds. Deployment, inventory and listener readiness report stage/node counts/elapsed time. Watch no longer labels preparation as all peers offline. Source reads honor Ctrl+C between buffers. Estimates exclude later work. [Examples, cancellation limits and validation](docs/PROGRESS.en.md).

## Installation

Download `swarmcopy-linux-x86_64` or `swarmcopy-linux-aarch64`, verify `SHA256SUMS`, rename to `swarmcopy` and make executable. Keep both original architecture filenames in `dist` and use `--bin-dir ./dist` for mixed architectures; the controller can deploy itself to same-architecture nodes.

Runtime requires Linux, an OpenSSH client on the controller, SSH services and basic shell utilities on targets. No pdsh, rsync, external tar, Python, Go, tracker, database or permanent service is required. Go/Python are development tools.

Preconfigure SSH keys, agent or config. Alternatively use `--ssh-user alice --ask-pass`: one hidden prompt for the SAME password on all hosts, never persisted. SSH defaults to StrictHostKeyChecking=no, UserKnownHostsFile=/dev/null, LogLevel=quiet and CheckHostIP=no; no host-key enrollment is required.

## Quick start

~~~bash
./swarmcopy
./swarmcopy distribute --help
./swarmcopy distribute --hosts 'node[001-005]' \
  --source /shared/nemo.sqsh --dest /raid/images --bin-dir ./dist
./swarmcopy jobs
./swarmcopy watch
~~~

Sources are local to the controller running the command; destinations are local disks on every target. A first trusted manifest requires reading and hashing the source. Later jobs may reuse its local verification cache when source inode/device/size/mtime/ctime and directory membership are unchanged. `--recheck` forces a reread.

Before transfer, each target is inventoried. Existing complete replicas seed missing nodes, skipping controller-to-seed media transfer. A controller that is also a target directly serves the original files; otherwise one initial seed receives an SSH stream before peer exchange. All peers stop after completion unless `--keep-seeding` is supplied.

## Multiple files, globs and directories

~~~bash
# Directly creates /raid/images/nemo.sqsh and /raid/images/config.json
./swarmcopy distribute --hosts 'node[001-005]' --dest /raid/images \
  --source /shared/nemo.sqsh --source /shared/config.json

# Quoted glob expanded by swarmcopy
./swarmcopy distribute --hosts 'node[001-005]' --dest /raid/images \
  --source '/shared/*.sqsh'

# Shell-expanded or positional inputs also work
./swarmcopy distribute --hosts 'node[001-005]' --dest /raid/images /shared/*.sqsh

# Preserves the input directory name: /raid/images/nemo/...
./swarmcopy distribute --hosts 'node[001-005]' --dest /raid/images --source /shared/nemo
~~~

Directories recurse, preserving internal layout and empty files/directories. A trailing slash does not change layout. Top-level basenames must be unique; overlapping source roots are rejected and repeated identical inputs are deduplicated. Unmatched globs fail. Globs support `*`, `?` and `[]`; `**` has no special recursive meaning—pass a directory for recursion.

Peers write native staged files: no full tar artifact or post-transfer unpacking phase. Only initial SSH import uses an in-memory tar stream. One job and chunk scheduler cover the entire dataset, including chunks spanning file boundaries.

## Existing files and conflicts

Matching SHA256 files are reused and can seed others. Missing nodes fetch data; complete nodes do not download it again. If every node is complete, media download is zero.

For different regular files, default `--existing ask` lists conflicts and prompts once on the controller:

- `resume`: reuse verified matching chunks from an independent staged copy of the old file.
- `replace`: receive conflicting files afresh, then replace them.
- `keep`: cancel and preserve existing files.

~~~bash
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --existing resume
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --existing replace
~~~

Use an explicit policy in unattended scripts when conflicts are possible. The choice applies to all detected regular-file conflicts. Same-name directories merge; extra destination files are retained. File/directory type collisions, symbolic links and special files require manual handling.

Old final files remain intact until chunk and per-file SHA256 verification succeeds. Publication/replacement is atomic **per file**, not across the entire dataset. An interrupted publication can leave some completed files; resume reuses them. Allow disk space for independent staged replacement data.

## Download/upload concurrency

~~~bash
# Fixed per-node concurrency
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --workers 8 --uploads 8

# Automatic downloads: initial 4, minimum 2, maximum 16; uploads remain capped at 8
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --adaptive --workers 4 --min-workers 2 --max-workers 16 --uploads 8
~~~

Defaults: four download workers and four upload requests per node, each within 1..32. Workers are parallel chunk requests, not an exact socket count. Adaptive control changes download concurrency only; minimum defaults to the initial worker count, maximum to 16. It is not always faster and **remains off by default**. Settings are saved at job creation; there is currently no live concurrency-edit command.

No default bandwidth cap. `--download-mib 200 --upload-mib 200` sets aggregate per-node MiB/s; zero means unlimited.

## Monitor, stop and resume

~~~bash
./swarmcopy jobs
./swarmcopy jobs --json
./swarmcopy inspect --job <ID>
./swarmcopy watch --job <ID> --interval 2s --plain
./swarmcopy status --job <ID>
./swarmcopy stop --job <ID>
./swarmcopy resume --job <ID>
./swarmcopy resume --job <ID> --existing resume --ask-pass
~~~

Without a selector, watch/status choose the latest active job, otherwise the latest saved job. Completed jobs show recorded results; `--live` explicitly queries peers. The global monitor shows dataset file counts, per-node progress, aggregate rates, verification phases and completed nodes. 100% bytes still requires verification/publication.

Jobs live under `~/.local/state/swarmcopy/jobs`, honoring `XDG_STATE_HOME`. Retain job metadata, token, manifest and node `--dest/.swarmcopy-trees/<manifest-id>/data` for resume. Input glob matches are frozen at creation; resume does not add newly matching files.

Use `stop --job` to stop owned processes by PID/start-time checks. Nodes run the same normal binary, with `.swarmcopy/<job-id>/process.json` and `peer.log` for diagnosis. Avoid a cluster-wide `pkill swarmcopy`. Leaving watch only exits the monitor.

Incomplete initial SSH import restarts that phase; it is not yet chunk-resumable. P2P transfer supports restart, repair and retries. Legacy v1 single-file jobs and the `prepare --seed-file` workflow remain compatible and retain their existing-final no-overwrite rule. New SHA256 source jobs use v2; custom algorithms and the chunks preset use v3, with the conflict policies above.

## Network and integrity

TCP remains default; peers listen on all local addresses at port 19700. Hostname resolution from `--hosts` chooses routing; listening on every interface does not aggregate NIC bandwidth. Resolve names to the intended data network.

~~~bash
# UDP/QUIC is already included
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --transport quic
~~~

QUIC also needs UDP at that port; control/status stay on TCP, with no silent TCP data fallback. Receive-socket sharding, negotiated jumbo ceilings and bounded GSO are included. Existing cluster measurements still favor TCP, so the default stays unchanged.

Explicit full mode defaults to pipelined file verification and a 256-chunk window; chunks disables the whole-file reread and that window. `--pipeline-verify=false` disables both; `--verify-window-chunks 0` disables only the window. SHA256 and BLAKE3 are cryptographic digests with CPU acceleration where supported.

Target caches bind every verified file's inode/device/size/mtime/ctime to its complete manifest ID; `--recheck` bypasses them. Keep sources and destinations immutable to other writers during a job. HTTP control and TCP data are plaintext; QUIC data uses task-bound TLS. Use a trusted cluster network.

## Scope and maintenance

Supports regular files and directories, at most 100,000 entries. Symbolic links are rejected, not followed; devices, FIFOs and sockets are unsupported. Source hardlinks copy as independent files. UID/GID, ACLs, xattrs, hardlink relationships and sparse layout are not preserved. Newly published files retain ordinary permission bits and mtime, stripping special bits. Matching existing files are unchanged; dataset directory modes/mtime are applied from source. Extra target files are never deleted.

[Tree design and validation](docs/TREES.en.md) · [Developer guide](docs/DEVELOPMENT.en.md) · [QUIC maintenance](docs/QUIC-TUNING.en.md)

Historical v0.1.0 single-file cold-cache study: 157 nodes, verified SCDP median 153.29s vs rsync-over-SSH copy 362.76s / verified 408.73s, excluding initial import. This compares encrypted SSH and plaintext HTTP solutions, not a new multi-file release benchmark. [All rounds and limitations](docs/COLD-RESULTS.en.md) · [Pipeline comparison](docs/PIPELINE-RESULTS.en.md) · [All UDP ablations](docs/QUIC-TUNING-RESULTS.en.md).

~~~bash
go test -mod=vendor -race ./...
bash scripts/build.sh
~~~

Build with Go 1.26+; dependencies are vendored. See QUIC maintenance for reproducible vendor patches. x86_64 has real-node validation; aarch64 is statically cross-built without an ARM hardware run in this change. Future development enters main; historical release tags remain.
