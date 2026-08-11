# Design brief: tag generation, shape verification, and UDTs in the manifest

Status: **built.** All six pieces of §8 are implemented and tested; §2 argues
the approach and §4 answers every question it opened. Findings were verified
against the tree at `040b2a4`; notes marked *Built* record where the
implementation sharpened a decision.

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

### 2.1 One mechanism, and why the second one was refused

**Settled first, in case the rest reads as deliberation: there is exactly one
mechanism, and this section is a negative result.** It asks whether the two
sources of bulk tags in §0 need different machinery, and concludes they do
not. Nothing here adds a feature.

*Generator*, *codegen*, and *importer* all name the same thing in this doc,
and using them interchangeably above was sloppy. Precisely:

> **A generator is a program that reads something and writes a `tags/*.yaml`
> file, which you commit.** `nautilus eip import` is one. A twenty-line
> spreadsheet script is one. Nothing distinguishes them but who wrote them and
> what they read.

Everything downstream is identical either way: the file is committed with a
*do not edit* header, listed in `tag-files:` (3.2), validated by the schema
(3.1), and checked against the programs (3.3).

Three kinds, and they stay named separately because they are separate tools:

| | **protocol** | **structural** | **csv** |
|---|---|---|---|
| source of truth | a live device | the project's own intent | an existing spreadsheet |
| input | discovery — browse the controller | a compact config table (§2.3) | exported rows + a column mapping |
| example | `nautilus eip import` | `tools/tags.ts` | `nautilus tags import-csv` |
| roles / types | derivable (§4.6) | authored | mapped from columns |
| regenerating tells you | the PLC program drifted | nothing — you edited the input | the export changed |
| shipped by nautilus | yes | no — project-shaped | yes |

**Why csv is its own tool and not a swap of structural's front end.** A
project's config table is bespoke, so an expander over it can only ever live
in that project — which is why nautilus should not ship one. CSV is the
opposite: the *format* is standard even though every plant's columns differ,
so a shipped tool has a stable contract and only needs a column mapping to
fit any project. That is exactly the line between what belongs in the tool and
what belongs in the repo.

**All three are codegen, and that split is not the one that decides anything.**
Each writes committed source with a *do not edit* header. What decides is
**whether an upstream source exists** — and that cuts across the table, not
along it: protocol and csv both have one, structural often does not.

A committed generated file earns its keep when its input is *external and
unavailable at deploy time*. Nobody reconstructs `eip_manifest.yaml` without
the controller on the network, so committing it is what makes the deploy
self-contained and the diff report news. **But a plant's 40 pumps almost
always have an upstream source too** — an equipment list, a P&ID export, a
spreadsheet from the integrator — living in somebody's email rather than the
repo. That is the same category: external input, committed output, and the
regenerate-and-diff loop works identically. Codegen is right for it.

The one case that looks badly served is repetition with **no upstream
source**, where the instance list exists only in the engineer's head: there,
demanding a generator seems to mean inventing an input file whose sole purpose
is to be expanded. **§2.3 dissolves this**, and it is worth reading before
acting on this paragraph. That invented file turns out to be a compact config
table — the form people reach for anyway, and a better authoring surface than
the expanded tags it produces. Codegen wins there too, and this was the
weakest objection to it, not the strongest.

**So: yes, codegen for structural too.** What nautilus should *not* do is ship
the generators. The eip importer belongs in the tool because the protocol is
nautilus's business; an expander over a project's own config table is twenty
lines and shaped entirely by that project. **Ship the contract instead** —
which is precisely 3.1's argument, and the reason it is listed first for
leverage. A JSON Schema plus the Go guard makes a generator in *any* language
safe, and is worth more than any one generator nautilus could bundle.

**On §5.2, be honest that it passes by indirection.** `type:` (3.4) is the
real win — 40 motors go from 320 hand-typed lines to 40 — but 40 lines is not
one screen, and `tag-files:` does not shrink them, it relocates them.

The residual no-upstream case has a cheap answer if it ever bites: a
list-valued `name:`.

```yaml
- { name: [P101, P102, P103, P104], type: Motor, role: input }
```

The test for data versus templating is whether **every name still appears
literally in the file**. `[P101, P102]` passes — greppable, diffable, no
evaluation. `P{{ i }}` fails and stays refused. It adds no key, reuses
`role`/`type`/`init`/`tag-meta` unchanged, and expands in `tagDefs` beside the
expansion `expandTags` (`runtime/tagdef.go:94`) already does at load.

