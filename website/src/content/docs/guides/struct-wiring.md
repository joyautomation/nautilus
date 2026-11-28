---
title: Struct wiring
description: Give a plain IEC TYPE its I/O member by member, and declare its tags as instances, so one struct holds a device's readings, its commands and the program's own values.
---

**Struct wiring** gives a plain IEC `TYPE` its I/O member by member. The
type stays plain ST, written like any other. Beside it, a **wiring** says
where each member's value comes from, and an **instance** is a tag of the
type with the values that wiring needs (an endpoint, a unit id). A program,
an operator screen and a function block then see one struct per device: its
readings, its commands and the program's own values together.

This build runs struct wiring on **Modbus**. The rest of the design arrives
slice by slice (issue #1458), and what is not here yet is refused at load,
naming when it arrives (see [Not yet](#not-yet)). Nothing is run half-way.

## A pump

```iecst
TYPE PumpCmd : STRUCT
  Start    : BOOL;
  SpeedRef : REAL;
END_STRUCT; END_TYPE

TYPE Pump : STRUCT
  Running  : BOOL;
  Speed    : REAL;          (* % *)
  SpeedSP  : REAL := 50.0;
  Cmd      : PumpCmd;
END_STRUCT; END_TYPE
```

```yaml
# nautilus.yaml
wirings:
  - type: Pump
    params: { endpoint: null, unit-id: 1 }   # null: every instance gives it
    sources:
      drive: { driver: modbus, endpoint: "{endpoint}", unit-id: "{unit-id}" }
    members:
      Running:      { from: drive, table: coil, address: 0 }
      Speed:        { from: drive, table: holding, address: 4, format: int16, scale: 0.1, unit: "%" }
      Cmd.Start:    { to: drive, table: coil, address: 10 }
      Cmd.SpeedRef: { to: drive, table: holding, address: 20, format: int16, scale: 0.1, readback: Speed }

instances:
  - { name: P101, type: Pump, desc: "Lift pump 1", params: { endpoint: 10.0.0.11 } }
  - { name: P102, type: Pump, desc: "Lift pump 2", params: { endpoint: 10.0.0.12 } }
```

```iecst
PROGRAM Main
  P101.Cmd.Start := Demand AND NOT P101.Running;
END_PROGRAM
```

`P101` and `P102` are tags of type `Pump`. A member's address keys are the
Modbus manifest's own (`table`, `address`, `format`, `scale`, `offset`,
`scan-class`, and `rewrite` on a command), and a source takes `endpoint`,
`unit-id`, `word-order`, `byte-order`, `timeout` and `max-block`. The
project's Modbus driver carries the bindings; one is added when the project
has none.

## Member kinds

| Kind | Value comes from | Who may write it |
|---|---|---|
| `from: <source>` | the device, each poll | nobody: a program's assignment is never committed, and an API, OPC UA or Sparkplug write is refused |
| `to: <source>` | the program or an operator; the driver sends it | the program, an operator, a client the server opens it to |
| `memory: retained` or `memory: initial` | its initial value, then whoever writes it | the program, an operator, a client the server opens it to |

A member the wiring does not name is memory (`SpeedSP` above).
`naut check` lists those members, so a forgotten entry is seen.

## Commands

A `to:` member is the Modbus driver's own output, with the driver's rules.
It is bound write-only (its read-back is the `from:` member, not the
command's own register), so the first value a program writes after a start
is sent once, as for a `devices:` row's command tag; after that only a
change is sent, and after the write gate reopens or a takeover the command
is sent again (a level). On top of that:

- **Written per leadership.** A command that nothing has written since the
  start (or the takeover) is not sent, whatever it holds. Its initial value
  is a seed for the program, never a command. The program usually writes
  its commands on its first scan, so they go out as before.
- **`event: true`** marks a command that must never be sent twice (a reset,
  a pulse): its first value after a start, a takeover or a gate that
  reopened is a baseline, and only a later change goes out.
- **`readback: <member>`** names the `from:` member that shows what the
  device did. A command of the same type that nothing has written yet takes
  the read-back's value when it first arrives. That is no command; it only
  means "commanded, not reached" does not show after every restart.

## Params and overrides

`{param}` substitutes into a source's keys and a member's address keys. A
value that is one `{param}` alone takes the param's own type
(`address: "{reg}"` with `reg: 40` is the register 40). `{name}` and
`{desc}` are built in. An instance changes one member's address or source
with `wire:`, never its kind:

```yaml
instances:
  - { name: P103, type: Pump, params: { endpoint: 10.0.0.13 }, wire: { Speed: { address: 6 } } }
```

## Wiring files

`wiring-files: [wiring/pumps.yaml]` lists YAML files that hold `wirings:`
and `instances:`, resolved inside the project like `tag-files:`. Wirings,
instances and tags share one namespace across every file.

## Companions and quality

An instance on its own connection gets `<Instance>__Online` and
`<Instance>__Outages`, as a `devices:` row does. Quality is per member: the
root shows the worst of its device members, and a screen bound to
`P101.Speed` greys only that member.

## No command is lost without a word

Every command an instance sends must reach a driver. A project whose
instance has a command or a reading no driver binds does not start; the
error names the member and its key (`P101.Cmd.Start`). A write the drivers
of a multi-driver set do not claim is logged once, naming its key.

## Not yet

| Refused today | Arrives with |
|---|---|
| `memory:` retained across a restart (accepted, but `naut check` warns: it starts from its initial value) | slice 6 |
| `set:` on an instance | slice 6 |
| `expr:` members | slice 8 |
| an instance on more than one connection, OPC UA sources, wiring `alarms:`, instance `alarms:` and `offline-alarm:` | slice 9 |
| `fill:` and `both:` (EtherNet/IP) | slice 12 |
| the Sparkplug host | slice 14 |
| the IT drivers | slice 15 |
| a wiring edit through the [live I/O apply](/guides/live-io-configuration/) | a restart applies it |

On a `logix` or `siemens` target `naut check` refuses a wired instance:
the writer writes the `TYPE`, but does not carry wiring yet (#1478), so
nothing on the controller would fill it, and so do `naut logix` and
`naut siemens` when they write the project. Two instances on one device
(one endpoint and unit id, however the endpoint is spelled), or an instance
on a device a `devices:` row already polls, are refused too: one device,
two connections. So are two commands of one instance on one register.

The design is `docs/design/one-struct-model.md` in the repository.
