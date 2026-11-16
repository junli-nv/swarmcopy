# Adaptive scheduling: 45-node comparison

> Historical study/validation record: versions, parameters and results describe that measurement, not current defaults. For current use, see [project home](../README.en.md) and [documentation](README.en.md).

[中文](ADAPTIVE-INITIAL-RESULTS.zh-CN.md) · [Design and metric definitions](ADAPTIVE.en.md) · [Numeric results](data/adaptive-initial-results.json)

This experiment did not show an adaptive speedup over the release. The prototype must not be promoted as a performance improvement; scheduling remains opt-in.

45 identical participants per case: one prepared seed and 44 fresh receivers, with a 40,188,444,672-byte file. All methods use HTTP/TCP, the same seed, default upload slots/initial download concurrency, and unlimited rates. Wall time includes deployment, whole-file SHA-256, fsync, and owned-peer shutdown.

| Round | v0.1.0 / s | Candidate fixed / s | Candidate adaptive / s |
|---|---:|---:|---:|
| 1 | 85.03 | 85.17 | 101.14 |
| 2 | 82.97 | 82.97 | 104.75 |
| median | 84.00 | 84.07 | 102.94 |

Median ratios: **0.816×** versus v0.1.0 and **0.817×** versus candidate fixed. Above one means faster. Ranges: baseline 82.97–85.03s; fixed 82.97–85.17s; adaptive 101.14–104.75s.

## Phase diagnostics

| Round / mode | Max transfer s | Max fsync s | Max final hash s | Failed requests | Hedges | Worker changes |
|---|---:|---:|---:|---:|---:|---:|
| 1 / fixed | 36.00 | 6.16 | 41.40 | 3011 | 0 | 0 |
| 1 / adaptive | 55.00 | 2.34 | 41.94 | 3683 | 0 | 261 |
| 2 / adaptive | 61.00 | 4.68 | 42.20 | 4167 | 0 | 277 |
| 2 / fixed | 35.00 | 5.77 | 40.68 | 3448 | 0 | 0 |

Phase maxima can come from different nodes and overlap; do not sum them into batch time. Transfer includes discovery/scheduling/waiting; controller deployment and stopping are included separately in wall time. [Anonymous node records](data/adaptive-initial-node-results.json) preserve work sums and application payload reads. ReceivedBytes is not NIC traffic. Hedges may increase traffic, but valid chunks are committed/accounted only once.

## Controls and limits

Every method/round used a new UUID directory, absent on every host before sync/drop_caches=3: 270 successful node barriers. Round orders were baseline→fixed→adaptive and adaptive→fixed→baseline. Normal in-case retransmission was allowed without resetting clocks. Every receiver had Origin=false, Downloaded equal to the full file, and Complete=true.

Post-study checks confirm 45 distinct kernel boot IDs and all 270 owned peer records stopped. Full identifying evidence remains private in run/compare45-initial; public data is anonymized. Executable/deployment hashes were frozen and checked before each case; only documents changed during timing.

Two observations cannot establish a general performance guarantee. Device caches and background load were uncontrolled. This is not a commercial Aspera test or UDP implementation; speedup ratios must not be multiplied by earlier rsync results. ARM64 is cross-built only.

- [Anonymous per-node data](data/adaptive-initial-node-results.json)
- [Cache barriers](data/adaptive-initial-cache-barriers.json)
- [Reproduction driver](../scripts/benchmark-adaptive.py)

Actual local study jobs: ./swarmcopy jobs --state-dir ./run/compare45-initial; select --job ./run/compare45-initial/<case-id>-job. Default jobs does not search custom locations.

[Initial prototype source](https://github.com/junli-nv/swarmcopy/commit/82e5707c3ed60f90b5fbc02f8c4938ca4ddd7e6a)
