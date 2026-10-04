## Scan lateness — mira1, 2026-10-03 17:44

Intel(R) Core(TM) i9-14900K · amd64 · kernel 6.16.3-76061603-generic · go1.25.5 · GOMAXPROCS=32 of 32 · GOGC=100 · governor powersave

Ran 1m0s. Load average 11.92 7.76 6.75 7/14530 2301768 → 7.04 7.16 6.61 6/14806 2310257. desktop in use (VS Code, browser, incus rig idle); preliminary 60 s runs.
Tasks: main 1ms (loopback I/O), fast2 10ms, slow 100ms (≈20 ms of logic, 114439 iterations), alloc 50ms (200 string concats).

| task | target | scans (ran / due) | late (> target + thr) | overruns | p50 | p99 | p99.9 | max |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| main | 1 ms | 35559 / 60000 | 3998 (11.24 %, thr 100 µs) | 605 | 68.0 µs | 27.14 ms | 67.58 ms | 178.26 ms |
| fast2 | 10 ms | 4666 / 6000 | 611 (13.09 %, thr 1 ms) | 598 | 1.0 µs | 53.25 ms | 65.54 ms | 169.24 ms |
| slow | 100 ms | 599 / 600 | 0 (0.00 %, thr 10 ms) | 0 | 12.2 µs | 992 µs | 2.18 ms | 2.17 ms |
| alloc | 50 ms | 1197 / 1200 | 86 (7.18 %, thr 5 ms) | 126 | 1.0 µs | 22.02 ms | 26.11 ms | 129.26 ms |

GC: 4949 collections, 270.3 ms total stop-the-world, pause p50 20.5 µs · p99 115 µs · max 5.24 ms, heap 1.8 MB.

Lateness histogram (scans per bucket, cumulative):

| bucket | main | fast2 | slow | alloc |
|---|---:|---:|---:|---:|
| < 1.0 µs (incl. early) | 1375 | 2745 | 291 | 606 |
| 1.0 µs – 2.0 µs | 5 | 1 | 0 | 0 |
| 2.0 µs – 5.0 µs | 4 | 3 | 1 | 4 |
| 5.0 µs – 10.0 µs | 6 | 5 | 5 | 6 |
| 10.0 µs – 20.0 µs | 127 | 10 | 9 | 10 |
| 20.0 µs – 50.0 µs | 1416 | 35 | 21 | 37 |
| 50.0 µs – 100 µs | 28627 | 26 | 24 | 55 |
| 100 µs – 200 µs | 2709 | 53 | 57 | 81 |
| 200 µs – 500 µs | 430 | 111 | 105 | 149 |
| 500 µs – 1.00 ms | 225 | 1065 | 81 | 115 |
| 1.00 ms – 2.00 ms | 29 | 20 | 3 | 16 |
| 2.00 ms – 5.00 ms | 6 | 30 | 1 | 31 |
| 5.00 ms – 10.00 ms | 1 | 34 | 0 | 24 |
| 10.00 ms – 20.00 ms | 22 | 221 | 0 | 49 |
| 20.00 ms – 50.00 ms | 412 | 236 | 0 | 12 |
| 50.00 ms – 100.00 ms | 163 | 69 | 0 | 0 |
| ≥ 100.00 ms | 1 | 1 | 0 | 1 |

Lateness = (period − target) per scan, cumulative since start, cold start included. Percentiles are bucket upper edges (≤ 3.2 % above the true value). Overruns = scans whose execution exceeded the period.
