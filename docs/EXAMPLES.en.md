# Common examples

[中文（主文档）](EXAMPLES.zh-CN.md) · [README](../README.en.md)

Use the unified main release, named `swarmcopy`. For mixed architectures, put both original architecture binaries in a sibling `dist` directory (automatically detected), or supply `--bin-dir /path/to/dist`. Replace example hosts and paths.

## Files, globs and directories

~~~bash
./swarmcopy distribute --hosts 'node[001-005]' \
  --source /shared/nemo.sqsh --dest /raid/images

# Two files directly under /raid/images
./swarmcopy distribute --hosts @hosts.txt --dest /raid/images \
  --source /shared/nemo.sqsh --source /shared/config.json

# Program-expanded glob
./swarmcopy distribute --hosts @hosts.txt --dest /raid/images --source '/shared/*.sqsh'

# Shell-expanded glob, including flags after expanded inputs
./swarmcopy distribute --hosts @hosts.txt --source /shared/*.sqsh --dest /raid/images

# Creates /raid/images/nemo/...
./swarmcopy distribute --hosts @hosts.txt --dest /raid/images --source /shared/nemo
~~~

Use one host per line in hosts.txt, comma-separated names, or `'node[001,006,010-020]'`. Directory recursion includes empty files/directories. Symbolic links and special files are rejected; avoid duplicate basenames and overlapping roots.

## Fixed download/upload concurrency

~~~bash
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --workers 8 --uploads 8
~~~

| Option | Per-node meaning | Default |
| --- | --- | --- |
| `--workers` | Concurrent chunk downloads | 4 |
| `--uploads` | Maximum concurrent uploads | 4 |
| `--adaptive` | Adjust download concurrency automatically | off |
| `--min-workers` | Adaptive download minimum | initial workers |
| `--max-workers` | Adaptive download maximum | 16 |

Workers/uploads range from 1 to 32 per node. These are chunk requests, not an exact socket count. Leave adaptive off for fixed concurrency.

## Adaptive downloads

~~~bash
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --adaptive \
  --workers 4 --min-workers 2 --max-workers 16 --uploads 8
~~~

Starts at four downloads and adjusts within 2..16 based on measured performance; uploads remain capped at eight. Adaptive is not always faster and stays off by default. Without an explicit minimum it normally cannot reduce below the initial count.

Concurrency settings are saved at job creation and retained on resume. No live-edit or resume worker-override command exists.

## Bandwidth cap

~~~bash
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --download-mib 200 --upload-mib 200
~~~

Aggregate 200 MiB/s in each direction **per node**, not per cluster. Defaults are zero/unlimited.

## Only a few missing nodes

~~~bash
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh --dest /raid/images
~~~

Matching files are reused and complete replicas seed missing nodes. A complete existing replica skips controller-to-seed media upload; all-complete targets download zero new media bytes. Initial trusted-source hashing and uncached target validation still cost disk reads; unchanged caches reduce later rereads.

## Different existing files

Omit `--existing` in an interactive run for a prompt: `r` resume/repair, `p` receive conflicting files afresh then replace, `c` cancel and preserve. One choice applies to all detected regular-file conflicts.

~~~bash
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --existing resume
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --existing replace
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --existing keep
~~~

Repair copies old data into independent staging and retains verified chunks. Old final files remain intact until complete verification, followed by atomic per-file replacement. Unrelated files remain. Replace still reuses matching final files and valid task partials. A cross-file chunk may include a small range of a neighboring complete file without rewriting it.

## Find, monitor, stop, resume

~~~bash
./swarmcopy jobs
./swarmcopy jobs --help
./swarmcopy inspect --job <ID>
./swarmcopy watch --job <ID> --plain --interval 2s
./swarmcopy status --job <ID>
./swarmcopy stop --job <ID>
./swarmcopy resume --job <ID>
./swarmcopy resume --job <ID> --existing resume
~~~

Replace <ID> with the printed/listed job ID; retain job metadata and node staging. Watch defaults to the latest active/latest saved job. Exiting watch does not stop peers. Completed jobs show recorded results unless `--live` is requested.

Resume keeps the same manifest and verified chunks; distribute starts a new inventory/fill job. Resume never expands source globs again. Incomplete initial SSH staging restarts its phase; the P2P phase supports chunk resume.

## SSH password, UDP, full recheck

~~~bash
./swarmcopy distribute --hosts @hosts.txt --ssh-user alice --ask-pass \
  --source /shared/nemo.sqsh --dest /raid/images

./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --transport quic

./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --recheck
~~~

Ask-pass requests one shared password, never persists it or installs permanent trust. Host-key enrollment is not required. QUIC requires UDP and TCP at the peer port, is not guaranteed faster, and remains optional. Listening on all NICs does not aggregate their bandwidth; resolve peer names onto the intended network.

See `./swarmcopy distribute --help` for all options/defaults. New conflict policies apply to v2 source jobs; legacy v1 prepared-seed jobs retain their original final-file no-overwrite rule.

## Preparation is visible before transfer

No extra flags are needed. hashing-source builds the trusted manifest; importing streams the initial seed. Both show percent/rate/current-phase ETA every two seconds. Watch switches automatically into transfer; 100% bytes may still await validation/finalization. Ctrl+C or stop --job cancels source hashing; an incomplete initial hash restarts. [Details](PROGRESS.en.md).

## SSH defaults and encryption

From v0.4.2 all controller SSH calls use these options, with no strict-mode switch:

~~~text
-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=quiet -o CheckHostIP=no
~~~

SSH login is checked before expensive source hashing; old jobs use the same settings on resume. Default --ssh-cipher auto prefers AES-GCM when local AES/GCM hardware capabilities are detected, with ChaCha20/AES-CTR fallback; otherwise it leaves OpenSSH selection unchanged. OpenSSH/its crypto library performs actual acceleration.

~~~bash
./swarmcopy resume --job <ID>
./swarmcopy resume --job <ID> --ssh-cipher aes128-gcm@openssh.com
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh --ssh-cipher system
~~~

system leaves cipher negotiation entirely to OpenSSH. Username/password/key/agent behavior is unchanged. SSH encryption affects deployment/initial staging, not SCDP content verification.

## v0.5.0 integrity modes and SSH options

Defaults remain full + SHA256. Choose `--verify chunks` to omit the final disk reread, or `--verify full --chunk-hash xxh3-128 --file-hash blake3` for fast chunks plus a cryptographic file digest. XXH3 is rejected in chunks mode. Initial source hashing still reads every byte. See [guarantees, combinations and measured hardware results](INTEGRITY.en.md).

SSH defaults include `StrictHostKeyChecking=no`, `UserKnownHostsFile=/dev/null`, `LogLevel=quiet`, `CheckHostIP=no`. Repeat `--ssh-opt KEY=VALUE` to extend/override saved defaults, e.g. `--ssh-opt ConnectTimeout=20 --ssh-opt ServerAliveInterval=15`. Select AES-GCM with `--ssh-cipher aes128-gcm@openssh.com`.

```bash
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh --dest /raid/images \
  --verify full --chunk-hash xxh3-128 --file-hash blake3 \
  --ssh-opt ConnectTimeout=20 --ssh-opt ServerAliveInterval=15
./swarmcopy resume --job <ID> --ssh-opt ConnectTimeout=30 --verify full
```
