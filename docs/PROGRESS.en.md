# Preparation progress, estimates and cancellation

[中文（主文档）](PROGRESS.zh-CN.md) · [Examples](EXAMPLES.en.md) · [Documentation](README.en.md)

Preparation reports the current action, completed bytes or nodes, elapsed time and a rate/ETA when measurable. No extra flags are required. The path shown during source hashing is the current file; totals cover the entire selected dataset.

~~~text
PREPARING hashing-source  32.3% | 156.40 / 483.84 GiB | 1126.4 MiB/s | ETA ~4m58s | elapsed 2m22s | /shared/local-ai.tar
PREPARING deploying | nodes 3/9 | elapsed 4s | deploying executables and job configuration over SSH
PREPARING checking-existing | nodes 5/9 | elapsed 20s | verifying existing files on each node
PREPARING importing  40.0% | 193.54 / 483.84 GiB | 980.0 MiB/s | ETA ~5m3s | elapsed 3m22s | /shared/local-ai.tar
PREPARING starting-peers | nodes 8/9 | elapsed 12s | starting peers
~~~

These numbers illustrate formatting, not promised performance. Hashing and initial seed streaming report aggregate content bytes, percent, rate, estimated remaining time and current file about every two seconds. Short stages report their start/end too. Directory payload excludes tar headers.

Deployment, existing-file checks, peer startup and listener readiness report action, node counts and elapsed time. Uncached large existing files still require disk reads. These stages do not invent a byte ETA without suitable measurements.

Initially rate/ETA say calculating. ETA uses observed average throughput for the **current phase**, excluding later deployment, verification, fsync and transfer work. After all bytes have been processed, unfinished phase work displays finalizing (time unknown), rather than falsely declaring the job finished.

## Monitor

~~~bash
./swarmcopy jobs
./swarmcopy watch --job <ID> --plain
./swarmcopy status --job <ID>
~~~

Preparation views read the controller's progress.json instead of probing unopened peer ports. Watch reloads job state on every refresh, enters normal global peer monitoring after preparation, and uses recorded results after normal peer shutdown.

During preparation, status returns Stage, Preparation and Complete=false. Preparation progress is not proof of whole-job completion. Controller host/PID/start identity and a fresh attempt ID prevent stale execution progress from being reused. A locally exited controller displays controller-stopped.

Replacing a binary does not change an already running process. Restart/resume uses the installed executable. Old records without telemetry/process identity can show recorded phase only, not prove liveness.

## Stop and resume

While the controller hashes, press Ctrl+C, or run:

~~~bash
./swarmcopy stop --job <ID>
./swarmcopy resume --job <ID>
~~~

Source reads check cancellation between buffers; directory snapshot checks also respect context. Source-hash stop requests are local, without SSH to not-yet-deployed nodes. Remote inventory monitors its input heartbeat and cancels reads after the controller stream closes.

Cancellation is normally prompt on local disks but cannot guarantee interrupting an already blocked kernel/filesystem read. Source hashing and incomplete initial SSH import are not chunk-resumable; those incomplete phases restart. Verified P2P partials remain.

Ctrl+C stops the controller; previously deployed detached peers may continue. Use stop --job to stop them explicitly. Exiting watch stops only the monitor. Avoid global pkill -9 swarmcopy, which also kills monitors/unrelated jobs and prevents normal exit-state recording.

## Implementation and historical validation

An independent 64GiB sparse-file process test observed byte/rate/ETA output, no peer probing by watch/status, SIGINT exit in about 0.007s and stop-command exit in about 0.35s, without deployment. A separate 64GiB inventory process exited about 0.005s after its input stream closed. These are observations, not latency bounds or media-throughput benchmarks.

A real two-node TCP job with 139,460,641 bytes and five files passed all preparation stages, SSH heartbeat checks, listener readiness, complete SHA256 and recorded-result monitoring. The user's original local-ai job was not restarted. x86_64 was tested on hardware; aarch64 is cross-built.

progress.go owns telemetry, estimates, cancellation and phase views. progress.json uses atomic replacement without per-heartbeat fsync; durable job/manifest records retain their original rules. Telemetry write failure is reported but cannot certify unverified content. Unit/race tests cover ETA, cancellation, stop requests, attempt isolation, process identity, no peer probes and payload-only counting.

Ready peers transfer independently without an all-listener barrier. A current-file path during hashing-source is not the entire source selection; totals cover all frozen inputs. Old waiting-peers output is not proof that a node died: consult process/log/node diagnostics. Completion summaries and cleanup are described in [examples](EXAMPLES.en.md) and [cleanup](CLEANUP.en.md).

## Background verification of existing replicas

When the source node is a target (SOURCE MODE: direct), startup inventory checks metadata and valid content-verification caches. A same-name, same-size file without a trusted cache remains unverified. Peers open their status endpoint before background local verification; each verified chunk becomes available to other peers immediately. Local downloads start after initialization. A slow disk no longer forces all targets to wait for a full inventory pass. Staged mode retains full inventory to find a complete seed and avoid unnecessary SSH import.

VERIFYING LOCAL in watch means local verification is active and proven chunks can seed; it does not mean offline or complete. Fully verified files get incremental cache checkpoints that survive cancellation. Names and sizes never establish availability. Same-size conflicts may be discovered later. The default still prompts; unattended jobs should specify --existing resume, replace or keep. A chosen policy also applies to later conflicts. Originals remain until verified replacements publish.
