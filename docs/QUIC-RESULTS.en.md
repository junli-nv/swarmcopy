# UDP/QUIC: 45-node comparison

> Historical study/validation record: versions, parameters and results describe that measurement, not current defaults. For current use, see [project home](../README.en.md) and [documentation](README.en.md).

[中文](QUIC-RESULTS.zh-CN.md) · [Implementation](QUIC.en.md)

A 40,188,444,672-byte file (37.43 GiB), one prepared seed and 44 fresh receivers on 45 distinct nodes.

| Round | v0.2.0 TCP / s | Candidate TCP / s | Candidate QUIC/UDP / s |
|---|---:|---:|---:|
| 1 | 67.36 | 68.07 | 159.16 |
| 2 | 65.16 | 69.71 | 153.13 |
| median | 66.26 | 68.89 | 156.15 |

The same-candidate TCP/QUIC median ratio is **0.441x**; above one favors QUIC. QUIC time changes by **+126.7%** relative to TCP (positive means slower). Two observations are not a universal result.

| Round / mode | Max transfer s | Max hash-tail s | HTTP/3 chunk responses | Application retries | Seed valid GiB |
|---|---:|---:|---:|---:|---:|
| 1 / baseline | 39.00 | 18.45 | 0 | 3349 | 98.80 |
| 1 / tcp | 36.00 | 18.17 | 0 | 3810 | 96.50 |
| 1 / quic | 143.00 | 0.65 | 169835 | 64411 | 51.35 |
| 2 / quic | 138.00 | 0.51 | 170653 | 65229 | 50.99 |
| 2 / tcp | 42.00 | 17.58 | 0 | 3117 | 98.88 |
| 2 / baseline | 36.00 | 19.66 | 0 | 3828 | 89.22 |

HTTP/3 response counts include non-successful status codes. QUIC packet retransmissions are not application retries. Seed useful bytes are not total wire traffic; maxima must not be summed.

All methods use four fixed workers, four upload slots, unlimited bandwidth, pipeline verification, a 256-chunk window, and disabled adaptive/endgame. Order is baseline/tcp/quic then reversed. Each case starts with an absent UUID destination and per-compute-node sync/drop_caches=3: 270 successful barriers, excluding the BCM head. Prepared seed staging is excluded; deployment, transfer, retries, full SHA-256, fsync, atomic publication, and shutdown are timed.

Every receiver has full Downloaded bytes, Origin=false, Complete=true, and full FileHashBytes. QUIC receivers report actual HTTP/3 requests and cannot silently fall back. Post-study checks confirm 45 distinct boot IDs and all 270 owned process records stopped.

This compares HTTP/3+QUIC+TLS to plaintext HTTP/TCP, not UDP alone or IBM FASP. Encryption, userspace processing, flow/congestion control, buffers, and offloads may all matter. Background jobs, device caches, and network traffic were not isolated; WAN conditions were not simulated. Kernel settings were unchanged. Whole-node UDP counter deltas may include unrelated traffic.

TCP stays the default. Pipeline/window are regular v0.2.0 defaults and adaptive remains off. UDP is an independent optional transport. ARM64 is cross-built only.

Evidence: [summary/artifact hashes](data/quic-results.json), [anonymous nodes](data/quic-nodes.json), [cache barriers](data/quic-barriers.json), [driver](../scripts/benchmark-quic.py). Private records use --state-dir ./run/quic45 and individual <case-id>-job paths. Host identities, tokens, and complete logs remain private.

## Post-timing implementation audit

Ordinary rmem_max is256 KiB, but an identical 8-second seed probe reports14 MiB read/write buffers (Linux accounting): quic-go attempts privileged per-socket enlargement. The sysctl maximum is not the effective socket allocation. Whole-node receive-buffer errors rise by2,712,406 across44 nodes, potentially including other traffic. Default http3.Transport shares one receive UDP socket across destinations; this is a design bottleneck hypothesis to test, not an established sole cause.

[Kernel counters](data/quic-kernel-counters.json), [log summary](data/quic-log-diagnostics.json), [socket probe](data/quic-socket-probe.json).
