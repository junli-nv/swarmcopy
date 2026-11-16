# Changelog

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

