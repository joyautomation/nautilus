## Scan lateness — mira1, 2026-10-03 18:27

Intel(R) Core(TM) i9-14900K · amd64 · kernel 6.16.3-76061603-generic · go1.25.5 · GOMAXPROCS=32 of 32 · GOGC=100 · governor powersave

Ran 1m0s. Load average 5.89 5.20 5.68 5/14345 2527439 → 4.52 4.95 5.56 3/14281 2530767. desktop in use; preliminary 60 s runs; AFTER absolute-deadline loop + clock_nanosleep on a locked thread, timer slack 1 µs.
Tasks: main 1ms (loopback I/O), fast2 10ms.

| task | target | scans (ran / due) | late (> target + thr) | overruns | missed | p50 | p99 | p99.9 | max |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| main | 1 ms | 60003 / 60005 | 177 (0.29 %, thr 100 µs) | 15 | 2 | 6.2 µs | 31.5 µs | 344 µs | 2.53 ms |
| fast2 | 10 ms | 6000 / 6000 | 2 (0.03 %, thr 1 ms) | 0 | 0 | 8.8 µs | 54.0 µs | 512 µs | 2.41 ms |

GC: 25 collections, 1.1 ms total stop-the-world, pause p50 20.5 µs · p99 81.9 µs · max 81.9 µs, heap 2.3 MB.

Lateness histogram (scans per bucket, cumulative):

| bucket | main | fast2 |
|---|---:|---:|
| < 1.0 µs (incl. early) | 0 | 0 |
| 1.0 µs – 2.0 µs | 0 | 0 |
| 2.0 µs – 5.0 µs | 13178 | 192 |
| 5.0 µs – 10.0 µs | 35735 | 3798 |
| 10.0 µs – 20.0 µs | 8378 | 1354 |
| 20.0 µs – 50.0 µs | 2400 | 594 |
| 50.0 µs – 100 µs | 134 | 26 |
| 100 µs – 200 µs | 54 | 18 |
| 200 µs – 500 µs | 90 | 11 |
| 500 µs – 1.00 ms | 29 | 4 |
| 1.00 ms – 2.00 ms | 3 | 0 |
| 2.00 ms – 5.00 ms | 1 | 2 |
| 5.00 ms – 10.00 ms | 0 | 0 |
| 10.00 ms – 20.00 ms | 0 | 0 |
| 20.00 ms – 50.00 ms | 0 | 0 |
| 50.00 ms – 100.00 ms | 0 | 0 |
| ≥ 100.00 ms | 0 | 0 |

Lateness = how late each scan started against its slot on the absolute schedule, cumulative since start, cold start included. Percentiles are bucket upper edges (≤ 3.2 % above the true value). Overruns = scans whose execution exceeded the period; missed = slots the loop skipped after falling a whole period behind.
