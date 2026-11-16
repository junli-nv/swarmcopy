# Integrity modes, algorithms and performance

[中文（主文档）](INTEGRITY.zh-CN.md) · [README](../README.en.md)

From v0.7.1, new distribute jobs default to chunks with BLAKE3/BLAKE3; explicit `--verify full` selects SHA256/SHA256 and final file verification. It adds selectable stages, not a no-check or sampled-check shortcut.

| Mode | Incoming chunks | Completion |
| --- | --- | --- |
| full | Compare every chunk with the trusted manifest | Read staged file contents, verify a cryptographic file digest, then publish |
| chunks (new-job default) | Verify every chunk with SHA256 or BLAKE3 | Require all chunks, fsync, publish without a final full-file reread |

Full verification is pipelined by default and may overlap download. Chunks cannot detect corruption after a successful chunk check/write; the guarantees differ. fsync requests OS persistence, not independent hardware readback. Both modes retain recovery and retransmission. Chunks completion never creates a full-file verification cache. Full caches require manifest identity and matching file fingerprints; `--recheck` bypasses them.

## Two integrity modes (v0.7.1)

For new `--source` jobs, choose `--verify full` or `--verify chunks`. Full defaults to SHA256 chunks/files with final file verification; chunks defaults to BLAKE3 chunks/files and publishes after all chunk checks and fsync, omitting the final full-file reread. From v0.7.1, omitting `--verify` selects chunks; explicitly use `--verify full` for final whole-file verification. No hash flags are needed.

```bash
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh --dest /raid/images --verify chunks
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh --dest /raid/images --verify full
```

`--chunk-hash` / `--file-hash` remain advanced overrides. Resume/prepared seed manifests retain their algorithms; changing mode never rewrites a manifest. An existing SHA256 job still uses SHA256 chunks after switching to chunks mode.

## Combinations and examples

`--chunk-hash sha256|blake3|xxh3-128` selects chunk validation.
`--file-hash sha256|blake3` selects source/full-file validation.
XXH3-128 is noncryptographic and requires full mode with a cryptographic file digest. Chunks mode rejects XXH3-128; a weak chunk match cannot bypass the full-file publication gate.

~~~bash
# Strong full-file gate with fast chunk checks
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --verify full --chunk-hash xxh3-128 --file-hash blake3

# Cryptographic chunk checks, omit final receiving disk reread
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --verify chunks

./swarmcopy jobs
./swarmcopy inspect --job <ID>
./swarmcopy watch --job <ID>
./swarmcopy resume --job <ID> --verify full
~~~

Mode changes force owned peers to restart/recheck, even with `--recheck=false`. Algorithms are bound to the saved manifest; changing algorithms requires a new job. Keep controller records and node partials. Source hashing still reads every source byte initially; chunks does not eliminate source preparation. Existing-file inventory still uses file digests. ETA covers the current stage only.

Legacy v1/v2 encoding and IDs stay compatible. SHA256 tree jobs use v2; custom algorithms use v3 with explicit algorithm fields and `algorithm:digest` file entries. v3 requires v0.5+ peers. The tree SHA256 hashes entry metadata; it is not a concatenated-file content hash. Manifest identity binds algorithms, paths, sizes and digests. Newly created unwritten sparse ranges are skipped during initial scanning; pre-existing partials are checked on restart. Publication remains atomic per file, not per dataset.

## Evidence and hardware

Memory-only dual-hash benchmark: identical 1 MiB stream into chunk and file hashers, GOMAXPROCS=1, 400 ms per trial, three trials, median decimal MB/s. These numbers are not disk or distribution throughput.

| CPU / host | SHA256 + SHA256 | XXH3-128 + SHA256 | BLAKE3 + BLAKE3 | XXH3-128 + BLAKE3 |
| --- | ---: | ---: | ---: | ---: |
| Xeon 6776P / HGX167 | 560.13 | 1052.81 | 1157.99 | 1993.80 |
| Xeon 6746E / head | 871.89 | 1512.26 | 446.79 | 830.15 |

HGX167 combinations were measured in one run. Head-node XXH3+SHA256 was added in a later run; the other head values are earlier reference trials. CPU differences change the fastest combination. The v0.7.1 BLAKE3/chunks new-job default targets large-file distribution; full still selects SHA256, and BLAKE3 is not universally faster. Go SHA256 uses available hardware acceleration. The selected BLAKE3 implementation has x86 AVX2/AVX512 paths; availability depends on hardware/runtime. aarch64 is statically cross-built, not hardware-benchmarked in this round. Do not compound these ratios with earlier cluster or SSH benchmarks.

Real two-node tests on HGX061/HGX066 used a 139,460,641-byte file/directory fixture: default SHA256, both mixed full combinations, BLAKE3 full, both cryptographic chunks combinations, QUIC chunks, chunks-to-full upgrade, and damaged-file repair passed. Independent Python SHA256 checked every final file. Repair downloaded the affected 16 MiB chunk only. These are functional checks, not a new large-scale rsync comparison.

## Extensible SSH defaults

All controller SSH calls start with:

~~~text
-o StrictHostKeyChecking=no
-o UserKnownHostsFile=/dev/null
-o LogLevel=quiet
-o CheckHostIP=no
~~~

Repeat `--ssh-opt KEY=VALUE` to add options. Keys match case-insensitively; the last explicit value overrides defaults or saved job settings. Values remain one argument without shell splitting. Settings persist for resume. There is no dedicated strict switch.

~~~bash
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --ssh-opt ConnectTimeout=20 \
  --ssh-opt ServerAliveInterval=15 --ssh-cipher aes128-gcm@openssh.com
./swarmcopy resume --job <ID> --ssh-opt ConnectTimeout=30
~~~

An explicit algorithm in `--ssh-cipher` takes precedence over generic Ciphers. Under auto/system, an explicit `--ssh-opt Ciphers=...` can select the list. SSH covers deployment and initial seed import; peer TCP/QUIC data does not all travel over SSH.


## Reproduce CPU benchmark

```bash
GOMAXPROCS=1 go test -mod=vendor -run '^$' -bench '^BenchmarkIntegritySourcePipeline$' -benchtime=400ms -count=3 -cpu=1
```

[HGX167 raw CPU benchmark](integrity-bench-hgx167.txt).

The final candidate additionally passed interrupted BLAKE3/chunks stop/resume with the same job ID and partial reuse, independently SHA256-checked. Explicit `--recheck=false` cannot bypass rechecking during chunks-to-full upgrade.

## Recover after fsync errors

A data fsync error prevents completion/publication of that data and retains job/staging records. Resolve space, RAID/filesystem or I/O errors first. If the controller still waits, stop it before resuming the same job:

```bash
./swarmcopy jobs
./swarmcopy stop --job <ID>
./swarmcopy resume --job <ID>
```

Recovery rechecks retained data, reuses valid blocks and fills missing/damaged blocks before attempting completion again. A full restart is normally unnecessary, but power loss or storage data loss can require retransmission, and persistent storage errors prevent completion. Keep the job and partials; remove --purge-partials discards that recovery entry point.

A later directory/marker fsync may fail after a file has already been renamed/linked into place. Some targets may therefore exist while the job remains unsuccessful; recovery checks and reuses valid files. Publication is per file, not a dataset transaction. Successful fsync depends on correct OS/device flush behavior and does not replace content checking; choose --verify full for an additional final content check.
