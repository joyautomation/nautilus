# Writer goldens: a task's dt-tag (#1310)

Two tasks, each with a dt-tag: `main` (100 ms, `MainDtS`, a REAL) and
`fast` (10 ms, `FastDt`, an LREAL). `Main` also reads `FastDt`, so that
tag is shared and `naut_OB_main` copies it in. `golden/` holds the output.

nautilus writes a task's dt-tag before the scan runs: the seconds since
the task's previous scan started, and the task's interval on its first
scan. Each OB does the same as its first statements: `RUNTIME` gives the
seconds since its previous call, kept in `naut_Clock` (NON_RETAIN, so it
resets when the CPU goes to RUN), and the first scan, or a return of zero
or less, writes the interval instead. In `naut_OB_fast` that comes before
the copy-in of the shared tags. See `siemens/writer/dt.go`.

Regenerate with `go test ./siemens/writer/ -update` and review the diff.

A REAL dt-tag holds the measurement rounded to single precision on the
CPU (0.1 is 0.1000000015); nautilus keeps a REAL tag's value as a float64.
That is so for every REAL tag, not only a dt-tag.

Compiled 0 errors, 0 warnings on TIA V21 through tiad. Run on the bench
S7-1500 (1510SP, FW V3.1, 2026-11-23 UTC) as a two-task probe of the same
shape, which recorded the dt-tags on the CPU itself; an upload read the
record back. 100 ms task (REAL): first scan 0.1, the next 600 between
0.0997 and 0.1003, mean 0.099999986. 10 ms task (LREAL): first scan 0.01,
the next 6007 between 0.0097 and 0.0103, mean 0.0100000. After a STOP to
RUN the first scan wrote the interval again.
