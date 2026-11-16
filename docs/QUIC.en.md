# UDP / QUIC chunk transport

The current candidate adds four client socket shards, an8952 negotiated ceiling and a64KiB GSO bound, with an explicitly recorded local dependency patch. See [current design](QUIC-TUNING.en.md) and [complete ablations](QUIC-TUNING-RESULTS.en.md). Base transport behavior follows below.

[中文](QUIC.zh-CN.md) · [TCP release](https://github.com/junli-nv/swarmcopy/releases/tag/v0.2.0)

`--transport quic` selects HTTP/3 reliable streams over QUIC/UDP. TCP stays the default. Pipelined whole-file SHA-256 and the 256-chunk prefix window stay on; adaptive concurrency stays off. This is not IBM FASP and UDP is not assumed to be faster.

~~~bash
./swarmcopy distribute --hosts 'node[001-005]' \
  --source /shared/nemo.sqsh --dest /raid/images --bin-dir ./dist \
  --transport quic
./swarmcopy watch --job <ID>
./swarmcopy stop --job <ID>
./swarmcopy resume --job <ID>
~~~

The job persists transport and resume uses it. Missing values in old jobs mean TCP. Deploy matching binaries to every participant; mixed TCP/QUIC data nodes are unsupported. Create a new TCP job rather than editing a running job's transport.

The port number (19700 by default) is used for TCP status/control and UDP chunk data. Controllers need SSH/TCP reachability; peers additionally need bidirectional UDP reachability. DNS and routing choose interfaces. No firewall, routing, sysctl, MTU, or congestion-control changes are made. This is not native RDMA or automatic NIC aggregation.

QUIC /chunk must use actual HTTP/3. TCP chunk requests are refused and no silent fallback occurs. Discovery/status/watch remain TCP. Blocked UDP causes failed requests and retries within the job lifetime; status/stop remain available and partial files can be resumed.

## Integrity and identity

Pinned quic-go v0.65.0 supplies reliable streams, loss recovery, congestion/flow control, and TLS 1.3. Chunk SHA-256, WriteAt commit, ordinary whole-file SHA-256, fsync, atomic publication, and restart revalidation remain mandatory. Unreliable datagrams do not carry file chunks.

A shared job Ed25519 identity is derived from the high-entropy random job token. Both ends present certificates; mandatory public-key pinning, validity, and signature checks replace DNS/CA validation. No certificate files are required. Identity authenticates the job, not individual hosts; all token holders are trusted peers. Manually supplied tokens must be random and strong.

The system still requires a trusted network: TCP status remains plaintext with the existing bearer token. Encrypted QUIC chunks do not provide system-wide confidentiality or make this safe for an untrusted network.

## Implementation

- quic.go owns TLS identity, UDP listener, HTTP/3 client, and close paths.
- main.go/transfer.go use a separate chunk client; discovery/status keep their TCP client.
- Job/Options persist transport through SSH deployment. Empty old settings mean TCP.
- Performance.Transport and QUICChunkRequests identify mode and actual HTTP/3 chunk responses, including non-successful responses such as 429; this is not successful chunk count or wire bytes.
- quic_test.go uses a real bidirectional lossy UDP relay, checks exact payload, identity isolation, bearer auth, cancellation, and no TCP fallback.
- scripts/benchmark-quic.py compares released TCP, candidate TCP, and candidate QUIC with cold OS caches.

Initial/max stream windows are 2/32 MiB and connection windows 8/128 MiB, per connection rather than per-node hard memory limits. Worker chunk buffers are additional. Incoming bidirectional streams are capped at 64, handshake idle timeout is 3s, QUIC idle timeout 45s. No 0-RTT or unreliable datagrams. Supported GSO/batching is handled by quic-go; no custom FASP rate algorithm is implemented.

Build requires Go 1.26+ (tested toolchain 1.27.4) and pinned pure-Go modules. Runtime remains a standalone static CGO_ENABLED=0 executable for Linux x86_64/aarch64; dependency licenses are in THIRD_PARTY_NOTICES.txt. ARM64 is cross-built only.

## Verification and limits

Race unit tests and go vet pass. A real race-instrumented three-process chain passes interrupted/corrupt-partial recovery and forwarding with `--transport quic --verify-window-chunks 4 --endgame --adaptive --min-workers 2 --max-workers 8`. Both modes pass two-node real SSH deployment, full hashes, default-window propagation, and HTTP/3 response evidence.

Benchmark methods use identical worker/upload counts and integrity requirements, fresh directories, per-compute-node sync/drop_caches barriers, continuous timing including retries/deployment/shutdown, and prepared common seed staging outside timing.

This is a complete HTTP/3+QUIC+TLS versus plaintext HTTP/TCP comparison, not an isolated causal test of UDP. Encryption, userspace protocol work, packet processing, GSO, socket buffers, and TCP offloads may affect results. WAN conditions were not simulated; unit-test packet loss is not cluster-scale loss validation.

References: [client](https://quic-go.net/docs/http3/client/), [server](https://quic-go.net/docs/http3/server/), [GSO/UDP buffers](https://quic-go.net/docs/quic/optimizations/).


[Complete 45-node results](QUIC-RESULTS.en.md)
