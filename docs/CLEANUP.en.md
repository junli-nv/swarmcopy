# Unified metadata and automatic cleanup (v0.8.0)

[中文（主文档）](CLEANUP.zh-CN.md) · [Examples](EXAMPLES.en.md) · [Development](DEVELOPMENT.en.md)

Use one `.swarmcopy` under each destination. After every target passes its selected integrity mode and publication, the default controller stops owned peers, removes this job's deployment and exhausted staging, and saves completion statistics. Media/partials are not migrated to the user's home directory. Source and published files stay in place; controller jobs/status/watch records remain.

After a normal successful transfer, reusable metadata has fixed names:

```text
/raid/images/
  nemo.sqsh
  .swarmcopy/
    cache.json       # latest manifest's verified file fingerprints, when available
    last-peer.log    # latest peer log, at most 8 MiB, replaces previous log
```

This avoids permanent per-job or per-data-file cache/log accumulation. Cache acceptance requires the complete manifest ID and matching device/inode/size/mtime/ctime. Alternating manifests may require another file scan. Chunks-only downloads do not mint full-file verification caches; independently checked existing files and full-mode outputs can be cached.

Running/interrupted jobs retain deployment in `.swarmcopy/<job-id>` and same-filesystem partials in `.swarmcopy/trees/<manifest-id>/data`. Isolation is needed during operation/recovery, then discarded after success. Existing controller state and source-manifest caches remain where they were; no destination media is copied there. Persistent lock files retain their concurrency semantics and are not unlinked merely because one process exits.

## Explicit cleanup and retries

```bash
./swarmcopy jobs
./swarmcopy cleanup --job <ID>
./swarmcopy cleanup --help
```

Cleanup requires a completed saved result. It stops owned seeding peers and cleans deployment/empty staging while keeping media and controller history. It differs from remove/delete, which removes job records. An already successful cleanup returns without another SSH deployment.

Normal completion cleans automatically. `--keep-seeding` deliberately retains running peers and their files until explicit cleanup. Failure to stop a peer prevents automatic deletion. Cleanup failures print CLEANUP WARNING and persist per-node CleanupIssues in job.json; verified transfer completion remains recorded and cleanup can be retried.

Never automatically discard incomplete/error/fsync-failed partial data, other jobs' datasets, live locks, source files, published media or unrelated files. Same-manifest references retain shared staging and print KEEP SHARED STAGING. Symlinks, unexpected files or uncertain ownership keep data and report the reason.

Old `.swarmcopy-trees/<id>` is read/resumed in place, without bulk migration. Only the current completed manifest's empty staging is removed; old parent directories disappear only when empty. A legacy full-verification cache can be retained in fixed cache.json. Shared/incomplete legacy datasets remain. Cleanup does not recursively search the whole RAID disk for deletion candidates.

## Maintenance and validation

`treeNamespace` recognizes legacy staging; new staging/imports use the unified directory. Cache writes use a temporary file, fsync, atomic replacement and directory fsync. `cleanupPeerMode` verifies configuration ownership, agent/peer locks and process identity/start time. `completedTreeCleanup` accepts only empty data directory structure and known completion/cache metadata; any retained data file prevents deletion. Explicit remove --purge-partials remains a separate discard operation.

`cleanupCompletedNodes` runs after all targets finish and peers stop. Failures are independently recorded rather than reported as content-verification failures. The cleanup CLI holds the controller lock and supports stored SSH settings/shared password authentication. Completion elapsed time includes automatic cleanup; cleaning a historical job does not fabricate a new transfer summary.

Validated race/vet/CLI, published-file/cache preservation, active locks, shared/incomplete data, symlink rejection, bounded cache replacement and 8 MiB logs, plus legacy in-place recovery. Hardware checks cover staged chunks/full, three-node identical source/destination and QUIC, keep-seeding then explicit cleanup, and 0.7.1 interruption followed by 0.8.0 resume. Outputs passed independent SHA256 and completed watch/status/stop checks. Linux x86_64 ran on hardware; aarch64 is cross-built only.
