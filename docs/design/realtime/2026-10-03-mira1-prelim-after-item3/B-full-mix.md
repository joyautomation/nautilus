## Scan lateness — mira1, 2026-10-03 20:14

Intel(R) Core(TM) i9-14900K · amd64 · kernel 6.16.3-76061603-generic · go1.25.5 · GOMAXPROCS=32 of 32 · GOGC=100 · governor powersave

Ran 1m0s. Load average 4.63 4.99 5.31 4/14393 2902002 → 3.17 4.52 5.12 6/14539 2906721. desktop in use; preliminary 60 s runs; AFTER items 1, 2 and 3 (allocation-free VM call path).
Tasks: main 1ms (loopback I/O), fast2 10ms, slow 100ms (≈20 ms of logic, 149978 iterations), alloc 50ms (200 string concats).

| task | target | scans (ran / due) | late (> target + thr) | overruns | missed | p50 | p99 | p99.9 | max |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| main | 1 ms | 59997 / 60001 | 176 (0.29 %, thr 100 µs) | 10 | 4 | 6.4 µs | 32.0 µs | 368 µs | 2.87 ms |
| fast2 | 10 ms | 6000 / 6000 | 1 (0.02 %, thr 1 ms) | 0 | 0 | 9.0 µs | 44.0 µs | 288 µs | 1.11 ms |
| slow | 100 ms | 599 / 600 | 0 (0.00 %, thr 10 ms) | 0 | 0 | 12.0 µs | 100 µs | 1.09 ms | 1.08 ms |
| alloc | 50 ms | 1200 / 1200 | 0 (0.00 %, thr 5 ms) | 0 | 0 | 10.2 µs | 56.0 µs | 1.95 ms | 4.98 ms |

GC: 5 collections, 0.6 ms total stop-the-world, pause p50 32.8 µs · p99 197 µs · max 197 µs, heap 3.3 MB.

Lateness histogram (scans per bucket, cumulative):

| bucket | main | fast2 | slow | alloc |
|---|---:|---:|---:|---:|
| < 1.0 µs (incl. early) | 0 | 0 | 0 | 0 |
| 1.0 µs – 2.0 µs | 0 | 0 | 0 | 0 |
| 2.0 µs – 5.0 µs | 13339 | 154 | 1 | 2 |
| 5.0 µs – 10.0 µs | 34761 | 3514 | 225 | 586 |
| 10.0 µs – 20.0 µs | 9224 | 1640 | 179 | 440 |
| 20.0 µs – 50.0 µs | 2344 | 637 | 175 | 157 |
| 50.0 µs – 100 µs | 152 | 35 | 13 | 7 |
| 100 µs – 200 µs | 66 | 11 | 1 | 1 |
| 200 µs – 500 µs | 67 | 3 | 2 | 2 |
| 500 µs – 1.00 ms | 38 | 4 | 1 | 2 |
| 1.00 ms – 2.00 ms | 4 | 1 | 1 | 1 |
| 2.00 ms – 5.00 ms | 1 | 0 | 0 | 1 |
| 5.00 ms – 10.00 ms | 0 | 0 | 0 | 0 |
| 10.00 ms – 20.00 ms | 0 | 0 | 0 | 0 |
| 20.00 ms – 50.00 ms | 0 | 0 | 0 | 0 |
| 50.00 ms – 100.00 ms | 0 | 0 | 0 | 0 |
| ≥ 100.00 ms | 0 | 0 | 0 | 0 |

Lateness = how late each scan started against its slot on the absolute schedule, cumulative since start, cold start included. Percentiles are bucket upper edges (≤ 3.2 % above the true value). Overruns = scans whose execution exceeded the period; missed = slots the loop skipped after falling a whole period behind.
