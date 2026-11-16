# Distribution benchmark

> Historical study/validation record: versions, parameters and results describe that measurement, not current defaults. For current use, see [project home](../README.en.md) and [documentation](README.en.md).

[157-node three-pair cold-cache study](COLD-RESULTS.en.md)

[中文（主文档）](BENCHMARK.zh-CN.md) · [Numeric results](data/benchmark-results.json) · [Validation](VALIDATION.en.md)

Development measurements on 2026-11-15: one 40,188,444,672-byte (37.43 GiB) file, the same compute seed and receiver cohort, and node-local RAID. Every round uses fresh destination files. SCDP has unlimited bandwidth, four workers/four upload slots, and a prepared unchanged verified seed.

| Total nodes / receivers | rsync copy seconds | rsync copy + SHA256 | SCDP including verification | Speedup vs copy | Speedup vs copy + SHA256 |
| --- | ---: | ---: | ---: | ---: | ---: |
| 5 / 4 | 98.6 | 141.9 | **81.0** | 1.22× | 1.75× |
| 45 / 44 | 122.2 | 165.7 | **85.2** | 1.43× | 1.95× |

For 45 total nodes, SCDP reduced elapsed time by about 30% against copy-only and 49% against copy plus SHA-256. All 44 receivers passed verification.

## Seed load

The 45-node run delivered 1.608 TiB of valid media data. The seed supplied approximately 50.6 GiB; peers supplied the rest, reducing valid seed-supplied bytes by about 96.9% relative to a single source serving all copies.

These are accepted From counters, not NIC captures. They exclude headers, status traffic, and failed retries. A seed can resend pieces, so this is not a promise of exactly one uploaded file. Every receiver still writes a complete copy.

## Earlier slower builds

Five-node development runs took 310.1 seconds with a 256 MiB/s protection limit and 157.0 seconds after removing the limit but retaining repeated seed scans. These builds were slower than the baseline.

Profiling found about 80 seconds of redundant startup validation on an already prepared seed. Final-file verification caching with inode/size/mtime/ctime checks, skipping fresh sparse-file scans, buffer reuse, and bounded deployment removed that overhead. New receivers still verify chunks and whole-file SHA-256. --recheck bypasses the cache; first-time manifest/cache preparation still costs time.

## Method and limitations

- rsync and SCDP run sequentially without competing transfers.
- Baseline receivers concurrently pull from one seed using rsync --whole-file -e ssh, without bandwidth limits/compression. Targets are new. A separate all-receiver SHA-256 phase is recorded.
- SCDP timing includes SSH deployment, transfer, final verification, fsync, and owned-peer shutdown. The five-node cached run predates parallel deployment; the 45-node run uses fanout=8.
- Both start from a prepared compute seed. Initial NFS/controller staging and one-time manifest/cache preparation are excluded. New prepare imports create their cache during import validation.
- One observation per configuration; OS caches were not flushed. Hardware, background load, topology, and ordering are uncontrolled. The rsync command does not add --fsync, so its completion/durability semantics differ from SCDP.
- An expanded run with an SSH host-key precondition failure and a later membership-cancelled run were discarded.
- Validation stops at 45 nodes. Benefits need not grow monotonically, and these results are not a general performance guarantee.

## Reproduce

Prepare the seed and its .manifest.json, a literal host file including the seed, fresh node-local --dest-root and local --out directories, and sufficient space. This optional benchmark needs Python 3, SSH, and rsync on receivers; the distributor itself does not require Python/rsync.

~~~bash
python3 scripts/benchmark.py --help
python3 scripts/benchmark.py \
  --hosts-file hosts.txt --seed node001 \
  --seed-file /raid/images/nemo.sqsh \
  --dest-root /raid/images/benchmark-new-run \
  --out ./benchmark-results-new-run --swarmcopy ./swarmcopy --bin-dir ./dist
~~~

The script completes and verifies rsync before starting SCDP, retaining JSON and logs. Optional --pin-seed-host-key reads the seed public key through already authenticated controller SSH and creates temporary benchmark-only known_hosts files, without changing standard SSH configuration or disabling host-key checks.
