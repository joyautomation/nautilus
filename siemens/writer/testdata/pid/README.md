# Writer goldens: nautilus's PID (coverage D2, #1301)

`Main.st` uses nautilus's PID four ways: `lic` bound to tags with every
pin, DT from the task's dt-tag and an `=>` output; `pic` called only when
`Every3` is TRUE, DT unbound; `heater`, the library's `Loop` (`lib.st`),
which holds a PID of its own; and `hold`, declared `VAR RETAIN`.
`golden/` holds the output (globals: `pidGlobals`).

`naut_PID.scl` is `siemens/writer/pid.scl` behind the block's name. Each
PID is a multi-instance in the block that declares it; `hold` is written
in the program's plain VAR section with a comment, since nautilus starts
a PID over after a restart and TIA would keep one held in a RETAIN
section.

Regenerate with `go test ./siemens/writer/ -update` and review the diff.

Compiled 0 errors on TIA V21 through tiad on tia1, no CPU contact,
2026-11-25 UTC, in the order Tags, Clock, PID, Loop, Main, Main_DB, OB.
Its 4 warnings ("The sign or the accuracy of the value may be lost") are
the REAL tags that read an LREAL output (`Valve`, `Pump`, `Held`) and
`Loop`'s REAL `Out`. The bench trace compare is in
docs/design/siemens-ide.md, "PID (D2, #1301)".
