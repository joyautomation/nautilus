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

**A `VAR_EXTERNAL` declaration binds a name; it does not create the tag.**
Declaring `TempC : REAL;` in your program means "resolve this name in the
tag store at scan time", and nothing more. Existence
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

## Adding a field input end to end

Adding a **new field input** is three lines in three places, all by the
same name: declare it in the program (`VAR_EXTERNAL testExt : REAL;` — or
from the diagram's vars panel), produce it in the driver
(`"testExt": p.testExt` in the `ReadInputs` map), and bind it in the
composition (`runtime.Input("testExt")`). The `Inputs` list is a deliberate
allowlist — a driver can't spray arbitrary names into the store — which is
why the middle step alone isn't enough.
