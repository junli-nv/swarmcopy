# Development guide

[中文（主文档）](DEVELOPMENT.zh-CN.md) · [Usage](../README.en.md) · [Validation](VALIDATION.en.md)

This describes the actual v0.1.0 implementation. Keep both language versions, CLI help, and tests synchronized when changing behavior.

## Source map

| File | Responsibility |
| --- | --- |
| adaptive.go / transfer.go | Adaptive scheduler, hedge deduplication, and phase diagnostics; see [experiment](ADAPTIVE.en.md) |
| verify.go / hashbench.go | Ordered-prefix verifier, cancellation gates, and CPU/file diagnostics; see [design](PIPELINE.en.md) |
| main.go | Manifest, prepare, peer, HTTP chunks, validation, scheduling, dispatch |
| cluster.go | Host expansion, SSH deployment, persistent jobs, distribute/resume/stop |
| statefiles.go | Atomic state-file replacement and file/parent fsync |
| agent.go | Owned peer startup/reuse, PID identity checks, controlled stop |
| auth_linux.go | Hidden password input and in-memory Unix-socket askpass |
| parallel.go | Bounded concurrent SSH deployment and stop |
| verified_linux.go | Verified final-file cache with inode/mtime/ctime invalidation |
| watch.go | Bounded monitoring concurrency, rates, ETA, global terminal display |
| help.go | English help, default job selection, recorded completion display |
| *_test.go | Integrity, scheduling, host parsing, monitoring, local job tests |
| scripts/integration.py | Three-process chain, interruption, corruption repair |
| scripts/cli-smoke.py | CLI/help/default-selection contracts without SSH |
| scripts/build.sh | Static x86_64/aarch64 release builds |

Runtime needs OpenSSH and basic remote utilities. Go and Python are development/test tools only.

## Control and data planes

The controller uploads executables, a task token, the manifest, and configuration through SSH. The same executable's internal agent command launches a detached peer with setsid and writes logs into the task directory. It does not install a system service. uname -m selects the correct architecture.

Peers exchange chunks over HTTP/TCP, while monitors query small JSON status messages. There is no tracker, database, queue, public discovery, or NFS fallback. Initial staging uses SSH; peer data stays between compute nodes.

Recorded job states: created → starting → importing (new source only) → running → complete. Failures record interrupted. stop creates stop.request and records stopped/stop_failed after attempting all owned peers. This record is not consensus or a live heartbeat and can become stale after controller failure.

Peer lifecycle: acquire destination lock → open/create file → scan existing partial chunks or validate a final-file cache (skip new sparse partials) → listen/discover → download and upload → fsync → whole-file SHA-256 → atomic publication → continue seeding. The default controller stops peers after every target completes; direct peer mode ends on a signal or lifetime.

## Manifest and persistence formats

JSON keys come from exported Go fields:

~~~json
{"Version":1,"Name":"nemo.sqsh","Size":123,"ChunkSize":16777216,"SHA256":"<64 hex>","Chunks":["<64 hex>"]}
~~~

This illustrates fields, not a valid manifest. Manifest.Version is 1. Chunk sizes must be 64 KiB..64 MiB; the final chunk can be shorter. Size and chunk count must agree. Empty files, path-like Name values, malformed hashes, and excessive metadata are rejected.

Manifest ID is SHA-256 of Go json.Marshal(Manifest), not the original JSON file bytes. Cross-language implementations must reproduce field order, names, numeric representation, and Go JSON escaping. Arbitrary JSON key reordering does not preserve this identity.

Partials are named <final>.<first-16-ID-hex>.part. Different manifests do not reuse a partial filename. A sidecar .swarmcopy.lock uses flock to serialize writers. File descriptors are close-on-exec, and target/partial/lock opens use O_NOFOLLOW.

Controller directories contain job.json, job.token, manifest.json, controller.lock, result.json when completed, and optional stop.request. Durations are JSON integer nanoseconds. job.json is updated using a temporary file, fsync, and rename. result.json retains completion evidence. Treat the directory/token as private credentials.

