# Design brief: the context map

Status: **proposal.** §1 and §2 describe the tree as it is at `4b61bca` and
are checked by `internal/contextmap` (`go test ./internal/contextmap/`).
§3 is a classification James has to make; the one here is a draft to
overrule. §4 is a glossary that grows when a PR adds a noun. §5 says what
we are deliberately not adopting.

## 0. Why this exists

Nautilus is built mostly by agents working from briefs, often several PRs in
parallel. Each one sees a slice. The failure that mode invites is not a bad
algorithm; it is two packages inventing different words for one thing, or a
vendor's spelling leaking across a seam because nobody said the seam was
there. The repo has resisted that well so far because the package graph is
clean and the design briefs argue names before code lands. But the rules
were implicit, so every brief had to re-derive them.

Domain-driven design's *strategic* half is a vocabulary for exactly this:
**bounded contexts** (a word means one thing inside a boundary), a **context
map** (which context is upstream of which, and where translation happens),
**anti-corruption layers** (a seam that keeps a foreign model's names out),
a **shared kernel** (one small model two contexts agree to share), and the
**core / supporting / generic** split (where to spend, where to wrap). This
brief adopts those five words and nothing else, because those five are the
ones that change what a brief says and what a reviewer checks. The tactical
half of DDD (aggregates, repositories, application services, CQRS) is for
business software with rich mutable records; a compiler plus a scan loop
plus protocol codecs gets nothing from it (§5).

Much of nautilus's language is not ours to invent. IEC 61131-3 names task,
program, function block, retain. ISA-18.2 names alarm states, ack, shelve,
journal. Sparkplug names node, device, metric, birth. The work is not to
discover a language with domain experts; it is to keep the standards'
language intact and to say precisely where a vendor's or a protocol's
dialect is allowed to exist.

## 1. The contexts

One row per bounded context. *Language* is the handful of nouns that mean
something specific inside it. *Upstream of* is who consumes its model.
*Translation* is where another model is turned into this one, or this one
into another's.

| Context | Packages | Language | Upstream of | Translation |
|---|---|---|---|---|
| **Languages** | `lang/st` `lang/ld` `lang/fbd` `lang/sfc` `lang/ir` `lang/fbcatalog` `lang/stgen` `internal/stproject` | IEC 61131-3: POU, program, function block, task, retain, rung, network, step, transition. `lang/ir` is the substrate every other context shares for types and values. | Runtime, Authoring, Logix target, every codegen | `lang/l5x` turns a Logix export into ladder and ST (an importer, see Logix target) |
| **Runtime** | `runtime` `io` `retain` `leader` | scan, task, tag store, quality, generation, lateness, force, retained, coordinator. `io.Driver` is the published seam; `io.Quality` the one quality enum. | Operator surface, Sparkplug edge, Verification, Logix facade | Manifest tags become `runtime.TagDef` in `internal/project` |
| **Field drivers** | `modbus/…` `eip/…` (`cip`, `logix`, `codegen`) `sparkplug/host/…` `replay` (and the pending `opcua`) | each protocol's own: register, coil, holding; CIP path, symbol, UDT; metric, NBIRTH. | nothing above the seam | **Anti-corruption layer.** The protocol's names stop at the package. What crosses is `io.Driver` upward and `internal/tagfile.Tag` into the tag file. |
| **IT drivers** | `hw` `snmp/…` `redfish/…` `prom/…` | source, scan class, the shared UDT set (SwitchPort, PSU, …), counter→rate. `hw` is to these what `runtime` is to the languages. | nothing above the seam | Same ACL rule as field drivers; `hw` owns the shared UDTs so a port is one struct whether it came from IF-MIB or REST |
| **Sparkplug edge** | `sparkplug` `sparkplug/spb` | node, device, metric, birth, rebirth, NCMD, RBE. | Sparkplug host (payload codec) | The one adapter that imports `runtime` directly: it publishes a runtime's store, so it is downstream of Runtime, not a driver |
| **Alarms** | `alarm` | ISA-18.2: definition, rule, active, unacked, shelved, journal, notifier. | Operator surface | Imports only `lang/ir` and `retain`; the manifest hands it a tag store. It does not import `runtime`, and that is the point |
| **Operator surface** | `server` `oplog` `hist` | tag API, snapshot, SSE stream, write, program history, operator intent, sink. | HMI 2D, HMI 3D, external clients | Serves Runtime and Alarms; knows no protocol |
| **Manifest** | `internal/project` `internal/dialect` `cmd/naut` | `nautilus.yaml` nouns: tags, tasks, drivers, alarms, scene, dialect. | nothing; it is the composition root | Every context's manifest form lives here (`project.TagConfig` ↔ `runtime.TagDef`). It is the only package allowed to import everything |
| **Verification** | `acceptance` `lang/conformance` `internal/vcs` | suite, case, expect, virtual time, corpus. | CLI, Authoring | Composes whatever it tests; held to no import rule |
| **Authoring** | `internal/lsp` `tools/vscode-iec` | diagnostics, hover, completion, diagram editor, live values, component preview, kind editor. | the person | Reads the Manifest and Languages; the only non-CLI importer of `internal/project` |
| **Logix target** | `logix/writer` `logix/importer` `logix/deploy` `logix/logixd` `logix/facade` `lang/l5x` `eip/logixserver` | Rockwell's: L5X, controller, routine, rung, AOI, alias tag, download. | nothing | **Anti-corruption layer, both directions.** Writer and importer are the only places vendor spellings may appear (see *Language stays vendor-neutral*). `logix/facade` is an application layer for `naut logix serve`, so it may import Runtime and Operator surface |
| **HMI 2D** | `hmi` (npm) | mimic, component, port, catalog, realtime, trend, quality, alarms view. | the operator | Consumes Operator surface over HTTP/SSE; no Go |
| **HMI 3D** | `hmi-3d` (npm) `internal/scene` | scene, kind, node, pipe, port, look, assembly. `internal/scene` is the offline half (`naut check`, `naut scene init`). | the operator, Authoring | Consumes Operator surface. **Open edge:** argonaut's `parts` format is a second model of the same things; §2.4 |
| **Tag file** | `internal/tagfile` | one row: name, role, type, init, unit, desc, alias. | every codegen, `lang/l5x` | **Shared kernel.** The one format every importer renders and the manifest reads |

Two packages are substrates rather than contexts and import nothing of
ours: `lang/ir` (types and values) and `io` (the driver seam and `Quality`).
`hist` and `oplog` are seams in the same sense. They are what DDD calls a
*published language*: small, stable, and the price of changing one is paid
by every context.

## 2. The map rules

These are the rules `internal/contextmap` enforces. A PR that needs to
break one changes this section first and says why.

### 2.1 Direction

- Substrates (`io`, `lang/ir`, `hist`, `oplog`) import no other nautilus
  package. `retain` imports only `internal/k8sapi`.
- Languages import only each other and the tag-file shared kernel.
- Runtime imports only Languages, `io`, `retain`, `internal/stproject`.
  It never knows a driver, the server, alarms or the manifest.
- Alarms import only `lang/ir` and `retain`.
- Field and IT drivers and `sparkplug/host` never import `runtime`,
  `server`, `alarm`, `acceptance`, `internal/project`, `internal/lsp` or
  the CLI.
- The operator surface never imports a protocol package, the Logix target,
  or the manifest.
- `internal/project` is imported only by the CLI and the LSP. Nothing
  imports the CLI.

Verification packages, `examples/`, `testdata/` and `tools/` compose freely
and are excluded from the check. Test files may import anything.

### 2.2 Anti-corruption layers

A driver or writer package owns its protocol's names and nothing above it
sees them. Concretely: no `cip.*`, Modbus function code, OID, Redfish
resource path or L5X element name appears in `runtime`, `server`, `alarm`,
`internal/project` or the HMIs. What crosses the boundary is the published
language: `io.Driver`, `io.Quality`, `lang/ir` values, `internal/tagfile.Tag`.

The vendor-neutral rule already in force (nautilus features feel
IEC-standard; vendor spellings only in writers and importers; a `dialect`
block only when no standard equivalent exists) is this rule applied to the
Languages context. The `dialect:` field in the manifest is a context-map
entry: it names which ACL a project's sources will pass through.

### 2.3 Duplication is allowed; coupling is not

Two contexts may each have a type for the same idea. §4 lists the cases.
The fix for a duplicate is never an import from one context into another
that today do not depend on each other. If a type genuinely belongs to
both, it moves into a substrate or the shared kernel, and that is a
deliberate, reviewed change to the published language.

### 2.4 Open edge: the 3D formats

`hmi-3d` and `internal/scene` carry one model of a scene (`*.scene.json`,
`*.kind.json`). The argonaut repo carries another (`parts`). Spatial HMI
§3g and the kind-editor work have both noted they must converge. In map
terms the decision is which is upstream and where the translator lives.
Proposal: `hmi-3d` is upstream, since it is the published npm package and
has the editor; argonaut conforms, with a one-way converter in argonaut.
This is James's call and is listed here so no PR decides it by accident.

## 3. Subdomains: where to spend

DDD's one budgeting tool. **Core** is where nautilus must be better than
anything else and where tokens go first. **Supporting** is needed, ours,
and good enough is good enough. **Generic** is solved elsewhere: wrap a
library, own the ACL and the codegen, do not own the protocol.

Draft classification, for James to overrule:

| Subdomain | Contexts | Why here |
|---|---|---|
| **Core** | Languages, Runtime, Manifest, Verification, Authoring | "SCADA, built like software" lives in exactly these: IEC sources as code, a deterministic scan loop, a manifest that is the product, tests in virtual time, and the editor experience. Nobody else ships this combination |
| **Core by decision** | Logix target | A business choice, not a technical one: AB development authored only in nautilus, Studio 5000 as headless middleware. Worth owning the writer, the importer, the CIP stack and the emulator because that is the wedge |
| **Supporting** | Alarms, Operator surface, Sparkplug edge, HMI 2D, HMI 3D | Every SCADA needs them and ours should be good, but they follow standards (ISA-18.2, Sparkplug) and a customer would not pick nautilus for them alone. The 3D editor is the exception that may move up if the AI-generate, human-tweak thesis becomes the pitch |
| **Generic** | Field drivers (Modbus wire, OPC UA via gopcua, the pending client), IT drivers (gosnmp, Redfish), `spb` | The protocol is the library's job. Ours is the seam, the codegen, the tag file, the quality rules. `eip/cip` is the exception: already built, pure Go, and load-bearing for the Logix target, so it stays, but it grows only as the target needs |

What this changes in practice: a brief for generic work says "wrap, do not
reimplement" up front; a brief for core work gets the audit pass (the
runtime-claims lesson) and the rig check; a supporting feature gets built
to the standard and stops.

