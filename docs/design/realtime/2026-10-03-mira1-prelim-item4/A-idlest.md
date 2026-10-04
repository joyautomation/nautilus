## Scan lateness — mira1, 2026-10-03 20:55

Intel(R) Core(TM) i9-14900K · amd64 · kernel 6.16.3-76061603-generic · go1.25.5 · GOMAXPROCS=32 of 32 · GOGC=100 · governor powersave

Ran 1m0s. Load average 3.33 4.44 5.13 14/14320 3048898 → 7.97 5.46 5.43 13/14375 3052195. desktop in use; items 1-3 in; pinned to the idlest core at launch (cpu 25).
Tasks: main 1ms (loopback I/O), fast2 10ms; main task pinned to cpu 25.

| task | target | scans (ran / due) | late (> target + thr) | overruns | missed | p50 | p99 | p99.9 | max |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| main | 1 ms | 60011 / 60018 | 1599 (2.66 %, thr 100 µs) | 3 | 7 | 13.0 µs | 408 µs | 1.06 ms | 1.98 ms |
| fast2 | 10 ms | 6001 / 6001 | 2 (0.03 %, thr 1 ms) | 0 | 0 | 17.0 µs | 70.0 µs | 464 µs | 1.98 ms |

GC: 1 collections, 0.3 ms total stop-the-world, pause p50 131 µs · p99 197 µs · max 197 µs, heap 1.0 MB.

Lateness histogram (scans per bucket, cumulative):

| bucket | main | fast2 |
|---|---:|---:|
| < 1.0 µs (incl. early) | 1 | 0 |
| 1.0 µs – 2.0 µs | 1 | 0 |
| 2.0 µs – 5.0 µs | 2605 | 77 |
| 5.0 µs – 10.0 µs | 22298 | 1650 |
| 10.0 µs – 20.0 µs | 25258 | 1874 |
| 20.0 µs – 50.0 µs | 7558 | 2295 |
| 50.0 µs – 100 µs | 690 | 69 |
| 100 µs – 200 µs | 506 | 16 |
| 200 µs – 500 µs | 646 | 13 |
| 500 µs – 1.00 ms | 367 | 4 |
| 1.00 ms – 2.00 ms | 80 | 2 |
| 2.00 ms – 5.00 ms | 0 | 0 |
| 5.00 ms – 10.00 ms | 0 | 0 |
| 10.00 ms – 20.00 ms | 0 | 0 |
| 20.00 ms – 50.00 ms | 0 | 0 |
| 50.00 ms – 100.00 ms | 0 | 0 |
| ≥ 100.00 ms | 0 | 0 |

Lateness = how late each scan started against its slot on the absolute schedule, cumulative since start, cold start included. Percentiles are bucket upper edges (≤ 3.2 % above the true value). Overruns = scans whose execution exceeded the period; missed = slots the loop skipped after falling a whole period behind.
