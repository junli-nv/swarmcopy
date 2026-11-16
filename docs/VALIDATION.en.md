# Validation record

[中文](VALIDATION.zh-CN.md) · [Development](DEVELOPMENT.en.md) · [Preliminary benchmark](BENCHMARK.en.md) · [Cold-cache study protocol](COLD-STUDY.en.md)

## Current adaptive experiment

- Race unit tests and go vet pass, including request deduplication/cancellation, corrupt-copy recovery, admission bounds, and write feedback.
- The actual race executable passes interrupted/corrupt-partial forwarding with adaptive/endgame and a custom minimum concurrency.
- Fixed-mode recovery and real two-node SSH deployment/option propagation pass.
- A v0.1.0 seed and new receivers pass mixed-version interruption/recovery/forwarding.
- The first prototype regressed; [both initial rounds](ADAPTIVE-INITIAL-RESULTS.en.md) and matching source are retained, without crediting a performance improvement.

The corrected 45-node, three-mode/two-round study passes all six complete transfers and 270 cache barriers, with all 270 owned process records stopped; see [results](ADAPTIVE-RESULTS.en.md).

The following are historical v0.1.0 checks, not evidence of a new 157-node benchmark.

## Completed checks

- The 157-node cold-cache study completes all six fresh cases and 942 cache barriers; post-study audit confirms 157 distinct boot IDs and all 471 owned process records stopped. See [results](COLD-RESULTS.en.md).
- Go unit tests with the race detector and go vet pass.
- A real three-process A→B→C chain passes interruption and damaged-partial recovery, with matching final SHA-256 and observed B→C forwarding.
- The chain also passes using race-instrumented executables, without DATA RACE reports.
- CLI checks cover no-argument help, every public subcommand's --help, help COMMAND, missing arguments, empty job state, and jobs.
- Verification-cache tests reject a different manifest, replacement inode, and content modification with restored mtime.
- Bounded deployment tests verify concurrency limits and error propagation/cancellation.
- A real five-node transfer of a 40,188,444,672-byte file passes whole-file SHA-256.
- A separate full-job resume revalidates existing real files with zero re-download; this is not counted as a fresh-transfer benchmark.
- A 45-node run (one seed, 44 receivers) completes full transfer/verification. Subsequent owned-process inspection confirms all 45 peers stopped.
- A two-node small-file cold-cache driver test passes fresh-directory gates, sync/drop_caches, full rsync literal-byte accounting, immediate SHA-256, and full SCDP download accounting.

An additional development command was cancelled and was not credited as passed. The race-instrumented chain was subsequently run separately and passed. Incomplete/cancelled output is not treated as successful evidence.

## Platforms and builds

The actual toolchain is Go 1.27.4; the module declares Go 1.24+. Release targets are Linux x86_64 (amd64) and aarch64 (arm64), CGO_ENABLED=0, trimpath/buildvcs=false, with SHA256SUMS and static ELF inspection.

Both architectures were rebuilt into independent output paths before publication, reproducing the exact frozen benchmark artifact SHA-256 values.

Cluster execution coverage is x86_64. ARM64 has cross-build coverage only, without ARM hardware execution in this study. The build must not be described as GB-series hardware validation.

The actual cluster uses existing SSH public-key authentication. Real-target end-to-end password-account validation was not performed, although the in-memory askpass path is implemented.

## Evidence and scope

Raw data, complete logs, host identities, and private job records remain under the controller run/ and private state directories and are excluded from Git. Public reports contain sanitized numeric data and conditions, without media, tokens, known_hosts, or host lists.

See the developer guide for limits: initial staging lacks chunk-level recovery, partial recovery rereads hashes, the data plane is plaintext HTTP in a controlled network, and this release handles one file.

