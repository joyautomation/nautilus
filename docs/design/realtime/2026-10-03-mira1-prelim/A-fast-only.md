## Scan lateness — mira1, 2026-10-03 17:43

Intel(R) Core(TM) i9-14900K · amd64 · kernel 6.16.3-76061603-generic · go1.25.5 · GOMAXPROCS=32 of 32 · GOGC=100 · governor powersave

Ran 1m0s. Load average 5.34 6.13 6.20 4/14747 2297262 → 11.92 7.76 6.75 9/14541 2301761. desktop in use (VS Code, browser, incus rig idle); preliminary 60 s runs.
Tasks: main 1ms (loopback I/O), fast2 10ms.

| task | target | scans (ran / due) | late (> target + thr) | overruns | p50 | p99 | p99.9 | max |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| main | 1 ms | 57487 / 60000 | 7068 (12.29 %, thr 100 µs) | 3 | 70.0 µs | 204 µs | 1.06 ms | 3.04 ms |
| fast2 | 10 ms | 5999 / 6000 | 21 (0.35 %, thr 1 ms) | 0 | 1.0 µs | 944 µs | 1.09 ms | 3.87 ms |

GC: 24 collections, 1.7 ms total stop-the-world, pause p50 32.8 µs · p99 98.3 µs · max 98.3 µs, heap 1.8 MB.

Lateness histogram (scans per bucket, cumulative):

| bucket | main | fast2 |
|---|---:|---:|
| < 1.0 µs (incl. early) | 2147 | 3938 |
| 1.0 µs – 2.0 µs | 1 | 3 |
| 2.0 µs – 5.0 µs | 8 | 6 |
| 5.0 µs – 10.0 µs | 10 | 15 |
| 10.0 µs – 20.0 µs | 234 | 34 |
| 20.0 µs – 50.0 µs | 2047 | 58 |
| 50.0 µs – 100 µs | 45971 | 70 |
| 100 µs – 200 µs | 6488 | 88 |
| 200 µs – 500 µs | 332 | 88 |
| 500 µs – 1.00 ms | 151 | 1677 |
| 1.00 ms – 2.00 ms | 92 | 20 |
| 2.00 ms – 5.00 ms | 5 | 1 |
| 5.00 ms – 10.00 ms | 0 | 0 |
| 10.00 ms – 20.00 ms | 0 | 0 |
| 20.00 ms – 50.00 ms | 0 | 0 |
| 50.00 ms – 100.00 ms | 0 | 0 |
| ≥ 100.00 ms | 0 | 0 |

Lateness = (period − target) per scan, cumulative since start, cold start included. Percentiles are bucket upper edges (≤ 3.2 % above the true value). Overruns = scans whose execution exceeded the period.