**Recommendation: defer it.** Ship `type:` + `tag-files:` + `check`, and see
whether anyone actually hits 40 instances with no upstream list. Widening
`name` from string to string-or-list is backward compatible in both the schema
and the loader, so it composes later at no cost — and a generator can emit the
list form too, if the compressed output ever reads better than the expanded
one. Deferring also keeps §3.1's schema frozen for its first release, which is
when a frozen schema is worth the most.

One wording fix while here: calling generation "a build step" above is loose.
`eip import` is an occasional authoring action, not something CI runs — closer
to `go generate`. Nautilus has no pre-deploy build step for tags today, and
this design does not add one.

**A third option, named to be set aside.** Tags could be derived from the
programs themselves — `Runtime.Globals()` already yields name → type for every
`VAR_EXTERNAL` (`runtime/runtime.go:248`). But a program does not know whether
`PIT_001` is an input or a setpoint, and carries no unit or description, so
this can only ever scaffold stubs a person then fills in. The same information
is worth more pointed the other way: as `nautilus check` (3.3), verification
rather than generation.

### 2.2 What authoring actually looks like

40 pumps, each a `Motor`, plus the derived tags each one needs. The exemplar
is a **compact config table and an expander** — §2.3's idiom, which is how this
is really authored. A spreadsheet is a variant of step 2, not the default.

**1 — the type, once, in ST.** A root `.st` file with no `PROGRAM` is a
library (`project.go:316`), so it is in scope for every task's compile and
`type:` resolves against it (§4.3).

```
(* motor.st *)
TYPE Motor : STRUCT Running : BOOL; Fault : BOOL; Speed : REAL; Hours : REAL; END_STRUCT; END_TYPE
```

On an eip project you do not write this — `eip import` emits `eip_types.st`.

**2 — the config tables.** The varying fields only. Two small tables, crossed:

```ts
// tools/tags.ts
const pumps = [
  { id: "P101", desc: "Filter backwash pump 1" },
  { id: "P102", desc: "Filter backwash pump 2" },
  // … 38 more
];
const perPump = [
  { suffix: "RunHrs",  role: "state",    init: 0.0,  unit: "h", desc: "run hours" },
  { suffix: "StartSP", role: "setpoint", init: 45.0, unit: "%", desc: "start speed" },
];
```

Forty-two authored lines describe 120 tags. That ratio is the point, and it is
why the input shape matters more than the output mechanism.

**3 — the expander.** A `flatMap`, and a write. Boilerplate — `role`, `type`,
the absent `init` — is supplied here rather than typed 120 times:

```ts
const tags = pumps.flatMap((p) => [
  { name: p.id, type: "Motor", role: "input", desc: p.desc },
  ...perPump.map((d) => ({
    name: `${p.id}_${d.suffix}`, role: d.role, init: d.init,
    unit: d.unit, desc: `${p.desc} — ${d.desc}`,
  })),
]);
Deno.writeTextFile("tags/pumps.yaml", header + stringify(tags));
```

```yaml
# tags/pumps.yaml — generated by tools/tags.ts. Do not edit; re-run it.
- { name: P101, type: Motor, role: input, desc: "Filter backwash pump 1" }
- { name: P101_RunHrs, role: state, init: 0.0, unit: h, desc: "Filter backwash pump 1 — run hours" }
- { name: P101_StartSP, role: setpoint, init: 45.0, unit: "%", desc: "Filter backwash pump 1 — start speed" }
```

Two things to notice. `desc:` is emitted **here**, composed from the table —
`tag-meta:` (§4.1) is only for generators whose *source* cannot supply
documentation, which is the eip importer's problem and not a config table's.
And the `Motor` tag carries no `init:`, because `role: input` with a `type:`
is deliberately unseeded (§4.4), while the derived state and setpoint tags
need theirs.

**When the table already exists in a spreadsheet**, steps 2 and 3 are replaced
by the csv tool (§2.1) rather than hand-written: it reads the export through a
column mapping and emits the same `tags/*.yaml`. Commit the export as
provenance. Steps 1, 4, and 5 are unchanged either way — which is the point of
having one output format.

**4 — the manifest.** One line for the generated set; hand-authored tags stay
where a person edits them.

```yaml
tag-files: [tags/pumps.yaml]
tags:
  - { name: ResLowSP, role: setpoint, init: 5.0 }
```

**5 — verify.** `nautilus check` validates the file against the schema (3.1)
and both directions against the programs (3.3). A fat-fingered id surfaces
here as *"no program binds P1O1"*, not as a silent dead tag.

