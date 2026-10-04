## Scan lateness — mira1, 2026-10-03 20:53

Intel(R) Core(TM) i9-14900K · amd64 · kernel 6.16.3-76061603-generic · go1.25.5 · GOMAXPROCS=32 of 32 · GOGC=100 · governor powersave

Ran 1m0s. Load average 5.21 5.01 5.37 4/14381 3039945 → 3.71 4.64 5.22 5/14346 3043263. desktop in use; preliminary 60 s runs; items 1-3 in; B-pcore2.
Tasks: main 1ms (loopback I/O), fast2 10ms, slow 100ms (≈20 ms of logic, 132767 iterations), alloc 50ms (200 string concats); main task pinned to cpu 2.

| task | target | scans (ran / due) | late (> target + thr) | overruns | missed | p50 | p99 | p99.9 | max |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| main | 1 ms | 59940 / 60001 | 1942 (3.24 %, thr 100 µs) | 32 | 61 | 6.5 µs | 424 µs | 1.41 ms | 3.41 ms |
| fast2 | 10 ms | 6000 / 6000 | 0 (0.00 %, thr 1 ms) | 0 | 0 | 11.2 µs | 68.0 µs | 472 µs | 632 µs |
| slow | 100 ms | 599 / 600 | 0 (0.00 %, thr 10 ms) | 0 | 0 | 20.5 µs | 84.0 µs | 640 µs | 634 µs |
| alloc | 50 ms | 1200 / 1200 | 0 (0.00 %, thr 5 ms) | 0 | 0 | 12.8 µs | 39.0 µs | 102 µs | 163 µs |

GC: 5 collections, 0.8 ms total stop-the-world, pause p50 57.3 µs · p99 262 µs · max 262 µs, heap 3.2 MB.

Lateness histogram (scans per bucket, cumulative):

| bucket | main | fast2 | slow | alloc |
|---|---:|---:|---:|---:|
| < 1.0 µs (incl. early) | 0 | 0 | 0 | 0 |
| 1.0 µs – 2.0 µs | 0 | 0 | 0 | 0 |
| 2.0 µs – 5.0 µs | 23508 | 148 | 1 | 2 |
| 5.0 µs – 10.0 µs | 16043 | 2413 | 96 | 374 |
| 10.0 µs – 20.0 µs | 15768 | 1846 | 198 | 531 |
| 20.0 µs – 50.0 µs | 1962 | 1499 | 292 | 284 |
| 50.0 µs – 100 µs | 716 | 56 | 9 | 6 |
| 100 µs – 200 µs | 660 | 19 | 0 | 2 |
| 200 µs – 500 µs | 818 | 13 | 1 | 0 |
| 500 µs – 1.00 ms | 314 | 5 | 1 | 0 |
| 1.00 ms – 2.00 ms | 117 | 0 | 0 | 0 |
| 2.00 ms – 5.00 ms | 33 | 0 | 0 | 0 |
| 5.00 ms – 10.00 ms | 0 | 0 | 0 | 0 |
| 10.00 ms – 20.00 ms | 0 | 0 | 0 | 0 |
| 20.00 ms – 50.00 ms | 0 | 0 | 0 | 0 |
| 50.00 ms – 100.00 ms | 0 | 0 | 0 | 0 |
| ≥ 100.00 ms | 0 | 0 | 0 | 0 |

Lateness = how late each scan started against its slot on the absolute schedule, cumulative since start, cold start included. Percentiles are bucket upper edges (≤ 3.2 % above the true value). Overruns = scans whose execution exceeded the period; missed = slots the loop skipped after falling a whole period behind.
