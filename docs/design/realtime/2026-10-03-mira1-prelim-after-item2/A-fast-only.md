## Scan lateness — mira1, 2026-10-03 19:42

Intel(R) Core(TM) i9-14900K · amd64 · kernel 6.16.3-76061603-generic · go1.25.5 · GOMAXPROCS=32 of 32 · GOGC=100 · governor powersave

Ran 1m0s. Load average 4.61 4.98 5.31 2/14309 2786270 → 4.52 4.88 5.26 3/14329 2789654. desktop in use; preliminary 60 s runs; AFTER item 1 (deadline loop + clock_nanosleep) AND item 2 (per-task scan isolation).
Tasks: main 1ms (loopback I/O), fast2 10ms.

| task | target | scans (ran / due) | late (> target + thr) | overruns | missed | p50 | p99 | p99.9 | max |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| main | 1 ms | 59992 / 60001 | 146 (0.24 %, thr 100 µs) | 16 | 9 | 6.1 µs | 28.0 µs | 312 µs | 2.37 ms |
| fast2 | 10 ms | 6000 / 6000 | 0 (0.00 %, thr 1 ms) | 0 | 0 | 8.8 µs | 47.0 µs | 352 µs | 465 µs |

GC: 25 collections, 1.1 ms total stop-the-world, pause p50 20.5 µs · p99 115 µs · max 115 µs, heap 2.6 MB.

Lateness histogram (scans per bucket, cumulative):

| bucket | main | fast2 |
|---|---:|---:|
| < 1.0 µs (incl. early) | 0 | 0 |
| 1.0 µs – 2.0 µs | 0 | 0 |
| 2.0 µs – 5.0 µs | 14704 | 134 |
| 5.0 µs – 10.0 µs | 36778 | 3889 |
| 10.0 µs – 20.0 µs | 6641 | 1504 |
| 20.0 µs – 50.0 µs | 1623 | 419 |
| 50.0 µs – 100 µs | 99 | 20 |
| 100 µs – 200 µs | 50 | 20 |
| 200 µs – 500 µs | 70 | 13 |
| 500 µs – 1.00 ms | 21 | 0 |
| 1.00 ms – 2.00 ms | 4 | 0 |
| 2.00 ms – 5.00 ms | 1 | 0 |
| 5.00 ms – 10.00 ms | 0 | 0 |
| 10.00 ms – 20.00 ms | 0 | 0 |
| 20.00 ms – 50.00 ms | 0 | 0 |
| 50.00 ms – 100.00 ms | 0 | 0 |
| ≥ 100.00 ms | 0 | 0 |

Lateness = how late each scan started against its slot on the absolute schedule, cumulative since start, cold start included. Percentiles are bucket upper edges (≤ 3.2 % above the true value). Overruns = scans whose execution exceeded the period; missed = slots the loop skipped after falling a whole period behind.