**Re-running** is the loop the whole design is shaped around: add a row, re-run
the expander, and the diff is three lines. `check` then says whether the logic
actually handles them. Adding a *derived* tag to all 40 pumps is one row in
`perPump` and a 40-line diff that is trivially reviewable because every line
has the same shape.

**The part this design does not solve — say it plainly.** The manifest now
holds 40 pumps in one generated file, but the *program* still has to bind
them: 40 `VAR_EXTERNAL` entries, and 40 FB instantiations if the per-pump
logic is an FB. Nothing here shortens that. Two honest notes:

- The same generator can emit the `VAR_EXTERNAL` block, and **`eip import`
  already does exactly this** — a ready-to-paste block in a comment
  (`eip/codegen/codegen.go:334,359`). It is a comment because `VAR_EXTERNAL`
  is per-POU and the POU is hand-authored, so it cannot be a separate
  generated file.
- `ARRAY[1..40] OF Motor` collapses the ST side to one declaration and is
  fully supported by the parser and lowerer (`lower.go:435`, and
  `eip_types.st:211` uses arrays). But it makes the manifest hold **one** tag
  named `Pumps`, not forty named `P101…P140` — losing the names the plant,
  the HMI, and Sparkplug all address equipment by. Arrays are right for
  homogeneous data and wrong for named equipment. §4.3 settled `type:` for UDT
  names only; whether `type:` accepts an array type is **open**, and only
  worth opening if a project wants array-shaped tags for their own sake.

### 2.3 Prior art: tentacle's variable generators, and a correction

`tentacle-xbox` (Deno, running a real emissions skid) generates several
hundred tags from ~1550 lines of `variables/*.ts`. It is the strongest
evidence available about how this is actually authored, and it is **where
§2.2's shape comes from** — an earlier draft of that walkthrough made a
spreadsheet the exemplar, and this codebase says otherwise: a CSV is nowhere
in it. The primary input is a compact hand-authored config table.

Three layers:

1. **Config tables** — the varying fields only, nothing else:
   ```ts
   [`pm_${getSubnet()}_27`]: [
     { id: "FAN_Po", description: "FAN Outlet", port: 1 },
     { id: "FAN_Pi", description: "FAN Inlet",  port: 3 },
   ]
   ```
2. **Expanders** — `Object.fromEntries(Object.entries(cfg).flatMap(…))`,
   supplying every boilerplate field (datatype, default, decimals, deadband,
   source binding) and taking cross-products: `i550.ts` crosses 2 Modbus
   sources × 4 registers; `tr7439.ts` emits `_C` and `_F` variants of every
   temperature from one entry.
3. **Composition** — spread-merge with per-site conditionals:
   `...(hasDampers ? moduflexVariables : {})`.

**The correction: §2.1 called "repetition with no upstream source" a narrow
residual case. At machine scale it is the default.** Two project shapes, and
this design was written for only one of them:

| | plant scale (client60) | machine scale (xbox) |
|---|---|---|
| tags come from | an existing controller | the engineer's intent |
| bulk source | import over a protocol | a compact config table |
| heterogeneity | one controller, 230 tags | ~20 instruments, 8 protocols |
| dominant cost | transcription | boilerplate per tag |

**What it does better, and nautilus should learn from:**

- **The input is compact.** Three fields per tag, not a full record. Nautilus's
  equivalent is `type:` (3.4) supplying a UDT's eight fields — the same
  instinct, and mostly already planned.
- **Derived families** live beside the definition (`_C`/`_F`), so the
  relationship is stated once.
- **The source binding sits on the tag** — Modbus register or MQTT topic *plus
  its decode function*. Nautilus splits these across `nautilus.yaml` and
  `eip_manifest.yaml`, joined by name. Tentacle's version is more ergonomic
  and is exactly why it cannot have a manifest: the decoder is code.
- **Per-site variation is solved.** One codebase, N deployments, selected by
  settings at boot. **Nautilus has no answer to this at all** — see below.

**What it does worse:**

- **There is no artifact.** To know what tags exist you run the process or
  mentally execute nested `flatMap`s. Nothing to diff, review, or ship. This
  is the §2 argument, and this codebase is the demonstration.
- **The type gymnastics have a ceiling.** Names are tracked with template
  literal types (`${I550SourceId}_${I550VariableId}`), which is genuinely
  clever and gives editor completion — but the central composition in
  `variables/variables.ts` carries a `@ts-expect-error TODO: will fix later`.
  The type system lost.
