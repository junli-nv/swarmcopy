# Changelog

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

