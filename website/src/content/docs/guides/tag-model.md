---
title: The tag model
description: The tag store is the seam everything meets at — how tags come to exist, which role fits which job, and the one rule that bites.
---

The tag store is the seam everything meets at: the IEC program, the field
driver, the HMI/API, and the editor's live values all read and write the
same named values. One scan looks like this:

```
driver.ReadInputs() ──(Input tags)──▶ ┌───────────┐ ──(Output tags)──▶ driver.WriteOutputs()
                                      │ tag store │
HMI / POST /api/tags ──(writes)─────▶ │           │ ──(reads)────────▶ HMI / editor pills
                                      └───────────┘
                                        ▲       │
                                 program writes │ program reads
                                     (coils)    ▼
```

**A `VAR_EXTERNAL` declaration is a binding, not a creation.** Declaring
`TempC : REAL;` in your program tells the compiler "resolve this name in
the tag store at scan time" — it does not make the tag exist. Existence
comes from a write, and there are exactly four writers:

1. a **seed** in the Go composition (initial value, exists from scan one),
2. a **driver** delivering it as an input each scan,
3. an **operator** writing it through the HMI or `POST /api/tags`,
4. the **program itself** assigning it (a coil write creates the tag).

The one rule that bites: **writes create, reads fault.** Reading a tag
nobody has ever written stops the scan with `undefined tag <name>` —
deliberately, so a mis-wired binding fails loudly instead of running on a
silent zero. The classic trap is a seal-in latch that *reads its own coil*
on scan one, before the first write; the fix is a seed. (In the FBD
diagram, with live values on, externals with no backing tag are flagged —
an amber *no tag* badge in the vars panel and on any chip that reads one —
so you see this before the download, not after.)

## Declaring tags: one entry per tag

`runtime.Options.Tags` states each tag's **role** once — which direction it
moves each scan, its initial value, and its HMI documentation — instead of
spreading the name across the flat `Inputs`/`Outputs`/`Seed`/`Meta` fields
(all of which still work and compose with `Tags`):

```go
Tags: []runtime.TagDef{
    // Field input: the driver produces it. NOT seeded — if the driver
    // doesn't deliver it, you want the loud fault, not a silent zero.
    // (The name must also appear in the driver's ReadInputs map.)
    runtime.Input("TempC", runtime.Desc("Tank temperature"), runtime.Unit("°C")),

    // Operator setpoint / config: seeded so it exists from scan one;
    // the HMI or POST /api/tags writes it later.
    runtime.Setpoint("TempSP", 65.0, runtime.Unit("°C")),

    // Field output: the logic produces it, the driver consumes it.
    // Init() any output the logic also READS — the seal-in latch.
    runtime.Output("PumpRun", runtime.Init(false), runtime.Desc("Pump run command")),

    // Logic-owned state that must survive read-before-write: latches,
    // integrators, mode words.
    runtime.State("Mode", int64(0), runtime.Desc("0=off 1=auto 2=manual")),
},
```

Picking a role, by use case:

| You want a tag that…                        | Use                          | Backed by                                   |
| ------------------------------------------- | ---------------------------- | ------------------------------------------- |
| comes from a sensor / the field             | `Input(name, …)`             | driver `ReadInputs` map + this entry        |
| goes to an actuator / the field             | `Output(name, …)`            | your logic writes it (coil)                 |
| …and your logic also reads it (latch)       | `Output(name, Init(v), …)`   | the seed covers scan one, then the coil     |
| an operator adjusts (setpoint, gain, limit) | `Setpoint(name, initial, …)` | the seed, then HMI / `POST /api/tags`       |
| your logic owns across scans (integrator)   | `State(name, initial, …)`    | the seed, then the coil                     |
| the HMI watches but the field never sees    | plain coil write — no entry  | the program creates it on first write       |

## Seeding struct members

A manifest tag naming a UDT (`type: Motor1Speed`) seeds to the zero of that
type by default — every member `FALSE`/`0`/`0.0` until something writes it.
For a `setpoint` or `state` tag, `init:` can instead be a mapping of member
name to value, nested for a member that is itself a struct:

