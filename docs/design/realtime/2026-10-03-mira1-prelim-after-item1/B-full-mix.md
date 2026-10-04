## Scan lateness — mira1, 2026-10-03 18:28

Intel(R) Core(TM) i9-14900K · amd64 · kernel 6.16.3-76061603-generic · go1.25.5 · GOMAXPROCS=32 of 32 · GOGC=100 · governor powersave

Ran 1m0s. Load average 4.52 4.95 5.56 8/14268 2530775 → 4.14 4.77 5.46 4/14322 2533975. desktop in use; preliminary 60 s runs; AFTER absolute-deadline loop + clock_nanosleep on a locked thread, timer slack 1 µs.
Tasks: main 1ms (loopback I/O), fast2 10ms, slow 100ms (≈20 ms of logic, 68315 iterations), alloc 50ms (200 string concats).

| task | target | scans (ran / due) | late (> target + thr) | overruns | missed | p50 | p99 | p99.9 | max |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| main | 1 ms | 51418 / 60011 | 673 (1.31 %, thr 100 µs) | 610 | 8593 | 6.1 µs | 264 µs | 928 µs | 2.16 ms |
| fast2 | 10 ms | 5951 / 6001 | 112 (1.88 %, thr 1 ms) | 132 | 50 | 8.8 µs | 5.12 ms | 9.22 ms | 9.96 ms |
| slow | 100 ms | 600 / 600 | 0 (0.00 %, thr 10 ms) | 0 | 0 | 9.8 µs | 68.0 µs | 376 µs | 372 µs |
| alloc | 50 ms | 1200 / 1200 | 0 (0.00 %, thr 5 ms) | 0 | 0 | 10.5 µs | 50.0 µs | 360 µs | 1.20 ms |

GC: 3055 collections, 246.1 ms total stop-the-world, pause p50 16.4 µs · p99 229 µs · max 2.62 ms, heap 2.9 MB.

Lateness histogram (scans per bucket, cumulative):

| bucket | main | fast2 | slow | alloc |
|---|---:|---:|---:|---:|
| < 1.0 µs (incl. early) | 2 | 0 | 0 | 0 |
| 1.0 µs – 2.0 µs | 1 | 1 | 0 | 0 |
| 2.0 µs – 5.0 µs | 13853 | 131 | 2 | 1 |
| 5.0 µs – 10.0 µs | 30123 | 3858 | 319 | 555 |
| 10.0 µs – 20.0 µs | 5462 | 1335 | 183 | 503 |
| 20.0 µs – 50.0 µs | 1182 | 449 | 86 | 129 |
| 50.0 µs – 100 µs | 121 | 22 | 6 | 7 |
| 100 µs – 200 µs | 109 | 19 | 2 | 1 |
| 200 µs – 500 µs | 210 | 10 | 1 | 2 |
| 500 µs – 1.00 ms | 352 | 13 | 0 | 0 |
| 1.00 ms – 2.00 ms | 1 | 15 | 0 | 1 |
| 2.00 ms – 5.00 ms | 1 | 37 | 0 | 0 |
| 5.00 ms – 10.00 ms | 0 | 60 | 0 | 0 |
| 10.00 ms – 20.00 ms | 0 | 0 | 0 | 0 |
| 20.00 ms – 50.00 ms | 0 | 0 | 0 | 0 |
| 50.00 ms – 100.00 ms | 0 | 0 | 0 | 0 |
| ≥ 100.00 ms | 0 | 0 | 0 | 0 |

Lateness = how late each scan started against its slot on the absolute schedule, cumulative since start, cold start included. Percentiles are bucket upper edges (≤ 3.2 % above the true value). Overruns = scans whose execution exceeded the period; missed = slots the loop skipped after falling a whole period behind.