- **It proves the wrong thing.** The `Variables` type is exact about object
  shape and says nothing about whether the tag set matches the logic — §2's
  claim, confirmed in production.

**Verdict: this is a generator design, not a competing architecture.** Every
layer above ports into this design unchanged; only the last line differs —
`export const variables = {…}` becomes a write of `tags/*.yaml`. Keep the
config tables, keep the expanders, gain a committed artifact, lose only
runtime dynamism. **So the thing worth stealing is the shape of the input, not
the mechanism of the output** — and the way to steal it is 3.1's schema plus a
worked example in this idiom, not a CSV importer.

**Two things it exposes as genuinely open:**

1. **Per-site variation.** `hasDampers` decides at boot which tags exist.
   §6 refuses that outright, so nautilus's answer must be one committed tags
   file per variant, or one built image per site (`nautilus build` embeds the
   project). Image-per-site versus config-per-site is a real fork and this
   doc does not settle it.
2. **Heterogeneous buses.** Eight instrument protocols behind one tag set is
   why tentacle puts decoders on tags. Nautilus routes that to the Go tier
   (§6), which is defensible but means the manifest tier stops at whatever
   drivers exist. Worth stating as a boundary rather than discovering it.

### 2.4 Per-site variation

**Today this is unsupported, and the gap is bigger than it looks.** The whole
of the tree's env surface is `NAUTILUS_ADDR`, `NAUTILUS_TOKEN`,
`NAUTILUS_MQTT_PASSWORD`, `NAUTILUS_CLI` — two secrets and a listen address.
Everything that actually varies between sites is a literal in the manifest:
driver `host:`, sparkplug `broker:`/`edge-node:`/`group-id:`, the tag list,
scan rates. `Load` hardcodes `ManifestName` (`project.go:211`), and
`nautilus build [dir]` zips one directory onto a copy of the runner. There is
no overlay, no parameter, no manifest selection.

Which leaves three bad options: **N copies of the project** (programs
duplicated N times — one logic fix becomes N edits); **rewriting
`nautilus.yaml` before each build** (what deployed was never committed,
against §4.2); or **living with env**, which cannot reach the PLC host.

**Recommendation: select the manifest at build time, and let `tag-files:`
carry the structural difference.**

```
plant/
  motor.st  watch.st        # shared logic and types — one copy
  tags/common.yaml          # generated
  tags/dampers.yaml         # generated
  nautilus.yaml             # dev default
  sites/hayward.yaml        # tag-files: [tags/common.yaml, tags/dampers.yaml]
  sites/fremont.yaml        # tag-files: [tags/common.yaml]
```

`nautilus build -m sites/hayward.yaml` → one self-contained binary per site.

Why this and not the alternatives:

- **No program duplication.** The `.st` is fixed once for every site.
- **Every deployed artifact is committed and diffable**, so §4.2 holds
  unchanged — the reason rewriting the manifest in CI was rejected.
- **Structural variation is expressed as which tag files are included**, which
  is data. No conditionals, no interpolation; §6 survives intact.
- **It is small.** `ReadManifest`/`Load` take a path instead of the constant,
  and there are six call sites (`runcmd.go:33,141`, `testcmd.go:40`,
  `lsp/testdoc.go:282`, `lsp/manifest.go:56,105`).

One detail the design missed, found while building it: a built binary embeds
the whole directory, so `-m` has to survive into the artifact or the binary
boots `nautilus.yaml` regardless. `emitBinary` writes the selected path into
a `.manifest` entry in the archive and `main` reads it. Every manifest still
ships — the marker only says which one boots — so the binary stays a complete,
inspectable project: `unzip -p <binary> .manifest` answers "what was this
built from?".
- **It makes `tag-files:` do double duty** — generated tag sets *and* site
  composition — which strengthens the case for building it early (§8).

Rejected: interpolation (`${SITE_HOST}`) as the Helm road §6 refuses; a
`sites:` block with conditionals inside one manifest, which grows without
bound; and blanket env overrides for every field.

**Env-for-structure is specifically disqualifying here, and the reason is the
domain.** A controller runs unattended for years, and "what is this thing
running?" has to be answerable from the artifact — commissioning records,
change control, and 3 a.m. troubleshooting all assume the program *is* the
deliverable. Push the tag set or the PLC address into the environment and that
question can only be answered by inspecting a live process's environment,
which lives in a systemd unit or a cluster manifest owned by someone else and
may have changed since commissioning. The failure mode is also silent: a
typo'd `NAUTILUS_DRIVER_HOST` falls back to a default and quietly polls the
wrong controller, where a missing `sites/hayward.yaml` fails at build. Env
stays for secrets, which must *not* be in the artifact, and for nothing else.

