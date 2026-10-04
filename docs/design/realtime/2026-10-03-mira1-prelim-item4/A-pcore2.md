## Scan lateness — mira1, 2026-10-03 20:51

Intel(R) Core(TM) i9-14900K · amd64 · kernel 6.16.3-76061603-generic · go1.25.5 · GOMAXPROCS=32 of 32 · GOGC=100 · governor powersave

Ran 1m0s. Load average 3.96 4.64 5.31 2/14391 3033406 → 4.85 4.92 5.37 6/14429 3036494. desktop in use; preliminary 60 s runs; items 1-3 in; A-pcore2.
Tasks: main 1ms (loopback I/O), fast2 10ms; main task pinned to cpu 2.

| task | target | scans (ran / due) | late (> target + thr) | overruns | missed | p50 | p99 | p99.9 | max |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| main | 1 ms | 59998 / 60011 | 1670 (2.78 %, thr 100 µs) | 19 | 13 | 6.2 µs | 312 µs | 1.22 ms | 2.89 ms |
| fast2 | 10 ms | 6001 / 6001 | 1 (0.02 %, thr 1 ms) | 0 | 0 | 11.2 µs | 61.0 µs | 232 µs | 1.91 ms |

GC: 1 collections, 0.1 ms total stop-the-world, pause p50 28.7 µs · p99 81.9 µs · max 81.9 µs, heap 1.1 MB.

Lateness histogram (scans per bucket, cumulative):

| bucket | main | fast2 |
|---|---:|---:|
| < 1.0 µs (incl. early) | 0 | 0 |
| 1.0 µs – 2.0 µs | 0 | 0 |
| 2.0 µs – 5.0 µs | 24585 | 83 |
| 5.0 µs – 10.0 µs | 15432 | 2415 |
| 10.0 µs – 20.0 µs | 15452 | 1959 |
| 20.0 µs – 50.0 µs | 2087 | 1458 |
| 50.0 µs – 100 µs | 771 | 65 |
| 100 µs – 200 µs | 733 | 11 |
| 200 µs – 500 µs | 582 | 6 |
| 500 µs – 1.00 ms | 263 | 2 |
| 1.00 ms – 2.00 ms | 87 | 1 |
| 2.00 ms – 5.00 ms | 5 | 0 |
| 5.00 ms – 10.00 ms | 0 | 0 |
| 10.00 ms – 20.00 ms | 0 | 0 |
| 20.00 ms – 50.00 ms | 0 | 0 |
| 50.00 ms – 100.00 ms | 0 | 0 |
| ≥ 100.00 ms | 0 | 0 |

Lateness = how late each scan started against its slot on the absolute schedule, cumulative since start, cold start included. Percentiles are bucket upper edges (≤ 3.2 % above the true value). Overruns = scans whose execution exceeded the period; missed = slots the loop skipped after falling a whole period behind.
