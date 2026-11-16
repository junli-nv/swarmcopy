# Development guide

[中文（主文档）](DEVELOPMENT.zh-CN.md) · [Usage](../README.en.md) · [Examples](EXAMPLES.en.md)

This describes unified main v0.4.0. Maintain one long-lived branch, main. TCP, optional UDP, adaptive scheduling, pipelined integrity and trees belong to one executable. Retain historical tags; do not maintain parallel feature releases.

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
