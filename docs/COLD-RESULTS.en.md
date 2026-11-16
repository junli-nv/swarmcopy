# 157-node cold-OS-cache results

> Historical study/validation record: versions, parameters and results describe that measurement, not current defaults. For current use, see [project home](../README.en.md) and [documentation](README.en.md).

[中文](COLD-RESULTS.zh-CN.md) · [Preregistered protocol](COLD-STUDY.en.md) · [Numeric summary](data/cold-results.json)

157 compute nodes: one prepared seed and 156 fresh receivers. Each file is 40,188,444,672 bytes (37.43 GiB); each case delivers 6,269,397,368,832 bytes (5.70 TiB) to receivers. All three pairs completed.

| Round | rsync copy / s | rsync verified / s | SCDP verified + stopped / s | vs copy | vs verified |
|---|---:|---:|---:|---:|---:|
| 1 | 364.55 | 410.87 | 153.29 | 2.38× | 2.68× |
| 2 | 362.76 | 408.73 | 153.47 | 2.36× | 2.66× |
| 3 | 360.13 | 406.45 | 153.15 | 2.35× | 2.65× |
| median | 362.76 | 408.73 | 153.29 | 2.37× | 2.67× |

Verified ranges: rsync 406.45–410.87 s; SCDP 153.15–153.47 s. Summary speedups divide the respective medians; all paired ratios are also shown. Verified median time is reduced by 62.5%.

## Freshness and verification

Every method/case used a unique absent directory on all 157 hosts, followed by successful sync/drop_caches=3: 942 node-cache barriers. The order was rsync→SCDP, SCDP→rsync, rsync→SCDP. Both methods were unlimited and shared the same prepared seed, excluding initial import. Executable and both deployment artifact hashes were frozen and checked before every method. Normal in-case retransmission/recovery was allowed with continuous timing.

All 156 rsync receivers in each round passed full SHA-256; per-attempt literal bytes, transferred bytes, and exit codes are retained. Hashing started immediately after each receiver's copy and overlapped across hosts. All SCDP receivers had Origin=false, Downloaded equal to the complete file size, and Complete=true. Each new SCDP file passed final SHA-256 and fsync. SCDP timing also includes deployment and owned-peer shutdown.

A post-study audit found 157 distinct kernel boot IDs and checked 471 owned process records across all three jobs: all stopped. Identifying hostnames, UUIDs, PIDs, and complete logs remain in the private controller run/ directory.

## Seed load

SCDP accepted 47.10, 46.83, 47.68 GiB of valid chunks from the seed across the three respective cases, versus 5838.83 GiB of useful file payload for single-source fan-out. Seed-origin useful bytes decreased by 99.18%–99.20%. These are accepted-chunk provenance counters, not NIC wire counters; discarded retransmission and protocol overhead are excluded.

## Scope

All execution was x86_64, with 116 XFS and 41 ext4 local RAID filesystems. The seed used four 100 Gb/s ports in an LACP bond. Both methods used the same seed and ordinary routed TCP; no native RDMA.

This compares complete solutions: encrypted rsync-over-SSH versus plaintext SCDP-over-HTTP. It is not a topology-only ablation. Only OS caches were dropped; device caches and background load were uncontrolled. Baseline SHA-256 establishes readable content integrity without promising SCDP-equivalent power-loss durability. Three pairs do not guarantee performance on other clusters or monotonic gains with node count. Earlier 5/45-node tests did not drop caches and cannot form a controlled scaling curve with this table.

## Evidence

- [Anonymous per-node transfer/verification records](data/cold-node-results.json)
- [All 942 anonymous cache barriers](data/cold-cache-barriers.json)
- [Conditions, hashes, and summary](data/cold-results.json)
- [Reproducible driver](../scripts/benchmark-cold.py)

For actual local study jobs, use ./swarmcopy jobs --state-dir ./run/cold157 and watch/inspect --job ./run/cold157/<case-id>-job. Default jobs does not search these custom locations. The rsync baseline is in results.json/summary.json, not in swarmcopy jobs.
