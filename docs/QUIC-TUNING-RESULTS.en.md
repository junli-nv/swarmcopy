# UDP integration audit: three controlled ablations

> Historical study/validation record: versions, parameters and results describe that measurement, not current defaults. For current use, see [project home](../README.en.md) and [documentation](README.en.md).

[中文](QUIC-TUNING-RESULTS.zh-CN.md) · [Implementation](QUIC-TUNING.en.md) · [Retained initial results](QUIC-RESULTS.en.md)

All studies use the same45-node cohort, one prepared seed and44 fresh receivers, and a40,188,444,672-byte file (37.43GiB). Each table compares the same frozen binary and configuration, except the stated variable. Ratios from different studies are not multiplied.

## Final same-binary TCP/GSO comparison

Both QUIC methods use four source-affine client socket shards and a negotiated8952-byte ceiling. Individual packets retain peer/path limits and congestion/pacing checks.

| Round | TCP / s | QUIC20KiB GSO / s | QUIC64KiB GSO / s |
|---|---:|---:|---:|
| 1 | 65.06 | 77.05 | 71.12 |
| 2 | 66.46 | 74.96 | 69.22 |
| median | 65.76 | 76.01 | 70.17 |

64KiB time changes by **-7.7%** against20KiB; positive means slower. TCP/64KiB median ratio is **0.937x**, above one favors QUIC. Two observations do not establish a universal result. TCP stays default and v0.2.0 is the regular stable release.

## Client receive-socket ablation

Same code,1452-byte ceiling and20KiB GSO; one versus four sockets.

| Round | Single socket / s | Four sockets / s |
|---|---:|---:|
| 1 | 157.03 | 112.91 |
| 2 | 153.23 | 117.05 |
| median | 155.13 | 114.98 |

Median time decreases by 25.9%. Profiles and this ablation support receive-path parallelism as one real initial limitation; download-worker count is unchanged.

## Payload ceiling ablation

Same binary, four sockets,20KiB GSO; conservative initial packets and peer/path validation retained.

| Round | TCP / s | QUIC1452 ceiling / s | QUIC8952 ceiling / s |
|---|---:|---:|---:|
| 1 | 69.17 | 112.85 | 82.72 |
| 2 | 65.17 | 113.08 | 79.07 |
| median | 67.17 | 112.97 | 80.90 |

The larger ceiling reduces QUIC median time by 28.4% within this study.

## Phase and kernel evidence

| Study / round / mode | Max transfer s | Max hash tail s | Node UDP received-datagram delta | Receive-buffer-error delta |
|---|---:|---:|---:|---:|
| shards / 1 / single | 141.00 | 1.55 | 1347347191 | 1242149 |
| shards / 1 / sharded | 95.00 | 1.95 | 1320052925 | 104120 |
| shards / 2 / sharded | 99.00 | 2.65 | 1319549558 | 94826 |
| shards / 2 / single | 138.00 | 0.15 | 1348377357 | 1281904 |
| pmtu / 1 / tcp | 42.00 | 22.28 | 16682 | 0 |
| pmtu / 1 / small | 96.00 | 2.68 | 1319396997 | 148696 |
| pmtu / 1 / jumbo | 63.00 | 2.48 | 224383022 | 2943 |
| pmtu / 2 / jumbo | 61.00 | 1.83 | 224292072 | 6871 |
| pmtu / 2 / small | 96.00 | 0.79 | 1319185665 | 163466 |
| pmtu / 2 / tcp | 36.00 | 19.77 | 15831 | 0 |
| gso / 1 / tcp | 38.00 | 20.09 | 16468 | 0 |
| gso / 1 / gso20 | 60.00 | 2.57 | 224700896 | 5192 |
| gso / 1 / gso64 | 55.00 | 1.20 | 221047782 | 14275 |
| gso / 2 / gso64 | 53.00 | 3.00 | 221089953 | 9515 |
| gso / 2 / gso20 | 59.00 | 1.38 | 224307537 | 10032 |
| gso / 2 / tcp | 36.00 | 20.88 | 15160 | 0 |

Maxima may come from different nodes and cannot be summed. Kernel deltas are whole-node and may include other processes. InDatagrams is receive accounting; send counters such as OutDatagrams may use GSO-dependent accounting and are not wire packet or exact syscall counts. Receive-buffer errors are not every task packet loss. A smaller hash tail does not omit integrity work.

## Controls, integrity and retained observations

Including the initial trial:22 complete large-file cases,990 successful compute-node sync/drop_caches barriers,45 distinct boot IDs per study, and all990 owned process records stopped. Formal studies do not overlap and do not enable CPU profiling.

Every receiver destination is absent before its case. The BCM head is not cache-dropped. Prepared seed staging is excluded; deployment, transfer, retries, full SHA-256, fsync, atomic publication, and shutdown are timed. All methods use four fixed workers, four upload slots, unlimited bandwidth, disabled adaptive/endgame, pipeline hashing and a256-chunk window. Every receiver is Origin=false with full Downloaded/FileHashBytes and Complete=true. Actual HTTP/3 responses prove selected UDP, with no silent TCP payload fallback.

The initial approximately156-second QUIC results remain intact at source checkpoint6ec0d33, followed by socket checkpoint724883e and PMTU checkpointd459d31. Public JSON records binary/deployment/driver hashes; later measurements do not replace earlier ones.

Separate five-node8GiB CPU profiling is diagnostic only. One receiver samples approximately23% syscall and4% AES decrypt; this is not a cluster-wide CPU distribution. An identical short seed probe reports14MiB effective socket buffers; ordinary256KiB sysctl limits are not effective allocation.

One auxiliary capture assertion mixed BPF packet length with decoded UDP payload length. The8GiB transfer and full hash passed; the assertion was corrected against the original capture (7993/8232-byte payloads), without rerunning/resuming a formal case to replace results.

Evidence by study: [socket summary](data/quic-shards-results.json), [nodes](data/quic-shards-nodes.json), [barriers](data/quic-shards-barriers.json), [kernel deltas](data/quic-shards-kernel-deltas.json); [PMTU summary](data/quic-pmtu-results.json), [nodes](data/quic-pmtu-nodes.json), [barriers](data/quic-pmtu-barriers.json), [kernel deltas](data/quic-pmtu-kernel-deltas.json); [GSO summary](data/quic-gso-results.json), [nodes](data/quic-gso-nodes.json), [barriers](data/quic-gso-barriers.json), [kernel deltas](data/quic-gso-kernel-deltas.json).

Additional evidence: [anonymous CPU profiles](data/quic-cpu-diagnosis.json), [socket probe](data/quic-socket-probe.json), [actual ingress payload lengths](data/quic-jumbo-ingress.json).

This compares HTTP/3+QUIC+TLS with plaintext HTTP/TCP, not UDP alone or IBM FASP. WAN conditions, device caches, and background load were not isolated/simulated. Global network/sysctl/MTU settings were unchanged. ARM64 is cross-built only; mid-transfer MTU-reduction recovery remains unverified. Local dependency changes are explicitly recorded, not described as unmodified upstream.

Actual records use the corresponding trial parent as --state-dir and individual <case-id>-job paths for --job. Default jobs does not recursively find custom paths. Host identities, tokens, complete logs, raw CPU profiles and captures remain private.
