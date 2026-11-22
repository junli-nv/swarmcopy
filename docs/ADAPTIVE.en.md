# Adaptive concurrency and endgame

[45-node two-round measurements](ADAPTIVE-RESULTS.en.md) · [Documentation](README.en.md)

[Initial regression measurements](ADAPTIVE-INITIAL-RESULTS.en.md)

[中文](ADAPTIVE.zh-CN.md) · [Development](DEVELOPMENT.en.md)

Adaptive scheduling is an optional application-level download/source feedback controller in the same executable, off by default. It works with supported TCP/QUIC chunk transports, is not FASP and does not replace their congestion control. Selected chunks/full integrity remains independent; new jobs default to chunks/BLAKE3.

## Usage

~~~bash
./swarmcopy distribute \
  --hosts 'node[001-005]' --source /raid/source/nemo.sqsh \
  --dest /raid/images --bin-dir ./dist \
  --adaptive --workers 4 --max-workers 16 --endgame --hedge-delay 2s
~~~

--adaptive enables concurrency probing, measured source selection, and source cooldown. --workers is fixed or initial concurrency (default 4); --min-workers defaults to 0, meaning the initial concurrency, and can explicitly be set to 1..workers; --max-workers is the adaptive cap (default 16, at most 32 and no smaller than the initial value). --endgame independently enables delayed final-chunk hedges; --hedge-delay defaults to 2s, with learned source duration also considered.

Omit --adaptive and --endgame for fixed scheduling. New jobs persist the options and resume reuses them; old jobs leave them disabled. Live tuning of an existing job is not exposed. Default bandwidth remains unlimited; explicit upload/download limits still apply. Ordinary binaries are deployed automatically. No runtime Python, pdsh, or Go dependency is added; race builds remain development-only.

## Concurrency feedback

Every two seconds the node measures application-read network chunk payload throughput. It probes one additional slot, rolling back if the gain is below roughly 5%, then cooling down for two intervals. A completed-work write-call share above 35% also removes one slot. The target stays between the effective --min-workers floor and configured cap. By default it cannot drop below initial concurrency.

The initial prototype collapsed many receivers to a single worker based on write-work share, causing a measured regression. The corrected default preserves initial concurrency; a lower floor requires an explicit --min-workers choice. See the [retained initial evaluation](ADAPTIVE-INITIAL-RESULTS.en.md).

This is a heuristic application scheduler, not FASP packet-level congestion control or a replacement for TCP/QUIC control. WriteAt may complete into page cache: call latency is not physical RAID throughput and may reveal writeback pressure only when writes block. Final fsync is measured separately. Background I/O and network queue causes are not fully classified.

Reducing the target drains existing work without canceling ordinary requests. Worker buffers are allocated lazily and retained until exit; the cap times chunk size bounds their approximate capacity. At defaults this is up to 16×16 MiB of download buffers, plus HTTP/state/runtime memory.

## Sources and endgame

Chunk selection uses bounded approximate rarity with sparse-source fallback; native source-host copies prefer local reads. With adaptive enabled, source scores use network-duration EWMA, local outstanding work and remote upload occupancy, preferring peer forwarding. Per-source concurrency is at most two per receiver. Cooldowns survive status refreshes; unknown sources start neutral. No rack model or global optimum is promised.

When few chunks remain and no ordinary chunk can be scheduled, an overdue request may be hedged to a different source. At most two requests exist for one chunk, within the node-wide target. Every copy is hashed before commit. A per-chunk lock allows exactly one write and one unique-byte accounting update, followed by cancellation of the losing requests. A corrupt hedge does not cancel a good original. Different chunks retain parallel writes.

Retain selected chunk/full verification, fsync and atomic publication: full additionally checks file contents, while chunks omits final whole-file verification. Local source/write failures retain recovery data and are not remote-source retries. Resume rehashes partials rather than trusting an old in-memory bitmap.

## Diagnostics

watch/distribute show active/target workers, sync/hash/publication phases, retry/hedge/cancel counts, application receive bytes, and maximum per-node phase times. status and saved result.json add an optional Performance object; legacy peers without it remain readable.

| Fields | Meaning |
|---|---|
| Stage | transfer, sync, final-hash, publish, complete, error |
| ActiveDownloads / TargetDownloads / MinDownloads / MaxDownloads | Outstanding work, current admission target, effective floor, configured cap |
| ActiveUploads / UploadLimit | Local upload use and cap |
| ScanSeconds | Initial scan or final-file verification-cache acceptance |
| TransferSeconds | Wall time from post-scan startup to complete chunks, including discovery/waiting |
| FinalSyncSeconds / FinalHashSeconds / PublishSeconds | Explicit final fsync, whole-file hash, publication/cache-record work |
| NetworkWorkSeconds / ChunkHashWorkSeconds / WriteWorkSeconds | Concurrent request work sums, not serial phase durations |
| Requests / Retries / BusyResponses | Data attempts, failed requests requiring recovery, HTTP 429s |
| HedgeRequests / CanceledRequests / WorkerChanges | Hedges, canceled requests, target changes |
| ReceivedBytes / DuplicateBytes | Application-read chunk payload, complete valid already-committed copies |
| SourceCount | Learned source count; full source history is omitted from frequent status responses |

ReceivedBytes includes consumed partial, invalid, and duplicate payload. It excludes headers, unread kernel buffering, and TCP retransmission; it is not NIC wire traffic. Downloaded remains unique committed bytes. Counters are per peer process and restart on relaunch. Saved completion records preserve the terminal snapshot.

Per-node maximum phases can belong to different nodes and overlap. Do not sum them into batch duration; the benchmark controller wall time also includes deployment and stopping peers.

## Validation

Tests cover admission bounds and write feedback, measured-source selection with preserved rarity, persistent busy cooldown, real HTTP hedge cancellation, duplicate commit races, corrupt-hedge recovery, local write errors, and whole-file validation. Unit tests use the race detector; the actual race executable also runs interruption/corruption/forwarding integration tests.

~~~bash
go test -race ./...
go vet ./...
SWARMCOPY_TEST_FLAGS='--adaptive --max-workers 8 --endgame' python3 scripts/integration.py
python3 scripts/benchmark-adaptive.py --help
~~~

The baseline/fixed/adaptive comparison uses fresh directories and sync/drop_caches before each method, with time through shutdown. Two reversed rounds provide only two observations, not a universal guarantee. These historical prototype measurements do not mean that current default chunks, multi-file or native local-copy paths have been benchmarked again under the same conditions.

## References

- [IBM FASP deployment conditions](https://www.ibm.com/docs/en/connect-direct/6.2.0?topic=v62-using-fasp)
- [IBM transfer rate controls](https://www.ibm.com/docs/en/ahts/4.4.x?topic=csfcl-asperaconf-transfer-configuration-2)
- [Aspera storage feedback](https://download.asperasoft.com/download/docs/csrv/3.2.1/linux/html/fasp/conf-file_system-cmd.html)

Current metrics also include LocalCopied/LocalReadWorkSeconds and SchedulerCalls/Candidates/WorkSeconds; local copies are not network traffic. See [development contracts](DEVELOPMENT.en.md).
