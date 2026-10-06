---
title: Forcing
description: Hold an input or an output at a value against the field and the logic, see every force at a glance, and jump an SFC chart to a step or fire a transition once.
---

A **force** holds a tag at a value until you remove it, whatever the field
and the logic say. It is the commissioning tool every PLC has: force a level
switch to check an interlock before the tank is wet, hold a motor output off
while the sequence runs, stand in for a sensor that is not wired yet.

**Set Live Value** is not that. It writes once; the driver rewrites an input
on the next scan, and the logic rewrites an output it drives. A force is
re-applied every scan until it is removed.

## What a force does

| Forced tag | Every scan |
|---|---|
| an **input** (`role: input`) | the driver's reading is replaced by the forced value before the logic runs — the program sees the forced value |
| an **output** or **state** tag | the forced value is laid back over whatever the logic wrote, so the driver is handed the forced value |
| a **struct member** (`P101.Speed`, `Recipe.Steps[2].Mode`) | only that member is held; the rest of the struct follows its writers |

A forced value is coerced like any write: an enumerated member or tag takes
its member name (`Run`), and a name that is no member is refused with the
list of members.

The controller records what the tag *would* hold — the driver's latest
reading, the logic's latest write — and the force table shows it beside the
forced value. Removing a force puts the tag back to that value at once, not
on the next write.

A write to a forced tag is refused (`409`) rather than accepted and then
silently overridden: change the force, or remove it.

## From VS Code

- **Force…** — right-click an identifier in any `.st`, `.fbd`, `.ld` or
  `.sfc` file, hover a live value pill, or use the lock on a Live Values
  row. A dotted member path (`P101.Speed`) can be forced too.
- **Remove Force** / **Remove All Forces** — the same places, and the
  unlock on a forced row.
- Forced values are marked everywhere a live value is shown: the inline
  pill turns amber with an **F**, the Live Values panel lists a **Forces**
  group first and marks each forced row `F <value>`, and the FBD, ladder
  and SFC diagrams put an **F** on the value.
- A status-bar item, **N forces active**, stays up while anything is forced
  (Logix keeps this in plain sight too). Click it to list the forces and
  remove one, or all.

Force and Remove All ask for confirmation, naming the controller, while
`nautilus.confirmControllerWrites` is on (the default). Against a
token-protected controller the editor sends `nautilus.token`.

## SFC: set a step, fire a transition

With a chart running, right-click a step in the SFC diagram for **Set
Active Step**, or a transition for **Fire Transition** (both are also in the
Command Palette, with a pick list of the running charts).

- **Set Active Step** jumps the chart once, Codesys's *set step*: every step
  is deactivated and that one activated. Old steps see a falling edge (P0
  and final-scan actions run), the new step a rising edge (P/P1 actions
  run, `Step.T` starts from zero). Nothing is held — the chart evolves
  normally from the next scan, so a step whose outgoing condition is already
  TRUE is left on that scan.
- **Fire Transition** takes one transition once, whatever its condition
  says: its source steps are deactivated and its targets activated. It is
  refused when a source step is not active — firing it then would invent a
  token; use Set Active Step for that.

## Lifetime

Forces are deliberately short-lived:

- **Not retained.** A restart comes up with no forces. Logix keeps forces
  in the project across a power cycle; nautilus does not, because a
  controller that boots holding forces nobody remembers is a commissioning
  hazard, and a nautilus restart is usually a deploy.
- **Active controller only.** With [redundancy](/guides/redundancy/), the
  force table lives on the leader (a standby proxies the API to it). A
  takeover drops it, and so does a leader stepping down — a standby never
  saw the forces, and a flapping leader must not quietly resume holding
  values nobody may still be watching.
- **Audited.** Every force, removal, clear-all, step jump and fired
  transition leaves a line in the controller's log with the caller's
  address.

## The API

Every write passes the same guard as `POST /api/tags` (same-origin by
default; the token when `NAUTILUS_TOKEN` is set).

```text
GET    /api/forces          {"forces": [{"name", "value", "actual"?, "sinceMs"}]}
POST   /api/forces          {"name": "StartPB", "value": true}   force (or change a force)
DELETE /api/forces/{name}   remove one force (404 if it was not forced)
POST   /api/forces/clear    remove every force → {"removed": n}
GET    /api/sfc             running charts: steps (active), transitions (enabled)
POST   /api/sfc/step        {"step": "Fill", "pou"?: "Batch"}    jump once
POST   /api/sfc/transition  {"transition": "Start", "pou"?: …}   fire once
```

A transition is addressed by its name, or `t<line>` (the line of its
`TRANSITION` keyword) when it has none. `pou` is needed only when two
running charts share the step or transition name.

Every stream frame carries `forces` (address → forced value) while at least
one force is active, and omits it when none is; it is never delta-gated, so
an absent block always means nothing is forced. `/api/meta` advertises
`"forces": true`.

## In tests

An acceptance test can hold a tag the same way, with `force:` and
`unforce:` steps — see [Testing](https://github.com/joyautomation/nautilus/blob/main/docs/testing.md#forcing--holding-a-tag-against-its-writers).
