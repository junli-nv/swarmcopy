# Operation examples

[中文（主文档）](EXAMPLES.zh-CN.md) · [Quick start](../README.en.md) · [Documentation](README.en.md)

Run commands on the controller holding the sources. Replace example hosts, paths and `<ID>`. Defaults are chunks/BLAKE3, TCP, four downloads/uploads per node, unlimited bandwidth and adaptive off.

## Distribute into the same paths from the source node

Run on HGX167 and include HGX167 among targets:

```bash
./swarmcopy distribute \
  --hosts 'HGX[061,066,082,138,169,167]' \
  --source '/raid/nemo*' --dest /raid
```

The local originals are checked, reused and served without copying onto themselves. Other targets receive the same paths. The glob matches immediate entries under /raid, not files under /raid/images; matched directories are recursively copied.

Automatic direct mode needs the controller in the target list. Use real hostnames rather than expecting arbitrary SSH aliases to identify the local host. SOURCE MODE and SOURCE READY display the path and endpoint.

## Distribute from BCM or into a different directory

```bash
# On BCM: copy once to a compute seed through SSH when needed
./swarmcopy distribute --hosts 'HGX[061,066,082]' \
  --source /shared/nemo.sqsh --dest /raid/images

# On HGX167: serve originals; native reads/checks/writes fill the local destination
./swarmcopy distribute --hosts 'HGX[061,066,167]' \
  --source /raid/source/nemo.sqsh --dest /raid/images
```

A complete existing replica avoids unnecessary initial media upload. Importing means seed copying, not unpacking. If BCM reaches only one compute node, log in there to run distribution; there is no automatic controller handoff or SSH tunnel manager.

## Multiple files, directories and globs

```bash
./swarmcopy distribute --hosts @hosts.txt --dest /raid/images \
  --source /shared/nemo.sqsh --source /shared/config.json

./swarmcopy distribute --hosts @hosts.txt --dest /raid/images \
  --source '/shared/*.sqsh'

./swarmcopy distribute --hosts @hosts.txt --dest /raid/images \
  --source /shared/models
```

| Input | Destination layout |
| --- | --- |
| `/shared/nemo.sqsh` | `/raid/images/nemo.sqsh` |
| Two source files | Both directly under /raid/images, without a batch directory |
| `/shared/models` or `/shared/models/` | `/raid/images/models/...` |

Shell expansion/positional inputs also work: `./swarmcopy distribute --hosts @hosts.txt --dest /raid/images /shared/*.sqsh`. Prefer quoting globs and bracketed host lists. Globs support `*`, `?`, `[]`; `**` has no special recursive meaning. Unmatched patterns, overlapping roots and duplicate top-level names fail. Empty files/directories are retained; symlinks and special files are unsupported.

## Existing files and conflicts

```bash
# Reuse matching chunks from old files before verified replacement
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --existing resume

# Receive conflicting files afresh, then replace
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --existing replace
```

Default --existing ask lists conflicts and prompts once. Keep cancels on a conflict; it does not skip conflicts and declare success. Matching files are reused even with replace. Old final files remain until verified per-file replacement; allow independent staging space. Extra target files are not deleted.

## Host lists and SSH

```bash
./swarmcopy distribute --hosts 'HGX[061,066,167],node[001-005]' \
  --source /shared/nemo.sqsh --dest /raid/images

./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --ssh-user root --ask-pass

./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --ssh-opt ConnectTimeout=20 \
  --ssh-opt ServerAliveInterval=15 --ssh-cipher aes128-gcm@openssh.com
```

hosts.txt can contain one host per line. Use SSH keys, agent or config. Ask-pass prompts once, hidden, for the same password on all targets without persisting it or configuring trust. SSH defaults are StrictHostKeyChecking=no, UserKnownHostsFile=/dev/null, LogLevel=quiet and CheckHostIP=no. Repeat --ssh-opt KEY=VALUE to override keys; settings persist. Explicit --ssh-cipher takes precedence.

## Concurrency, bandwidth, ports and QUIC

