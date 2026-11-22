# Pipelined whole-file verification and CPU acceleration

[中文](PIPELINE.zh-CN.md) · [Development](DEVELOPMENT.en.md) · [Documentation](README.en.md)

Pipelining implements full-mode final content verification and can overlap transfer independently of adaptive scheduling. New distribution jobs default to chunks, without a whole-file reread or prefix window; explicitly select --verify full to use the controls below. Chunk checks, selected integrity gates, fsync and publication remain required.

## Usage

~~~bash
./swarmcopy distribute \
  --hosts 'node[001-005]' --source /raid/source/nemo.sqsh \
  --dest /raid/images --bin-dir ./dist \
  --verify full --pipeline-verify --verify-buffer-mib 4 --verify-window-chunks 256
~~~

In full mode, pipeline-verify defaults to true and the window to 256. Pipeline-verify=false disables the pipeline and implicit window, selecting serial full verification; an explicitly nonzero window with the pipeline disabled is invalid. Verify-buffer-mib defaults to 4 (1..64). Directories/multiple files are supported. Saved settings persist without applying new defaults to old records.

## Integrity

Workers declare Have only after manifest-bound chunk checks and WriteAt. The verifier reads the ready contiguous prefix from actual files in logical offset order, not arrival order. Concatenated chunk digests cannot replace file hashing.

Full verifies every regular file byte with its manifest-bound cryptographic algorithm: SHA256 by default or an explicit BLAKE3 choice. The tree SHA256 remains an Entries metadata digest. A ready digest does not publish files: complete chunks, matching file digests, fsync/metadata and per-file publication gates all remain. Publication is not atomic across the dataset. Cancellation/read errors retain data without declaring successful completion; shutdown joins the verifier before closing files.

Resume revalidates existing chunks and rebuilds whole-file hash state from the beginning; it does not trust a persisted mid-hash state. The contract requires exclusive task ownership and immutable committed chunks. External programs must not modify partials. Like any finite file hash, this is not protection against external changes after verification or a physical-media readback guarantee.

## Performance boundaries

The verifier uses one bounded read buffer, not a whole-image heap copy. A buffered notification wakes it when missing prefix data arrives. Verified final seed files retain the existing cache/recheck behavior and do not start an unnecessary second verifier.

In full mode, window 0 disables prefix preference while retaining bounded approximate rarest-first selection. A missing early block can delay ordered verification, and hashing competes for CPU/memory/storage. Evaluate complete job elapsed time, not just the final hash tail.

Default --verify-window-chunks 256 prefers the next 256 chunks from the hash frontier (about 4 GiB at the default chunk size), retaining rarity within that range and falling back globally if it cannot schedule useful work there. It is a priority, not a hard transfer/memory bound. Values are 0..4096 and nonzero requires --pipeline-verify.

This changes scheduling order and commonly reduces scheduler search work. Its effect cannot be attributed solely to overlapping hashing. The initial two-round unwindowed experiment is retained, followed by a new-directory same-binary plain/windowed comparison.

## Metrics

watch shows a receiver's whole-file hash percentage. Performance in status/result.json adds:

| Field | Meaning |
|---|---|
| VerifyWindowChunks | Prefix-priority chunk count; zero means global rarity |
| PipelineVerify | Configured mode |
| FileHashBytes | Bytes included in this process's whole-file digest |
| FileHashBytesAtTransferEnd | Progress when the main loop observes all chunks and begins finalization |
| FileHashReadSeconds | Accumulated read-call wall time |
| FileHashComputeSeconds | Accumulated file-hasher Write wall time, including possible scheduling |
| FileHashWaitSeconds | Waiting for ready prefix data |
| FileHashWallSeconds | Verifier lifetime including waits; serial mode is the whole scan |
| FinalHashSeconds | Remaining digest wait after complete chunks and the first fsync |

The transfer-end sample is not an exact NIC-arrival timestamp. Reduced tail time is not reduced integrity work. Per-node phase maxima may overlap and must not be summed.

## SHA256 acceleration and historical validation

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
SWARMCOPY_TEST_FLAGS='--verify full --pipeline-verify --endgame' python3 scripts/integration.py
python3 scripts/benchmark-verify.py --help
~~~

Compare the previous release, current buffered serial mode, and current pipeline at fixed concurrency. Every case is fresh and starts after OS cache dropping. Full transfer/hash/fsync plus deployment and shutdown are timed. CPU microbenchmarks are reported separately.
