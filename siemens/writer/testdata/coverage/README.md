# Writer goldens: coverage PR A

`Main.st` holds one of each construct writer-coverage PR A added (#789):
bitwise operators on integers and bit strings, bit access, the guarded
divisions (an FC per type that answers 0 for a zero divisor, as nautilus
does), TRUNC and the overloaded TO_* conversions, MIN/MAX of more than two,
MUX, the shifts and rotates, named arguments put in order, and SR/RS kept
as a static STRUCT and written inline. It is written with three manifest
tags (`coverageGlobals`) and one 100 ms task; `golden/` holds the output.

Regenerate with `go test ./siemens/writer/ -update` and review the diff.

Compiled 0 errors, 0 warnings on TIA V21 through tiad (a build-only
`naut siemens deploy --adopt last-deploy` on tia1, no CPU contact,
2026-11-23 UTC, #789), together with the corpus projects this PR made
pass (st-div-zero, st-mux-fault, st-xor, bin-fill). Compile only: the
runtime behaviour on a CPU (INT_TO_WORD as a bit copy, TRUNC_LINT toward
zero, MOD's sign) is TIA's documented behaviour, not yet measured.
