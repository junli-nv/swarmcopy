# Per-node NIC benchmark

[中文（主文档）](NETWORK.zh-CN.md) · [Documentation](README.en.md) · [Examples](EXAMPLES.en.md)

Default mesh means every node sends and receives. Normally only hosts are needed:

```bash
./swarmcopy benchmark --hosts 'HGX[603-608,610-618,620-653,655-665]'
```

Fixed buffers generate traffic; receivers count and discard it, without media I/O or content hashes. SSH deploys and controls only. Payload stays among listed hosts; a controller outside the list does not participate.

## See whether each NIC is saturated

The main table reads Linux byte counters for each node's routed network interface, rather than estimating NIC throughput from application bytes. It detects interface/capacity and shows RX/TX utilization. One actual row from the 60-node run:

```text
NODE               INTERFACE/LINK       RX Gbit/s    TX Gbit/s RX/LINK TX/LINK STATE
HGX603             bond0/400G              227.75       226.00   56.9%   56.5% ACTIVE
```

- bond0/400G is the four 100G ports combined, per direction. A node exceeding 100G does not imply one physical port exceeded 100G.
- Live NIC CURRENT uses successive counter samples, refreshed in place about every two seconds. Final NIC AVERAGE covers the complete post-warmup measurement interval.
- Assess RX and TX separately. They are never added together. Cluster Aggregate is no longer emphasized.
- RX/LINK and TX/LINK use detected nominal capacity. The summary counts nodes averaging at least 90%; ACTIVE means traffic, not saturation.
- NIC counters include other interface traffic. Bond counters are counted once, without adding physical members again. Physical member counters/rates are retained separately in JSON.
- Unknown interfaces, loopback, counter resets and invalid intervals fail explicitly; application statistics are not substituted and rates are not clamped or invented.

This synthetic traffic does not exercise file chunk scheduling, common-file forwarding, storage or publication. It is not a real file-distribution benchmark.

## Quiet defaults

Interactive terminals refresh one dashboard and retain one final table. If terminal height is limited, the live view prioritizes lower-utilization/problem nodes without scrolling; the final table includes all nodes. Redirected output or --plain emits occasional one-line progress and the final table. SSH commands, process IDs and deployment details are hidden unless requested; errors remain visible.

```bash
./swarmcopy benchmark --hosts @hosts.txt --duration 60s
./swarmcopy benchmark --hosts @hosts.txt --workers 128
./swarmcopy benchmark --hosts @hosts.txt --output network.json
./swarmcopy benchmark --help
./swarmcopy benchmark --help-all
```

Normal help lists common options; full help retains advanced tuning. --verbose shows SSH/configuration details; --plain disables terminal refresh. --output accepts a new path only. JSON retains application diagnostics, real NIC/member counters, error categories and measurement intervals.

## Defaults and optional tuning

| Setting | Default / meaning |
| --- | --- |
| topology | mesh; cyclic source ordering balances load even when workers are fewer than nodes |
| duration / warmup | 30-second measurement, 3-second warmup; deployment/warmup excluded |
| workers / uploads / per-source | 64 download streams, 256 uploads, at most 64 streams/source per node; each configurable 1..1024 |
| cpus | Go parallelism 32 capped to available CPUs; --cpus 0 follows runtime defaults, not CPU affinity |
| buffer-kib / block-mib | 1 MiB receive buffer per stream, 64 MiB streamed response; response size is not allocation size |
| port / transport | 19701 / TCP; distribution still defaults to 19700; QUIC also needs same-number UDP |
| numa | system; explicit NUMA policies alone require target numactl |

Explicit payload buffers default to about `64 × 1 MiB + 1 MiB = 65 MiB`, plus Go/protocol/kernel socket memory. Duration and cumulative traffic do not accumulate receive data. No rate cap or physical-memory locking; configured receive buffers are capped at 1 GiB per node. More concurrency need not improve performance.

Advanced seed sends only from the first host, with no forwarding. Ring downloads from the predecessor. Neither is default mesh. SSH user, shared password, cipher, repeated --ssh-opt and architecture --bin-dir options remain available.

## Interpret problems

BUSY identifies HTTP 429 source upload-slot rejection, separately from connection, read, other HTTP and protocol errors. These are not NIC packet-loss counts. Bounded backoff avoids fixed-interval retry storms. Final output summarizes problems and recent concrete errors; JSON holds details.

No request errors means traffic measurement finished, not that links were saturated. An interface at 60% should be reported as 60%.

## LACP, MTU and NUMA

Read-only checks:

```bash
ip link show bond0
cat /sys/class/net/bond0/bonding/slaves
cat /sys/class/net/bond0/bonding/xmit_hash_policy
cat /proc/net/bonding/bond0
numactl --hardware
```

The tested cluster already has MTU 9000, 802.3ad LACP, layer3+4 and four active 100G ports. The program does not reconfigure these. Jumbo MTU needs path support; LACP hashes flows rather than combining four ports for one TCP flow.

Earlier HGX061/HGX066 checks found two NUMA nodes with two NIC ports on each. Binding the whole process to one node still accesses remote ports and need not help. Hold hosts, direction, concurrency, buffers and timing constant while comparing --numa system, 0, 1 or interleave. Numeric policies bind CPU and memory; interleave sets memory interleaving. Explicit policies use installed numactl; default system adds no binding.

## Actual measurements and evidence

[Real NIC records](data/network-nic-mesh-20261116.json) contain parameters, per-node bond/member counters and independent samples. The exact 60 hosts in the opening command ran defaults for 30 seconds:

| Per-node metric | Minimum | Median | Maximum |
| --- | --- | --- | --- |
| NIC RX Gbit/s | 222.52 | 229.00 | 256.90 |
| NIC TX Gbit/s | 212.02 | 229.95 | 264.44 |

All 60 exchanged payload in both directions; HTTP 429 and other request errors were zero. Zero of 60 averaged at least 90% nominal capacity in either direction: this run did not saturate the links. Independent SSH samples from HGX603/608/618/653/665 agreed with the program's counters. A separate three-node HGX603–605 short test reached roughly 396..397 Gbit/s in each direction. Small-group near-line-rate results do not prove 60-node mesh saturation; topology and software costs need further isolation.

[Earlier application-byte observations](data/network-benchmark-20261116.json) retain directional/concurrency/NUMA trials, including slower rounds. Their application rates differ from this guide's NIC metric. Different hardware, scale, direction or metrics must not be combined into software speedup claims.

## Stop and cleanup

Ctrl+C closes SSH stdin leases, waits for owned peers and cleans exclusive temporary /tmp/.swarmcopy/benchmark-... deployment. Unconfirmed shutdown/cleanup failures name the retained path. Unrelated jobs are not stopped.

Benchmarks do not appear in jobs and are not resumable; each invocation is fresh. Interruptions, failed nodes or unavailable real NIC counters mark the run failed instead of implying a complete measurement.
