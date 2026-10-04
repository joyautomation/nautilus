## Scan lateness — mira1, 2026-10-03 22:28

Intel(R) Core(TM) i9-14900K · amd64 · kernel 6.16.3-76061603-generic · go1.25.5 · GOMAXPROCS=32 of 32 · GOGC=100 · governor powersave

Ran 1m0s. Load average 2.60 3.50 4.30 1/14434 3346175 → 3.32 3.58 4.28 9/14349 3349210. desktop in use; items 1-3 in; setcap cap_sys_nice+ep on the harness; A-fifo50.
Tasks: main 1ms (loopback I/O), fast2 10ms; main task SCHED_FIFO 50.

| task | target | scans (ran / due) | late (> target + thr) | overruns | missed | p50 | p99 | p99.9 | max |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| main | 1 ms | 60016 / 60017 | 15 (0.02 %, thr 100 µs) | 2 | 1 | 7.9 µs | 26.5 µs | 59.0 µs | 460 µs |
| fast2 | 10 ms | 6001 / 6001 | 0 (0.00 %, thr 1 ms) | 0 | 0 | 16.0 µs | 68.0 µs | 432 µs | 925 µs |

GC: 1 collections, 0.2 ms total stop-the-world, pause p50 57.3 µs · p99 197 µs · max 197 µs, heap 1.2 MB.

Lateness histogram (scans per bucket, cumulative):

| bucket | main | fast2 |
|---|---:|---:|
| < 1.0 µs (incl. early) | 0 | 0 |
| 1.0 µs – 2.0 µs | 0 | 0 |
| 2.0 µs – 5.0 µs | 17572 | 42 |
| 5.0 µs – 10.0 µs | 20800 | 1531 |
| 10.0 µs – 20.0 µs | 19784 | 2204 |
| 20.0 µs – 50.0 µs | 1776 | 2133 |
| 50.0 µs – 100 µs | 68 | 55 |
| 100 µs – 200 µs | 10 | 12 |
| 200 µs – 500 µs | 5 | 19 |
| 500 µs – 1.00 ms | 0 | 4 |
| 1.00 ms – 2.00 ms | 0 | 0 |
| 2.00 ms – 5.00 ms | 0 | 0 |
| 5.00 ms – 10.00 ms | 0 | 0 |
| 10.00 ms – 20.00 ms | 0 | 0 |
| 20.00 ms – 50.00 ms | 0 | 0 |
| 50.00 ms – 100.00 ms | 0 | 0 |
| ≥ 100.00 ms | 0 | 0 |

Lateness = how late each scan started against its slot on the absolute schedule, cumulative since start, cold start included. Percentiles are bucket upper edges (≤ 3.2 % above the true value). Overruns = scans whose execution exceeded the period; missed = slots the loop skipped after falling a whole period behind.
