# Integrity modes and guarantees

[中文（主文档）](INTEGRITY.zh-CN.md) · [Examples](EXAMPLES.en.md) · [Documentation](README.en.md)

## Choose a mode

| New-job mode | Hash preset | Receiver completion gate |
| --- | --- | --- |
| chunks (default) | BLAKE3 chunks/files | Check every chunk, require all chunks, fsync and per-file publication |
| full | SHA256 chunks/files | Additionally verify actual file contents against the manifest before completing publication |

```bash
./swarmcopy distribute --hosts @hosts.txt --source /raid/source/nemo.sqsh --dest /raid/images
./swarmcopy distribute --hosts @hosts.txt --source /raid/source/nemo.sqsh --dest /raid/images --verify full
```

Chunks does not disable or sample verification; it omits final whole-file content verification. Initial trusted source hashing still reads all bytes; existing replicas require file digests or valid caches. Direct-mode startup inventory checks only metadata/caches; uncached contents are verified by the peer in the background before being trusted. Each advertised chunk has passed its bound digest (or derives from a valid full-file cache). Full hashing is pipelined by default and can overlap transfer rather than always adding a serial scan.

Chunks cannot detect damage after a verified chunk write; the guarantees differ. Fsync requests persistence and relies on correct filesystem/RAID/device behavior. It is not independent content verification or physical-media readback.

## Algorithms and advanced combinations

| Option | Values | Constraint |
| --- | --- | --- |
| --chunk-hash | sha256, blake3, xxh3-128 | Chunks needs cryptographic block digests; XXH3 requires full |
| --file-hash | sha256, blake3 | Cryptographic source/full-file digest |
| --verify | chunks, full | New-job presets; explicit algorithms override |

```bash
./swarmcopy distribute --hosts @hosts.txt --source /raid/source/nemo.sqsh \
  --dest /raid/images --verify full --chunk-hash xxh3-128 --file-hash blake3
```

A noncryptographic XXH3 match cannot bypass the final strong file gate. The fastest combination depends on the CPU; the measurements below do not predict network speedup. See [pipelining](PIPELINE.en.md) for hash timing and [examples](EXAMPLES.en.md) for SSH options.

## Manifests, caches and compatibility

Trusted manifests bind algorithms. SHA256 trees can use v2; custom algorithms and the BLAKE3 preset use v3 with algorithm-tagged file Digest. Preserve v1/v2 encoding and IDs. Tree SHA256 binds entry metadata, not raw concatenated file bytes.

Resume retains saved mode/algorithms. Explicit mode changes force peer recheck/restart without rewriting the manifest. Missing legacy mode means full; prepared seeds retain their original algorithms.

```bash
./swarmcopy resume --job <ID> --verify full
./swarmcopy resume --job <ID> --verify chunks
```

Chunks completion cannot mint full-file verification caches. Destination .swarmcopy/cache.json binds the complete manifest and each file's device/inode/size/mtime/ctime. Recheck forces content reads. Read legacy verified.json on demand; length or directory mtime alone is insufficient.

## I/O faults and recovery

A data fsync failure cannot report Complete; retain staging. Resolve space, filesystem, RAID or I/O faults, stop a controller still waiting, then resume the same ID:

```bash
./swarmcopy jobs
./swarmcopy stop --job <ID>
./swarmcopy resume --job <ID>
```

Recovery rechecks retained blocks and fills only missing/damaged data. Power-loss data loss still needs retransmission. Do not remove/purge first. A later directory/marker fsync may fail after some files have been renamed into place; their existence does not prove job success. Publication is per file, and external writers must remain excluded.

Automatic cleanup handles only completed deployment/empty staging, protecting failed/shared data. durability_test.go injects EIO to check these paths; it is not a physical power-loss/RAID hardware test. See [cleanup](CLEANUP.en.md) and [layout](TREES.en.md).

## Historical CPU microbenchmarks, not cluster throughput

Memory-only dual-hash benchmark: identical 1 MiB stream into chunk and file hashers, GOMAXPROCS=1, 400 ms per trial, three trials, median decimal MB/s. These numbers are not disk or distribution throughput.

| CPU / host | SHA256 + SHA256 | XXH3-128 + SHA256 | BLAKE3 + BLAKE3 | XXH3-128 + BLAKE3 |
| --- | ---: | ---: | ---: | ---: |
| Xeon 6776P / HGX167 | 560.13 | 1052.81 | 1157.99 | 1993.80 |
| Xeon 6746E / head | 871.89 | 1512.26 | 446.79 | 830.15 |

HGX167 combinations were measured in one run. Head-node XXH3+SHA256 was added in a later run; the other head values are earlier reference trials. CPU differences change the fastest combination. The BLAKE3/chunks new-job default targets large-file distribution; full still selects SHA256, and BLAKE3 is not universally faster. Go SHA256 uses available hardware acceleration. The selected BLAKE3 implementation has x86 AVX2/AVX512 paths; availability depends on hardware/runtime. aarch64 is statically cross-built, not hardware-benchmarked in this round. Do not compound these ratios with earlier cluster or SSH benchmarks.

Historical v0.5.0 two-node tests on HGX061/HGX066 used a 139,460,641-byte file/directory fixture: default SHA256, both mixed full combinations, BLAKE3 full, both cryptographic chunks combinations, QUIC chunks, chunks-to-full upgrade, and damaged-file repair passed. Independent Python SHA256 checked every final file. Repair downloaded the affected 16 MiB chunk only. These are functional checks, not a new large-scale rsync comparison.


[Raw memory benchmark](data/integrity-bench-hgx167.txt). Run `go test -mod=vendor -run '^$' -bench '^BenchmarkIntegritySourcePipeline$'` for a development microbenchmark. Hardware, cache and background load affect results.
