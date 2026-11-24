# Writer goldens: every instance the writer knows

`main.st` declares one instance of each IEC function block the writer
writes (`instances` and `latches` in the writer, `instMembers` in
`types.go`) and calls each the way programs do: every input given, inputs
left out, outputs read as members and bound with `=>`, a counter's PV from
a LINT variable. `golden/` holds what the writer emits, with the tags `Go`,
`Out` (BOOL) and `N` (DINT), one 100 ms task and the block prefix `naut_`.

These exact files compiled 0/0 on TIA V21 through tiad (offline, imported
into a throwaway project), 2026-11-24 (#1338, #1371). The same program
written by the writer before that fix failed: CTU's and CTD's `Q` were
"not defined" on `CTU_LINT`/`CTD_LINT`, which TIA also refuses with a LINT
variable as PV in other call shapes. A counter is now an `IEC_LCOUNTER`
called by its `CTU`/`CTD`/`CTUD` method.

The counters' behaviour was checked on the bench 1510SP the same day (LD,
rising edges only, Q thresholds, counting below zero, R first, and an input
left out of a call keeping its last value through the counter's member):
it matches nautilus's `lang/ir/builtins_fb.go`.

Change the writer's instance spelling, or add an instance type, only with
a new TIA compile of this golden. `TestInstancesGolden` fails when a type
in the writer's maps is missing from `main.st`.
