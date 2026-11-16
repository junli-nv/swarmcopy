# swarmcopy

Resumable P2P file and directory distribution for Linux clusters, using **SCDP/1**. Nodes forward verified chunks to other nodes, reducing pressure on the BCM/NFS head node and a single seed.

[Download](https://github.com/junli-nv/swarmcopy/releases/latest) · [中文（主文档）](README.md) · [Documentation](docs/README.en.md) · [Examples](docs/EXAMPLES.en.md)

## Installation and SSH

Runtime requires Linux, an OpenSSH client on the controller, SSH services and basic shell tools on targets. No pdsh, rsync, Python, Go or permanent service is needed. One executable contains all features.

Download both architecture binaries and `SHA256SUMS` into `dist/`, then run on the controller:

```bash
(cd dist && sha256sum -c SHA256SUMS)
cp dist/swarmcopy-linux-x86_64 ./swarmcopy
chmod +x ./swarmcopy dist/swarmcopy-linux-*
./swarmcopy version
```

On an ARM controller, copy `swarmcopy-linux-aarch64` instead. Keep both original filenames in `dist/` for automatic deployment by target architecture; a homogeneous cluster can also deploy the controller executable itself. x86_64 has hardware validation; aarch64 is a static cross-build without ARM hardware validation.

Ensure the **machine running the command can SSH to every target, including itself when listed**. Use SSH keys, agent or config, or add `--ssh-user root --ask-pass` for one hidden prompt for the same password on every node. The program does not configure SSH trust. Host-key checking is disabled by default; see [SSH examples](docs/EXAMPLES.en.md).

## Quick start

Prefer running on the compute node holding the media and include that node among targets. A common case is running on HGX167 to distribute `/raid/nemo*` into the same `/raid` paths everywhere:

```bash
./swarmcopy distribute \
  --hosts 'HGX[061,066,082,138,169,167]' \
  --source '/raid/nemo*' \
  --dest /raid
```

The local originals are checked, reused and served without copying back onto themselves. Other nodes exchange verified chunks. If the local destination differs, its copy uses native source reads, chunk validation and writes. Nodes with complete matching files do not download them again.

Sources are on the **machine executing the command**. A BCM controller outside the target list first copies media through SSH to a compute seed when needed, then uses P2P:

```bash
./swarmcopy distribute --hosts 'HGX[061,066,082]' \
  --source /shared/nemo.sqsh --dest /raid/images
```

`SOURCE MODE: direct/staged` explains the selected path. `hashing-source` builds a trusted manifest and initially reads every source byte; unchanged sources can reuse a cache. `importing` means seed copying, not unpacking or importing a container. Preparation and transfer show progress; an ETA describes the current phase, not the entire remaining job.

## Files, directories and existing data

```bash
# Files go directly under the destination
./swarmcopy distribute --hosts @hosts.txt --dest /raid/images \
  --source /shared/nemo.sqsh --source /shared/config.json

# Keep the directory name: /raid/images/models/...
./swarmcopy distribute --hosts @hosts.txt \
  --source /shared/models --dest /raid/images
```

Quote globs for program expansion. `'/raid/nemo*'` matches immediate entries under `/raid`, not files under `/raid/images`. Matched directories are copied recursively, including empty files/directories. Inputs must have distinct top-level names and no overlapping parent/child roots. Selections are frozen at job creation; resume does not add newly matching inputs.

Matching files are reused; missing data is filled only where needed. Different content at the same filename prompts by default. For scripts, choose explicitly:

| Option | Behavior |
| --- | --- |
| `--existing resume` | Reuse matching chunks from old files, fill the rest, then replace |
| `--existing replace` | Receive conflicting files afresh and replace after verification |
| `--existing keep` | Cancel on content conflicts and retain old files |

Publication is atomic per file, not across a dataset. Extra target files are not deleted, so this is not a directory mirror with deletion. Regular files/directories are supported; symlinks and special files are not. See [layout and limits](docs/TREES.en.md). Keep sources and targets unchanged by other writers during a job.

## Monitor, stop and resume

Startup prints the job ID. On the same controller:

```bash
./swarmcopy jobs
./swarmcopy inspect --job <ID>
./swarmcopy watch --job <ID>
./swarmcopy status --job <ID>
./swarmcopy stop --job <ID>
./swarmcopy resume --job <ID>
```

Watch shows global text progress; status returns JSON. Without an ID, they select the latest active job, otherwise the latest saved job. Successful runs print and save elapsed time and average rates. Network, native-copy and reused bytes are separate; aggregate cluster rate is not one node's network speed.

A failed node does not cancel healthy nodes, but remains incomplete. Recover it and resume the same ID. Keep controller records in `~/.local/state/swarmcopy/jobs` (honoring `XDG_STATE_HOME`) and node partials. Initial source hashing and unfinished SSH seed import restart their phase; P2P recovery rechecks and retains valid chunks.

Leaving watch only stops monitoring. Ctrl+C stops the distribution controller, but independent peers may continue; use stop for that job rather than global `pkill swarmcopy`. See [progress and cancellation](docs/PROGRESS.en.md).

## Integrity modes

| Mode | New-job hash preset | Completion |
| --- | --- | --- |
| `chunks` (default) | BLAKE3 chunks/files | Validate every chunk, fsync and publish; omit final whole-file verification |
| `full` | SHA256 chunks/files | Additionally verify final file contents before completing publication; hashing can overlap transfer |

No integrity flags are needed normally; add `--verify full` for the final content check. Resume retains saved mode/algorithms. Fsync relies on correct storage flush behavior and does not validate content; chunks cannot detect damage after a successful chunk write. After resolving storage faults, same-ID recovery rechecks retained data. See [integrity guarantees and advanced algorithms](docs/INTEGRITY.en.md).

## Cleanup after completion

After every target verifies and publishes, stop peers and clean owned deployment/empty staging automatically. One destination `.swarmcopy` reuses `cache.json` and an at-most-8-MiB `last-peer.log`; it does not permanently accumulate a directory per job or move media into home directories.

| Operation | Purpose |
| --- | --- |
| `stop --job <ID>` | Stop the job and retain recovery data |
| `cleanup --job <ID>` | Clean an old completed job or retry cleanup, retaining history |
| `remove --job <ID>` | Delete job records/deployment; alias: delete |
| `remove --job <ID> --purge-partials` | Additionally discard exclusive partial data and its recovery path |

Interrupted/shared data, active locks and uncertain ownership are protected. `--keep-seeding` retains seeding peers until explicit cleanup. Cleanup failures are reported and remain retryable. See [metadata and cleanup rules](docs/CLEANUP.en.md).

## Common options and networking

| Option | Default / meaning |
| --- | --- |
| `--hosts` | List, quoted `'node[001-005]'`, or `@hosts.txt` |
| `--workers` / `--uploads` | Four per-node downloads/uploads; range 1..32 |
| `--download-mib` / `--upload-mib` | Aggregate per-node MiB/s; default 0 is unlimited |
| `--port` | Peer port 19700; override with e.g. `--port 19800` |
| `--adaptive` | Off unless explicitly enabled; adjusts download concurrency |
| `--transport quic` | Optional UDP/QUIC; TCP remains default |
| `--ssh-opt KEY=VALUE` | Repeat to extend/override SSH settings, saved with the job |

The controller needs SSH/HTTP access to all targets; peers must reach each other. Peers listen on all local addresses; DNS/routing selects interfaces without automatic NIC aggregation. QUIC additionally uses the same-number UDP port. A direct source uses a separate dynamic endpoint shown by SOURCE READY. If BCM reaches only one compute node, log in there to run distribution. TCP data and HTTP control are plaintext; use a trusted cluster network.

## Documentation and development

The [documentation index](docs/README.en.md) covers all topics and historical studies. Start with [examples](docs/EXAMPLES.en.md), [source modes/failures](docs/SOURCE-MODES.en.md) or the [developer guide](docs/DEVELOPMENT.en.md). Studies record their measured versions and limits, not a speed guarantee at every scale. Release changes belong in [CHANGELOG](CHANGELOG.md).

Only main is maintained; all features ship in the same executable. Builds need Go 1.26+ with vendored dependencies. See the developer guide for build, validation, compatibility and release procedures.
