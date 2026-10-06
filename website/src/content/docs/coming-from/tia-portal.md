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
| Tag table visible in every block | manifest tags are in scope in every program without a declaration (`VAR_EXTERNAL` stays legal). A function block reaches a tag only through its own `VAR_EXTERNAL`, so it stays self-contained, as a TIA FB with its interface does | Today, [#177](https://github.com/joyautomation/nautilus/issues/177), [#210](https://github.com/joyautomation/nautilus/issues/210) |
| Data type column (`Int`, `Bool`, `Real`, `Time`, a UDT) | the tag's `type:`: any elementary type, a UDT, or an `ARRAY`; `init:` must agree with it | Today, [#200](https://github.com/joyautomation/nautilus/issues/200) |
| UDT with `Time` members seeded by default values | `init:` seeds a TIME member from `T#2s`, `2s` or milliseconds, and an array member from a list | Today, [#201](https://github.com/joyautomation/nautilus/issues/201) |
| An error in one block | reported once, on its own line; the blocks that use it are not marked | Today, [#199](https://github.com/joyautomation/nautilus/issues/199) |
| Library | files in `lib/`, composed ahead of every task | Today |
| Global library, version management | not available; copy or vendor the `lib/` files with git | Declined for now, [#193](https://github.com/joyautomation/nautilus/issues/193) |

## SCL

| SCL | nautilus | Status |
| --- | --- | --- |
| `IF`, `CASE`, `FOR`, `WHILE`, `REPEAT` | the same | Today |
| `CASE` labels that are named constants | the same: a `VAR CONSTANT` (or project constant, or enumeration member) label ends its clause; two labels with one value are an error naming both | Today, [#196](https://github.com/joyautomation/nautilus/issues/196) |
| Case-insensitive identifiers (`MyTag` = `mytag`) | the same: any casing names the one declaration, and diagnostics, live values and the API show it as declared | Today, [#197](https://github.com/joyautomation/nautilus/issues/197) |
| `#name` for a local variable | write `name`. `#` is rejected everywhere with "the # prefix is Siemens SCL syntax; write the name without it". It may be accepted later only under `dialect: siemens` | Rejected with a clear message, [#198](https://github.com/joyautomation/nautilus/issues/198) |
| `"Tag"` quoted global name | write the tag's plain name | Not applicable |
| `VAR_TEMP` | the same as `Temp`: starts every call (and every scan of a program) at its initial value | Today, [#203](https://github.com/joyautomation/nautilus/issues/203) |
| `REGION … END_REGION` | the same: groups statements, folds in the editor, shows in the Outline, opens no scope | Today, [#202](https://github.com/joyautomation/nautilus/issues/202) |
| User constants | `VAR CONSTANT` in a block, or `VAR_GLOBAL CONSTANT` in a `lib/` file for the whole project | Today, [#176](https://github.com/joyautomation/nautilus/issues/176) |
| `Real`, `DInt`, `Time` in SCL's casing | the same; any casing | Today |
| `Word.%X3` bit access, `%B`/`%W`/`%D` partial access | the same, or Logix's `Word.3`; bounded by the declared type | Today, [#222](https://github.com/joyautomation/nautilus/issues/222) |
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
table maps, and the one rule that surprises everyone, which is that naming
a tag binds it and does not create it.
