# Adaptive scheduling: 45-node comparison

> Historical study/validation record: versions, parameters and results describe that measurement, not current defaults. For current use, see [project home](../README.en.md) and [documentation](README.en.md).

[中文](ADAPTIVE-RESULTS.zh-CN.md) · [Design and metric definitions](ADAPTIVE.en.md) · [Numeric results](data/adaptive-results.json)

This experiment did not show an adaptive speedup over the release. The prototype must not be promoted as a performance improvement; scheduling remains opt-in.

45 identical participants per case: one prepared seed and 44 fresh receivers, with a 40,188,444,672-byte file. All methods use HTTP/TCP, the same seed, default upload slots/initial download concurrency, and unlimited rates. Wall time includes deployment, whole-file SHA-256, fsync, and owned-peer shutdown.

| Round | v0.1.0 / s | Candidate fixed / s | Candidate adaptive / s |
|---|---:|---:|---:|
| 1 | 84.83 | 82.77 | 83.37 |
| 2 | 84.92 | 85.28 | 88.93 |
| median | 84.88 | 84.03 | 86.15 |

Median ratios: **0.985×** versus v0.1.0 and **0.975×** versus candidate fixed. Above one means faster. Ranges: baseline 84.83–84.92s; fixed 82.77–85.28s; adaptive 83.37–88.93s.

## Phase diagnostics

| Round / mode | Max transfer s | Max fsync s | Max final hash s | Failed requests | Hedges | Worker changes |
|---|---:|---:|---:|---:|---:|---:|
| 1 / fixed | 35.00 | 6.22 | 41.55 | 2968 | 0 | 0 |
| 1 / adaptive | 37.00 | 6.09 | 42.15 | 4071 | 0 | 47 |
| 2 / adaptive | 39.00 | 9.55 | 42.09 | 3465 | 0 | 37 |
| 2 / fixed | 37.00 | 6.07 | 40.52 | 2856 | 0 | 0 |

Phase maxima can come from different nodes and overlap; do not sum them into batch time. Transfer includes discovery/scheduling/waiting; controller deployment and stopping are included separately in wall time. [Anonymous node records](data/adaptive-node-results.json) preserve work sums and application payload reads. ReceivedBytes is not NIC traffic. Hedges may increase traffic, but valid chunks are committed/accounted only once.

## Controls and limits

Every method/round used a new UUID directory, absent on every host before sync/drop_caches=3: 270 successful node barriers. Round orders were baseline→fixed→adaptive and adaptive→fixed→baseline. Normal in-case retransmission was allowed without resetting clocks. Every receiver had Origin=false, Downloaded equal to the full file, and Complete=true.

Post-study checks confirm 45 distinct kernel boot IDs and all 270 owned peer records stopped. Full identifying evidence remains private in run/compare45; public data is anonymized. Executable/deployment hashes were frozen and checked before each case; only documents changed during timing.

Two observations cannot establish a general performance guarantee. Device caches and background load were uncontrolled. This is not a commercial Aspera test or UDP implementation; speedup ratios must not be multiplied by earlier rsync results. ARM64 is cross-built only.

- [Anonymous per-node data](data/adaptive-node-results.json)
- [Cache barriers](data/adaptive-cache-barriers.json)
- [Reproduction driver](../scripts/benchmark-adaptive.py)

Actual local study jobs: ./swarmcopy jobs --state-dir ./run/compare45; select --job ./run/compare45/<case-id>-job. Default jobs does not search custom locations.
