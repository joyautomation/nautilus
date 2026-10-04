## Scan lateness — mira1, 2026-10-03 18:29

Intel(R) Core(TM) i9-14900K · amd64 · kernel 6.16.3-76061603-generic · go1.25.5 · GOMAXPROCS=32 of 32 · GOGC=100 · governor powersave

Ran 1m0s. Load average 4.14 4.77 5.46 4/14304 2533983 → 3.68 4.56 5.35 4/14296 2537088. desktop in use; preliminary 60 s runs; AFTER absolute-deadline loop + clock_nanosleep on a locked thread, timer slack 1 µs.
Tasks: main 1ms (loopback I/O), fast2 10ms, alloc 50ms (200 string concats).

| task | target | scans (ran / due) | late (> target + thr) | overruns | missed | p50 | p99 | p99.9 | max |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| main | 1 ms | 59992 / 60001 | 166 (0.28 %, thr 100 µs) | 12 | 9 | 6.2 µs | 29.5 µs | 376 µs | 3.39 ms |
| fast2 | 10 ms | 6000 / 6000 | 0 (0.00 %, thr 1 ms) | 0 | 0 | 9.0 µs | 37.0 µs | 368 µs | 824 µs |
| alloc | 50 ms | 1200 / 1200 | 0 (0.00 %, thr 5 ms) | 0 | 0 | 10.5 µs | 64.0 µs | 912 µs | 2.30 ms |

GC: 81 collections, 5.1 ms total stop-the-world, pause p50 28.7 µs · p99 164 µs · max 197 µs, heap 2.6 MB.

Lateness histogram (scans per bucket, cumulative):

| bucket | main | fast2 | alloc |
|---|---:|---:|---:|
| < 1.0 µs (incl. early) | 0 | 0 | 0 |
| 1.0 µs – 2.0 µs | 0 | 0 | 0 |
| 2.0 µs – 5.0 µs | 12982 | 101 | 2 |
| 5.0 µs – 10.0 µs | 38021 | 3878 | 511 |
| 10.0 µs – 20.0 µs | 7100 | 1560 | 558 |
| 20.0 µs – 50.0 µs | 1607 | 424 | 116 |
| 50.0 µs – 100 µs | 115 | 21 | 7 |
| 100 µs – 200 µs | 65 | 7 | 0 |
| 200 µs – 500 µs | 73 | 5 | 3 |
| 500 µs – 1.00 ms | 24 | 3 | 1 |
| 1.00 ms – 2.00 ms | 3 | 0 | 0 |
| 2.00 ms – 5.00 ms | 1 | 0 | 1 |
| 5.00 ms – 10.00 ms | 0 | 0 | 0 |
| 10.00 ms – 20.00 ms | 0 | 0 | 0 |
| 20.00 ms – 50.00 ms | 0 | 0 | 0 |
| 50.00 ms – 100.00 ms | 0 | 0 | 0 |
| ≥ 100.00 ms | 0 | 0 | 0 |

Lateness = how late each scan started against its slot on the absolute schedule, cumulative since start, cold start included. Percentiles are bucket upper edges (≤ 3.2 % above the true value). Overruns = scans whose execution exceeded the period; missed = slots the loop skipped after falling a whole period behind.
