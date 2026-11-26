# Writer goldens: the project's blocks (coverage D1, #1301)

`lib.st` is a project library: global constants (`VAR_GLOBAL CONSTANT`),
two FUNCTIONs (one with a local and a RETURN) and two FUNCTION_BLOCKs,
`Debounce` inside `Station`. `Station` has every kind of variable: inputs,
outputs, an in-out, statics (a nested block instance named `db`, an
R_TRIG, a TON called without PT), a temp with an initial value and a
`VAR CONSTANT`, and uses the global constants as a FOR bound and in
comparisons. `Main.st` declares two instances of `Station`, one bound to
tags and one to a local, and calls the FUNCTIONs positionally and
formally. `golden/` holds the output (globals: `pousGlobals`).

Each FUNCTION_BLOCK is an FB used as a multi-instance in its caller; each
FUNCTION an FC whose body sets every local first and copies its result to
the return value at the end and at each RETURN. Blocks come callees first
(TIA generates a block from source only once the blocks it instantiates
exist). A global constant is a `VAR CONSTANT` copy in each block that
names one.

Regenerate with `go test ./siemens/writer/ -update` and review the diff.

Compiled 0 errors on TIA V21 through tiad on tia1, no CPU contact,
2026-11-23 UTC (one warning: `#Count` is read before the block sets it,
as nautilus allows), by importing the generated sources in the order
`Write` returns them into a scratch project. The same run compiled the
well-site, epic-bench, plcnext-bench and remote-fleet well-1 examples and
the dialect-logix-tonr conformance project 0/0. What the runs measured:

- `#x.a := 5`, `#x.q`, nested multi-instances and two calls of one
  instance a scan compile; so do an FC's `#naut_F := …`, reading it back,
  and a temp named like the function.
- An FB generated before the FB it instantiates fails ("Data type
  "naut_X" is unknown"): hence callees first.
- TIA wants PT at every timer call and PV at every counter call
  ("Parameter 'PT' has to be used"): a call that leaves one out passes the
  instance's own (`PT := #t.PT`).
- An instance named `db` fails the import, and so does a tag named with
  two underscores in a row (`EPIC__Online`): both are written quoted.
- A start value given to a multi-instance of a project block (`i :
  "naut_X" := (PT := T#2s)`) is dropped silently by the import (nautilus
  has no instance initializers, so nothing is written that way).
- A CTD (`CTD_LINT`, `CTD_DINT` or `CTD_INT`) does not compile as written
  ("Invalid data type"), and CTU's `CTU_LINT` has no `.Q` either. Fixed
  in #1338/#1371: every counter is an `IEC_LCOUNTER` called by its method
  (`../instances/README.md`).