Remote directories are <dest>/.swarmcopy/<ID>, containing executable, token, manifest, peer.json, process.json, agent.lock, and peer.log. Media and partials live beside their final destination. Old directories are not automatically garbage-collected.

## SCDP/1 wire protocol

SCDP means SwarmCopy Distribution Protocol. Application version 0.1.0, protocol SCDP/1, and Manifest.Version=1 are independent version domains.

Requests and responses carry X-SCDP-Version: 1; /status includes Protocol: "SCDP/1". Explicitly unsupported versions are rejected. Missing version markers from early development builds are temporarily accepted, including historical completion records. A future major protocol must define its migration policy explicitly.

Every request requires Authorization: Bearer <job-token>, compared in constant time. Only GET is accepted. Clients reject redirects and do not use environment HTTP proxies.

| Endpoint | Success | Failures |
| --- | --- | --- |
| /status | 200, Status JSON | 401 authentication, 405 method |
| /chunk?i=N | 200, exact Content-Length, raw bytes | 400 index/version, 404 unavailable, 429 upload capacity |

Status fields include Protocol, ID, Version (application version), Size, VerifiedBytes, Have (boolean array), Complete, Origin, Downloaded, Uploaded, From, Error, and Recorded. Recorded is used by the controller for historical display and is false in live peers.

Have is published only after hash verification and a successful WriteAt. Complete additionally requires whole-file SHA-256 and publication. Downloaded/From count valid accepted chunks during this process lifetime; Uploaded counts bytes written to HTTP responses. They are not NIC wire counters and do not persist across peer restart. Origin means the final file existed at startup.

The data plane has no TLS, end-to-end peer identities, or Byzantine protection. Token holders can read that task's file. Hashes detect damaged data, not the authenticity of an untrusted manifest. Manifests/tokens are supplied through the trusted SSH control plane.

## Scheduling, concurrency, and memory

Peers discover a fixed neighbor list by querying status approximately every second, rejecting incompatible manifest IDs or chunk counts and expiring stale snapshots. Peer discovery currently fans out across the neighbor list.

Missing-piece scans start at a randomized offset and prefer rare pieces. For a selected piece, less busy non-Origin sources are preferred. Each downloading node allows at most two outstanding requests per source. There is no global piece allocator; startup duplicates remain possible. HTTP 429, network, length, and checksum errors are retried.

Node.mu protects Have, busy flags, remote snapshots, source concurrency, From, and completion state. Transfer counters are atomic. Upload slots are bounded channels with 256 KiB response buffers. Each download worker reuses one chunk buffer, normally about 4×16 MiB raw buffers plus GC/state overhead. Limits default to zero/unlimited; when configured, a per-node aggregate pacer reserves time in 256 KiB increments.

New partials are created using O_EXCL and start with no advertised valid chunks, avoiding an unnecessary scan of sparse zero-filled space. Existing partials are always rehashed. Worker buffers are reused rather than allocated for every chunk.

Monitoring permits at most 16 concurrent status requests with per-request deadlines. Its denominator includes all requested targets. Offline nodes contribute the last confirmed bytes or zero and never count as live completion. Recorded results are explicitly labeled. Rates use counter deltas and clamp restarts to zero; ETA excludes final full-file hashing.

## Integrity and recovery

Per chunk: check exact length → SHA-256 → WriteAt → advertise Have. Valid dirty pages may be served before per-chunk fsync. Recovery always rereads and hashes the partial file, so crash consistency does not depend on a bitmap being persisted first.

Completion: fsync data → sequential whole-file SHA-256 → set final permissions/fsync → same-directory hard-link publication (fails if final exists) → unlink partial name → fsync parent directory. This gives same-filesystem atomic visibility/no-overwrite, not a transaction across data and manifest files. Published media must remain immutable. Corrupt existing final files are refused.

Resume retains the original manifest/token/partials. Matching live peers are reused using PID, /proc start ticks, executable argv, and token-file path. Other peers restart and rehash partial data; unchanged cached final files can reuse previous verification unless --recheck is requested. Old bitmap data is not trusted.

SIGINT of the controller does not delete files or stop all detached peers. stop records a stop request and sends SIGTERM only to matching processes; it does not escalate automatically to SIGKILL. Persistent lock filenames are normal: the kernel lock, not file existence, determines ownership.

