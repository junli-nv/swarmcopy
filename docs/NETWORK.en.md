# Memory network benchmark and throughput diagnosis

[中文（主文档）](NETWORK.zh-CN.md) · [Documentation](README.en.md) · [Examples](EXAMPLES.en.md)

`benchmark` measures application payload throughput between compute nodes. Each node repeatedly serves a small pre-generated random buffer; receivers count and discard bytes. There are no media reads, payload files or content hashes. The separate streaming endpoints do not exercise SCDP chunk scheduling, forwarding, storage or publication, so their rates are not file-distribution performance claims.

## Run

```bash
# Simultaneous sending/receiving: 64 download streams, 256 upload limit per node
./swarmcopy benchmark --hosts 'HGX[061,066,082,138,169]'

# Unidirectional, comparable in direction to default iperf: first host sends
./swarmcopy benchmark --hosts 'HGX[061,066]' --topology seed \
  --workers 64 --per-source 64 --uploads 128 \
  --cpus 32 --buffer-kib 1024 --warmup 3s --duration 30s \
  --output network-forward.json

# Reverse direction by reversing the ordered host list
./swarmcopy benchmark --hosts 'HGX[066,061]' --topology seed \
  --workers 64 --per-source 64 --cpus 32 --buffer-kib 1024

./swarmcopy benchmark --help
```

SSH deploys binaries and maintains process leases; HTTP controls and queries peers. Payload flows only between listed nodes. A controller included in hosts also participates. Benchmark port defaults to 19701, configurable with `--port`; normal distribution defaults to 19700. The same SSH user, shared-password prompt, cipher and repeatable `--ssh-opt` options apply. Architecture binaries in `--bin-dir` must match the controller. Default testing needs no external benchmark utility; explicit NUMA policies require target `numactl`.

## Topology, concurrency and memory

| Option | Meaning |
| --- | --- |
| `--topology mesh` (default) | Every node downloads from others and serves its own buffer; limited stream counts may not cover every host pair |
| `--topology ring` | Download from predecessor; synthetic local buffers are served, not a common file relayed around the ring |
| `--topology seed` | First node serves the rest; no receiver forwarding, useful for unidirectional/source limits |
| `--workers 64` | Total download streams per node, 1..1024 |
| `--per-source 64` | Streams per source, 1..1024; actual total is min(workers, sources × per-source) |
| `--uploads 256` | Concurrent upload response limit, 1..1024; insufficient capacity produces busy/failure counts |
| `--buffer-kib 256` | Receive buffer per stream, plus one shared send buffer, 16..1024 KiB |
| `--block-mib 64` | Streamed bytes per response, not allocation size, 1..1024 MiB |
| `--cpus 0` | Go CPU parallelism; 0 keeps runtime defaults, not affinity or physical core count |
| `--transport quic` | Optional QUIC, TCP control plus same-number UDP; no TCP payload fallback |

Default explicit payload buffers use approximately `64 × 256 KiB + 256 KiB = 16.25 MiB`. Go, connections, protocol/statistics and kernel sockets add memory. Duration, cumulative bytes and block-mib do not accumulate received content. Configured receive buffers are capped at 1 GiB per node. Physical pages are not locked; OS swap policy still applies.

There is no rate cap. More streams can improve LACP distribution or increase contention; 128/1024 is not automatically faster than 64.

## Interpret results

Every two seconds, the table shows remaining time, average per-node RX/TX Gbit/s since warmup, received GiB and failed requests. Gbit/s is decimal; GiB is binary. Aggregate RX sums node RX rates without adding TX again.

Deployment and warmup are excluded. Each node uses its own monotonic measurement interval, recorded as Seconds; synchronized wall clocks are unnecessary, though control RPC boundaries have small skew. Counts include actual payload read/written, including partial failed transfers. They are not unique-file bytes, NIC wire rates or TCP retransmission counts. QUIC still incurs TLS/AEAD costs without content hashing.

