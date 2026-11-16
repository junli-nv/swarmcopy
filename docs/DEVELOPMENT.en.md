# Development guide

[中文（主文档）](DEVELOPMENT.zh-CN.md) · [Usage](../README.en.md) · [Examples](EXAMPLES.en.md)

This describes unified main v0.5.0. Maintain one long-lived branch, main. TCP, optional UDP, adaptive scheduling, pipelined integrity and trees belong to one executable. Retain historical tags; do not maintain parallel feature releases.

## Source map

| File | Responsibility |
| --- | --- |
| main.go | Manifest dispatch, v1 prepare, peer, HTTP service and CLI |
| cluster.go / parallel.go | Host expansion, persisted jobs, SSH controller, bounded deployment/stop |
| cli_sources.go / tree_source.go | Interspersed operands, glob/tree snapshots and SSH stream writer |
| tree_plan.go / tree_import.go | Trusted source manifest cache and independently verified seed import |
| tree_manifest.go | v2 validation, tree digest, streaming per-file hashes, contentFile |
| tree_store.go / tree_detach.go | Virtual offsets, staging, publication and hardlink-crash recovery |
| tree_existing.go | Final inventory, per-file fingerprint cache and conflict prompt |
| adaptive.go / transfer.go | Scheduler, adaptive downloads, hedge deduplication, transfer phases |
| verify.go / verified_linux.go | Ordered verifier and v1 final-file cache |
| quic.go / patches / vendor | Optional reliable UDP, job identity, sharding/PMTU/GSO integration |
| agent.go / auth_linux.go | PID/start-time ownership, hidden password and in-memory askpass |
| statefiles.go / watch.go / help.go | Atomic records, global progress and English help |
| *_test.go / scripts | Unit/race, CLI, process-chain, build and benchmark tools |

Runtime requires OpenSSH/basic Linux utilities; Go/Python are development-only. Static Linux amd64/arm64 builds do not depend on target glibc. quic-go v0.65.0 requires Go 1.26+.

## Contracts

- Preserve SCDP/1 chunk APIs and v1 manifest JSON/IDs. New source jobs use v2; new optional fields must not change old JSON output.
- v1 Manifest.SHA256 hashes raw file content; v2 hashes Entries JSON, with raw file hashes inside Entries. See [tree design](TREES.en.md).
- Verify chunk SHA256 before writing or advertising Have. Full ordered content verification and durable publication still gate Complete. Pipeline completion alone is insufficient.
- v2 is one logical concatenation with cross-file chunks; empty files/directories require explicit publication.
- Retries, hedges and source changes must not rewrite completed chunks or declare canceled/unverified content complete.
- Rehash retained partials on resume. Cache acceptance binds the trusted manifest and every data file's fingerprint, never just root directory stat.
- Default conflicts prompt; resume/replace preserve old finals until verification. Atomicity is per file, not dataset-wide.
- os.Root plus ancestor checks reject tree symlinks. New types require security, crash and recovery tests together.
- Keep external writers excluded for the whole job. Detected-race rejection is not a substitute for application-level write coordination.

## Planes, state and monitoring

SSH uploads the same executable, token, trusted manifest and configuration; agent launches an owned detached peer. Data defaults to HTTP/TCP, optionally QUIC. Control/status remain TCP. No tracker or NFS fallback.

Jobs roughly traverse created → starting → hashing-source/importing when needed → running → complete; failures record interrupted. Stop writes a request and terminates owned processes. Recorded state is not live cluster consensus; watch/status use live data or explicitly recorded completed results.

v2 inventories all nodes and skips controller media upload when a complete replica exists. Otherwise the initial tar is only an SSH stream, directly materialized and matched to the trusted manifest. Source hashing reads and network staging are separate costs.

Workers/uploads default to 4/4. Fixed/adaptive modes share a worker pool; adaptive changes download targets only, minimum defaults to initial workers and maximum to 16, off by default. Settings persist at creation; there is no live tuning API. Explicit old false/zero values must not inherit new defaults.

Monitor bytes count verified chunks across all files. Complete still waits for final verification/publication. Zero-byte datasets must use actual completion without division by zero. Saved results include phase and source traffic diagnostics.

## Build and verification

