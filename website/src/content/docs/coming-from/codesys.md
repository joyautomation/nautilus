---
title: Coming from Codesys
description: Codesys habits (GVLs, enums, timed SFC qualifiers, step supervision, the library manager) mapped to nautilus, with the gaps marked planned or declined.
sidebar:
  order: 3
---

Codesys and its many vendor builds are close to the IEC standard, so
nautilus will read like a smaller version of what you know. The rows below
say what carries over, what is pursued, and what is declined. **Planned**
means pursued in the parity batch and **not available yet**.

## Project and data

| Codesys | nautilus | Status |
| --- | --- | --- |
| POU (`PROGRAM`, `FUNCTION_BLOCK`, `FUNCTION`) | the same, in `.st`, `.ld`, `.fbd` or `.sfc` | Today |
| Case-insensitive identifiers (`MyVar` = `MYVAR`) | the same: any casing names the one declaration, shown as declared | Today, [#197](https://github.com/joyautomation/nautilus/issues/197) |
| Task configuration | `tasks:` in `nautilus.yaml` | Today |
| GVL (global variable list) | tags in `nautilus.yaml` or `tags/*.yaml`, in scope in every program without a declaration (`VAR_EXTERNAL` stays legal); a function block names the ones it uses in its own `VAR_EXTERNAL` | Today, [#177](https://github.com/joyautomation/nautilus/issues/177) |
| A GVL file of only `VAR_GLOBAL` | the same: a `gvl.st` with a file-level `VAR_GLOBAL` block declares globals for every program; one the manifest does not declare is a `state` tag starting at zero. An initial value goes in the manifest's `init:` | Today, [#175](https://github.com/joyautomation/nautilus/issues/175) |
| `VAR_GLOBAL CONSTANT` shared by the project | the same, in a `lib/` file: every POU sees the constants, folded at compile time; they are not tags | Today, [#176](https://github.com/joyautomation/nautilus/issues/176) |
| Enumerations (`TYPE E : (A, B, C)`, `(A := 1, …)`) | the same: `E#A` or plain `A`, `CASE` labels, comparisons, `TO_INT` / `TO_E`; live values show the member name | Today, [#238](https://github.com/joyautomation/nautilus/issues/238) |
| `VAR_TEMP` | the same: reset on every call | Today, [#203](https://github.com/joyautomation/nautilus/issues/203) |
| `CASE` on named constants | the same | Today, [#196](https://github.com/joyautomation/nautilus/issues/196) |
| `STRUCT`, arrays, `FUNCTION_BLOCK` instances | the same | Today |
| `VAR_IN_OUT` | the same | Today |
| Library manager, `.library` files | not available. A project's reusable blocks live in `lib/`; share them between projects by copying files or vendoring with git | Declined for now, [#193](https://github.com/joyautomation/nautilus/issues/193) |
| Online force | no force table in the IDE yet | Planned, [#192](https://github.com/joyautomation/nautilus/issues/192) |

## SFC

nautilus SFC is the IEC textual form (`STEP`, `TRANSITION`, `ACTION`), and
the editor draws it as a chart.

| Codesys SFC | nautilus | Status |
| --- | --- | --- |
| `Step.X`, `Step.T` | `Step.X`, `Step.T`, an elapsed `TIME`: `ChargeA.T >= T#5M` | Today |
| Qualifiers `N`, `S`, `R`, `P` | the same, plus `P1` and `P0` | Today |
| Qualifiers `L`, `D`, `SD`, `DS`, `SL` | parse, then error naming the supported set. Until they land, use `Step.T` in the action or in the transition | Planned, [#190](https://github.com/joyautomation/nautilus/issues/190), error text [#184](https://github.com/joyautomation/nautilus/issues/184) |
| The IEC textual association `Detergent(D, T#3S);` | not accepted | Planned, [#189](https://github.com/joyautomation/nautilus/issues/189) |
| `SFCError` and step supervision (maximum step time) | no step supervision today. Planned: a small per-step maximum time that raises an alarm through the alarm engine and sets an error flag | Planned, [#191](https://github.com/joyautomation/nautilus/issues/191) |
| Alternative branch priority | declaration order, first highest. Reordering gestures are planned | Today; gestures [#181](https://github.com/joyautomation/nautilus/issues/181) |
| Numeric transition priority | not supported; declaration order is the only rule | Declined |
| Transition conditions in LD, FBD or IL | not supported; a transition is an ST expression | Declined |
| Action bodies in LD, FBD or IL | not supported; action bodies are ST | Declined |
| Force a step or transition online | no | Planned, [#192](https://github.com/joyautomation/nautilus/issues/192) |
| Step names in the editor, new action, new POU | gestures to create an `ACTION` ([#182](https://github.com/joyautomation/nautilus/issues/182)) and a new empty POU ([#179](https://github.com/joyautomation/nautilus/issues/179)) | Planned |

See the [SFC page](/languages/sfc/) for the semantics: one transition per
scan from a snapshot, divergence and convergence, and how associations and
action bodies interact.