**The honest trade-off:** twelve sites differing only by PLC address means
twelve near-identical manifests. Two mitigations — a site manifest is small
(tag-files, driver host, sparkplug identity, ~15 lines), and it is itself a
natural codegen target from a site table, the same pattern as everything else
here. If that is still too much duplication in a real fleet, *that* is when a
narrow env override for `driver.host` earns its place — driven by a deployment
that exists, not a guessed one. Secrets stay env-only regardless.

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

*Built, and two rules came out sharper than written here.* The read/write
split is decidable without dataflow analysis — `ir.Program.GlobalUses()` walks
the body and classifies each `GlobalRef` by position — so a **read** of an
undeclared tag is an error (unseeded and not driver-fed: it faults, and a real
run confirms it faults on *every* scan, not just the first) while a
write-only one is a warning. A compound target (`P101.Speed := …`) counts as
both, since setting a field reads the aggregate.

The other direction needed narrowing to stay useful: an unbound **input** is
not reported at all. Driver-fed telemetry that no program binds is the bulk of
any imported tag list — `examples/client60` has eight — and warning on all of
them would have taught people to ignore this warning before it ever caught
anything. Unbound setpoints, states, and outputs still warn, because those
exist to be read or written by logic. A task's `dt-tag` counts as declared:
the runtime writes it every scan.

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

## 4. The questions, answered

### 4.1 Where a generated tags file lives, and merge semantics

**An explicit `tag-files: [...]` list. Duplicate name anywhere is an error.**

Not a convention directory. A glob makes the tag set depend on whatever
happens to be on disk, so a stale generated file silently joins the
controller — precisely the failure mode §0.2 exists to prevent. The manifest
is the deploy artifact; it should say what it is made of.

```yaml
tag-files:
  - tags/eip.yaml        # generated — do not edit
  - tags/pumps.yaml      # generated from the pump schedule
tags:                    # hand-authored, stays where a person edits it
  - { name: ResLowSP, role: setpoint, init: 5.0 }
```

- Each file is a **bare YAML sequence of the same `TagConfig` item** — one
  schema entry (`$defs/tag`) reused, so a generator targets one shape and the
  editor validates both files with the same rules.
- Merge order is listed order, then inline `tags:` last. Order is for
  reading, not precedence: **a name declared twice in any two sources is an
  error naming both files.** Last-wins is a silent override, and a
  regenerated file would trip it without a diff to show for it.

**No overrides — decided, and the reasoning is worth keeping.** The failure
is not that an override is wrong on the day it is written; it is that
regeneration rots it in silence. Re-run the import, and a tag that changed
type or vanished leaves an override that still applies, or now applies to
nothing — and last-wins semantics means there is nothing to report. It also
dissolves the one thing the manifest is for: with precedence, "what does this
controller consume" is a fold over N files, so a `check` error can point at a
name but not at a line. The remedies stay legible: fix the generator, or
narrow its scope so the tag is never generated and lives inline instead
(`eip import --tags` globs already do this).

That is strict about **declaration** — `role`, `init`, `type`, the fields
that change behaviour. It is deliberately not strict about documentation,
because documentation is not a declaration:

```yaml
tag-meta:                       # layers onto a generated tag, never redeclares it
  P2P_RTU44B_RES13_LIT_00A_LI: { desc: "Reservoir 13 level, sight A" }
  RTU60_ZONE13_PIT_001.AI:     { unit: psi }
```

This is load-bearing, not a convenience. `eip/codegen` and `eip/logix`
capture **no descriptions** — Logix keeps them in the offline ACD project,
not in the CIP tag-list service — so a generated tags file has no `desc:` or
`unit:`, permanently. And `desc:` is nearly the entire payload of client60's
hand-transcribed scalars (§1.5). Without somewhere for documentation to land
that is not a duplicate declaration, **acceptance bar 5.1 cannot be met**: the
choice would be an un-generatable file or 500 undocumented tags.

`tag-meta:` is safe where an override is not, and for a reason that survives
regeneration: it is additive-only and cannot alter behaviour, so its worst
failure is a stale sentence. A key naming no tag is a `check` warning (3.3),
which catches even that. Keys are tag names or dotted paths — the same key
space as §4.5, so this is one mechanism, not two.
- Paths are project-relative and may not escape it (`path.Clean`, no `..`) —
  the artifact is the directory or the embedded archive.