## 4. Glossary: words that mean different things by context

Legitimate per-context meanings, so nobody "deduplicates" them:

| Word | Context → meaning |
|---|---|
| **Tag** | Runtime: a cell in the store (`TagDef`, `Tags`, `TagMeta`). Manifest: its YAML form (`TagConfig`). Tag file: one generated row (`tagfile.Tag`). Field/IT drivers: a binding from a tag to an address (`TagBinding`, `snmp.Tag`, `redfish.Tag`, `prom.Tag`, `replay.Tag`). Logix: a controller symbol (`l5x.Tag`, `cip.TagSegment`). Logix emulator: the served surface (`TagSpec`, `TagSurfaceSpec`, `TagConfig`, `TagStore`) |
| **Node** | ST and SFC: an AST node. FBD: a netlist node. Sparkplug edge: this edge node. Sparkplug host: its view of a remote edge node. Oplog: one replica. Scene: one placed thing |
| **Device** | Sparkplug edge: a device under this node. Sparkplug host: a device under a remote node. Modbus codegen: a device *type* in the committed device map (`DeviceMap`, `DeviceType`), which is a kind of hardware, not an instance. Drivers call a polled instance a **source** |
| **Program** | IEC: a POU scheduled by a task, everywhere in nautilus. Logix: a container of routines. The writer translates; "program" never means the Logix thing outside `logix/` |
| **Quality** | One meaning, `io.Quality`, in every context. Keep it that way |
| **Component** | HMI 2D: a mimic symbol (`*.component.json` + `.svelte`). HMI 3D uses **kind** (`*.kind.json`) for the same role on purpose, so the two are never confused |
| **Scene** | HMI 3D only. The 2D equivalent is **mimic** |
| **Source** | Field and IT drivers: one polled endpoint (`Source`, `SourceConfig`, `SourceHealth`). Languages: a file of IEC text. Say "IEC source" when both are in play |

