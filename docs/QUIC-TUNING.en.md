# QUIC integration audit and tuning

[中文](QUIC-TUNING.zh-CN.md) · [Initial comparison](QUIC-RESULTS.en.md)

Stable v0.2.0 continues to default to TCP with pipelined full-file verification and a 256-chunk window; adaptive concurrency stays off. This candidate preserves those choices. UDP requires explicit `--transport quic`.

## Findings

The original http3.Transport shares one client UDP socket/receive loop across destinations. The candidate assigns sources consistently to a bounded set of transports, preserving connection affinity. `--quic-sockets` accepts 1..32 and defaults to 4 for new jobs. These are static receive-path shards, not additional/adaptive download workers. Unused shards initialize lazily; the metric is configuration, not observed open sockets.

Five-node 8 GiB CPU profiles are diagnostics, not formal timings. One receiver showed about 23% syscall and 4% AES-decrypt samples, plus significant scheduler/receive work. Cumulative stack percentages must not be added. A separate 45-node cold-cache one/four-socket ablation retains both orders.

Upstream fixes packet capacity/negotiation at1452 bytes. A narrow local patch allows a larger negotiated ceiling on the cluster's MTU9000 network. Initial packets remain1280 with upstream fallback behavior; larger packets require peer permission and PMTU confirmation. No system MTU changes are made.

Ordinary rmem_max256 KiB is not effective socket allocation: an identical short seed probe reports14 MiB read/write buffers. quic-go attempts privileged per-socket enlargement when available. Whole-node UDP receive-buffer errors rose by roughly2.71 million across44 nodes during the initial study; these include potentially unrelated traffic and do not prove one sole cause.

## Usage

~~~bash
./swarmcopy distribute --hosts 'node[001-005]' \
  --source /shared/nemo.sqsh --dest /raid/images --bin-dir ./dist \
  --transport quic
~~~

Candidate QUIC defaults are `--quic-sockets 4 --quic-max-payload 8952`; default transport remains TCP. The1452..8952 ceiling refers to UDP payload bytes, not NIC MTU.8952 leaves48 bytes for IPv6/UDP within a9000-byte path. The actual size also respects peer limits and PMTU discovery.

For a new diagnostic job, `--quic-sockets 1 --quic-max-payload 1452` restores the initial configured limits. Job parameters persist through resume. Missing old fields explicitly mean1/1452 rather than inheriting newer defaults. Performance.QUICSockets/QUICMaxPayload are configured limits, not observed socket count/path MTU.

## Integrity and PMTU coverage

Real UDP proxy tests observe8937-byte payloads on a wide path. Silently dropping packets above1472 still transfers every byte and observes payloads up to1470. A1452-limited peer negotiates approximately1441. Race checks pass.

Real five-node8 GiB default deployment passes full SHA-256 and observes roughly8 KiB ingress UDP payloads on an actual receiver. Public evidence contains decoded lengths, not raw captures or media. BPF greater matches captured packet length including headers, which differs from UDP payload length.

Tests cover connection establishment on a narrow path. Recovery from a route/MTU reduction during an established transfer is unverified; this is not a universal dynamic-network claim. Full SHA-256, fsync, atomic publication, and resume remain mandatory.

## Dependency maintenance

Pinned quic-go v0.65.0 and build dependencies are vendored with licenses. `patches/quic-go-pmtu.patch` records four modified upstream files:

- interface.go: per-connection maximum UDP payload.
- config.go: validation,1452 default, copy, initial-size clamp.
- connection.go: advertisement and local/peer minimum ceiling.
- internal/protocol/protocol.go:8952-byte buffer capacity.

Upstream-style zero configuration remains1452; this application's CLI explicitly supplies its new-job ceiling. Conservative initial packets, loss recovery, congestion control, TLS, and the PMTU algorithm remain. Larger buffers do not force larger outgoing packets.

~~~bash
SWARMCOPY_GO=/path/to/go bash scripts/vendor-quic.sh
go test -race ./...
go vet ./...
bash scripts/build.sh
~~~

Regeneration from fixed modules plus the patch reproduces928 files byte-for-byte. An upstream upgrade requires revisiting all four changes and rerunning narrow-path, mixed-limit, loss, recovery, and fresh-target performance tests. Runtime stays a static single executable.

## Rate-control roadmap

Network queueing, receiver CPU/storage limits, and user caps need separate measurements before combining well-defined constraints. Network RTT must exclude application/disk time; loss alone is not a complete congestion signal. Receiver feedback and batched rate pacing warrant independent experiments.

This version does not layer a second delay controller over QUIC, disable congestion control, or change download-worker defaults. Formulas/constants from static samples are not dynamically validated control algorithms. Keep future rate-controller experiments separate from socket/packet-size ablations.

