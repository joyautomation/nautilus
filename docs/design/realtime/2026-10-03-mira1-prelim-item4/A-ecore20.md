## Scan lateness — mira1, 2026-10-03 20:52

Intel(R) Core(TM) i9-14900K · amd64 · kernel 6.16.3-76061603-generic · go1.25.5 · GOMAXPROCS=32 of 32 · GOGC=100 · governor powersave

Ran 1m0s. Load average 4.85 4.92 5.37 3/14423 3036502 → 5.21 5.01 5.37 4/14388 3039937. desktop in use; preliminary 60 s runs; items 1-3 in; A-ecore20.
Tasks: main 1ms (loopback I/O), fast2 10ms; main task pinned to cpu 20.

| task | target | scans (ran / due) | late (> target + thr) | overruns | missed | p50 | p99 | p99.9 | max |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| main | 1 ms | 60000 / 60003 | 1961 (3.27 %, thr 100 µs) | 2 | 3 | 12.8 µs | 392 µs | 1.06 ms | 2.47 ms |
| fast2 | 10 ms | 6000 / 6000 | 1 (0.02 %, thr 1 ms) | 0 | 0 | 18.0 µs | 98.0 µs | 800 µs | 1.32 ms |

GC: 1 collections, 0.2 ms total stop-the-world, pause p50 41.0 µs · p99 164 µs · max 164 µs, heap 1.1 MB.

Lateness histogram (scans per bucket, cumulative):

| bucket | main | fast2 |
|---|---:|---:|
| < 1.0 µs (incl. early) | 1 | 0 |
| 1.0 µs – 2.0 µs | 0 | 0 |
| 2.0 µs – 5.0 µs | 2307 | 47 |
| 5.0 µs – 10.0 µs | 22251 | 1549 |
| 10.0 µs – 20.0 µs | 24725 | 1744 |
| 20.0 µs – 50.0 µs | 7947 | 2515 |
| 50.0 µs – 100 µs | 807 | 87 |
| 100 µs – 200 µs | 750 | 20 |
| 200 µs – 500 µs | 796 | 23 |
| 500 µs – 1.00 ms | 349 | 13 |
| 1.00 ms – 2.00 ms | 64 | 1 |
| 2.00 ms – 5.00 ms | 2 | 0 |
| 5.00 ms – 10.00 ms | 0 | 0 |
| 10.00 ms – 20.00 ms | 0 | 0 |
| 20.00 ms – 50.00 ms | 0 | 0 |
| 50.00 ms – 100.00 ms | 0 | 0 |
| ≥ 100.00 ms | 0 | 0 |

Lateness = how late each scan started against its slot on the absolute schedule, cumulative since start, cold start included. Percentiles are bucket upper edges (≤ 3.2 % above the true value). Overruns = scans whose execution exceeded the period; missed = slots the loop skipped after falling a whole period behind.
