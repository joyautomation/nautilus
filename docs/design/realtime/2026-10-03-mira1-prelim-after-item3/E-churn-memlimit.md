## Scan lateness — mira1, 2026-10-03 20:17

Intel(R) Core(TM) i9-14900K · amd64 · kernel 6.16.3-76061603-generic · go1.25.5 · GOMAXPROCS=32 of 32 · GOGC=-1 · GOMEMLIMIT=256MiB · governor powersave

Ran 1m0s. Load average 4.16 4.61 5.09 10/14453 2920878 → 5.27 4.84 5.13 6/14488 2924201. desktop in use; full mix + 50 MB/s Go-side churn; GOGC=off GOMEMLIMIT=256MiB.
Tasks: main 1ms (loopback I/O), fast2 10ms, slow 100ms (≈20 ms of logic, 154303 iterations), alloc 50ms (200 string concats); Go-side churn 50 MB/s.

| task | target | scans (ran / due) | late (> target + thr) | overruns | missed | p50 | p99 | p99.9 | max |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| main | 1 ms | 59993 / 60001 | 171 (0.29 %, thr 100 µs) | 12 | 8 | 6.0 µs | 28.5 µs | 336 µs | 2.30 ms |
| fast2 | 10 ms | 6000 / 6000 | 2 (0.03 %, thr 1 ms) | 0 | 0 | 8.5 µs | 49.0 µs | 480 µs | 1.49 ms |
| slow | 100 ms | 599 / 600 | 0 (0.00 %, thr 10 ms) | 0 | 0 | 9.8 µs | 48.0 µs | 200 µs | 200 µs |
| alloc | 50 ms | 1200 / 1200 | 0 (0.00 %, thr 5 ms) | 0 | 0 | 8.8 µs | 63.0 µs | 228 µs | 235 µs |

GC: 12 collections, 0.7 ms total stop-the-world, pause p50 24.6 µs · p99 81.9 µs · max 81.9 µs, heap 133.7 MB.

Lateness histogram (scans per bucket, cumulative):

| bucket | main | fast2 | slow | alloc |
|---|---:|---:|---:|---:|
| < 1.0 µs (incl. early) | 0 | 0 | 0 | 0 |
| 1.0 µs – 2.0 µs | 1 | 0 | 0 | 0 |
| 2.0 µs – 5.0 µs | 15702 | 186 | 1 | 4 |
| 5.0 µs – 10.0 µs | 35394 | 4074 | 318 | 780 |
| 10.0 µs – 20.0 µs | 6968 | 1205 | 178 | 311 |
| 20.0 µs – 50.0 µs | 1652 | 477 | 96 | 89 |
| 50.0 µs – 100 µs | 104 | 31 | 3 | 10 |
| 100 µs – 200 µs | 64 | 13 | 2 | 3 |
| 200 µs – 500 µs | 76 | 8 | 0 | 2 |
| 500 µs – 1.00 ms | 25 | 3 | 0 | 0 |
| 1.00 ms – 2.00 ms | 5 | 2 | 0 | 0 |
| 2.00 ms – 5.00 ms | 1 | 0 | 0 | 0 |
| 5.00 ms – 10.00 ms | 0 | 0 | 0 | 0 |
| 10.00 ms – 20.00 ms | 0 | 0 | 0 | 0 |
| 20.00 ms – 50.00 ms | 0 | 0 | 0 | 0 |
| 50.00 ms – 100.00 ms | 0 | 0 | 0 | 0 |
| ≥ 100.00 ms | 0 | 0 | 0 | 0 |

Lateness = how late each scan started against its slot on the absolute schedule, cumulative since start, cold start included. Percentiles are bucket upper edges (≤ 3.2 % above the true value). Overruns = scans whose execution exceeded the period; missed = slots the loop skipped after falling a whole period behind.