Known duplicates, kept on purpose under §2.3:

- `alarm.TagInfo` and `internal/scene.TagInfo` both describe a declared tag
  and its UDT. Alarms and Scene do not depend on each other and should not
  start to for a four-field struct. If a third copy appears, that is the
  signal to move it into `lang/ir`.
- `runtime.TagMeta` and `modbus/codegen.TagMeta` both carry desc and unit.
  Same reasoning; the codegen one also carries `Init` and is heading for
  the tag file, not the runtime.

A PR that adds a noun to a context's public API adds a row here if the word
already has a meaning elsewhere in this table. That is the whole process.

## 5. Not adopted

- **Aggregates, repositories, application services, domain events as a
  pattern, CQRS, event sourcing.** The runtime is a scan loop over a tag
  store; the compiler is a compiler; the drivers are codecs. None of these
  has the invariant-protecting-mutable-entity shape the patterns exist for.
  Two places have the shape by nature and already have it in code: the
  alarm engine is a state machine with a journal, and the oplog is a
  replicated log of operator intent. They do not need renaming.
- **A domain layer directory, hexagonal folder layout, or any restructuring.**
  The packages already are the contexts. Moving files to match a textbook
  would cost a week and buy nothing the test in §2 does not already give.
- **Event storming, modeling workshops, a glossary tool.** The design briefs
  are the modeling sessions. §4 is the glossary. They are in git.

## 6. Using the map in a brief

A brief for a new piece of work names its context, its upstream, and the
seam it crosses, in one line near the top. For example:

> OPC UA server: Operator surface, downstream of Runtime and Alarms; gopcua's
> names stop at `opcua/`; generic, so wrap, do not reimplement.

> Kind editor: Authoring over HMI 3D; reads `*.kind.json` through
> `internal/scene`; core-adjacent, gets a rig check.

That line is what lets an agent that sees one slice behave as if it saw the
whole, and it is what a reviewer checks first.
