## Scan lateness — mira1, 2026-10-03 19:44

Intel(R) Core(TM) i9-14900K · amd64 · kernel 6.16.3-76061603-generic · go1.25.5 · GOMAXPROCS=32 of 32 · GOGC=100 · governor powersave

Ran 1m0s. Load average 3.68 4.57 5.12 2/14290 2792931 → 4.38 4.63 5.11 3/14302 2796476. desktop in use; preliminary 60 s runs; AFTER item 1 (deadline loop + clock_nanosleep) AND item 2 (per-task scan isolation).
Tasks: main 1ms (loopback I/O), fast2 10ms, alloc 50ms (200 string concats).

| task | target | scans (ran / due) | late (> target + thr) | overruns | missed | p50 | p99 | p99.9 | max |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| main | 1 ms | 59999 / 60001 | 200 (0.33 %, thr 100 µs) | 19 | 2 | 6.4 µs | 32.0 µs | 416 µs | 1.82 ms |
| fast2 | 10 ms | 6000 / 6000 | 1 (0.02 %, thr 1 ms) | 0 | 0 | 9.0 µs | 39.0 µs | 384 µs | 1.38 ms |
| alloc | 50 ms | 1200 / 1200 | 0 (0.00 %, thr 5 ms) | 0 | 0 | 10.2 µs | 41.0 µs | 440 µs | 593 µs |

GC: 82 collections, 5.5 ms total stop-the-world, pause p50 24.6 µs · p99 229 µs · max 262 µs, heap 1.7 MB.

Lateness histogram (scans per bucket, cumulative):

| bucket | main | fast2 | alloc |
|---|---:|---:|---:|
| < 1.0 µs (incl. early) | 0 | 0 | 0 |
| 1.0 µs – 2.0 µs | 0 | 0 | 0 |
| 2.0 µs – 5.0 µs | 13397 | 132 | 3 |
| 5.0 µs – 10.0 µs | 34203 | 3568 | 566 |
| 10.0 µs – 20.0 µs | 9291 | 1526 | 466 |
| 20.0 µs – 50.0 µs | 2772 | 736 | 154 |
| 50.0 µs – 100 µs | 135 | 22 | 5 |
| 100 µs – 200 µs | 72 | 6 | 1 |
| 200 µs – 500 µs | 84 | 6 | 3 |
| 500 µs – 1.00 ms | 41 | 2 | 1 |
| 1.00 ms – 2.00 ms | 3 | 1 | 0 |
| 2.00 ms – 5.00 ms | 0 | 0 | 0 |
| 5.00 ms – 10.00 ms | 0 | 0 | 0 |
| 10.00 ms – 20.00 ms | 0 | 0 | 0 |
| 20.00 ms – 50.00 ms | 0 | 0 | 0 |
| 50.00 ms – 100.00 ms | 0 | 0 | 0 |
| ≥ 100.00 ms | 0 | 0 | 0 |

Lateness = how late each scan started against its slot on the absolute schedule, cumulative since start, cold start included. Percentiles are bucket upper edges (≤ 3.2 % above the true value). Overruns = scans whose execution exceeded the period; missed = slots the loop skipped after falling a whole period behind.
