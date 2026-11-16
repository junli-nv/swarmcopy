# Pipelined whole-file verification and CPU acceleration

[中文](PIPELINE.zh-CN.md) · [Development](DEVELOPMENT.en.md)

v0.2.0 enables overlapped whole-file SHA-256 by default and a standalone hashbench subcommand. It is independent of adaptive concurrency. Chunk verification, full-file SHA-256, fsync, and atomic publication remain required.

## Usage

~~~bash
./swarmcopy distribute \
  --hosts 'node[001-005]' --source /shared/nemo.sqsh \
  --dest /raid/images --bin-dir ./dist \
  --pipeline-verify --verify-buffer-mib 4 --verify-window-chunks 256
~~~

--pipeline-verify defaults to true. Use --pipeline-verify=false to disable both the pipeline and its implicit window; an explicitly nonzero window with the pipeline disabled is rejected. --verify-buffer-mib defaults to 4 (1..64), applying to serial and pipelined file verification. Saved new jobs retain these options for resume; old records leave the pipeline disabled. Directories and multiple files remain future work.

## Integrity

Workers publish have only after chunk SHA-256 and WriteAt succeed, and never rewrite a committed chunk. The verifier reads the actual file's ready contiguous prefix in offset order. It does not hash arrival order or replace full SHA-256 with concatenated chunk digests.

Every file byte is included in the ordinary full SHA-256. A digest becoming ready does not publish the file: complete chunks, matching full digest, final fsync/permissions, and atomic publication are all required. Cancellation, read failure, and mismatches retain a partial without publication. Shutdown joins the verifier before closing the file.

Resume revalidates existing chunks and rebuilds whole-file hash state from the beginning; it does not trust a persisted mid-hash state. The contract requires exclusive task ownership and immutable committed chunks. External programs must not modify partials. Like any finite file hash, this is not protection against external changes after verification or a physical-media readback guarantee.

## Performance boundaries

The verifier uses one bounded read buffer, not a whole-image heap copy. A buffered notification wakes it when missing prefix data arrives. Verified final seed files retain the existing cache/recheck behavior and do not start an unnecessary second verifier.

With --verify-window-chunks 0 (default is 256), global rarest-first scheduling remains unchanged. An early missing chunk can postpone sequential SHA, so overlap is not guaranteed to hide all hashing. Hashing also competes for CPU, memory, and storage. Compare complete job wall time rather than only the final hash tail.

Default --verify-window-chunks 256 prefers the next 256 chunks from the hash frontier (about 4 GiB at the default chunk size), retaining rarity within that range and falling back globally if it cannot schedule useful work there. It is a priority, not a hard transfer/memory bound. Values are 0..4096 and nonzero requires --pipeline-verify.

This changes scheduling order and commonly reduces scheduler search work. Its effect cannot be attributed solely to overlapping hashing. The initial two-round unwindowed experiment is retained, followed by a new-directory same-binary plain/windowed comparison.

## Metrics

watch shows a receiver's whole-file hash percentage. Performance in status/result.json adds:

| Field | Meaning |
|---|---|
| VerifyWindowChunks | Prefix-priority chunk count; zero means global rarity |
| PipelineVerify | Configured mode |
| FileHashBytes | Bytes included in this process's whole-file SHA |
| FileHashBytesAtTransferEnd | Progress when the main loop observes all chunks and begins finalization |
| FileHashReadSeconds | Accumulated read-call wall time |
| FileHashComputeSeconds | Accumulated SHA256.Write wall time, including possible scheduling |
| FileHashWaitSeconds | Waiting for ready prefix data |
| FileHashWallSeconds | Verifier lifetime including waits; serial mode is the whole scan |
| FinalHashSeconds | Remaining digest wait after complete chunks and the first fsync |

The transfer-end sample is not an exact NIC-arrival timestamp. Reduced tail time is not reduced integrity work. Per-node phase maxima may overlap and must not be summed.

## Hardware and hashbench

Go crypto/sha256 automatically selects supported x86 SHA-NI, AVX2/BMI2, or generic code; aarch64 can use Arm SHA2. One binary per architecture retains runtime CPU dispatch.

Actual compute-node profiles sampled crypto/internal/fips140/sha256.blockSHANI. Child-only GODEBUG=cpu.sha=off and cpu.all=off selected AVX2 and generic paths with identical digests. This acceleration was already active before the change; microbenchmark ratios are not an end-to-end version speedup. ARM64 remains cross-built only.

~~~bash
./swarmcopy hashbench --help
./swarmcopy help hashbench
./swarmcopy hashbench --bytes-mib 2048
./swarmcopy hashbench --file /raid/images/nemo.sqsh --buffer-mib 4 \
  --cpu-profile /tmp/new-sha-profile.pprof
go tool pprof -top ./swarmcopy /tmp/new-sha-profile.pprof
~~~

Input files are read-only; profile paths must be new. Default memory buffer/input are 64/2048 MiB, bounded to 512/65536 MiB. Profiles include setup and memory/file tests; file read and compute timings are also reported separately. AdvertisedCPUFeatures is not proof of active dispatch. Profiling and background load affect measurements. Normal distribution still needs no Go or profiler installation.

## Checks and reproduction

Tests cover out-of-order prefix waiting, short tails, disk corruption, inconsistent whole-file digest, cancellation, read failure, and publication gates. Actual race executables pass interruption/corruption/forwarding; mixed old-seed/new-receiver recovery is also tested.

~~~bash
go test -race ./...
go vet ./...
SWARMCOPY_TEST_FLAGS='--pipeline-verify --endgame' python3 scripts/integration.py
python3 scripts/benchmark-verify.py --help
~~~

Compare the previous release, current buffered serial mode, and current pipeline at fixed concurrency. Every case is fresh and starts after OS cache dropping. Full transfer/hash/fsync plus deployment and shutdown are timed. CPU microbenchmarks are reported separately.
