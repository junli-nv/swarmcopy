# Changelog

## 0.5.0 — composable integrity and extensible SSH options

- Add full/chunks verification with SHA256, BLAKE3 and XXH3-128 chunk checks; require cryptographic full-file checks with XXH3. Preserve SHA256/full defaults.
- Bind custom algorithms in v3 manifests; preserve v1/v2 identities and resume support. Prevent chunks-only completion from creating a full-file verification cache.
- Skip unwritten fresh sparse ranges without trusting existing partial files by size. Force recheck/restart when resume changes verification mode.
- Add repeatable, persisted --ssh-opt KEY=VALUE over the four requested defaults, with explicit cipher precedence.
- Expand bilingual examples/development/integrity documentation and English help; record two-node functional checks and CPU-only algorithm benchmarks separately from cluster throughput.

## 0.4.2 — cluster SSH defaults

- Use the requested no-host-key-checking, null known-hosts, quiet, no-host-IP-check options for all controller SSH calls, including old-job resume.
- Check SSH login before expensive source hashing and label sibling SSH cancellation clearly.
- Prefer negotiated AES-GCM with fallback on AES/GCM-capable controllers; provide --ssh-cipher auto/system/explicit overrides.
- Validate the previously failing controller-to-node login and record a small SSH cipher comparison; preserve full file integrity checks.


## 0.4.1 — preparation progress and cancellation

- Report source hash/import bytes, percent, rate and estimated remaining phase time every two seconds; show deployment/inventory/listener readiness counts and elapsed time.
- Make watch/status phase-aware, reload job state and avoid unopened-peer OFFLINE errors and stopped-peer completion races.
- Honor Ctrl+C between source read buffers and local source-hash stop requests; cancel remote inventory when controller stdin closes.
- Persist attempt-bound telemetry with controller identity, detect locally exited controllers and clearly explain restart/resume boundaries.
- Add native 64GiB cancellation/ETA checks, race tests, two-node transfer validation and bilingual examples.


## 0.4.0 — unified main release

- Ship TCP, optional QUIC/UDP tuning, pipeline verification and opt-in adaptive scheduling in one binary and one maintained main branch. TCP stays default, pipeline/window stay on, adaptive stays off.
- Add repeatable sources, positional operands, quoted/shell-expanded globs and recursive directories, directly under dest.
- Preserve v1 manifest identity and prepared-seed/old-job compatibility; use native virtual-file trees with v2 manifests for new source jobs.
- Inventory existing replicas, skip unnecessary head-node media upload and fill only missing data; all-complete targets download zero new media.
- Prompt for conflicting regular files, with explicit ask/resume/replace/keep policies and independent staging until verified per-file atomic replacement.
- Support per-file SHA256, cross-file chunks, empty files/directories, persistent chunk resume, validated source/file caches and hardlink-crash-safe repair.
- Add Chinese-primary bilingual examples, updated English help, source/developer documentation and five-node TCP/QUIC/repair/resume evidence.
- Publish static Linux x86_64 and aarch64 builds; x86_64 tested on real nodes, ARM cross-built only.


## 0.3.0-dev.4 (optional UDP candidate)

- Fix QUIC integration limits with bounded source-affine receive-socket shards, negotiated path-safe UDP ceilings, and configurable congestion/pacing-limited GSO batches.
- Default optional QUIC parameters to4 sockets/8952 bytes/64KiB; TCP stays default and adaptive concurrency stays off.
- Preserve saved old-job values as1/1452/20KiB.
- Vendor pinned dependencies with a reproducible four-file patch and upstream notices.
- Validate real UDP loss, narrow-path/limited-peer negotiation, cancellation, race recovery, actual receiver ingress packets, and four separate45-node studies.
- Retain initial slow results, all ablations, anonymous CPU/kernel evidence, and bilingual maintenance documentation.

The dev.1/dev.2/dev.3 source checkpoints were internal and not separately published; their original binary hashes remain in the reports.

## 0.3.0-dev.1 (optional QUIC transport)

- Add --transport quic for reliable HTTP/3 chunk streams over UDP; TCP remains default.
- Keep TCP status/control, persisted transport selection, task-bound TLS identity, and explicit no-fallback behavior.
- Preserve full integrity, pipeline defaults, fixed concurrency, and single-binary deployment.
- Add real UDP packet-loss/authentication/cancellation checks and cold-cache comparison driver.
- Pin quic-go v0.65.0 and ship dependency license notices; this branch requires Go 1.26+ to build.

## 0.2.0

- Promote ordered whole-file verification to a regular release; new jobs enable the pipeline and 256-chunk prefix window by default.
- Keep adaptive concurrency disabled by default.
- Preserve saved verification settings when deploying/resuming old jobs.
- Add explicit pipeline/window opt-outs with documented English help and bilingual usage.
- Retain the experimental benchmarks as historical measurements of the same verification algorithm.

## 0.2.0-dev.2 (experimental)

- Opt-in ordered whole-file SHA-256 pipeline, bounded read buffers, and optional prefix-window chunk priority.
- Preserve full digest comparison, fsync, atomic publication, and restart revalidation.
- Add hashbench with optional CPU profiles; confirm existing SHA-NI acceleration on x86_64.
- Persist hash progress/phase diagnostics in job results and display live hash progress.
- Race, corruption, cancellation, mixed-version recovery, and window scheduling checks.
- Retain initial and final 45-node comparisons with fresh targets, cold OS caches, and bilingual evidence.
- Linux x86_64/aarch64 static builds; pipeline/window disabled by default.

## 0.2.0-dev (experimental)

- Opt-in adaptive download concurrency with explicit minimum/maximum bounds, measured source ranking, and persistent busy-source cooldown.
- Optional delayed endgame requests with per-chunk commit deduplication and cancellation.
- Per-node phase timings, payload/retry diagnostics, dynamic concurrency display, and local-write error reporting.
- Retained initial regression measurements and source history; corrected default concurrency floor.
- Bilingual design and reproducible baseline/fixed/adaptive comparisons.
- SCDP/1 over TCP remains wire compatible; scheduling changes are disabled by default.

## 0.1.0

- Initial SCDP/1 single-file P2P distribution with verified chunk forwarding.
- Standalone Linux x86_64 and aarch64 builds; SSH bootstrap without pdsh or permanent agents.
- Persistent jobs, explicit resume/stop, global monitoring, JSON status, and complete English help.
- SSH user selection and in-memory shared-password askpass.
- Unlimited default bandwidth with optional per-node upload/download limits.
- Verified final-file cache with inode/size/mtime/ctime invalidation and --recheck.
- Bounded SSH deployment/stop, atomic state records, and owned-PID checks.
- Chinese-first bilingual usage/development/validation documents and reproducible benchmark drivers.

Directories, multiple files, and file-glob distribution are deferred to the next release.
