# L5X fixtures

Three files, and what each one is for.

## `DemoProgram.L5X` — a partial export, verbatim

A Studio 5000 export of one program (`TargetType="Program"`), byte for byte
as Logix Designer wrote it. This is the shape File → Import takes, and the
shape `partial_import_from_xml_file` takes: a target nested inside a
`Controller` marked `Use="Context"`. It has no BOM, because a partial
export does not carry one.

## `demoline.L5X` / `demoline.v80.L5X` — a real pair, trimmed

Two exports of the same demo project differing by exactly one setpoint
(`HiLevelSP`, 85.0 → 80.0). They are the normalizer's whole reason to
exist: the difference an engineer needs to see is two lines, and one of
those is the redundant L5K copy of the same value.

Trimmed from the originals (1.8 MB each) by dropping all but two entries
of `<DataTypes>` — a controller export ships hundreds of module-defined
and product-defined shapes, none of them project code, and they were
almost the entire file. Everything retained is the exporter's own bytes,
BOM included.

Their `ExportDate` and `LastModifiedDate` already read `(pinned)`: the
capture these came from was normalized by the Python spike that measured
the export-determinism result in `docs/design/logix-target.md` §11. That
is why the normalizer's unit test builds its own input instead of using
these, and these cover the part they can still prove — that a real change
survives normalization.

The originals live in the content repo, at
`assets/capture/n26/fixtures/`.

## `variety.L5X` — hand-built coverage

Not an export. Every shape the reader has to survive, in one small file,
each of them taken from something the development corpus actually
contained:

- a UDT with a nested UDT, an array member, a `STRING` member, and BOOLs
  exported the way Logix exports them — `BIT` members overlaying a hidden
  `SINT` host
- a member named `retain`, which is an IEC keyword and a parse error until
  it is renamed. Four of the 52 files in the development corpus had one.
- a member of `REF_TO_AXIS_VIRTUAL`, a firmware structure with no public
  shape, which must be omitted rather than guessed at
- tags: documented and undocumented, an alias, a `Constant`, an array, a
  `TIMER` (a predefined type no export declares), a `MESSAGE` (no public
  shape), and a program-scoped one
- an operand comment, which is per-bit documentation a browse cannot see
- rungs: nested branches, a one-shot, `?` for an unset operand, a `CPT`
  carrying a whole expression, parallel outputs written as a branch of
  coils, an Add-On Instruction call, and a bare `NOP()`
- an ST routine, to prove it is captured rather than lost

## The corpus that is not here

The reader was developed against ~60 real exports totalling ~30 MB, most
of it client work that cannot be committed. `corpus_test.go` runs the
whole reader over a directory of them on demand:

    NAUTILUS_L5X_CORPUS=/path/to/exports go test ./lang/l5x/