- **Composition resolves inside `ReadManifest`, not `Load`.** `ReadManifest`
  already takes an `fs.FS`, and it is the shared door: the LSP goes through it
  (`internal/lsp/manifest.go:105`), and so will `check` and the schema guard.
  Resolve it there and every consumer gets the composed set for free; resolve
  it in `Load` and the LSP quietly sees a fraction of the project's tags.

### 4.2 Committed or generated in CI

**Committed.** Stated, not inherited. The diff is the review; a deploy must
not need the controller reachable; and an embedded-archive build has only the
tree to build from. Generated files carry the same `Do not edit — re-run …`
header `eip_types.st` already carries.

CI's role is *verification*, not generation: where a controller or a recorded
fixture is available, regenerate and diff, exactly like a golden test. The
committed file stays the source of truth.

### 4.3 Where UDT types for `type:` come from

**The project's ST `TYPE` declarations, resolved inside `runtime.New` after
`Compile` — not in `project.Load`.**

The load-order objection dissolves once the resolution point is right.
`TagConfig.Type` is only ever a *string* through `Load`; it rides across on a
new `runtime.TagDef.Type string`. And `runtime.New` already runs
`expandTags → Compile → seed` (`runtime/runtime.go:161-178`) — the compile
happens *before* the seed loop. So the resolution slot exists today, between
those two steps. `Load` keeps being pure transcription, which its package doc
says is the whole job.

One enabler is needed, and it is small. `lang/st/lower.go:377` builds the
type table in `l.types` and **throws it away**. Attach it:
`ir.Program.Types map[string]*Type`, assigned after `collectTypes`. That is
the same "introspection only — the VM does not consult this" category as
`SlotIndex` and `Globals` (`lang/ir/program.go:31,39`), with the same
justification. Then `Runtime.Types()` unions across tasks the way
`Runtime.Globals()` already does (`runtime/runtime.go:248`).

Why not the alternatives:

- **The eip manifest's `TypeDef`s** — they are *one driver's* types. A `type:`
  that works only when `driver.type: eip` puts the seam in the wrong place and
  leaves a memory-driver project unable to use UDTs. And `eip/manifest.go:74`
  already says the generated ST is the agreed representation: the driver's
  `structDefs` are built to mirror `eip_types.st` field-for-field *so that
  driver values bind to `VAR_EXTERNAL` declarations*. Two sources of truth for
  one shape is the bug, not the feature.
- **A manifest-level `types:`** — a third copy of the same struct, and it
  starts the manifest down the road to owning a type system. Against §6.

Two consequences to design around:

1. Libraries are joined per task (`stproject.Join`), and `Load` hands every
   task the same library set (`project.go:257,277`), so a `TYPE` in any root
   `.st` library is in scope for every compile. `type:` resolves against the
   **union of all compiled programs' type tables**; two tasks declaring
   different types under one name is an error at `New`, mirroring the existing
   POU-collision check.
2. A type nothing references still resolves — `collectTypes` walks every
   `TypeDecl` in the joined source regardless of use — so a driver-only or
   HMI-only UDT tag works. Worth an explicit test, since it is the client60
   case.

Bonus, free: with `type:` present the LSP can report a tag's type *without
compiling* — it is a string in the manifest. That closes the §1.7 hole
("a tag with no seed has no knowable type") on the cheap path.

### 4.4 `init:` for a UDT tag

**Zero-of-type by default — `ir.Zero(*Type)` already exists
(`lang/ir/value.go:35`) — but not for `role: input`.**

Seeding must not erase the loud failure. `RoleInput` is deliberately unseeded
so a driver that never delivers faults instead of running on a silent zero
(`runtime/tagdef.go:14-17`). A zero struct would silence exactly that. So:

| role | `type:` present, no `init:` |
|---|---|
| `input` | **not seeded** — reads fault until the first poll, as today |
| `output` | not seeded (unchanged; seed only when logic reads it back) |
| `setpoint`, `state` | seeded zero-of-type |

That last row relaxes a real rule: `tagDefs` currently *requires* `init:` for
setpoint and state (`project.go:367,373`). With `type:`, the requirement
becomes **"init or type"** — a string-presence check, so `Load` still needs no
compiled types.

**Per-field seeds: yes, but as a literal, not as syntax.** `init:` may be a
YAML mapping of field → scalar, validated against the resolved struct —
**unknown field is an error, omitted field takes zero-of-field**. That is a
value, not a template, so §6 holds. What stays out is any per-field *addressing*
form (`init.Speed:`, `init: {P101.Speed: …}`); anything that wants computing
computes upstream, and anything that wants sequencing is first-scan logic.

