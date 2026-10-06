---
title: Coming from Studio 5000
description: Every Logix ladder instruction you reach for, as nautilus rung text and as it draws in the diagram, with a clear row for each one that is declined by design.
sidebar:
  order: 1
---

Studio 5000 (RSLogix) ladder is relay logic with a vendor instruction set.
nautilus ladder is the IEC 61131-3 grammar written as text, so most Logix
habits carry over one for one and a few have a different shape. This page is
the map. Read the [ladder page](/languages/ladder/) for the full grammar.

Rows marked **(in review)** are in [pull request
#225](https://github.com/joyautomation/nautilus/pull/225) and become
available when it merges. Rows marked **declined** are not coming; the
section after the tables says why and what to do instead, and the same list
is on [Not supported, by design](/languages/not-supported/).

## Contacts and coils

| Logix | nautilus rung text | In the diagram |
| --- | --- | --- |
| `XIC Tag` | `Tag` | NO contact, `Tag` above `--\ \--` |
| `XIO Tag` | `/Tag` | NC contact, a slash through the contact |
| `OTE Tag` | `( Tag )` | output coil at the right rail |
| `OTL Tag` | `( S Tag )` | coil marked `S` |
| `OTU Tag` | `( R Tag )` | coil marked `R` |
| `OSR` (output) | `( P Tag )` | coil marked `P`: `Tag` is TRUE for one scan when the rung condition rises |
| `OSF` (output) | `( N Tag )` | coil marked `N`: one scan when the rung condition falls |
| `ONS` | `+Tag` as a contact, or `( P Pulse )` then `Pulse` on the next rung | the pulse coil draws as `P`; an edge contact (`+Tag`) is not drawn as a contact yet ([#212](https://github.com/joyautomation/nautilus/issues/212)) and has no palette item ([#213](https://github.com/joyautomation/nautilus/issues/213)), so write it in the text |
| Branch | `[ a \| b ]` | parallel legs, nested to any depth |

`ONS` is a one-shot at a position in the rung. nautilus has two spellings.
If the thing that should fire once is a single tag, put `+Tag` where the
`ONS` would be. If it is a whole rung condition, end that rung in a pulse
coil and use the pulse as the contact on the next rung. There are no
storage bits to declare: the compiler generates the edge instance and keeps
its state stable across unrelated edits. `-Tag` is the falling-edge
contact.

## Timers and counters

A timer or counter is a block instance in the rung. Power from the rung
drives its input pin and leaves from its output pin; every other pin is
bound by name in the parentheses. `PT` is a `TIME`, written `T#5S`, not a
millisecond `DINT`.

| Logix | nautilus | Notes |
| --- | --- | --- |
| `TON t PRE 5000` | `t:TON(PT := T#5S)` | declare `t : TON;` under `VAR` |
| `t.DN` | `t.Q` | readable anywhere, including as a contact |
| `t.TT` | the rung's own condition, and `/t.Q` | there is no separate timing bit |
| `t.EN` | the rung's own condition | power into the block is the enable |
| `t.ACC` | `t.ET` | a `TIME`; compare with `GE(t.ET, T#2S)` |
| `t.PRE` | `PT` | bound at the call, or any `TIME` expression |
| `TOF t` | `t:TOF(PT := T#5S)` | `Q` stays TRUE for `PT` after `IN` drops |
| `RTO t` | no retentive timer block | see below |
| `RES t` (timer) | `t:TONR(PT := T#5S, Reset := Clear)` **(in review)** | needs `dialect: logix` in `nautilus.yaml`; clears `ET` and `Q` while `Reset` is TRUE |
| `CTU c PRE 10` | `c:CTU(PV := 10, R := ResetC)` | rung power is `CU`; counts rising edges |
| `c.DN` | `c.Q` | |
| `c.ACC` | `c.CV` | |
| `CTD c` | `c:CTD(PV := 10, LD := Load)` | rung power is `CD`; `Q` when `CV <= 0` |
| `RES c` (counter) | the `R` pin: `c:CTU(PV := 10, R := ResetC)` | the reset is a pin bound to a tag, not a rung instruction |

Two semantic differences are worth knowing before you port logic:

- `CTD` is the IEC block. It counts down from `PV` (loaded by `LD`) and `Q`
  means `CV <= 0`. A Logix `CTD` counts from `ACC` and its `.DN` compares to
  the preset the other way round, so re-derive the done condition rather
  than translating the bit.
- There is no `RTO` block. A retentive timer is an accumulator you write
  yourself: in a rung with an assignment **(in review)**,
  `Run { Hours := Hours + ScanDt }`, or in an ST block, and cleared by a
  reset rung.

## Compare, math and data movement

| Logix | nautilus | Notes |
| --- | --- | --- |
| `EQU A B` | `EQ(A, B)` | a function contact; `/EQ(A, B)` negates |
| `NEQ`, `GRT`, `GEQ`, `LES`, `LEQ` | `NE`, `GT`, `GE`, `LT`, `LE` | the diagram captions follow the IEC names |
| `LIM Low Test High` | `GE(Test, Low) LE(Test, High)` | two contacts in series. The IEC `LIMIT(MN, IN, MX)` clamps a value, it does not test one, so it is the wrong spelling for this |
| `MOV Src Dest` | `{ Dest := Src }` **(in review)** | made while the rung has power; power passes through |
| `CPT Dest Expr` | `{ Dest := Expr }` **(in review)** | any IEC expression, evaluated every scan |
| `ADD`, `SUB`, `MUL`, `DIV`, `MOD`, `NEG`, `ABS` | `{ Sum := A + B }` and the matching operators **(in review)** | until then, an ST program or block does the arithmetic |
| bit of a `DINT` (`Status.3`) | `Status.3` **(in review)** | a BOOL, readable and assignable; bit 0 is least significant |

Ladder power is boolean, so a numeric function is not a contact on its own.
`ADD(A, B)` goes inside a comparison, `GE(ADD(Base, Bias), Limit)`, or into
an assignment once #225 lands.

The assignment is one element in the rung's text, `{ Dest := Expr }`, with
power passing straight through. Until #225 merges, put the math in an ST
program that shares the tag store with the ladder one; that is the pattern
the demo project uses.

## Subroutines and Add-On Instructions

| Logix | nautilus | In the diagram |
| --- | --- | --- |
| `JSR` / `SBR` / `RET` | a `FUNCTION_BLOCK` call | a block in the rung, pins down both sides |
| AOI | `FUNCTION_BLOCK` in `lib/*.ld` | the same block, opened by double-click |
| AOI parameters | `VAR_INPUT`, `VAR_OUTPUT`, `VAR_IN_OUT` | pins |
| AOI backing tag | the instance, `m101 : MotorStarter;` | the instance name above the block |
| `m101.Member` | `m101.Pin` | readable anywhere as a contact or an operand |
| local tags | `VAR` | per-instance retained state |

A `JSR` shares the controller's tags with its caller. A block has pins and
its own state per instance, so two pumps are two instances of one block with
no shared tags to collide. This is what an AOI already was; port a routine
as a block and pass what it needs as pins. See [Function blocks, libraries,
and tasks](/guides/blocks-and-tasks/).

## Tags, tasks and programs

| Logix | nautilus |
| --- | --- |
| controller-scope tag | an entry in `nautilus.yaml` or `tags/*.yaml`, named in each program by `VAR_EXTERNAL` |
| program-scope tag | `VAR` in the program |
| alias tag, rack point | the tag's `alias:` field, `dialect: logix` **(in review)** |
| task, program | `tasks:` in `nautilus.yaml`, one program file per task |
| UDT | `TYPE … STRUCT … END_STRUCT` |
| tag description | the tag's `desc:` (the diagram does not show it on the element yet, [#216](https://github.com/joyautomation/nautilus/issues/216)) |

Today every program repeats `VAR_EXTERNAL` for each manifest tag it uses.
That is changing: manifest tags become visible in every program without it
([#177](https://github.com/joyautomation/nautilus/issues/177),
[#210](https://github.com/joyautomation/nautilus/issues/210)), and
`VAR_EXTERNAL` stays legal.

## Declined, by design

| Logix | Answer |
| --- | --- |
| `JMP` / `LBL` | not supported. Put the skipped logic in a function block and call it conditionally, or gate its rungs on a shared condition |
| `MCR` | not supported. Use the function-block pattern below |
| `GSV` / `SSV` | `dialect: logix` (planned): controller and module attributes as blocks. Not available yet |
| `COP` / `CPY` | `dialect: logix` (planned). For a UDT, assign the structure or its members in ST |
| `AFI`, `NOP` | nothing to translate; they do nothing |

### JMP and LBL

Rungs run top to bottom every scan, and that is the whole control-flow
model. A jump that skips rungs is the same as the skipped rungs having an
enabling condition, and a block is the way to say that.

### MCR

A master control relay zone de-energizes every non-retentive output between
two `MCR` rungs when the zone is off. The function-block pattern is the same
behavior stated once, in a unit with its own pins. Put the zone's rungs in a
`FUNCTION_BLOCK` with an `Enable` input, and gate the outputs on it:

```iecld
FUNCTION_BLOCK Zone1
VAR_INPUT  Enable : BOOL;  Start : BOOL;  Stop : BOOL;  END_VAR
VAR_OUTPUT Run : BOOL;  END_VAR
LD
  RUNG run
    [ Start | Run ] /Stop Enable ( Run )
END_LD
END_FUNCTION_BLOCK
```

Call it from the main program with `z1:Zone1(Enable := ZoneOn, Start := …)`.
Every output inside is dead while `Enable` is FALSE, as `MCR` makes it.
Latched outputs that `MCR` would not clear are written explicitly, which is
the safer thing to read in review.

## Where the rest is

- Editing: **FB…** in the ladder palette inserts the block call that you
  would have built from the instruction toolbar. [In the
  editor](/languages/ladder/#in-the-editor) lists every gesture.
- Importing: a Rockwell `.L5X` export opens as ladder with no Studio 5000
  installed; see the [Allen-Bradley Logix guide](/guides/logix/).
- Starting from ladder: choose **nautilus: Create Project…**, then
  Minimal, then Ladder Diagram.
