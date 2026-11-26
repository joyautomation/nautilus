# Writer goldens: the bench program

`Main.st` is written with the bench's manifest tags (`writer_test.go`'s
`benchGlobals`) and one 100 ms task. `golden/` holds what the writer emits.
The goldens are written with the default block prefix `naut_` (#789): the
program `Main` becomes FB `"naut_Main"`, so it never takes TIA's `"Main"` [OB1].
Compiled 0/0 on TIA V21 through tiad, 2026-11-22 (#789); `naut_OB_main`
still becomes a program cycle OB until tiad makes the cyclic interrupt OB first.

These files encode what TIA Portal's exported SCL sources look like, as far
as public knowledge goes. **They are not yet verified against a TIA compile.**
The first TIA session (S2b, docs/design/siemens-ide.md §7) imports them,
compiles them, and replaces any spelling TIA rejects with what its own
exports show. Points marked *verify* in the brief:

- the instance types: `TON_TIME` compiles; the counters' `CTU_LINT` does
  not (its `.Q` is "not defined"), so a counter is an `IEC_LCOUNTER` called
  by its method (#1338, #1371, `../instances/README.md`). Whether the
  counter saturates only at the LINT limit is not measured (nautilus's CV
  is its 64-bit int);
- ~~that S7-1500 IEC timers update `Q`/`ET` when read~~: they do, on
  every read (bench 1510SP, #1385), so a timer's `Q`/`ET` are latched in
  a static after each call and every read reads the latch;
- `MIN`/`MAX`/`LIMIT`/`SEL` with named parameters;
- `VAR RETAIN` sections in an FB and a global DB;
- the cyclic interrupt OB: that an `ORGANIZATION_BLOCK` source becomes a
  cyclic interrupt OB (event class) with number 30, and its cycle, which
  SCL source does not carry (tiad sets it from `Output.OBs`);
- integer arithmetic: that `LINT` arithmetic wraps on overflow like
  nautilus's 64-bit ints, and that `LINT_TO_INT` and friends are exact for
  in-range values (the writer wraps into range first; `types_test.go`
  proves the wrap formula itself);
- a known divergence, not refused: a REAL → integer conversion out of range
  faults the scan in nautilus, while the CPU sets ENO FALSE and carries on.

Regenerate with `go test ./siemens/writer/ -update` and review the diff.
