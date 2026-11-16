# Multiple files and directories: design and validation

[中文（主文档）](TREES.zh-CN.md) · [Examples](EXAMPLES.en.md) · [Development](DEVELOPMENT.en.md)

## Data model

New `distribute --source` jobs use Manifest Version 2, including single-file inputs, so inventory, repair and fill behavior is consistent. Version 1 JSON encoding, IDs and prepared-seed workflows stay compatible.

Entries sort by relative path bytes. Each contains Path, Kind (file/dir), Size, Mode (0777 only), Modified (Unix nanoseconds) and, for regular files, raw content SHA256. Directory basenames remain; multiple input files go directly into dest. Name is the dataset label files, not an extra directory.

Regular files form a virtual concatenation in entry order. Size sums their bytes; Chunks hash the logical stream at 16MiB boundaries, with a shorter final chunk. Chunks may cross file boundaries. Empty files/directories occupy metadata but no data bytes.

The v2 Manifest.SHA256 is **SHA256 of JSON-encoded Entries**, a tree digest binding metadata and individual file hashes. It is neither the raw concatenation hash nor an individual file hash. Entry SHA256 values are ordinary content hashes independently checkable with sha256sum. Manifest ID hashes the complete manifest JSON, additionally binding chunk boundaries/hashes.

treeHash consumes ordered bytes once, switching per-file SHA256 state at boundaries. Only complete, verified input yields the expected tree digest. This retains chunk plus per-file content hashing without adding a third whole-stream hash. Empty datasets still pass verification/publication gates.

## Controller

1. Freeze repeated sources, source operands and glob results; recursively collect entries. Reject duplicate basenames, overlapping roots, symlinks and special files.
2. Hash the trusted source, checking identity/size/mtime/ctime, permissions and directory membership before/after. Reuse unchanged source metadata caches unless recheck is set.
3. Deploy the same executable over SSH, upload the trusted manifest, then run tree-check on every node.
4. List content conflicts and apply existing policy. Ask prompts once through /dev/tty before publication; scripts must select a policy explicitly.
5. If a complete replica exists, skip controller media upload and let that replica seed others—even if the selected seed lacks files. Otherwise import once to the selected seed.
6. Initial import uses a Go archive/tar stream with no complete intermediate archive. Only after the controller's final source snapshot check is a completion marker written. The seed independently matches its computed manifest to the trusted controller manifest before retaining imported data.
7. Run existing P2P scheduling, TCP/QUIC, pacing, verification and global monitoring; atomically save results and stop owned peers on completion.

A first source needs an extra controller hashing read, followed by independent seed verification during import. Source caching reduces later rereads. Include this cost when reporting end-to-end performance; historical prepared-seed studies do not measure this new path.

## Storage and resume

The contentFile interface retains ReadAt/WriteAt/Sync semantics: os.File for v1, treeStore for v2. Binary-search offsets map logical I/O across file boundaries. Descriptors open only for active file spans rather than remaining open for every entry.

Stage files live under `dest/.swarmcopy-trees/<manifest-id>/data/<relative-path>` on the final filesystem. Resume rehashes retained chunks. Only matching chunk SHA256 marks Have; network retries remain allowed. Empty files and directories are explicitly represented.

Matching final files seed others after per-file hashing or validated fingerprint-cache reuse. Resume copies conflicting old data to independent stage files, then validates global chunks. Replace omits that old-data copy but keeps valid same-task partials. Neither policy redownloads matching final files.

Cross-file chunks may transfer a range from a neighboring complete file. WriteAt compares such bytes with the existing published file and never rewrites it.

## Publication and crashes

Data fsync → ordered per-file SHA256 complete → per-file publication → directory metadata/parent fsync → complete.

New files publish with no-overwrite link/unlink. Authorized replacements recheck the old fingerprint before rename. Detected external changes fail safely; external writers must remain excluded throughout a job. Publication is per-file atomic, not dataset atomic; resume reuses files published before a crash.

A crash between link and unlink can retain a hardlink from stage to final. Partials with link count greater than one detach through a private copy before any repair writes. The resulting ctime change is accounted for without modifying old file contents. File permissions/mtime are set before fsync; directory metadata is applied bottom-up.

Extra destination files remain. Existing matching files keep their metadata; new files retain ordinary permission bits and mtime, and dataset directories receive source metadata. No UID/GID, ACL, xattr, symlink, hardlink-topology or sparse-layout preservation.

## Boundaries and cache

Entry paths must be clean relatives, without traversal, absolute roots, NUL, backslashes, duplicate/missing parents or reserved .swarmcopy prefixes. Limits: 100,000 entries, 1,000,000 chunks and bounded manifest metadata. os.Root confines access; final/stage ancestors are checked for symlinks and non-directories.

A destination-wide flock serializes peer/import access. treeStore RWMutex coordinates I/O with publication. Keep other applications from mutating source/destination data; caches do not defend against a malicious local administrator or a changing filesystem.

Source cache keys contain frozen paths, metadata and Linux fingerprints. Target verified.json is keyed by the complete manifest ID and records every file's device/inode/size/mtime/ctime. Root-directory mtime alone is never sufficient. Unique temporary cache names and atomic rename support concurrent cache readers; recheck forces content reads.

## Validation

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
