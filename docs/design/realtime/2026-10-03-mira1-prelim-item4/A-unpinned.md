## Scan lateness — mira1, 2026-10-03 20:50

Intel(R) Core(TM) i9-14900K · amd64 · kernel 6.16.3-76061603-generic · go1.25.5 · GOMAXPROCS=32 of 32 · GOGC=100 · governor powersave

Ran 1m0s. Load average 3.46 4.69 5.37 10/14383 3029798 → 3.96 4.64 5.31 5/14398 3033398. desktop in use; preliminary 60 s runs; items 1-3 in; A-unpinned.
Tasks: main 1ms (loopback I/O), fast2 10ms.

| task | target | scans (ran / due) | late (> target + thr) | overruns | missed | p50 | p99 | p99.9 | max |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| main | 1 ms | 60017 / 60018 | 366 (0.61 %, thr 100 µs) | 7 | 1 | 10.2 µs | 55.0 µs | 608 µs | 2.27 ms |
| fast2 | 10 ms | 6001 / 6001 | 6 (0.10 %, thr 1 ms) | 0 | 0 | 14.5 µs | 114 µs | 960 µs | 1.56 ms |

GC: 1 collections, 0.3 ms total stop-the-world, pause p50 115 µs · p99 164 µs · max 164 µs, heap 1.1 MB.

Lateness histogram (scans per bucket, cumulative):

| bucket | main | fast2 |
|---|---:|---:|
| < 1.0 µs (incl. early) | 0 | 0 |
| 1.0 µs – 2.0 µs | 0 | 0 |
| 2.0 µs – 5.0 µs | 7619 | 50 |
| 5.0 µs – 10.0 µs | 22269 | 1761 |
| 10.0 µs – 20.0 µs | 21761 | 2136 |
| 20.0 µs – 50.0 µs | 7692 | 1929 |
| 50.0 µs – 100 µs | 309 | 61 |
| 100 µs – 200 µs | 128 | 13 |
| 200 µs – 500 µs | 150 | 29 |
| 500 µs – 1.00 ms | 80 | 15 |
| 1.00 ms – 2.00 ms | 7 | 6 |
| 2.00 ms – 5.00 ms | 1 | 0 |
| 5.00 ms – 10.00 ms | 0 | 0 |
| 10.00 ms – 20.00 ms | 0 | 0 |
| 20.00 ms – 50.00 ms | 0 | 0 |
| 50.00 ms – 100.00 ms | 0 | 0 |
| ≥ 100.00 ms | 0 | 0 |

Lateness = how late each scan started against its slot on the absolute schedule, cumulative since start, cold start included. Percentiles are bucket upper edges (≤ 3.2 % above the true value). Overruns = scans whose execution exceeded the period; missed = slots the loop skipped after falling a whole period behind.
