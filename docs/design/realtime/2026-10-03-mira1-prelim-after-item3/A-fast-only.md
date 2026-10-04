## Scan lateness — mira1, 2026-10-03 20:13

Intel(R) Core(TM) i9-14900K · amd64 · kernel 6.16.3-76061603-generic · go1.25.5 · GOMAXPROCS=32 of 32 · GOGC=100 · governor powersave

Ran 1m0s. Load average 5.99 5.23 5.40 5/14430 2898436 → 4.63 4.99 5.31 6/14399 2901994. desktop in use; preliminary 60 s runs; AFTER items 1, 2 and 3 (allocation-free VM call path).
Tasks: main 1ms (loopback I/O), fast2 10ms.

| task | target | scans (ran / due) | late (> target + thr) | overruns | missed | p50 | p99 | p99.9 | max |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| main | 1 ms | 59998 / 60002 | 404 (0.67 %, thr 100 µs) | 7 | 4 | 11.0 µs | 60.0 µs | 656 µs | 2.17 ms |
| fast2 | 10 ms | 6000 / 6000 | 1 (0.02 %, thr 1 ms) | 0 | 0 | 15.8 µs | 74.0 µs | 672 µs | 1.06 ms |

GC: 1 collections, 0.1 ms total stop-the-world, pause p50 32.8 µs · p99 49.2 µs · max 49.2 µs, heap 1.0 MB.

Lateness histogram (scans per bucket, cumulative):

| bucket | main | fast2 |
|---|---:|---:|
| < 1.0 µs (incl. early) | 0 | 0 |
| 1.0 µs – 2.0 µs | 0 | 0 |
| 2.0 µs – 5.0 µs | 7183 | 37 |
| 5.0 µs – 10.0 µs | 20921 | 1679 |
| 10.0 µs – 20.0 µs | 22659 | 1984 |
| 20.0 µs – 50.0 µs | 8553 | 2208 |
| 50.0 µs – 100 µs | 277 | 51 |
| 100 µs – 200 µs | 136 | 13 |
| 200 µs – 500 µs | 177 | 19 |
| 500 µs – 1.00 ms | 80 | 7 |
| 1.00 ms – 2.00 ms | 10 | 1 |
| 2.00 ms – 5.00 ms | 1 | 0 |
| 5.00 ms – 10.00 ms | 0 | 0 |
| 10.00 ms – 20.00 ms | 0 | 0 |
| 20.00 ms – 50.00 ms | 0 | 0 |
| 50.00 ms – 100.00 ms | 0 | 0 |
| ≥ 100.00 ms | 0 | 0 |

Lateness = how late each scan started against its slot on the absolute schedule, cumulative since start, cold start included. Percentiles are bucket upper edges (≤ 3.2 % above the true value). Overruns = scans whose execution exceeded the period; missed = slots the loop skipped after falling a whole period behind.
