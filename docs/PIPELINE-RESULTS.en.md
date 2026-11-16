# Whole-file verification: 45-node comparison

[中文](PIPELINE-RESULTS.zh-CN.md) · [Design](PIPELINE.en.md) · [CPU backend evidence](sha-hardware-results.json)

A 40,188,444,672-byte file on the same 45 nodes: one prepared seed and 44 fresh receivers. All methods use fixed four download workers/four upload slots and unlimited HTTP/TCP. Adaptive concurrency is disabled.

## Final same-binary window comparison

| Round | Previous release / s | Plain pipeline / s | Pipeline + 256-chunk window / s |
|---|---:|---:|---:|
| 1 | 85.03 | 78.73 | 64.76 |
| 2 | 82.67 | 79.12 | 67.08 |
| median | 83.85 | 78.92 | 65.92 |

Median ratios: **1.272×** against the release and **1.197×** against the same binary without a window, with 21.4% less time versus the release. Above one means faster. Two observations do not establish a universal gain.

## Retained initial comparison

| Round | Previous release / s | New serial, 4 MiB buffer / s | Plain pipeline / s |
|---|---:|---:|---:|
| 1 | 83.23 | 84.93 | 78.98 |
| 2 | 83.03 | 84.98 | 80.83 |
| median | 83.13 | 84.95 | 79.90 |

The initial implementation is retained at commit fb8a12c. Out-of-order prefix gaps limited overlap. Initial measurements and both rounds remain visible; later results do not replace them.

## Final diagnostics

| Round / mode | Max transfer s | Max hash tail s | Max summed SHA call s | Max prefix wait s | Hashed at finalization | Seed valid GiB |
|---|---:|---:|---:|---:|---:|---:|
| 1 / plain | 36.00 | 37.26 | 33.45 | 34.40 | 4.8% | 51.29 |
| 1 / windowed | 37.00 | 20.77 | 34.93 | 11.43 | 74.3% | 86.40 |
| 2 / windowed | 38.00 | 19.77 | 35.54 | 10.59 | 75.5% | 90.15 |
| 2 / plain | 36.00 | 38.26 | 34.22 | 34.67 | 4.8% | 49.73 |

Maxima can come from different nodes and must not be summed. The overlap sample is taken when the main loop observes completed chunks, not at exact NIC arrival. Every new receiver still hashes the entire file. Overlap, window scheduling, scheduler search cost, and I/O locality can all affect results; the experiment does not isolate these individually. Seed provenance is useful accepted payload, not wire traffic.

## Hardware and integrity

Profiles sampled blockSHANI automatically, blockAVX2 with SHA disabled, and blockGeneric with optional CPU features disabled, producing identical digests. Hardware acceleration was already active in the baseline; microbenchmark ratios are not version speedups.

See [anonymous memory/backend results](sha-hardware-results.json) and [separate file diagnostics](sha-file-results.json). The latter were not paired cold-cache trials and include profiling overhead. Compute timings are call wall times, not hardware cycle counts. ARM64 is cross-built only.

Both studies together contain 12 fresh full transfers and 540 successful per-node sync/drop_caches barriers, excluding the BCM head. Prepared staging is excluded; retries remain within continuous timing. Every receiver has Origin=false, full Downloaded bytes, and Complete=true. Full SHA-256, fsync, and atomic publication remain mandatory. Post-study audits confirm 45 distinct boot IDs per study and all 540 owned process records stopped.

Device caches and background load were uncontrolled. Timing includes deployment/shutdown and controller polling granularity. Identifying hosts, tokens, and complete logs remain private; public JSON records hashes and anonymized evidence.

- Initial [summary](pipeline-initial-results.json), [nodes](pipeline-initial-nodes.json), [barriers](pipeline-initial-barriers.json)
- Window [summary](pipeline-window-results.json), [nodes](pipeline-window-nodes.json), [barriers](pipeline-window-barriers.json)
- Drivers: [initial](../scripts/benchmark-verify.py), [window](../scripts/benchmark-verify-window.py)

Actual local job listings use --state-dir ./run/verify45 and --state-dir ./run/verify45-window; individual selectors use the corresponding <case-id>-job path. Default jobs does not search custom locations.
