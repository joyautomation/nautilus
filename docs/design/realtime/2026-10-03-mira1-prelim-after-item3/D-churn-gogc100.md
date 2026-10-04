## Scan lateness — mira1, 2026-10-03 20:16

Intel(R) Core(TM) i9-14900K · amd64 · kernel 6.16.3-76061603-generic · go1.25.5 · GOMAXPROCS=32 of 32 · GOGC=100 · governor powersave

Ran 1m0s. Load average 4.28 4.67 5.13 4/14527 2917516 → 4.16 4.61 5.09 5/14470 2920870. desktop in use; full mix + 50 MB/s Go-side churn; GOGC=100.
Tasks: main 1ms (loopback I/O), fast2 10ms, slow 100ms (≈20 ms of logic, 150050 iterations), alloc 50ms (200 string concats); Go-side churn 50 MB/s.

| task | target | scans (ran / due) | late (> target + thr) | overruns | missed | p50 | p99 | p99.9 | max |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| main | 1 ms | 59993 / 60001 | 149 (0.25 %, thr 100 µs) | 6 | 8 | 6.1 µs | 28.0 µs | 256 µs | 3.23 ms |
| fast2 | 10 ms | 6000 / 6000 | 2 (0.03 %, thr 1 ms) | 0 | 0 | 8.5 µs | 39.0 µs | 416 µs | 2.24 ms |
| slow | 100 ms | 599 / 600 | 0 (0.00 %, thr 10 ms) | 0 | 0 | 12.2 µs | 82.0 µs | 944 µs | 941 µs |
| alloc | 50 ms | 1200 / 1200 | 0 (0.00 %, thr 5 ms) | 0 | 0 | 10.0 µs | 34.0 µs | 296 µs | 679 µs |

GC: 448 collections, 37.0 ms total stop-the-world, pause p50 28.7 µs · p99 393 µs · max 2.10 ms, heap 11.6 MB.

Lateness histogram (scans per bucket, cumulative):

| bucket | main | fast2 | slow | alloc |
|---|---:|---:|---:|---:|
| < 1.0 µs (incl. early) | 0 | 0 | 0 | 0 |
| 1.0 µs – 2.0 µs | 0 | 0 | 0 | 0 |
| 2.0 µs – 5.0 µs | 14457 | 201 | 6 | 2 |
| 5.0 µs – 10.0 µs | 37856 | 4177 | 283 | 616 |
| 10.0 µs – 20.0 µs | 6014 | 1203 | 145 | 487 |
| 20.0 µs – 50.0 µs | 1385 | 369 | 148 | 88 |
| 50.0 µs – 100 µs | 131 | 24 | 14 | 4 |
| 100 µs – 200 µs | 76 | 13 | 1 | 0 |
| 200 µs – 500 µs | 54 | 7 | 0 | 1 |
| 500 µs – 1.00 ms | 11 | 3 | 1 | 1 |
| 1.00 ms – 2.00 ms | 7 | 1 | 0 | 0 |
| 2.00 ms – 5.00 ms | 1 | 1 | 0 | 0 |
| 5.00 ms – 10.00 ms | 0 | 0 | 0 | 0 |
| 10.00 ms – 20.00 ms | 0 | 0 | 0 | 0 |
| 20.00 ms – 50.00 ms | 0 | 0 | 0 | 0 |
| 50.00 ms – 100.00 ms | 0 | 0 | 0 | 0 |
| ≥ 100.00 ms | 0 | 0 | 0 | 0 |

Lateness = how late each scan started against its slot on the absolute schedule, cumulative since start, cold start included. Percentiles are bucket upper edges (≤ 3.2 % above the true value). Overruns = scans whose execution exceeded the period; missed = slots the loop skipped after falling a whole period behind.
