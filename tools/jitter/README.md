# jitter — scan-loop lateness harness

Runs a small, deliberately mixed resource on the real `runtime.Run` tickers
for a fixed time and reports every task's **wake-up lateness** — how far
after its period each scan actually started — plus the Go collector's
stop-the-world pauses, with the machine written down beside the numbers.

Every measurement in `docs/design/realtime.md` comes from this program.
Run it the same way on the same box before and after a change and the two
reports are the before/after.

```sh
go run ./tools/jitter -duration 5m -out /tmp/jitter-idle          # the full mix
go run ./tools/jitter -duration 5m -slow 0 -alloc 0 -out …        # fast tasks alone
go run ./tools/jitter -duration 5m -slow 0 -out …                 # fast + allocation task
go run ./tools/jitter -help
```

The resource:

| task  | default period | what it does |
|-------|---------------:|--------------|
| main  | 1 ms   | a PI loop on loopback (Memory driver) I/O — the real read → execute → write path, outputs changing every scan |
| fast2 | 10 ms  | a ramp on the shared tag store, no I/O |
| slow  | 100 ms | a FOR loop calibrated at startup to `-slow-ms` (20 ms) of VM time — it holds the scan lock longer than the fast tasks' period |
| alloc | 50 ms  | `-alloc-iters` string concatenations — garbage for the collector to find on the fast tasks' time |

`-churn-mb` adds Go-side allocation churn from outside the scan loop (what
a web server or historian in the same process would do). `-listen`
(default on) holds a loopback socket so the Go poller is live, as it is
under `naut run`.

Output: a Markdown report on stdout and, with `-out DIR`, `report.json` +
`report.md`. The report records hostname, CPU, kernel (and whether it is
PREEMPT_RT), Go version, `GOMAXPROCS`, `GOGC`, governor, load average
before and after, and per task: target, scans ran vs due, late count and
threshold, overruns, p50/p99/p99.9/max lateness, and the lateness
histogram.

Definitions (the same ones `runtime.Lateness` uses):

- **lateness** = period − target for each scan, cumulative since start,
  cold start included. Percentiles are bucket upper edges, at most 3.2 %
  above the true value — a quoted p99 is a bound.
- **late** = lateness > threshold (`-threshold`, default a tenth of the
  task's period).
- **overruns** = scans whose execution took longer than the period.

Stdlib only, like the runtime it measures.
