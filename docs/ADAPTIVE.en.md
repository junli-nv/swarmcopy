# Adaptive transfer experiment

[45-node two-round measurements](ADAPTIVE-INITIAL-RESULTS.en.md)

[中文](ADAPTIVE.zh-CN.md) · [Development](DEVELOPMENT.en.md)

This 0.2.0-dev transport prototype borrows resource-feedback ideas from Aspera while retaining SCDP/1 over TCP, P2P forwarding, and SHA-256. It does not implement FASP or UDP. New scheduling features are opt-in until evidence supports a production default.

## Usage

~~~bash
./swarmcopy distribute \
  --hosts 'node[001-005]' --source /shared/nemo.sqsh \
  --dest /raid/images --bin-dir ./dist \
  --adaptive --workers 4 --max-workers 16 --endgame --hedge-delay 2s
~~~

--adaptive enables concurrency probing, measured source selection, and source cooldown. --workers is fixed or initial concurrency (default 4); --max-workers is the adaptive cap (default 16, at most 32 and no smaller than the initial value). --endgame independently enables delayed final-chunk hedges; --hedge-delay defaults to 2s, with learned source duration also considered.

Omit --adaptive and --endgame for fixed scheduling. New jobs persist the options and resume reuses them; old jobs leave them disabled. Live tuning of an existing job is not exposed. Default bandwidth remains unlimited; explicit upload/download limits still apply. Ordinary binaries are deployed automatically. No runtime Python, pdsh, or Go dependency is added; race builds remain development-only.

## Concurrency feedback

Every two seconds the node measures application-read HTTP payload throughput. It probes one additional slot, rolling back if the gain is below roughly 5%, then cooling down for two intervals. A completed-work write-call share above 35% also removes one slot. The target remains between one and the configured cap.

This is a heuristic application scheduler, not FASP packet-level congestion control or a replacement for TCP control. WriteAt may complete into page cache: call latency is not physical RAID throughput and may reveal writeback pressure only when writes block. Final fsync is measured separately. Background I/O and network queue causes are not fully classified.

Reducing the target drains existing work without canceling ordinary requests. Worker buffers are allocated lazily and retained until exit; the cap times chunk size bounds their approximate capacity. At defaults this is up to 16×16 MiB of download buffers, plus HTTP/state/runtime memory.

## Sources and endgame

Rarest-first selection is preserved. Source ranking considers an EWMA of measured request network duration, local outstanding work, remote upload-slot occupancy, and a preference for forwarding peers over the original seed. Each receiver uses at most two concurrent chunks from one source. Busy/failed sources retain bounded cooldown across status refreshes. Unknown sources start with a neutral estimate. There is no rack topology model or guarantee of globally optimal choices.

When few chunks remain and no ordinary chunk can be scheduled, an overdue request may be hedged to a different source. At most two requests exist for one chunk, within the node-wide target. Every copy is hashed before commit. A per-chunk lock allows exactly one write and one unique-byte accounting update, followed by cancellation of the losing requests. A corrupt hedge does not cancel a good original. Different chunks retain parallel writes.

Whole-file SHA-256, fsync, atomic publication, and partial rehash-on-resume remain mandatory. Local write failures terminate with a retained partial rather than blaming the source.

## Diagnostics

watch/distribute show active/target workers, sync/hash/publication phases, retry/hedge/cancel counts, application receive bytes, and maximum per-node phase times. status and saved result.json add an optional Performance object; legacy peers without it remain readable.

| Fields | Meaning |
|---|---|
| Stage | transfer, sync, final-hash, publish, complete, error |
| ActiveDownloads / TargetDownloads / MaxDownloads | Outstanding work, current admission target, configured cap |
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

The baseline/fixed/adaptive comparison uses fresh directories and sync/drop_caches before each method, with time through shutdown. Two reversed rounds provide only two observations, not a universal guarantee. Directories and multiple-file support remain future work outside this transport prototype.

## References

- [IBM FASP deployment conditions](https://www.ibm.com/docs/en/connect-direct/6.2.0?topic=v62-using-fasp)
- [IBM transfer rate controls](https://www.ibm.com/docs/en/ahts/4.4.x?topic=csfcl-asperaconf-transfer-configuration-2)
- [Aspera storage feedback](https://download.asperasoft.com/download/docs/csrv/3.2.1/linux/html/fasp/conf-file_system-cmd.html)

