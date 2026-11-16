# Large-scale cold-OS-cache comparison protocol

> Historical study/validation record: versions, parameters and results describe that measurement, not current defaults. For current use, see [project home](../README.en.md) and [documentation](README.en.md).

[中文](COLD-STUDY.zh-CN.md) · [Preliminary measurements](BENCHMARK.en.md)

The plan is saved before timed transfers, fixing executable/driver hashes, membership, ordering, and unique case IDs.

## Fixed design

- One immutable medium, the same prepared compute seed, and identical receivers.
- Three paired rounds: rsync→SCDP, SCDP→rsync, rsync→SCDP.
- Before every method, sync and drop_caches=3 on all participating compute nodes, recording exit codes/cache/load data. The BCM head node is not cleared.
- Every method and round uses a unique directory absent everywhere. No final files, partials, or task progress are reused across cases.
- TCP retransmission, chunk retries, and recovery after a case starts are allowed; the clock keeps running without reset.
- The local executable and both architecture deployment files are hashed and checked before every method.
- Both methods start from a prepared verified seed. Initial media staging is excluded. The seed's disk verification record is not pretransferred receiver data; OS cache clearing does not delete this record.

## Completion and fresh-transfer evidence

rsync uses encrypted SSH without compression/bandwidth limits. Each receiver immediately hashes its own completed copy, allowing natural overlap across nodes. Copy-complete and verified-complete times are recorded separately. A normal single successful attempt must report full-size literal and transferred-file bytes. All in-case retry attempts are retained.

SCDP is unlimited. Each receiver must have Origin=false, Downloaded equal to full size, and Complete=true. Aggregate downloads must equal file size times receiver count. Timing includes deployment, transfer, final SHA-256, fsync, and owned-peer shutdown.

Report every round, medians, and min/max. Preserve failed/cancelled records without counting them as successful samples.

## Interpretation

- This compares end-to-end alternatives, not a topology-only ablation: rsync carries data over encrypted SSH, while SCDP uses plaintext HTTP.
- drop_caches reclaims clean page cache and reclaimable dentries/inodes; active memory and disk/controller caches are not fully controlled. See the [Linux kernel documentation](https://docs.kernel.org/admin-guide/sysctl/vm.html#drop-caches).
- Cache clearing temporarily increases node I/O; explicit --drop-caches is required and only listed compute nodes are touched.
- Background load is not fully isolated. Three observations do not establish a universal speedup.
- Fair freshness constrains the state before timing, not normal recovery during a timed case.

## Run

~~~bash
python3 scripts/benchmark-cold.py --help
python3 scripts/benchmark-cold.py \
  --hosts-file hosts.txt --seed node001 \
  --seed-file /raid/images/nemo.sqsh \
  --dest-root /raid/images/cold-study \
  --out ./cold-results-new --swarmcopy ./swarmcopy --bin-dir ./dist \
  --rounds 3 --timeout 1800 --drop-caches
~~~

The controller needs SSH/Python 3; targets need rsync, sha256sum, awk, and cache-clearing privilege. The script obtains the seed public key over already authenticated controller SSH and installs only benchmark-private known_hosts files, without altering standard SSH configuration. Generated media copies, case JSON, and complete logs remain available for review; existing user files are not automatically deleted.


## Finding benchmark jobs

SCDP benchmark jobs use custom directories and are absent from the default jobs listing. For the example above, use ./swarmcopy jobs --state-dir ./cold-results-new, or ./swarmcopy watch --job ./cold-results-new/<case-id>-job --count 1. Read case-id from study-plan.json; it differs from the task JOB ID. The rsync baseline runs directly from the driver: consult results.json / summary.json and the live stage in progress.json.
