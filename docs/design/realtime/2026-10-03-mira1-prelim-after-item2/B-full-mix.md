## Scan lateness — mira1, 2026-10-03 19:43

Intel(R) Core(TM) i9-14900K · amd64 · kernel 6.16.3-76061603-generic · go1.25.5 · GOMAXPROCS=32 of 32 · GOGC=100 · governor powersave

Ran 1m0s. Load average 4.52 4.88 5.26 4/14316 2789662 → 3.68 4.57 5.12 5/14308 2792923. desktop in use; preliminary 60 s runs; AFTER item 1 (deadline loop + clock_nanosleep) AND item 2 (per-task scan isolation).
Tasks: main 1ms (loopback I/O), fast2 10ms, slow 100ms (≈20 ms of logic, 86813 iterations), alloc 50ms (200 string concats).

| task | target | scans (ran / due) | late (> target + thr) | overruns | missed | p50 | p99 | p99.9 | max |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| main | 1 ms | 59974 / 60001 | 477 (0.80 %, thr 100 µs) | 30 | 27 | 6.8 µs | 72.0 µs | 624 µs | 2.48 ms |
| fast2 | 10 ms | 6000 / 6000 | 1 (0.02 %, thr 1 ms) | 0 | 0 | 9.8 µs | 86.0 µs | 496 µs | 1.91 ms |
| slow | 100 ms | 599 / 600 | 0 (0.00 %, thr 10 ms) | 0 | 0 | 12.5 µs | 82.0 µs | 1.34 ms | 1.34 ms |
| alloc | 50 ms | 1200 / 1200 | 0 (0.00 %, thr 5 ms) | 0 | 0 | 12.5 µs | 88.0 µs | 256 µs | 445 µs |

GC: 3889 collections, 316.7 ms total stop-the-world, pause p50 16.4 µs · p99 229 µs · max 2.10 ms, heap 3.3 MB.

Lateness histogram (scans per bucket, cumulative):

| bucket | main | fast2 | slow | alloc |
|---|---:|---:|---:|---:|
| < 1.0 µs (incl. early) | 0 | 0 | 0 | 0 |
| 1.0 µs – 2.0 µs | 0 | 0 | 0 | 0 |
| 2.0 µs – 5.0 µs | 13136 | 93 | 1 | 0 |
| 5.0 µs – 10.0 µs | 30600 | 3184 | 211 | 307 |
| 10.0 µs – 20.0 µs | 12197 | 1740 | 223 | 612 |
| 20.0 µs – 50.0 µs | 3291 | 890 | 155 | 259 |
| 50.0 µs – 100 µs | 272 | 48 | 4 | 13 |
| 100 µs – 200 µs | 200 | 12 | 2 | 6 |
| 200 µs – 500 µs | 194 | 27 | 1 | 2 |
| 500 µs – 1.00 ms | 59 | 4 | 0 | 0 |
| 1.00 ms – 2.00 ms | 17 | 1 | 1 | 0 |
| 2.00 ms – 5.00 ms | 7 | 0 | 0 | 0 |
| 5.00 ms – 10.00 ms | 0 | 0 | 0 | 0 |
| 10.00 ms – 20.00 ms | 0 | 0 | 0 | 0 |
| 20.00 ms – 50.00 ms | 0 | 0 | 0 | 0 |
| 50.00 ms – 100.00 ms | 0 | 0 | 0 | 0 |
| ≥ 100.00 ms | 0 | 0 | 0 | 0 |

Lateness = how late each scan started against its slot on the absolute schedule, cumulative since start, cold start included. Percentiles are bucket upper edges (≤ 3.2 % above the true value). Overruns = scans whose execution exceeded the period; missed = slots the loop skipped after falling a whole period behind.
