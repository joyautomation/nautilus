## Scan lateness — mira1, 2026-10-03 20:15

Intel(R) Core(TM) i9-14900K · amd64 · kernel 6.16.3-76061603-generic · go1.25.5 · GOMAXPROCS=32 of 32 · GOGC=100 · governor powersave

Ran 1m0s. Load average 3.17 4.52 5.12 5/14524 2906729 → 4.89 4.81 5.19 12/14647 2914592. desktop in use; preliminary 60 s runs; AFTER items 1, 2 and 3 (allocation-free VM call path).
Tasks: main 1ms (loopback I/O), fast2 10ms, alloc 50ms (200 string concats).

| task | target | scans (ran / due) | late (> target + thr) | overruns | missed | p50 | p99 | p99.9 | max |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| main | 1 ms | 59994 / 60001 | 229 (0.38 %, thr 100 µs) | 12 | 7 | 7.0 µs | 37.0 µs | 440 µs | 1.41 ms |
| fast2 | 10 ms | 6000 / 6000 | 0 (0.00 %, thr 1 ms) | 0 | 0 | 10.5 µs | 42.0 µs | 408 µs | 760 µs |
| alloc | 50 ms | 1200 / 1200 | 0 (0.00 %, thr 5 ms) | 0 | 0 | 11.0 µs | 50.0 µs | 496 µs | 528 µs |

GC: 5 collections, 0.4 ms total stop-the-world, pause p50 20.5 µs · p99 131 µs · max 131 µs, heap 2.9 MB.

Lateness histogram (scans per bucket, cumulative):

| bucket | main | fast2 | alloc |
|---|---:|---:|---:|
| < 1.0 µs (incl. early) | 0 | 0 | 0 |
| 1.0 µs – 2.0 µs | 0 | 0 | 0 |
| 2.0 µs – 5.0 µs | 10511 | 92 | 5 |
| 5.0 µs – 10.0 µs | 31404 | 2706 | 469 |
| 10.0 µs – 20.0 µs | 12968 | 1999 | 453 |
| 20.0 µs – 50.0 µs | 4710 | 1152 | 262 |
| 50.0 µs – 100 µs | 171 | 26 | 7 |
| 100 µs – 200 µs | 85 | 10 | 0 |
| 200 µs – 500 µs | 105 | 11 | 2 |
| 500 µs – 1.00 ms | 35 | 3 | 1 |
| 1.00 ms – 2.00 ms | 4 | 0 | 0 |
| 2.00 ms – 5.00 ms | 0 | 0 | 0 |
| 5.00 ms – 10.00 ms | 0 | 0 | 0 |
| 10.00 ms – 20.00 ms | 0 | 0 | 0 |
| 20.00 ms – 50.00 ms | 0 | 0 | 0 |
| 50.00 ms – 100.00 ms | 0 | 0 | 0 |
| ≥ 100.00 ms | 0 | 0 | 0 |

Lateness = how late each scan started against its slot on the absolute schedule, cumulative since start, cold start included. Percentiles are bucket upper edges (≤ 3.2 % above the true value). Overruns = scans whose execution exceeded the period; missed = slots the loop skipped after falling a whole period behind.