```bash
# Per-node fixed concurrency; defaults are 4/4
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --workers 8 --uploads 8 --port 19800

# Aggregate per-node MiB/s; 0 means unlimited
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --download-mib 200 --upload-mib 200

# Explicit adaptive downloads: initial 4, minimum 2, maximum 16; uploads stay capped at 8
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --adaptive --workers 4 --min-workers 2 --max-workers 16 --uploads 8

# Optional UDP data in the same executable
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --transport quic
```

Workers/uploads accept 1..32 parallel chunk requests, not exact socket counts. Adaptive changes downloads only and is off by default; live tuning is not exposed. QUIC needs same-number UDP in addition to TCP status/control; direct sources use another dynamic endpoint. Listening on all interfaces does not aggregate NICs. Settings persist; do not change transport by editing live job JSON.

## Integrity selection

```bash
# Defaults to chunks/BLAKE3; no separate hash flags needed
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh --dest /raid/images

# Final file-content check; defaults to SHA256 and pipelined verification
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --verify full

# Advanced: noncryptographic chunk checks require a cryptographic full-file gate
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --verify full --chunk-hash xxh3-128 --file-hash blake3
```

Initial source hashing still reads all bytes. Chunks omits final whole-file verification, not chunk checks or fsync. Full hashing may overlap transfer. Resume retains algorithms: switching a saved BLAKE3 job to full does not convert its manifest to SHA256. See [integrity](INTEGRITY.en.md).

## Monitor, stop and recover

```bash
./swarmcopy jobs
./swarmcopy jobs --json
./swarmcopy inspect --job <ID>
./swarmcopy watch --job <ID> --interval 2s --plain
./swarmcopy status --job <ID>
./swarmcopy stop --job <ID>
./swarmcopy resume --job <ID>
./swarmcopy resume --job <ID> --existing resume --ask-pass
```

JOB SUMMARY saves invocation elapsed/preparation/distribution time, network/local/reused bytes and average rates. Aggregate rate divides new bytes by this invocation's total elapsed time including preparation; receiving-node median rate is separate. Completed watch replays records, status keeps JSON, and missing old timing is not invented.

One failed node does not cancel healthy nodes or count as completed. Leaving watch does not stop a transfer; Ctrl+C stops the controller while peers may continue, so explicitly stop the job. After resolving fsync/I/O faults, stop a waiting controller and resume the same ID: reuse good blocks and refill missing/damaged ones. A later directory fsync can fail after some files appear; the job still cannot be declared successful. Keep records/partials rather than purging first.

## Cleanup and job removal

```bash
# Normal completion cleans automatically; retry or clean an old completed job
./swarmcopy cleanup --job <ID>

# Remove job records/deployment, retaining sources and published files
./swarmcopy remove --job <ID>

# Additionally discard exclusive partials and their recovery path
./swarmcopy remove --job <ID> --purge-partials
```

Cleanup retains jobs/watch/status history; remove/delete removes records. The unified .swarmcopy keeps only a reusable latest cache and bounded log, protecting interrupted/shared data. Keep-seeding defers stopping and cleanup until requested. See [cleanup and legacy layout](CLEANUP.en.md).

## Help and further reading

```bash
./swarmcopy --help
./swarmcopy distribute --help
./swarmcopy jobs --help
./swarmcopy help cleanup
```

Prepare, peer and hashbench are advanced/development entry points; normal distribution deploys peers for you. See [installation](../README.en.md), [source modes](SOURCE-MODES.en.md), [progress](PROGRESS.en.md) and [development](DEVELOPMENT.en.md) for architecture deployment, connectivity and field semantics.

## Network benchmark

```bash
./swarmcopy benchmark --hosts 'HGX[603-608,610-618,620-653,655-665]'
```

Default mesh sends/receives on every node. The quiet live dashboard shows real per-node NIC rates, capacity and utilization, followed by one average-rate table. Defaults use 64 streams; tune only when needed with `benchmark --help-all`, and request deployment logs with `--verbose`. Fixed buffers discard payload without media I/O or content hashes. See the [network guide](NETWORK.en.md) for 400G bond, 64-stream, MTU/LACP and NUMA measurements.