A 4×100G bond has a nominal 400 Gbit/s per node in each direction, about 50 GB/s. Bidirectional tests send and receive simultaneously. Two-node Aggregate RX can therefore approach 800 Gbit/s; it is not one node's 400G rate. Mesh results cannot be compared directly with unidirectional iperf.

## LACP and NUMA

```bash
ip link show bond0
cat /sys/class/net/bond0/bonding/slaves
cat /sys/class/net/bond0/bonding/xmit_hash_policy
cat /proc/net/bonding/bond0
numactl --hardware
```

The measured cluster already had MTU 9000 on bond/members, 802.3ad LACP, layer3+4 hashing and four active 100G ports. The program does not change system configuration. Jumbo MTU requires path support; hashing distributes flows rather than combining four links for one TCP flow, and inbound distribution also depends on switches.

HGX061/HGX066 each have two NUMA nodes: both ens1 ports attach to NUMA 0, both ens3 ports to NUMA 1. Binding the entire process to one node still accesses two remote NIC ports and need not improve throughput. Hold every other option constant:

```bash
./swarmcopy benchmark --hosts 'HGX[061,066]' --workers 64 --per-source 64 --cpus 32 --buffer-kib 1024 --numa system
./swarmcopy benchmark --hosts 'HGX[061,066]' --workers 64 --per-source 64 --cpus 32 --buffer-kib 1024 --numa 0
./swarmcopy benchmark --hosts 'HGX[061,066]' --workers 64 --per-source 64 --cpus 32 --buffer-kib 1024 --numa 1
./swarmcopy benchmark --hosts 'HGX[061,066]' --workers 64 --per-source 64 --cpus 32 --buffer-kib 1024 --numa interleave
```

Numeric policies use numactl to bind CPU and memory; interleave sets memory interleaving over available nodes. Default system adds no binding. Match direction, hosts, streams, buffers, Go parallelism, warmup and timing; repeat in reversed order and inspect physical port counters, CPU/softirq, retransmissions and topology. NUMA numbering is host-specific.

## Evidence and limits

[Recorded measurements](data/network-benchmark-20261116.json) include parameters and slower trials. These are short diagnostics, not repeated statistical guarantees or before/after software-version comparisons.

- Five-node mesh: 64 workers over 20 seconds yielded about 1.18 Tbit/s; another 128-worker, 10-second run about 1.26 Tbit/s. Independent bond counters agreed in scale. Process RSS was about 93..96 MiB despite hundreds of GiB received per node.
- Two-node bidirectional, 64 workers, Go parallelism 32, 1 MiB buffers: system about 697.4 Gbit/s aggregate; NUMA 0/1/interleave about 672.4/678.0/683.3 Gbit/s. This set did not justify forced NUMA as default.
- Unidirectional on the same pair with 64 workers, Go parallelism 32 and 1 MiB buffers reached about 388.5 Gbit/s forward and 387.7 Gbit/s reverse, close to nominal 400G. Independent physical-port counters showed roughly 97.6..98.4 Gbit/s TX and 96.1..99.7 Gbit/s RX per member.

Lower simultaneous bidirectional/mesh rates do not prove a 200..350G link ceiling. Real distribution also includes storage, chunk validation, source limits, scheduling and finalization; this test cannot attribute their entire gap to hashing. Production verification/scheduling are unchanged.

## Stop, reports and cleanup

Ctrl+C closes SSH stdin leases so owned peers exit, with a bounded lifetime as a fallback. Normal exit removes this test's `/tmp/.swarmcopy/benchmark-...` deployment. Unconfirmed shutdown or cleanup errors report the owned path rather than claiming clean completion. Other jobs/processes are not killed.

Benchmarks are not resumable file jobs and do not appear in jobs. Start a fresh test. Only `--output` saves a new controller-side JSON; existing files are never overwritten. Interrupted/failed tests have State=failed and are not successful performance results.
