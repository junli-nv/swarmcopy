# v0.7.0: scheduling, native local copies and completion accounting

[中文（主文档）](OPTIMIZATION-0.7.0.zh-CN.md) · [Examples](EXAMPLES.en.md) · [Development](DEVELOPMENT.en.md) · [Measurement data](optimization-0.7.0-results.json)

## Result and scope

With nine nodes each receiving a fresh 32 GiB destination, fixed concurrency and unlimited TCP, the balanced comparison reduced mean distribution phase time from 32.39 to 25.10 seconds (**22.50%**). External controller wall time, including deployment and finishing, fell from 39.68 to 31.61 seconds (**20.34%**). The source-host destination now reads and verifies local source data instead of retrieving it through the network. Adaptive concurrency remains off; TCP remains the default.

These are combined changes on this cluster, not isolated attribution or a guarantee for a 484 GiB file or different node counts. Initial source-manifest hashing still reads the source; unchanged, fingerprint-bound source manifests can be reused.

## Diagnosis and implementation contracts

The previously completed approximately 484 GiB chunks job had zero FileHashBytes and FinalHashSeconds on every receiver. Its remaining distribution time cannot be attributed to final whole-file hashing. Source inspection identified expensive full missing-chunk/source scoring under the global lock, plus unnecessary source-host network reception. Concurrent cumulative work seconds are not serial phase durations. No contemporaneous disk/NIC utilization measurements establish either as saturated.

- Each range selection scores at most 255 missing candidates, using a random start and coprime stride across the entire range. Stop early at one eligible source. If sampling finds nothing, inspect eligible source availability bitmaps and admit one missing chunk, avoiding idle workers with sparse availability. At most 256 scored candidates are recorded, but cheap bitmap traversal can still scan the whole range; total scheduling is not guaranteed constant time.
- This is an approximate rarest-first heuristic, not an exact global minimum. Keep source concurrency bounds, stale-source filtering, pipeline-window preference, endgame and retry behavior. SchedulerCalls/Candidates/WorkSeconds expose the cost for future measurement.
- Only the source host included among targets receives native read-only source configuration. Its peer copies into destination staging through the same chunk verification/write path. Other targets continue exchanging verified blocks over TCP/QUIC. Preserve full-mode final verification, fsync and atomic publication. Size alone never proves completeness, and unverified reflinks/hardlinks are not substituted. Source changes and local I/O failures are local errors.
- Cache the immutable manifest ID at node construction for status/discovery; preserve SCDP/1 and old manifest identities.
- Downloaded/ReceivedBytes count network data; LocalCopied/LocalCopiedBytes and From[local-source] count native copies separately. Global progress still counts verified destination bytes. Download limits also pace native source reads.
- Agent reuse checks the started binary version. An upgrade restarts old peers while preserving normal validation/reuse of their partial data and caches.

## Completion summaries

Successful runs print JOB SUMMARY and save Summary in result.json: this invocation's elapsed/preparation/distribution-verification-finish durations, completed targets, network/local/reused bytes, aggregate average rates and median receiving-node network average.

Aggregate averages divide newly received/copied bytes by this invocation's **total controller elapsed time, including preparation**. Live peers may keep transferring during resume preparation, so both sides of the ratio must cover that window. Aggregate rate is across targets, not one node or NIC line rate. The median uses each receiving node's active transfer duration and excludes nodes with no new network bytes.

On resume, subtract initial counters only for the same peer instance. New instances use new counters; verified old data appears as reused bytes, not new traffic. Observation is not an atomic cluster snapshot; controller stage boundaries can overlap a little with early-starting peers. Old records without Summary retain their existing output without invented historical timing. Completed `watch --job ID` displays the text summary; completed `status --job ID` retains JSON with Summary.

## Controlled comparison

2026-11-16; same nine x86_64 nodes; one fixed read-only 32 GiB source; chunks/BLAKE3; TCP; workers/uploads=4/4; no rate limit; adaptive=false. Every round asserted a never-existing destination on all nodes, then sync and drop_caches=3 on all nine distinct machines. Source manifests were generated beforehand and validated cache reuse was required for each timed round. In-transfer retries remain included. No resumed data was timed. All nine final files were independently SHA256 checked after each round, outside transfer timing.

| Order | Build | Slowest node phase (s) | Controller wall (s) |
| --- | --- | ---: | ---: |
| baseline-2 | 0.6.0 | 31.865 | 38.730 |
| candidate-final-1 | 0.7.0 transfer build | 24.429 | 30.448 |
| candidate-final-2 | same | 25.766 | 32.767 |
| baseline-final-2 | 0.6.0 | 32.905 | 40.625 |
| candidate-release-1, additional artifact check | final 0.7.0 | 25.223 | 32.723 |

Phase = maximum across nodes of each node's TransferSeconds + FinalSyncSeconds + PublishSeconds. It excludes initial manifest construction. Controller wall uses an external monotonic clock and includes SSH deployment/start/finish, but excludes fixture construction and independent post-run hashing. Both versions create the same nine destination copies. Baseline network counters total 288 GiB including local reception/loopback; candidate totals 256 GiB network plus 32 GiB native copy. This deliberate behavior improvement is part of the result, not evidence that the network protocol alone became 22.5% faster.

Only completion accounting/display changed after the balanced transfer build. The exact release binary passed an additional fresh round. JSON preserves build hashes, exploratory rounds and independent check counts; distinct candidate variants are not pooled into final means and the fastest individual round is not the reported gain. Clearing page cache and reusing a validated source manifest are separate controls; cold page cache does not mean rebuilding a manifest each round.

## Validation and maintenance

Passed Go race tests, vet, English CLI help checks, a real process-chain interruption/corruption repair test, three-node QUIC/full with full-file digest and zero local network reception, interruption of partially completed native copy and same-ID resume, and saved status/watch summaries. Real-machine outputs were independently SHA256 checked. Linux x86_64 ran on hardware; aarch64 is a static cross-build without ARM hardware validation in this study.

To reproduce, freeze both binaries and the source; record its independent digest; prepare and validate the source-manifest cache; run B-A-A-B serially with fresh destinations and per-round cache drops. Preserve every success/failure and retry, independently hash complete outputs, and keep CPU microbenchmarks out of timed transfers. Examples show fixed concurrency. Future changes should use scheduler cost, lock contention and concurrent disk/NIC observations before changing batched I/O or concurrency. Adaptive mode stays optional rather than hiding lock-bound work.
