## Scan lateness — mira1, 2026-10-03 22:30

Intel(R) Core(TM) i9-14900K · amd64 · kernel 6.16.3-76061603-generic · go1.25.5 · GOMAXPROCS=32 of 32 · GOGC=100 · governor powersave

Ran 1m0s. Load average 5.51 4.04 4.38 5/14345 3352321 → 3.51 3.73 4.25 27/14380 3355417. desktop in use; items 1-3 in; setcap cap_sys_nice+ep on the harness; B-pcore2-fifo50.
Tasks: main 1ms (loopback I/O), fast2 10ms, slow 100ms (≈20 ms of logic, 133967 iterations), alloc 50ms (200 string concats); main task pinned to cpu 2; main task SCHED_FIFO 50.

| task | target | scans (ran / due) | late (> target + thr) | overruns | missed | p50 | p99 | p99.9 | max |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| main | 1 ms | 60000 / 60001 | 7 (0.01 %, thr 100 µs) | 1 | 1 | 2.4 µs | 14.8 µs | 27.0 µs | 1.92 ms |
| fast2 | 10 ms | 6000 / 6000 | 3 (0.05 %, thr 1 ms) | 0 | 0 | 8.5 µs | 45.0 µs | 608 µs | 1.94 ms |
| slow | 100 ms | 599 / 600 | 0 (0.00 %, thr 10 ms) | 0 | 0 | 9.8 µs | 53.0 µs | 208 µs | 205 µs |
| alloc | 50 ms | 1200 / 1200 | 0 (0.00 %, thr 5 ms) | 0 | 0 | 10.8 µs | 39.0 µs | 72.0 µs | 98.3 µs |

GC: 5 collections, 0.6 ms total stop-the-world, pause p50 41.0 µs · p99 229 µs · max 229 µs, heap 3.6 MB.

Lateness histogram (scans per bucket, cumulative):

| bucket | main | fast2 | slow | alloc |
|---|---:|---:|---:|---:|
| < 1.0 µs (incl. early) | 0 | 0 | 0 | 0 |
| 1.0 µs – 2.0 µs | 20623 | 0 | 0 | 0 |
| 2.0 µs – 5.0 µs | 26141 | 430 | 0 | 2 |
| 5.0 µs – 10.0 µs | 10913 | 3529 | 315 | 466 |
| 10.0 µs – 20.0 µs | 2169 | 1312 | 159 | 580 |
| 20.0 µs – 50.0 µs | 140 | 678 | 118 | 144 |
| 50.0 µs – 100 µs | 6 | 28 | 3 | 7 |
| 100 µs – 200 µs | 3 | 11 | 2 | 0 |
| 200 µs – 500 µs | 1 | 5 | 1 | 0 |
| 500 µs – 1.00 ms | 2 | 3 | 0 | 0 |
| 1.00 ms – 2.00 ms | 1 | 3 | 0 | 0 |
| 2.00 ms – 5.00 ms | 0 | 0 | 0 | 0 |
| 5.00 ms – 10.00 ms | 0 | 0 | 0 | 0 |
| 10.00 ms – 20.00 ms | 0 | 0 | 0 | 0 |
| 20.00 ms – 50.00 ms | 0 | 0 | 0 | 0 |
| 50.00 ms – 100.00 ms | 0 | 0 | 0 | 0 |
| ≥ 100.00 ms | 0 | 0 | 0 | 0 |

Lateness = how late each scan started against its slot on the absolute schedule, cumulative since start, cold start included. Percentiles are bucket upper edges (≤ 3.2 % above the true value). Overruns = scans whose execution exceeded the period; missed = slots the loop skipped after falling a whole period behind.
