# Writer goldens: more than one task (coverage C, #1300)

Three tasks (`Main` 100 ms, `Fast` 10 ms, `Slow` 1 s) with the tags in
`tasksGlobals` and the struct in `types.st`. Each task becomes its own
cyclic interrupt OB: OB30, OB31, OB32. `golden/` holds the output.

A tag that only one task names (`Run`, `Local1`) stays in `naut_Tags`, and
its program reads and writes it there. A tag that two or more tasks name
is read and written in the task's copy in `naut_Scan_<task>`. The task's
OB copies those tags in before it calls the program. After the call it
writes back only what the scan assigned, as nautilus commits a scan
(runtime/scanview.go). That covers:

- a write the scan skips (`Pump.Speed` past a `RETURN`): it is not written
  back;
- a member (`Pump.Run`): written back alone;
- single bits of a word (`Status.3`, `.0`, `.7` in `Slow`): written back
  bit by bit, and the whole word when the scan assigned it whole;
- a timer's and a latch's output bindings (`Done`, `Alarm`): writes too;
- a program with no `VAR_EXTERNAL` (`Slow`), whose tags still count as
  shared.

The fixture also holds the start values B1 (#1299) writes but no TIA
compile had seen yet: `-5`, `1.0e+20`, `T#5s`. It also has a local named
`Name`, written `#"Name"`.

Regenerate with `go test ./siemens/writer/ -update` and review the diff.

Compiled 0 errors, 0 warnings on TIA V21 through tiad (a build-only
`naut siemens deploy --adopt last-deploy` on tia1, no CPU contact,
2026-11-23 UTC) as a three-task project. In the same session, an offline
Openness probe imported cyclic interrupt OBs 30, 31 and 123 the way tiad
makes them. Each got PriorityNumber 13, so no task OB preempts another,
and PriorityNumber can be written after the import. This is a compile
only: copies and flags have not yet been measured running on a CPU.
