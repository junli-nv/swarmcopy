# Multiple files and directories: design and validation

[中文（主文档）](TREES.zh-CN.md) · [Examples](EXAMPLES.en.md) · [Development](DEVELOPMENT.en.md) · [Documentation](README.en.md)

## Data model

New distribute --source jobs use tree manifests for both single/multiple files: SHA256 combinations can use v2, while custom algorithms and the default BLAKE3/chunks preset use v3. Preserve v1 encoding/IDs and prepared-seed compatibility.

Entries sort by relative path bytes and contain Path, Kind (file/dir), Size, Mode (0777), and Modified (Unix nanoseconds). v2 regular files use SHA256; v3 uses an algorithm-tagged Digest. Directory basenames remain; files go directly under dest. Name is files, not another directory.

Regular files form a logical concatenation in Entries order. Size sums file bytes; default chunks span 16 MiB with a shorter tail and manifest-bound algorithms. Blocks may cross file boundaries. Empty files/directories have metadata but no logical data bytes.

v2/v3 Manifest.SHA256 hashes JSON-encoded Entries, binding metadata and file digests rather than raw concatenation. Independently check v2 files with SHA256 and v3 files with the declared algorithm. Manifest ID hashes the complete manifest, also binding algorithms and chunk boundaries/digests.

Full-mode treeHash consumes ordered bytes and verifies each file with its declared algorithm before returning the trusted tree digest. Default chunks checks every strong block and omits final whole-file verification; do not conflate the two gates. Empty datasets still require completion/publication checks.

## Controller

1. Freeze repeated sources, source operands and glob results; recursively collect entries. Reject duplicate basenames, overlapping roots, symlinks and special files.
2. Hash the trusted source, checking identity/size/mtime/ctime, permissions and directory membership before/after. Reuse unchanged source metadata caches unless recheck is set.
3. Deploy the same executable over SSH, upload the trusted manifest, then run tree-check on every node. Direct mode inventories metadata/caches only; staged mode verifies uncached contents before selecting a seed.
4. List content conflicts and apply existing policy. Ask prompts once through /dev/tty; direct-mode background verification may discover conflicts after healthy nodes have published. The selected policy applies to later conflicts; scripts must select a policy explicitly.
5. A controller included among targets directly serves original files; identical local paths reuse them and different paths use native copying. Otherwise, if a complete replica exists, skip controller media upload and let that replica seed others—even if the selected seed lacks files. Otherwise import once to the selected seed.
6. Initial import uses a Go archive/tar stream with no complete intermediate archive. Only after the controller's final source snapshot check is a completion marker written. The seed independently matches its computed manifest to the trusted controller manifest before retaining imported data.
7. Run existing P2P scheduling, TCP/QUIC, pacing, verification and global monitoring; atomically save results and stop owned peers and clean deployment/empty staging on completion, unless keep-seeding is selected.

A first source needs an extra controller hashing read, followed by independent seed verification during import. Source caching reduces later rereads. Include this cost when reporting end-to-end performance; historical prepared-seed studies do not measure this new path.

## Storage and resume

The contentFile interface retains ReadAt/WriteAt/Sync semantics: os.File for v1, treeStore for v2/v3. Binary-search offsets map logical I/O across file boundaries. Descriptors open only for active file spans rather than remaining open for every entry.

New staging uses `dest/.swarmcopy/trees/<manifest-id>/data/<relative-path>` on the final filesystem. Read/resume legacy .swarmcopy-trees in place. Rehash retained data; only manifest-matching blocks become Have, while proven unwritten sparse holes are missing. Clean only after successful completion; see [cleanup](CLEANUP.en.md).

Matching final files seed others after per-file hashing or validated fingerprint-cache reuse. Resume copies conflicting old data to independent stage files, then validates global chunks. Replace omits that old-data copy but keeps valid same-task partials. Neither policy redownloads matching final files.

Cross-file chunks may transfer a range from a neighboring complete file. WriteAt compares such bytes with the existing published file and never rewrites it.

## Publication and crashes

Data fsync → selected integrity gate (all strong blocks for chunks; additional per-file digests for full) → per-file publication → metadata/parent-directory fsync → Complete. Full hashing can overlap transfer.

New files publish with no-overwrite link/unlink. Authorized replacements recheck the old fingerprint before rename. Detected external changes fail safely; external writers must remain excluded throughout a job. Publication is per-file atomic, not dataset atomic; resume reuses files published before a crash.

