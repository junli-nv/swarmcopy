# Source modes and node failures

[中文（主文档）](SOURCE-MODES.zh-CN.md) · [README](../README.en.md)

From v0.7.0, the local target uses native source reads, chunk checks and writes rather than TCP/QUIC reception; watch separates Local source copied from network DL.

[Optimization report](OPTIMIZATION-0.7.0.en.md)

## Recommended: seed directly from a compute node

When media already resides on a compute node, run there and include that host among the targets. The program automatically serves the original files without first copying a complete seed. On HGX167:

~~~bash
./swarmcopy distribute --hosts 'HGX[061,066,082,138,169,167]' \
  --source /raid/local-ai/dist/local-ai.tar --dest /raid/images --verify chunks
~~~

Before large-file reads it prints SOURCE MODE: direct, source host/path and destination, then SOURCE READY with the serving endpoint. Original files are read-only and must remain unchanged. If the local destination differs, that target still receives a copy concurrently with remote targets. Identical existing destinations are reused.

The first trusted source manifest still requires a complete source read. Direct mode removes initial seed copying, not initial source validation, and is not a promise of multi-GB/s throughput.

## Head-node controller: staged compute seed

If the controller is not among the targets, it copies the source through SSH to a compute seed before compute-node P2P:

~~~bash
./swarmcopy distribute --hosts 'HGX[061,066,082]' \
  --source /home/cmsupport/workspace/nemo-26.08.00.sqsh \
  --dest /raid/images --verify chunks
~~~

SOURCE MODE: staged identifies both ends. Importing means this seed copy, not unpacking an archive or importing Docker/Enroot. The stage shows bytes/rate/ETA; 100% sent can still wait for seed fsync, manifest comparison and EOF confirmation. An unavailable seed before deployment can be replaced by a reachable target, reported as SEED FAILOVER. Losing the only data source during initial copying still requires recovery. Initial SSH seed copying is not chunk-resumable.

## Boundaries

--hosts lists receiving destinations. Automatic direct mode requires the current host in that list; matching ignores case, domain suffix and SSH username. Arbitrary aliases are not inferred; use the real hostname. Both controller output and watch retain the selected mode.

The direct source runs in the controller with a separate dynamic TCP port, plus matching UDP for QUIC. Receivers must resolve and reach SOURCE READY, not just peer port 19700. Keep the controller running while that source is needed. Exiting the controller stops original-file serving. Resume on the same source host with saved job records and original paths. No permanent agent is introduced.

The controller still needs SSH/HTTP access to every target. If BCM reaches only one compute node, manually stage the media and log in to that node to run distribute. This places the controller inside the compute network; no remote-controller handoff or tunnel management layer was added.

## Failure isolation

Monitoring no longer waits for every peer listener. A ready peer transfers independently. One host's SSH/deployment/inventory/start failure does not cancel healthy hosts.

Errors name the host and stage and persist in job.json. STARTING means the recorded peer process is alive; recent logs and their path are shown. FAILED means it exited; failed SSH diagnostics show UNREACHABLE. Logs remain untrusted diagnostic data.

Status requests are bounded. Peers avoid unavailable sources through discovery/retry while other sources have the needed chunks. Failed targets remain in the total and cannot count as completed. Healthy completed peers keep seeding until all targets finish or the controller stops/expires. A recovered running peer can reappear; failed deployment is not automatically relaunched. Use the same job ID after recovery:

~~~bash
./swarmcopy jobs
./swarmcopy watch --job <ID>
./swarmcopy inspect --job <ID>
./swarmcopy resume --job <ID>
~~~

Each SSH/deployment/inventory attempt defaults to one minute; --node-timeout 5m increases it. Timed-out inventory is unknown, not complete; a started peer can perform its own checks and show STARTING. Global unreadable source/no deployable target/unique-seed loss can still prevent progress.

From v0.7.1, new source jobs default to chunks/BLAKE3; explicit --verify full selects SHA256/SHA256. Saved manifests retain their algorithms.

## Validation

A 139,460,641-byte file/tree fixture passed staged, direct and QUIC direct tests with independent SHA256 checks on both targets. An unavailable initial seed did not cancel two healthy targets. In a three-node HGX061/HGX066/HGX082 test, HGX066 was stopped mid-transfer; both siblings finished, failure details named it, and same-ID resume reused partials and passed independent SHA256 on all three. Race/vet/CLI and source-change tests passed.

These are functional checks, not a new 483.84-GiB or hundred-node speed study. aarch64 is statically cross-built; physical ARM validation was not performed.


## Removal and sparse-file recovery

```bash
./swarmcopy remove --job <ID>
./swarmcopy delete --job <ID> --purge-partials
```

Removal verifies controller PID/start-time, holds the job lock and stops owned peers. Default removal deletes records and remote job directories; --purge-partials additionally removes partial datasets without other job-manifest references. Sources/published files remain. Failures retain the local record for retry. Removed IDs cannot be resumed.

Sparse recovery uses SEEK_DATA to prove unwritten holes, skips their read/hash work and still marks them missing. Empty files do not invalidate spanning chunks. Unsupported filesystems fall back to normal validation. Reopened 1-GiB sparse, retained-chunk reuse and removal-scope tests pass.

A real-RAID reopened 519,520,194,560-byte hole file (30,966 chunks) scanned in 0.153 s and listened in about 0.60 s, with zero verified bytes. This single metadata-scan check is not a transfer benchmark or paired old/new speed ratio. [Record](sparse-recovery-results.json).
