# testdata/projects

Frozen test fixtures, not examples. Go ignores `testdata` directories, so
this is the one shared copy the test suites below read from; nothing under
`examples/` is a build or test dependency any more, and `examples/` is free
to be rewritten (or deleted) without breaking any of these.

Each of these is a snapshot of a manifest project (or, for
`tank-batch-sfc/program.sfc`, a single chart file), copied once from the
`examples/` project it was cut from — minus the example's own `README.md`,
which is documentation for a human reading `examples/`, not part of the
fixture. Do not "fix" one of these to match a later edit to the `examples/`
project it came from; the whole point is that it stays put.

- `heated-tank/` — the flagship manifest project: four tasks, three IEC
  languages (FBD, ST, LD), a plant simulated in ST, and an acceptance test
  suite (`heated-tank_test.yaml`). Cut from `examples/heated-tank-nogo`.
  Used by:
  - `acceptance/heated_tank_test.go`
  - `internal/lsp/testdoc_test.go`
  - `tools/vscode-iec/.vscode/launch.json` (F5 "Run Extension" workspace)

- `ladder-subroutines/` — two instances of one ladder block, each with its
  own retained state; the end-to-end proof that a `PROGRAM`-less `.ld` file
  is a library like a `.st` one. Cut from `examples/ladder-subroutines`.
  Used by:
  - `internal/project/ldlib_test.go`

- `tank-batch-sfc/program.sfc` — an SFC chart with a divergence and an
  aborted branch, used to property-test selection operations (copy, cut,
  paste) over a real chart. Cut from `examples/tank-batch-sfc`.
  Used by:
  - `lang/sfc/selection_property_test.go`
