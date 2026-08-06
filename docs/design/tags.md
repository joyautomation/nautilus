# Design brief: tag generation, shape verification, and UDTs in the manifest

Status: **brief, not a design.** Findings are verified against the tree at
`040b2a4`; the recommendation in §2 is argued but the questions in §4 are open.

## 0. Why this exists

The manifest is the base architecture. Go is an SDK for custom field buses and
richer simulation. That split holds right up until a project has 500 tags, at
which point the manifest asks you to hand-type 500 lines and the only escape is
Go — which would mean the manifest is a demo format and Go is the real product,
exactly backwards.

Three things are wanted, and they are one thing:

1. **Write tag generators** — a plant has structural repetition (40 pumps × 8
   tags) and imported tag lists (a Logix controller, a Modbus map, a CSV from
   the integrator). Neither should be typed by hand.
2. **Verify the generated shape** — the reason hand-writing feels safer is that
   a generator can emit garbage silently. Whatever replaces hand-typing has to
   fail loudly.
3. **UDTs for type repetition** — one `Motor` type, N instances, rather than N×8
   flat names related only by a naming convention.

## 1. What is already true

This is the part that changes the design. Most of the machinery exists; the
manifest is the one place that cannot express it.

**1.1 The tag store already holds structs and arrays.** `ir.Value` carries
`Fld []Value` + `Struct *StructDef` for `TypeStruct` and `Arr []Value` for
`TypeArray`. The comment on `Value.Struct` says why it exists: *"so consumers
outside the VM (HMI JSON, field-bus drivers) can render or bind fields by
name."* Nothing needs inventing here.

**1.2 ST already declares UDT-typed globals.** `VAR_EXTERNAL PIT_001 :
Analog_Input;` lowers, member access resolves, and the LSP expands the type on
hover. The lowerer treats a global's type like any other declared type.

**1.3 A generation pipeline already exists, and it is the established
pattern.** `nautilus eip import` (`cmd/nautilus/eip.go`) writes two generated,
committed files, each carrying a *"Do not edit — re-run the import"* header:

| generated | contains |
|---|---|
| `eip_manifest.yaml` | the driver's tag bindings + UDT `TypeDef`s (230 entries in `examples/client60`) |
| `eip_types.st` | the same UDTs as ST `TYPE ... STRUCT` declarations, so programs can declare them |

So "generate a file, commit it, review the diff" is not a proposal. It is how
this codebase already handles imported tag lists.

**1.4 The EIP driver already delivers struct-valued tags.** `eip/driver.go:848`
assembles `ir.Value{Kind: ir.TypeStruct, Struct: sd, Fld: ...}` from flattened
leaf reads, under one nautilus tag name, and a test asserts the assembled value
carries its `StructDef` (`eip/driver_test.go:226`).

**1.5 The gap is exactly one file.** `nautilus eip import` generates the driver
manifest and the ST types, and then stops. The `tags:` block of `nautilus.yaml`
is still hand-written. `examples/client60/nautilus.yaml` transcribes 14 of the
230 by hand — and two of them are UDT tags with **no `init:`**, whose structure
is documented in an English prose `desc:` string:

```yaml
- { name: RTU60_13XFR9_GLV_001, role: input, desc: "Gate valve equipment descriptor
    (EQ_VLV_DSC_new UDT: OPNST/CLSST/TRNST, AUTOST, ANYFAULT, INTERLOCK, COSALM, …)" }
```

That line is the whole brief. The type is real, the driver delivers it, ST reads
its members — and the manifest can only describe it in a comment.

**1.6 Nothing verifies the manifest against the programs.** `runCheck`
(`cmd/nautilus/check.go`) walks `.st`/`.fbd`/`.ld`/`.sfc` files and compiles
them. It never reads `nautilus.yaml`. So a program may declare
`VAR_EXTERNAL Foo : REAL;` with no manifest entry and compile clean — it faults
at run time on a read-before-write, or silently becomes a tag with no unit and
no description in the HMI. There is no check in either direction.

**1.7 `TagConfig` is scalar-only.** `{Name, Role, Init any, Unit, Desc}`, with
the type *inferred from the seed's shape* (`iecTypeOf`: bool → BOOL, string →
STRING, number → REAL). There is no `type:` field, so a UDT tag is inexpressible
and a tag with no seed has no knowable type at all — which is why the LSP had to
learn to read declared globals out of the compiled programs (`Runtime.Globals`).

**1.8 There is no JSON Schema for `nautilus.yaml`.** One exists for
`*_test.yaml` (`tools/vscode-iec/schemas/`), with a Go guard test proving it
accepts and rejects exactly what the loader does. The manifest — the more
important file, and the one a generator would emit — has none.

## 2. Recommendation: generation, not templating

**Do not make this an SDK feature.** 500 tags is the ordinary case, not the
advanced one. If bulk tags require Go, the manifest cannot be the base
architecture.

**Do not put templating in the manifest either.** `docs/design/testing.md` §3
argued that a small frozen schema plus a real language as the pressure valve
beats growing a poor programming language one key at a time. That argument
transfers without modification. A `{{ range }}` in `nautilus.yaml` is the first
step to Helm, and the manifest's value *is* that it is dumb reviewable data —
the artifact that deploys and diffs.

So the split is:

- **The manifest stays data.** Its only new capability is composition: `tags:`
  may come from more than one file.
