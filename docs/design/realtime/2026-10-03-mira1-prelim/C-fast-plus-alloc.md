## Scan lateness — mira1, 2026-10-03 17:45

Intel(R) Core(TM) i9-14900K · amd64 · kernel 6.16.3-76061603-generic · go1.25.5 · GOMAXPROCS=32 of 32 · GOGC=100 · governor powersave

Ran 1m0s. Load average 7.04 7.16 6.61 3/14792 2310264 → 6.65 7.00 6.59 9/14810 2315016. desktop in use (VS Code, browser, incus rig idle); preliminary 60 s runs.
Tasks: main 1ms (loopback I/O), fast2 10ms, alloc 50ms (200 string concats).

| task | target | scans (ran / due) | late (> target + thr) | overruns | p50 | p99 | p99.9 | max |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| main | 1 ms | 57050 / 60001 | 9461 (16.58 %, thr 100 µs) | 9 | 74.0 µs | 304 µs | 976 µs | 4.41 ms |
| fast2 | 10 ms | 6000 / 6000 | 13 (0.22 %, thr 1 ms) | 0 | 1.0 µs | 944 µs | 1.06 ms | 1.08 ms |
| alloc | 50 ms | 1199 / 1200 | 0 (0.00 %, thr 5 ms) | 0 | 1.0 µs | 912 µs | 1.47 ms | 1.51 ms |

GC: 79 collections, 10.8 ms total stop-the-world, pause p50 32.8 µs · p99 164 µs · max 5.24 ms, heap 2.8 MB.

Lateness histogram (scans per bucket, cumulative):

| bucket | main | fast2 | alloc |
|---|---:|---:|---:|
| < 1.0 µs (incl. early) | 2071 | 3935 | 635 |
| 1.0 µs – 2.0 µs | 0 | 1 | 2 |
| 2.0 µs – 5.0 µs | 4 | 18 | 5 |
| 5.0 µs – 10.0 µs | 9 | 18 | 6 |
| 10.0 µs – 20.0 µs | 198 | 43 | 12 |
| 20.0 µs – 50.0 µs | 1746 | 76 | 41 |
| 50.0 µs – 100 µs | 43560 | 106 | 62 |
| 100 µs – 200 µs | 8634 | 124 | 105 |
| 200 µs – 500 µs | 474 | 139 | 211 |
| 500 µs – 1.00 ms | 310 | 1526 | 116 |
| 1.00 ms – 2.00 ms | 40 | 13 | 3 |
| 2.00 ms – 5.00 ms | 3 | 0 | 0 |
| 5.00 ms – 10.00 ms | 0 | 0 | 0 |
| 10.00 ms – 20.00 ms | 0 | 0 | 0 |
| 20.00 ms – 50.00 ms | 0 | 0 | 0 |
| 50.00 ms – 100.00 ms | 0 | 0 | 0 |
| ≥ 100.00 ms | 0 | 0 | 0 |

Lateness = (period − target) per scan, cumulative since start, cold start included. Percentiles are bucket upper edges (≤ 3.2 % above the true value). Overruns = scans whose execution exceeded the period.
