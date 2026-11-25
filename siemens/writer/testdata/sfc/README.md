# Writer goldens: SFC (#1302)

One fixture per `lang/conformance/sfc-*` chart, written as the CLI hands
it to the writer (one main task, the tags typed by the chart's
VAR_EXTERNAL, no start values). `<chart>/golden/` holds the output. A
chart that changes fails `TestSFCGolden`: regenerate, review the diff,
and compile it again.

Regenerate with `go test ./siemens/writer/ -update` and review the diff.

Compiled 0 errors, 0 warnings on TIA V21 through tiad on tia1, no CPU
contact, 2026-11-25 UTC, each chart on its own, in the order Tags, the
program's FB, its instance DB, the OB (declared as a cyclic interrupt
OB35 at 50 ms).

The bench trace compare (all nine charts against nautilus, scan by scan,
plus STOP to RUN and an online edit) is in docs/design/siemens-ide.md,
"SFC (E, #1302)".