### 4.5 Does `TagMeta` need to be per-field?

**No. The hidden cost the brief feared is not there — I checked all three
consumers.** Keep `map[string]TagMeta` keyed by name, and let the key hold a
dotted path.

What is actually coupled to name-keyed meta today:

- `/api/meta` returns `map[string]runtime.TagMeta` (`server/server.go:293`).
  Dotted keys need **no shape change** — only the note that a key may contain
  dots.
- **Sparkplug publishes no meta at all.** No `Desc`, `Unit`, or metric
  `Properties` anywhere in `sparkplug/birth.go` or `data.go`. Struct tags
  publish as Templates built purely from `StructDef` field names
  (`payload.go:246`). Zero cost here.
- **The HMI has no generic tag table.** `ControllerMeta` is an exported type
  (`hmi/src/lib/types.ts:166`) that no component consumes; units reach
  `Gauge`, `NumberField`, and `DriverStatusCard` as per-component props. The
  brief's "the HMI table reads it that way" overstates the coupling — there is
  one endpoint and one exported TS type.

So per-field meta is **additive whenever it is wanted**, needs no new type,
and does not gate 3.4. The design commits only to *"the meta key space is
dotted paths"* and ships `type:` without per-field meta in the first cut.
Nothing in §5 requires it.

The block it lands in already exists for another reason: §4.1's `tag-meta:`,
which the eip import needs because it cannot read descriptions off a
controller at all. Same key space — a tag name or a dotted path — so per-field
meta is that block with a longer key, not a new mechanism.

What is still deferred is the *per-type* form: declaring `Motor.Speed` once so
40 instances do not repeat 8 units, expanded per instance at `New`. It is
documentation for a type rather than a definition of one, so it does not
reopen 4.3, but nothing yet asks for it — client60's UDT tags want two
descriptions, not forty. Ship the per-tag keys; add the per-type fold when a
project has the repetition to justify it.

The one genuine gap, named so it is not mistaken for free: **writing a field
through the API does not exist.** `Tags.Set` and the server's write path are
whole-tag; nothing resolves a dotted name on write. Out of scope here, and
related to 4.7.

### 4.6 Roles for imported tags

**Derivable — confirmed, not assumed.** `--writable` glob patterns at import
time set `TagBinding.Writable` (`cmd/nautilus/eip.go:114`), and the driver
puts writable bindings in the output set (`eip/driver.go:166`) while
`InputNames()` returns every polled binding (`driver.go:252`). So
`Writable → role: output`, everything else `role: input`.

Two caveats worth writing into the generator:

1. A writable tag the logic also *reads back* needs `init:` — `RoleOutput` is
   unseeded. The generator cannot know which those are, so it emits
   `role: output` with no init and **`nautilus check`'s read-before-write rule
   (3.3) is what catches it.** That is the verification earning its keep.
2. Imports never produce `setpoint` or `state` — those are project-authored.
   client60 is the proof: 8 of its tags are derived setpoints and states, none
   importable. This is the concrete reason the generated file must be
   *separate* from the hand-edited one (3.2), not a section within it.

### 4.7 Does `given:` need to address a field?

**Yes, and the collision is worse than stated — settle the resolution order
now.** `testRun.value` (`acceptance/run.go:358`) cuts on the first `.`, looks
up a *task* by the head, and **errors out** when there is no such task. There
is no fallback. So `PIT_001.AI` today reports *"no task PIT_001"*. And
`given:` does not go through `value()` at all — it checks a flat `r.known` set
and writes whole values (`run.go:380-390`).

Resolution order, no new syntax:

1. an exact tag name (a tag literally named `A.B` wins — unambiguous);
2. `<tag>.<field>…` when the head names a tag holding a struct;
3. `<task>.<local>` when the head names a task;
4. otherwise error, listing what heads exist *in both namespaces*.

Tag-first: tags are the manifest's namespace, and a task-local is the more
specialized thing to reach for.

Sequencing, because the halves cost differently:

- **`expect` / `until` get field addressing in the first cut.** Read-only,
  cheap, and it is what a UDT test mostly wants.
- **`given:` on a field is a read-modify-write of the driver input image** —
  read the current struct, clone `Fld`, replace one slot, write back. Doable,
  not free, and it needs the image to *already hold a struct*, which 4.4 says
  production deliberately will not seed for `role: input`.
  Resolution: **the harness seeds zero-of-type into the input image at test
  start for typed tags; the runtime does not.** A test that never delivers a
  value is a test bug that `expect` catches, so the loud-fault argument does
  not apply inside the harness.
