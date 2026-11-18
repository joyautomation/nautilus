# Writer goldens: the bench program

`Main.st` is written with the bench's manifest tags (`writer_test.go`'s
`benchGlobals`) and one 100 ms task. `golden/` holds what the writer emits.

These files encode what TIA Portal's exported SCL sources look like, as far
as public knowledge goes. **They are not yet verified against a TIA compile.**
The first TIA session (S2b, docs/design/siemens-ide.md §7) imports them,
compiles them, and replaces any spelling TIA rejects with what its own
exports show. Points marked *verify* in the brief:

- the instance types `TON_TIME`, `CTU_INT` (or whether TIA takes the bare
  IEC names);
- `MIN`/`MAX`/`LIMIT`/`SEL` with named parameters;
- `VAR RETAIN` sections in an FB and a global DB;
- a cyclic interrupt OB's cycle, which SCL source does not carry (tiad sets
  it from `Output.OBs`).

Regenerate with `go test ./siemens/writer/ -update` and review the diff.