## Tests and contribution workflow

~~~bash
go test -race ./...
go vet ./...
go build -o swarmcopy .
python3 scripts/cli-smoke.py
python3 scripts/integration.py
bash scripts/build.sh
~~~

Unit tests cover corrupt-download rejection, damaged partial revalidation, no-overwrite publication, chunk authentication/availability, manifest validation, source selection, host ranges, mixed-manifest monitoring, restart-safe rates, and local job choice.

The integration test uses an A→B→C chain, interrupts B while incomplete, modifies its .part, restarts it, checks final SHA-256 on all nodes, and verifies B's upload contribution. CLI tests exercise root help, every public command's --help, help COMMAND, missing arguments, and empty job state.

Protocol changes must update compatibility/version policy, bilingual docs, and corruption/recovery tests. CLI changes must test default invocation, help, invalid input, empty records, and completed records. Process/locking changes must preserve other jobs, particularly with PID reuse or duplicate resume attempts.

## Limits and prioritized roadmap

1. Initial import has no chunk recovery and data/manifest publication has a crash window. Add recoverable staging sessions and explicit publication ordering.
2. Add TLS/mTLS, credential rotation, and appropriate tests before crossing untrusted networks.
3. Recovery scans the entire partial; space is not preallocated. Consider validated persistent indexes, fallocate/space preflight, and hashing pipelines without blindly trusting old bitmaps.
4. Full neighbor discovery is O(N²) control traffic. SSH deployment/stop has bounded concurrency (default eight); further work includes limited neighbor sets, hierarchy, and topology awareness. 1024 hosts is only an input ceiling.
5. Pacing is software throttling, not strict QoS. Profile CPU, disk, GC, and uplinks before optimizing buffers and I/O scheduling.
6. Local write failures currently retry until progress timeout; distinguish permanent disk errors for fail-fast behavior. Startup/port failures are primarily diagnosed through offline state and peer.log.
7. Saved job state is not a heartbeat. Controller resume currently requires SSH to every listed target even though already-running peers can exchange data without the controller.
8. Native RDMA, IPv6 host expansion in the controller, SSH-alias-to-data-IP maps, multi-media caching/deduplication, rack awareness, dynamic target addition, and Prometheus/browser dashboards are not implemented.
9. ARM64 cross-builds are checked but this run has no ARM target test. End-to-end --ask-pass login on actual password-authenticated targets also needs separate validation; the five-node run uses existing public-key authentication.
10. Directories, multiple inputs, and glob matching are explicitly deferred to the next release. Define relative paths, per-file verification, conflicts, and crash-safe multi-file publication before implementation. v0.1.0 accepts a single file.


## Verification cache and forced recheck

A fully verified final file gets a .swarmcopy.verified.json sidecar binding its manifest ID, device, inode, size, mtime, and ctime. The previous verification result is reused only when all fields match. Fresh sparse partials are not scanned; existing .part files are always rehashed, and newly received files still require whole-file SHA-256.

This cache assumes immutable files and correct OS write metadata. It does not replace periodic full verification for silent disk corruption. Use --recheck to bypass final-file caches:

~~~bash
./swarmcopy distribute --hosts 'node[001-005]' --seed node001 --seed-file /raid/images/nemo.sqsh --dest /raid/images --recheck
./swarmcopy resume --job <ID> --recheck
~~~

New prepare imports create the cache after verification. Legacy media is verified once on first use. Do not copy a cache from another file. SSH startup/stop uses at most eight concurrent operations by default; --fanout configures new jobs.

## Find and stop processes manually

~~~bash
pgrep -af swarmcopy
kill -TERM <confirmed-PID>
~~~

Prefer stop --job for an entire task. Killing the controller does not automatically terminate detached peers. Node PID records/logs are under <dest>/.swarmcopy/<ID>/. Avoid global pkill that could stop unrelated jobs. SIGKILL is a last resort; recovery revalidates retained partials.

This release accepts a single file. Directories, multiple inputs, and file globs are deferred to the next release. Concurrent jobs need different ports and independent destinations; a destination is protected by an exclusive lock.