- **Generation is a build step**, in whatever language suits — TypeScript, Go,
  a spreadsheet export, awk. Output is committed and reviewed, exactly as
  `eip_types.st` already is.
- **Verification is downstream**, against the programs that consume the tags —
  which is strictly stronger than type-checking the generator, because it
  checks the thing that actually matters.

That last point reframes the request. A TypeScript type over the generator
proves the *object* is shaped right. It cannot prove the tag set matches the
logic. `nautilus check` can.

## 3. The pieces, in order of leverage

**3.1 A JSON Schema for `nautilus.yaml`, plus the Go guard.** Cheapest, helps
every generator in every language, and gives editor + CI validation of emitted
output. Follow the `*_test.yaml` precedent exactly, including
`acceptance/schema_test.go`-style guards — those caught three real divergences
between schema and loader last time, none of which were visible by inspection.

**3.2 `tags:` composable from multiple files.** Something like `tag-files: [...]`
or a `tags/*.yaml` convention. Small and orthogonal, and it is the actual
architectural move: it makes generated output a separate reviewable artifact
instead of a 500-line smear through the file a human edits.

**3.3 `nautilus check` reads the manifest.** Report both directions:

- a manifest tag no program binds — dead, or HMI/driver-only (warn);
- a program global no manifest declares — no unit, no description, faults on
  read-before-write (warn, with the read-before-write case arguably an error).

This is the shape verification, and it is the piece that makes generation safe
enough to trust. It should land *with* 3.2, not after: composition without
verification just moves the hand-typing.

**3.4 `type:` on a tag, naming a UDT.** Given 1.1–1.4, this is closing a seam
rather than building one. `- { name: PIT_001, type: Analog_Input, role: input }`
replaces both the prose `desc:` and the type inference. Also lets
`nautilus eip import` emit the `tags:` file directly, since it already knows
every binding's type.

**3.5 Extend `nautilus eip import` to emit the tags file.** After 3.2 and 3.4
this is nearly free, and it deletes the hand-transcription in §1.5.

## 4. Open questions

1. **Where does a generated tags file live, and what are the merge semantics?**
   An explicit `tag-files:` list, or a convention directory? Is a duplicate name
   across files an error (probably) or last-wins?
2. **Committed or generated in CI?** The `eip import` precedent says committed —
   the diff is the review, and a deploy shouldn't need a controller on the
   network. Worth stating explicitly rather than inheriting by accident.
3. **Where do UDT types for `type:` come from?** The project's ST `TYPE`
   declarations (creates a manifest → compiled-types load-order dependency), the
   eip manifest's `TypeDef`s, or a new manifest-level `types:`? The ST library
   is the honest answer — it is what the programs already use — but `Load`
   currently builds `runtime.Options` before anything is compiled.
4. **What is `init:` for a UDT tag?** Zero-of-type is the obvious default. Are
   per-field seeds needed, or is that a job for first-scan logic?
5. **Does `TagMeta` need to be per-field?** `P101.Speed` is in rpm and
   `P101.Running` is not. Today meta is keyed by tag name only, and the HMI
   table and `/api/meta` read it that way. This may be the largest hidden cost
   in 3.4.
6. **Roles for imported tags.** `eip.TagBinding.Writable` maps cleanly to
   output/input, so this is probably derivable — confirm rather than assume.
7. **Does `given:` in an acceptance test need to address a field?**
   `given: { PIT_001.AI: 12.5 }` — `expect` already resolves dotted names for
   `task.local`, so the syntax collides. Worth settling before UDT tags ship.

## 5. Acceptance bar

1. `examples/client60` declares its imported tags by generation, not
   transcription, and its two UDT tags carry a real `type:` instead of a prose
   `desc:`.
2. A project with 40 instances of one `Motor` UDT is expressible in a manifest a
   person can read on one screen.
3. `nautilus check` fails a project whose program reads a tag the manifest never
   declares, and says which.
4. A generated tags file that is malformed is rejected in the editor by the
   schema, before it is ever run.

## 6. Non-goals

- **No expression language in the manifest.** No loops, no conditionals, no
  interpolation. If it needs computing, compute it upstream and commit the
  output.
- **No runtime tag creation from the manifest.** The tag set is static at boot;
  a self-enumerating device is a driver concern.
- **Not deleting the Go tier.** Composing `[]runtime.TagDef` in Go stays fully
  supported. It just stops being the *only* answer to "I have a lot of tags."

## 7. Reference points

| What | Where |
|---|---|
| Manifest tag schema | `internal/project/project.go` (`TagConfig`, `Load`) |
| Tag defs → flat options | `runtime/tagdef.go` (`TagDef`, `expandTags`) |
| Struct/array values | `lang/ir/types.go`, `lang/ir/value.go` (`Value.Fld`, `Value.Struct`) |
| Struct assembly from a bus | `eip/driver.go:848`, `eip/leaves.go` (`expandLeaves`) |
| Existing generator | `cmd/nautilus/eip.go` (`import`), `examples/client60/*` |
| What `check` does today | `cmd/nautilus/check.go` (`runCheck`) |
| Schema + guard precedent | `tools/vscode-iec/schemas/nautilus-test.schema.json`, `acceptance/schema_test.go` |
| Tags the programs bind | `runtime.Runtime.Globals`, `lang/ir/program.go` (`Program.Globals`) |
