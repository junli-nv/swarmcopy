# Development guide

[中文（主文档）](DEVELOPMENT.zh-CN.md) · [Documentation](README.en.md) · [Examples](EXAMPLES.en.md)

This guide describes current architecture, compatibility contracts and validation. Maintain main only and ship all options in one executable. Release history belongs in [CHANGELOG](../CHANGELOG.md); historical measurements have separate indexed reports.

## Build and local validation

Use Go 1.26+ and vendored dependencies. Go/Python are development tools, not distribution runtime requirements.

```bash
go test -mod=vendor -race ./...
go vet -mod=vendor ./...
bash scripts/build.sh
SWARMCOPY_BINARY="$PWD/dist/swarmcopy-linux-x86_64" python3 scripts/cli-smoke.py
SWARMCOPY_BINARY="$PWD/dist/swarmcopy-linux-x86_64" python3 scripts/integration.py
```

Build output contains both static Linux architectures and SHA256SUMS; SWARMCOPY_GO selects a compiler. The last two commands check CLI behavior and a real process chain in isolated temporary directories. x86_64 has cluster hardware validation; ARM cross-compilation is not hardware validation.

## Source map

| Module | Responsibility |
| --- | --- |
| benchmark*.go | Bounded network streams, SSH stdin leases, measurement windows and optional NUMA |
| main.go / help.go | CLI, peer lifecycle, HTTP and help |
| cluster.go / parallel.go | Host expansion, saved jobs, SSH deployment and controller |
| cli_sources.go / tree_source.go | Multiple sources, globs, immutable source plans and streams |
| tree_plan.go / tree_import.go | Trusted source cache, seed import and independent checks |
| tree_manifest.go / integrity.go | v1/v2/v3, logical streams, algorithms and policy validation |
| tree_store.go / tree_detach.go / tree_existing.go | Staging, inventory, conflicts, publication and file caches |
| source_origin.go / resilience.go | Direct original-file serving, isolated failures and diagnosis |
| adaptive.go / transfer.go | Bounded selection, source ranking, workers, deduplication and metrics |
| verify.go / verified_linux.go | Ordered full verification and legacy single-file caches |
| quic.go / patches / vendor | QUIC/HTTP3, receive shards, PMTU/GSO and dependency patch |
| agent.go / auth_linux.go | PID/start-time ownership, hidden passwords and memory askpass |
| progress.go / watch.go / completion.go | Preparation telemetry, global monitor and summaries |
| cleanup.go / remove.go / statefiles.go | Completed cleanup, explicit removal, locks and atomic state |

## Manifest and integrity contracts

- Preserve SCDP/1 and legacy v1/v2 JSON identities. Required added fields must not alter old IDs. v1 SHA256 hashes raw content; tree SHA256 hashes Entries metadata.
- SHA256 trees can use v2. Custom algorithms, including the default BLAKE3 chunks preset, use v3 with explicit chunk/file names and algorithm:digest entries. Manifest version does not describe file count.
- Resolve defaults only for new distribute jobs. Empty verifyMode remains full for legacy records and low-level peers. Resume must not rewrite algorithms or missing-field semantics.
- Validate chunks before writes/Have. Full also checks actual file contents; chunks requires every cryptographic block, fsync and publication. XXH3 requires full with a cryptographic file gate.
- Chunks-only completion cannot mint full verification caches. Cache identity includes the manifest and every file's device/inode/size/mtime/ctime, not merely length or directory stat.
- A tree is a logical byte stream with cross-file chunks. Full verifies each manifest-bound file digest. Empty files/directories must also publish. See [layout](TREES.en.md) and [integrity](INTEGRITY.en.md).

## Sources, storage and recovery

When the source controller is a target, originals are read-only. Identical paths reuse existing files; different local destinations use the same validated commit path. LocalCopied is distinct from network counters. Preserve source layout/fingerprints and treat source read errors as local, not remote retries.

Staged mode inventories existing copies first. Without a complete source, SSH tar is an in-memory stream independently checked against the trusted manifest, not a full archive artifact. Record source hashing/import costs separately; initial SSH import is not chunk-resumable.

New staging/imports stay in destination .swarmcopy on the final filesystem. Resume legacy .swarmcopy-trees in place without bulk media moves. Skip proven sparse holes, handle empty-file boundaries, and conservatively fall back when SEEK_DATA is unavailable.

Conflicting resume/replace uses independent staging and preserves old published files until selected verification passes. Publication is per file, not a dataset transaction. Recover link-before-unlink crashes by detaching retained hardlinks before repair. Metadata/fsync errors cannot report Complete. Use os.Root, validated relative paths and ancestor-type checks; reject symlinks/special files. Coordinate with external writers.

## Scheduling and accounting

Fixed downloads/uploads default to 4/4; adaptive and endgame are off. Adaptive changes download admission only, within effective min/max. Live tuning is not exposed. Reducing admission must not cancel normal in-flight work.

Range selection uses a random start/coprime stride, scores at most 255 missing candidates, then permits a sparse-source bitmap fallback. Bitmap traversal can still be linear; the score limit is not a constant bound on all work. Preserve pipeline-window preference, source limits, stale/cooldown filtering and deduplication. Selection approximates rarest-first rather than guaranteeing a global optimum.

Node.manifestID is immutable after construction and reused for status/discovery. Mutable manifests would require a new cache lifecycle. Network/hash/write work seconds are concurrent cumulative work, not serial phases. Never sum per-node phase maxima or count local copies as network throughput.

