---
title: Coming from TIA Portal
description: SCL and FBD habits from TIA Portal mapped to nautilus, with each gap marked as available today, planned (with its issue), or declined.
sidebar:
  order: 2
---

SCL is Structured Text with Siemens conventions, and TIA's FBD is the IEC
function block diagram with networks. nautilus runs the IEC languages, so
most of the body of an SCL program is already valid [Structured
Text](/languages/structured-text/). The differences are in how a project is
organised and in a handful of SCL extensions. Every row below says which of
three things it is:

- **Today**: works on the current release.
- **Planned**: pursued in the parity batch, linked to its issue, and **not
  available yet**. Do not write projects that depend on it.
- **Declined**: not coming, with the alternative.

## Program organisation

| TIA Portal | nautilus | Status |
| --- | --- | --- |
| OB1 (cyclic) | a task in `nautilus.yaml` with a scan period, one program file each | Today |
| FC | `FUNCTION`: no state, returns a value | Today |
| FB | `FUNCTION_BLOCK`: state per instance | Today |
| Instance DB | the instance, declared `pump : MotorStarter;` under `VAR` | Today |
| Multi-instance | an instance declared inside another block's `VAR` | Today |
| Global DB | a UDT-typed tag in the manifest (`type: Motor`), or plain tags | Today |
| PLC tag table | tags in `nautilus.yaml` or `tags/*.yaml` | Today |
| Tag table visible in every block | manifest tags in every program without `VAR_EXTERNAL`; today each program declares what it uses | Planned, [#177](https://github.com/joyautomation/nautilus/issues/177), [#210](https://github.com/joyautomation/nautilus/issues/210) |
| `INT`-typed entry in the tag table | `type: INT` is refused today; only a UDT name is accepted | Planned, [#200](https://github.com/joyautomation/nautilus/issues/200) |
| UDT with `Time` members seeded by default values | `init:` cannot seed a TIME member of a UDT tag yet | Planned, [#201](https://github.com/joyautomation/nautilus/issues/201) |
| Library | files in `lib/`, composed ahead of every task | Today |
| Global library, version management | not available; copy or vendor the `lib/` files with git | Declined for now, [#193](https://github.com/joyautomation/nautilus/issues/193) |

## SCL

| SCL | nautilus | Status |
| --- | --- | --- |
| `IF`, `CASE`, `FOR`, `WHILE`, `REPEAT` | the same | Today |
| `CASE` labels that are named constants | silently wrong today, avoid until fixed | Planned, [#196](https://github.com/joyautomation/nautilus/issues/196) |
| Case-insensitive identifiers (`MyTag` = `mytag`) | identifiers are case-sensitive today, so a `FUNCTION` called from FBD must match its spelling | Planned, [#197](https://github.com/joyautomation/nautilus/issues/197) |
| `#name` for a local variable | rejected; the message is being made to say why. It will be accepted later only under `dialect: siemens` | Rejected; dialect only, [#198](https://github.com/joyautomation/nautilus/issues/198) |
| `"Tag"` quoted global name | write the tag's plain name | Not applicable |
| `VAR_TEMP` | parses, but keeps its value between scans. TIA's `Temp` is scratch cleared per call | Planned, [#203](https://github.com/joyautomation/nautilus/issues/203) |
| `REGION … END_REGION` | not supported; the diagnostic is poor | Planned, [#202](https://github.com/joyautomation/nautilus/issues/202) |
| `Real`, `DInt`, `Time` in SCL's casing | `REAL`, `DINT`, `TIME` | Today (case-sensitive until #197) |
| `Word.%X3` bit access | `Word.3` | In review, [#225](https://github.com/joyautomation/nautilus/pull/225) |
| `IEC_TIMER` / `TON` DB | `t : TON;` and `t(IN := Run, PT := T#5S);` | Today |

Standard timers and counters have the IEC pins, so SCL calls such as
`t(IN := Run, PT := T#5S); Done := t.Q;` carry over unchanged.

## FBD and ladder

| TIA FBD or LAD | nautilus | Status |
| --- | --- | --- |
| Network | a `RUNG` in ladder, with a name and a comment. In FBD the netlist is one flat list of wires | Rungs today; FBD networks planned, [#207](https://github.com/joyautomation/nautilus/issues/207) |
| Network title and comment | the `(* … *)` slot on a rung | Rungs today; FBD planned, [#207](https://github.com/joyautomation/nautilus/issues/207) |
| `EN` / `ENO` on standard blocks | none: blocks always evaluate. A user block in a ladder rung takes power on its `EN` pin if it declares one | Planned for FBD, [#206](https://github.com/joyautomation/nautilus/issues/206) |
| Execution order by network | source order, with a block evaluated before its readers | Today |
| Block call from the palette | **FB…** in the ladder palette; the FBD palette suggests blocks | Today. Suggesting the project's own `FUNCTION`s in FBD is [#204](https://github.com/joyautomation/nautilus/issues/204) |
| Open inputs left unconnected | an unconnected input must be bound or dropped from the call | [#205](https://github.com/joyautomation/nautilus/issues/205) |

## Getting started

`naut new my-plant --template minimal --language fbd` scaffolds an FBD
program; **nautilus: Create Project…** asks for the language. Then read the
[tag model](/guides/tag-model/) first: it is where a global DB or a tag
table maps, and the one rule that surprises everyone, which is that a
`VAR_EXTERNAL` declaration binds a tag and does not create it.