```yaml
tags:
  - name: WEL15_FIT_001
    role: state
    type: AnalogInput
    init:
      RAWMIN: 6553.0
      RAWMAX: 32767.0
      HHSP: 2800.0
      ALMDLY: 5
  - name: WEL15_SUP_015
    role: state
    type: Motor1Speed
    init:
      STRTTMRSP: 30
      LVL: { CTL1HSP: 60.0, CTL1LSP: 40.0 } # nested struct member
```

Every member the mapping omits still takes the zero of its own field type —
`init:` names only what needs a real starting value, not the whole shape.
This is what replaces a first-scan `IF NOT CfgDone THEN ... END_IF` block of
plain field assignments: the same setpoints, declared once where the tag
already is, instead of duplicated as ST in every program that uses the type.

An unknown member name is a load error naming the tag and the member path
(`tag WEL15_FIT_001: init: unknown member RAWMN (did you mean RAWMIN?)`), and
a struct-typed tag given a scalar `init:` — rather than a mapping — is an
error too. A generator (`nautilus eip tags`, `nautilus tags import-csv`)
round-trips the same nested shape through a `tag-files:` entry.

## Writing a member: `POST /api/tags` with `Tag.Member`

A UDT tag is one value in the store, so an operator writing one of its
members is a read-modify-write of the whole struct. The API does that for
you — `name` takes a dotted member path, to any depth:

```sh
curl -X POST localhost:8080/api/tags \
  -d '{"name": "WEL15_SUP_015.START", "value": true}'
curl -X POST localhost:8080/api/tags \
  -d '{"name": "WEL15_SUP_015.LVL.CTL1HSP", "value": 60}'
```

`value` may also be an object, to set several members of one tag at once:

```json
{ "name": "WEL15_SUP_015", "value": { "START": true, "LVL": { "CTL1HSP": 60 } } }
```

That is a **merge**: members the object doesn't name keep their **current**
values. Deliberately unlike `init:` above, which zero-fills the members its
mapping omits — seeding builds a value from nothing, a write edits one the
plant is already running on.

- **Nothing is created.** An unknown tag, a misspelled member, or a member
  path into a scalar tag is a `400` carrying the reason
  (`tag WEL15_SUP_015: unknown member STRAT (did you mean START?)`), never a
  new top-level tag literally named `WEL15_SUP_015.STRAT` — which no program
  reads and a Sparkplug edge would publish as a bogus metric.
- **The leaf keeps its type.** A number lands on a `REAL` member as a REAL
  and on a `DINT` member as an integer; a mismatch is
  `tag WEL15_SUP_015.START: want BOOL, got a number`, not a silent retype.
- **The role rule is the root tag's**, because the store holds whole tags. A
  member of a driver-owned `input` is refused — the driver replaces the whole
  value before the next scan, so the edit could not survive one cycle.
  `setpoint`, `state` and `output` roots are writable; an output is exactly
  how a Sparkplug host commands an edge node, and a struct-typed output needs
  an `init:` so it exists before the first write.
- **`GET /api/meta`** reports `"memberWrites": true`, so an HMI can tell a
  controller that resolves member paths from an older one that swallowed them.

The same dotted paths work everywhere else a tag is addressed: a test
manifest's `given:`/`expect:`, the built-in dashboard's tag table (expand a
writable struct tag and each leaf is editable), and the HMI kit's
`rt.writeTag('WEL15_SUP_015.START', true)`, which resolves to `null` on
success or the controller's reason when it refuses.

## Adding a field input end to end

Adding a **new field input** is three lines in three places, all by the
same name: declare it in the program (`VAR_EXTERNAL testExt : REAL;` — or
from the diagram's vars panel), produce it in the driver
(`"testExt": p.testExt` in the `ReadInputs` map), and bind it in the
composition (`runtime.Input("testExt")`). The `Inputs` list is a deliberate
allowlist — a driver can't spray arbitrary names into the store — which is
why the middle step alone isn't enough.