Aggregate completion rates divide this invocation's new bytes by total controller elapsed time. Subtract initial counters for the same instance only; restarted peers use new counters. Peers may transfer during resume preparation, so monitor time alone is the wrong denominator. Preserve optional saved Summary on both writing/reading; do not invent timing for older results.

## Control and lifecycle

SSH login checks precede large-source reads. mergeSSHOptions merges default/saved/explicit case-insensitive keys without shell splitting values; explicit cipher wins. Passwords do not enter arguments, disk logs or job JSON.

Per-node operations have independent bounded contexts and saved records retain all Hosts. Monitoring has no all-listener barrier; healthy peers continue and failed targets remain incomplete. Direct original-file serving belongs to the controller and uses its own dynamic endpoint; controller exit stops it. There is no controller-handoff/tunnel layer.

Agent ownership checks PID, start time, executable and token path. Changed version/parameters/source may require restart without discarding valid caches. Development builds sharing a version must explicitly stop their owned peers rather than expecting a version string to identify every source change.

progress.json binds attempt/controller identity and separates stages, byte and node counters. ETA is phase-local. Source buffers honor cancellation; remote inventory uses input heartbeats to notice controller departure. Signals cannot guarantee immediate interruption of blocked kernel I/O. Completed saved results must not become offline failures merely because peers stopped normally.

## Completed cleanup and safe removal

Only clean owned deployment/empty staging after all targets verify/publish and peers stop. Fixed cache.json retains the latest manifest; last-peer.log is bounded to 8 MiB. Controller statistics remain. Protect incomplete/shared/real data, unknown entries, live locks and unsafe symlinks.

Cleanup checks ownership and controller/agent/destination locks, persisting CleanupIssues independently of verified transfer success. Repeated successful cleanup avoids deployment; stop of a completed cleaned job succeeds. Remove/delete deletes records; purge-partials explicitly discards exclusive recovery data and must not become the default cleanup behavior. See [cleanup contracts](CLEANUP.en.md).

## Regression, measurements and releases

Choose checks for the change: IDs/legacy manifests, source mutation, bad/short/out-of-order/cross-file blocks, empty datasets, conflicts, cancellation, Sync EIO, partial publication/hardlink recovery, cache isolation, active/shared ownership and TCP/QUIC. Use isolated cluster destinations and clean owned fixtures/processes promptly. Do not inject destructive faults into production data.

Freeze binaries/sources/settings for benchmarks, use fresh destinations and matching cache treatment, preserve every round and in-transfer retries. Distinguish preparation, inventory, transfer, hashing, fsync, publication and stop/cleanup. State timing and independent verification boundaries. Resume is not a fresh benchmark; CPU hash ratios are not network gains. Raw evidence lives in [data](data/README.md), with reports in the [index](README.en.md).

Update Chinese-primary/English docs, CLI help and change history together. Check links/examples/defaults and maintain topic guides rather than prepending release sections to README. Publish same-commit architecture builds, checksums and licenses. Read [QUIC patch maintenance](QUIC-TUNING.en.md) before vendor changes and keep upstream versions, patch and tests consistent. Do not publish credentials, media, private logs or reverse-analysis artifacts. Preserve upstream third-party documentation.

Future work should follow profiling: small-file metadata/FD costs, combining dispersed partial replicas, and initial import recovery. Preserve default TCP and selected integrity gates rather than masking bottlenecks with complexity or concurrency.

## Maintaining the network benchmark

benchmark is an isolated synthetic network test, not distribute with verification disabled. Bound payload memory independently of total traffic; discard receive buffers. Mesh is simultaneous send/receive; two-node seed is unidirectional. Compare like directions. Payload stays within hosts; SSH handles deployment/control/leases, with dedicated default port 19701. NUMA uses constrained arguments to optional target numactl; system adds no dependency. --cpus changes Go parallelism only.

Use per-node monotonic reset/freeze snapshots excluding warmup. Keep flows alive until all final snapshots are collected, so an early stop does not truncate another measurement. Reports omit tokens and mark failures. Ctrl+C closes stdin leases, waits for exit and cleans exclusive temporary directories without killing unrelated jobs. Validate real TCP/QUIC, no TCP fallback, frozen windows, bounded memory and cancellation. See the [network guide](NETWORK.en.md) for hardware checks, 64-stream/LACP/NUMA findings and evidence.

Network metrics must come from routed-interface /sys/class/net/<iface>/statistics counters with per-node monotonic deltas. CURRENT uses adjacent samples; AVERAGE uses the full post-warmup window. Keep bond/member counters separate without adding them. Unknown interfaces, loopback, counter resets and invalid intervals must not fall back to application bytes. Default output emphasizes per-node NIC throughput rather than cluster totals.

Mesh defaults are 64 workers, Go parallelism 32 and 1 MiB buffers. Cyclic source order starts after each node's own index, balancing uploads even when host count exceeds workers. Regression cases cover 60/160 nodes. Classify HTTP429, connect, read, other HTTP/protocol errors and apply bounded backoff. Quiet SSH is enabled only on the benchmark runner instance; ordinary file-job logging retains its contract.

Common help is concise; --help-all retains advanced options. Interactive terminals refresh in place; noninteractive output has sparse progress and a final table. Deployment commands require --verbose. UI tests must supply different application/NIC rates to prove NIC selection, and must not equate ACTIVE/no request errors with link saturation. See the network guide for the complete 60-node NIC checks.