~~~bash
go test -mod=vendor -race ./...
go vet -mod=vendor ./...
bash scripts/build.sh
SWARMCOPY_BINARY="$PWD/dist/swarmcopy-linux-x86_64" python3 scripts/cli-smoke.py
SWARMCOPY_BINARY="$PWD/dist/swarmcopy-linux-x86_64" python3 scripts/integration.py
~~~

The last two commands run local temporary-directory regressions; never run destructive test fixtures in production destinations. Cluster tests use isolated directories and stop only owned peers. [Five-node tree validation](TREES.en.md).

Build output contains both static architectures and SHA256SUMS. CPU SHA acceleration is selected by Go; hashbench profiles memory/file hashing. Existing SHA-NI support is not a new weaker checksum algorithm.

For vendor rebuilding, read [QUIC patch maintenance](QUIC-TUNING.en.md) and THIRD_PARTY_NOTICES. Keep pinned dependencies, vendor diff, patch, defaults and narrow-MTU/limited-peer tests aligned.

## Performance and release

[Pipeline](PIPELINE.en.md) · [Adaptive background](ADAPTIVE.en.md) · [All UDP studies](QUIC-TUNING-RESULTS.en.md) · [Cold-cache protocol](COLD-STUDY.en.md)

Distinguish hash, disk, metadata, syscalls, congestion and deployment before changing concurrency or rate control. UDP is not inherently faster; never multiply socket/PMTU/GSO gains across different trials. Fresh benchmarks exclude preexisting/retained data; normal in-transfer retries remain in measured time.

Synchronize bilingual README, examples, English help, development docs and CHANGELOG. Verify old jobs, source changes, cache invalidation, on-disk corruption, zero-byte sets, conflicts, cancellation, publication interruption, TCP and optional UDP as appropriate; avoid repeating expensive tests without new evidence.

Publish both architectures, checksums and notices from the same commit. State clearly if ARM hardware was not tested. Exclude credentials, job tokens, media, private logs, reverse-engineering materials and internal absolute paths. Read authorized GitHub tokens in memory, never through arguments/output or commits.

Future priorities: small-file metadata/FD profiling, combined partial replicas and initial staging resume. New control strategies stay in main behind explicit options, retaining the default stable TCP path and complete integrity checks.

## Preparation telemetry

progress.go passes a synchronized reporter through context, emitting and atomically recording progress.json every two seconds. Stage/bytes/files/nodes are separate. Attempt and controller PID/start identity reject stale progress. ETA is a current-phase average-rate estimate, not a whole-job promise. Metadata/listener stages without reliable rates show action and elapsed time. Watch reloads jobs and handles recorded completion. Readers must retain per-buffer cancellation; remote inventory uses stdin heartbeats to detect controller exit. See [progress design and validation](PROGRESS.en.md).

## Integrity implementation v0.5.0

`integrity.go` centralizes algorithm factories, manifest algorithm binding and verification-policy validation. Empty legacy fields mean SHA256/full; v1/v2 JSON identities must remain unchanged. v3 requires explicit supported algorithms and tagged per-file digests. `integrity_test.go` covers vectors, valid/invalid combinations, HTTP transfer, weak-chunk/strong-file mismatch, cache isolation, source-stream round trips, and fresh sparse-range inventory. See [integrity semantics](INTEGRITY.en.md).

Changes must preserve these invariants: XXH3 requires full; chunks must never create a full-read cache; mode changes force peer recheck/restart; incoming staged content must match the trusted source manifest; final publication stays behind the selected verification gate. Existing partials are always content-checked on restart. No size-only completion assumption is allowed.

`Job.SSHOptions` stores explicit repeated `--ssh-opt` values. `mergeSSHOptions` merges defaults, saved options and new overrides case-insensitively without shell splitting. Explicit cipher selection takes priority. SSH login preflight stays before source hashing. Add options to this collection instead of introducing parallel SSH invocation paths.

Pinned BLAKE3/XXH3 libraries are vendored with licenses. Run `scripts/vendor-quic.sh` after dependency changes so the existing QUIC bulk patch remains applied. Cross-build with CGO_ENABLED=0 for amd64 and arm64; distinguish cross-build validation from physical ARM tests. Preserve raw benchmark configuration and do not report CPU-only ratios as network speedups.