- Until `given:` field writes land, a dotted `given:` errors with *"seed the
  whole struct"* rather than silently doing nothing.

One dependency to schedule with it: the LSP validates test tag names against
`knownTags()` (`internal/lsp/testdoc.go:382`). That set must grow the fields
of typed tags, or the editor squiggles valid tests — the same class of
regression 3.1's guard tests exist to catch.

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
| Prior art (§2.3) | `~/Development/deno/tentacle-xbox` (`variables/`, `xboxSettings.ts`) |

## 8. Recommendation — and what was built

All six shipped. Three things the build changed, none of which reopened a
decision:

1. **`skip` patterns on the tag generator.** §4.1 said the remedy for
   something a generator cannot know is to "narrow its scope so the tag is
   never generated". client60 forced that from a sentence into a flag: five of
   its inputs need an `init:` so `watch.ld` runs before the first poll, and
   the import cannot know which. They are declared by hand and skipped by
   name. A skip pattern matching nothing is an error, because a stale
   exclusion silently regenerates the tag it was making room for.
2. **`nautilus eip tags`** re-derives the tag file from an already-committed
   `eip_manifest.yaml`, with no controller. The import needs hardware; review
   and CI do not, and client60's tag file was produced this way.
3. **A `.manifest` marker in built binaries** (§2.4) so `-m` survives into the
   artifact.

Everything dropped in this section stayed dropped, and nothing deferred was
pulled forward.

| # | piece | why it is where it is |
|---|---|---|
| 1 | **Schema + Go guard** (3.1) | no dependencies; it is the contract every generator in every language targets, and §2.3 concluded that shipping the contract beats shipping generators |
| 2 | **`tag-files:`** (3.2) | small; resolve it in `ReadManifest` so the LSP and `check` see what `Load` sees |
| 3 | **`check` reads the manifest** (3.3) | lands *with* 2, never after — composition without verification just relocates the hand-typing. Also the thing prior art cannot do at all |
| 4 | **`type:` + `tag-meta:`** (3.4, §4.1) | needs `ir.Program.Types` retained from lowering first; `tag-meta:` is load-bearing for acceptance 5.1, not a nicety |
| 5 | **`eip import` emits tags** (3.5) | nearly free after 2 and 4; deletes client60's transcription |
| 6 | **`tags import-csv`** (§2.1) | the third generator kind; a standard format earns a shipped tool, a bespoke config table does not |

Alongside 4: `expect`/`until` field addressing with §4.7's resolution order,
and the `knownTags()` widening the LSP needs so valid tests stop squiggling.

**Then write the worked example, and treat it as a deliverable.** An example
project generating tags in §2.3's idiom — a compact config table, an expander,
a write to `tags/*.yaml`. Not a nautilus feature and not a CSV importer: the
convention is what has value, and there is currently nowhere to point someone
who asks what a generator should look like.

### Dropped, not deferred

**List-valued `name:`.** §2.3 killed it, and in the opposite direction from
what was expected. It only helps when everything but the name is identical —
and real repetition is never that. Every xbox config row carries its own
description and port; even 40 pumps have 40 descriptions. The uniform case is
the rare one, and the tabular case wants a generator, which already exists.
Schema-widening stays backward compatible if this is ever wrong.

### Deferred (all backward compatible to add)

- **`init:` as a field mapping** (§4.4) — ship zero-of-type only. A frozen
  schema is worth the most at its first release.
- **Per-type meta** `Motor.Speed` (§4.5) — no project has the repetition yet.
- **`given:` field writes** (§4.7) — needs the harness to seed the input
  image; `expect` covers the common case first.
- **`type:` naming an array type** (§2.2) — only if a project wants
  array-shaped tags for their own sake.

### In scope after all: per-site variation

§2.4 answers it, and the answer is cheap enough to belong here rather than in
its own brief: **`-m <manifest>` on `run`/`build`/`check`/`test`, with
`tag-files:` carrying the structural difference.** Six call sites, no new
manifest syntax, and it is the second argument for building `tag-files:`
early. Schedule it with piece 2.

### Out of scope, and tracked elsewhere

- **Per-field API writes** (§4.5) — whole-tag today; unrelated to generation.
- **Heterogeneous buses** (§2.3) — the Go tier by §6. State it as a boundary
  in the docs so it is not discovered late.
