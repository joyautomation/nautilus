## Scan lateness — mira1, 2026-10-03 22:29

Intel(R) Core(TM) i9-14900K · amd64 · kernel 6.16.3-76061603-generic · go1.25.5 · GOMAXPROCS=32 of 32 · GOGC=100 · governor powersave

Ran 1m0s. Load average 3.32 3.58 4.28 7/14343 3349218 → 5.51 4.04 4.38 4/14352 3352313. desktop in use; items 1-3 in; setcap cap_sys_nice+ep on the harness; A-pcore2-fifo50.
Tasks: main 1ms (loopback I/O), fast2 10ms; main task pinned to cpu 2; main task SCHED_FIFO 50.

| task | target | scans (ran / due) | late (> target + thr) | overruns | missed | p50 | p99 | p99.9 | max |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| main | 1 ms | 60001 / 60001 | 16 (0.03 %, thr 100 µs) | 0 | 0 | 4.5 µs | 20.0 µs | 56.0 µs | 319 µs |
| fast2 | 10 ms | 6000 / 6000 | 1 (0.02 %, thr 1 ms) | 0 | 0 | 12.8 µs | 52.0 µs | 448 µs | 2.10 ms |

GC: 1 collections, 0.1 ms total stop-the-world, pause p50 20.5 µs · p99 49.2 µs · max 49.2 µs, heap 1.0 MB.

Lateness histogram (scans per bucket, cumulative):

| bucket | main | fast2 |
|---|---:|---:|
| < 1.0 µs (incl. early) | 0 | 0 |
| 1.0 µs – 2.0 µs | 9695 | 0 |
| 2.0 µs – 5.0 µs | 23415 | 110 |
| 5.0 µs – 10.0 µs | 20248 | 2130 |
| 10.0 µs – 20.0 µs | 6086 | 2097 |
| 20.0 µs – 50.0 µs | 479 | 1600 |
| 50.0 µs – 100 µs | 61 | 40 |
| 100 µs – 200 µs | 12 | 13 |
| 200 µs – 500 µs | 4 | 5 |
| 500 µs – 1.00 ms | 0 | 3 |
| 1.00 ms – 2.00 ms | 0 | 0 |
| 2.00 ms – 5.00 ms | 0 | 1 |
| 5.00 ms – 10.00 ms | 0 | 0 |
| 10.00 ms – 20.00 ms | 0 | 0 |
| 20.00 ms – 50.00 ms | 0 | 0 |
| 50.00 ms – 100.00 ms | 0 | 0 |
| ≥ 100.00 ms | 0 | 0 |

Lateness = how late each scan started against its slot on the absolute schedule, cumulative since start, cold start included. Percentiles are bucket upper edges (≤ 3.2 % above the true value). Overruns = scans whose execution exceeded the period; missed = slots the loop skipped after falling a whole period behind.