A crash between link and unlink can retain a hardlink from stage to final. Partials with link count greater than one detach through a private copy before any repair writes. The resulting ctime change is accounted for without modifying old file contents. File permissions/mtime are set before fsync; directory metadata is applied bottom-up.

Extra destination files remain. Existing matching files keep their metadata; new files retain ordinary permission bits and mtime, and dataset directories receive source metadata. No UID/GID, ACL, xattr, symlink, hardlink-topology or sparse-layout preservation.

## Boundaries and cache

Entry paths must be clean relatives, without traversal, absolute roots, NUL, backslashes, duplicate/missing parents or reserved .swarmcopy prefixes. Limits: 100,000 entries, 1,000,000 chunks and bounded manifest metadata. os.Root confines access; final/stage ancestors are checked for symlinks and non-directories.

A destination-wide flock serializes peer/import access. treeStore RWMutex coordinates I/O with publication. Keep other applications from mutating source/destination data; caches do not defend against a malicious local administrator or a changing filesystem.

Source caches bind frozen paths, metadata and Linux fingerprints. Fixed destination .swarmcopy/cache.json retains the latest manifest ID and per-file device/inode/size/mtime/ctime; directory stat is insufficient. Legacy verified.json remains readable on demand. Writes are atomic and recheck forces content reads. Chunks-only downloaded data does not mint full caches.

## Historical tree validation (v0.4.0)

Five real x86_64 nodes, 139,460,641 bytes, five files and seven entries including empty file/directory, 16MiB chunks. Every case independently rehashed every file on all five nodes. These are **functional tests, not cold-cache speed comparisons**.

| Case | Aggregate new media download bytes | Result |
| --- | ---: | --- |
| Fresh TCP | 557,842,564 | Four full receivers; SSH seed import |
| Three complete, two missing; selected seed also missing | 173,015,073 | Three nodes download zero; no controller media upload |
| All complete | 0 | Reused |
| One damaged chunk, resume | 16,777,216 | Bad chunk only |
| One conflicting file, replace | 38,797,345 | All chunks crossing that file |
| Fresh QUIC | 557,842,564 | UDP data in the same binary |
| Stop and resume same ID | 520,093,696 in resumed invocation | Retained chunks, all final hashes match |

Stop occurred after one observed receiver had 5,242,913 verified bytes. Do not interpret the restarted phase counter as a fresh speed measurement. All owned peers stopped after the cases.

Automated race tests cover v1 ID compatibility, cross-file out-of-order chunks, independent file/chunk integrity, final reuse/cache invalidation, zero-byte trees, changed sources, traversal/symlinks, concurrent destination changes, repair/replacement, partial publication, hardlink-crash recovery, trusted import mismatch and real TCP/UDP transfers. aarch64 is cross-built without a hardware run.

The table records development-build functional cases. Final x86_64 candidate SHA256 is 003a378c0fd4859cb085e67c100329f768d649e0ab6622389ea7f80b10bde5bb. It additionally completed fresh TCP and eight-worker QUIC cases (557,842,564 download bytes each), plus a real controlling-terminal default ask→r repair (16,777,216 bytes). Final-code race tests cover remaining crash/compatibility boundaries; not every historical case was rerun on the final binary.

## Optimization follow-up

Profile metadata/FD overhead on large small-file trees before adding descriptor pools or file-aligned small-chunk packing. Never trade away integrity for cache acceptance. Potential next work: initial SSH chunk resume, and combining partial replicas when no single complete replica exists.

All new strategies enter main and the same executable behind explicit options. New performance claims need identical hardware/manifests, fresh targets and comparable caches, with staging, inventory, deployment, transfer, hash, fsync and publication accounted for separately. This functional table is not a speedup claim.

## Background verification of existing replicas

When the source node is a target (SOURCE MODE: direct), startup inventory checks metadata and valid content-verification caches. A same-name, same-size file without a trusted cache remains unverified. Peers open their status endpoint before background local verification; each verified chunk becomes available to other peers immediately. Local downloads start after initialization. A slow disk no longer forces all targets to wait for a full inventory pass. Staged mode retains full inventory to find a complete seed and avoid unnecessary SSH import.

VERIFYING LOCAL in watch means local verification is active and proven chunks can seed; it does not mean offline or complete. Fully verified files get incremental cache checkpoints that survive cancellation. Names and sizes never establish availability. Same-size conflicts may be discovered later. The default still prompts; unattended jobs should specify --existing resume, replace or keep. A chosen policy also applies to later conflicts. Originals remain until verified replacements publish.
