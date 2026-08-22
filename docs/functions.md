# Language reference: evaluation flow, functions, and function blocks

This is the reference for what runs inside a nautilus controller: how a
scan evaluates your logic in each IEC 61131-3 language, and what every
built-in operator, function, and function block does — its arguments, its
result type, and its behavior. The compiler enforces everything described
here; when logic doesn't type-check (say, a numeric function used where a
BOOL must flow), the result is a **compile diagnostic on the offending
line/rung**, never a silent coercion.

- [How a scan evaluates](#how-a-scan-evaluates)
- [Ladder power flow](#ladder-power-flow)
- [Types](#types)
- [Operators](#operators)
- [Standard functions](#standard-functions)
- [Type conversions](#type-conversions)
- [Standard function blocks](#standard-function-blocks)
- [User function blocks](#user-function-blocks)

## How a scan evaluates

Every task runs the classic PLC cycle on its interval:

```
read inputs → evaluate the program top to bottom → write outputs
```

All three languages end up as the same intermediate form, one hop at a
time:

```
 .st  ─────────────────────────►  IR (compiled, type-checked)
 .fbd ── transpile ──► ST ──────►  IR
 .ld  ── transpile ──► FBD ──► ST ─►  IR
```

Because each hop preserves line maps, a diagnostic anywhere in that chain
lands back on **your** source — an `.ld` error points at the rung, an
`.fbd` error at the statement. The text file is always the source of
truth; the graphical editors are projections that edit it structurally.

Statements evaluate **top to bottom within a scan**, so a value written by
one rung/statement is visible to the ones below it in the same scan, and
to everything on the next scan (that's what makes the seal-in idiom work).

## Ladder power flow

A rung is a boolean expression that reads left to right:

| Element | Text | Meaning |
| --- | --- | --- |
| NO contact | `Tag` | passes power when `Tag` is TRUE |
| NC contact | `/Tag` | passes power when `Tag` is FALSE |
| Parallel branch | `[ a \| b ]` | OR of its legs; legs are series (AND) and nest freely |
| Function contact | `FN(args)` | passes power when the call yields TRUE — **the function must return BOOL** |
| Function block | `inst:TYPE(args)` | power drives the block's power-in pin; power continues from its power-out pin (table [below](#standard-function-blocks)) |
| Coil | `( Tag )` | `Tag :=` the rung condition, every scan |
| Set coil | `( S Tag )` | latch: `Tag := Tag OR condition` |
| Reset coil | `( R Tag )` | unlatch: `Tag := Tag AND NOT condition` |

Series elements AND together; a rung with only a coil is driven by the
rail (`TRUE`). Multiple coils on one rung share the same condition.

**The BOOL rule.** Power is boolean, so anything that gates it must yield
BOOL. Comparisons do: `GT(TempC, 90.0)` is a fine contact. Numeric
functions don't: `ADD(TempC, 0.0)` as a contact is a **compile error**
(`operator AND on BOOL and REAL`, or `cannot assign REAL to BOOL` when
it's alone on the rung) — ADD does not "pass through" its input. Numeric
functions belong *inside* a comparison's arguments — `GE(ADD(Base, Bias),
Limit)` — or in an FBD/ST program, where values rather than power flow
between elements.

Reading FB outputs: any instance output is addressable as `inst.Pin`
everywhere — `GE(t2.ET, T#2S)` as a contact, `t2.Q` as an operand, or
from another task's FBD/ST program. To capture an output **into a
variable** at the call site, use the standard's output binding:
`t2:TON(PT := T#5S, ET => Elapsed)` — the one way ladder stores a
non-BOOL (coils only assign BOOL). `=>` works in ST and FBD calls too.

## Types

`BOOL`, `INT`, `DINT`, `UINT`, `UDINT`, `WORD`, `REAL`, `LREAL`, `TIME`,
`STRING`, plus `ARRAY[lo..hi] OF T` and user `TYPE ... STRUCT`.

- Integer kinds share one 64-bit runtime representation; `REAL`/`LREAL`
  are float64.
- `TIME` counts **milliseconds** internally. Literals: `T#500MS`, `T#5S`,
  `T#2M30S`. TIME values compare with the ordinary comparisons.
- Mixed numeric arguments promote to REAL; comparing or combining a
  STRING with a number is an error, not a coercion.
- A `TYPE` is a first-class type: a UDT can be a variable, a tag, a struct
  field, an array element, a `FUNCTION` argument or return, and a
  `FUNCTION_BLOCK` pin. Structs nest.
- Assigning a struct or an array **copies** it. `b := a; b.F := 1` leaves
  `a.F` alone, and so does a struct passed to a `VAR_INPUT` pin. The one
  pin that writes back to the caller is `VAR_IN_OUT`, [below](#user-function-blocks).
- A field or element of a `VAR_EXTERNAL` tag assigns directly —
  `P101.Running := TRUE`, `Levels[2] := 41.0`. The tag store holds the
  whole aggregate, so the VM reads it, writes the field, and puts it back.

## Operators

These are the FBD/ladder block names that lower to ST operators. "n-ary"
means the block accepts 2+ inputs (the `+` pin in the FBD editor).

| Name | Arguments | Result | Behavior |
| --- | --- | --- | --- |
| `AND`, `OR`, `XOR` | n-ary BOOL (or INT for bitwise) | same | logical/bitwise |
| `NOT` | 1 BOOL/INT | same | negation/complement |
| `ADD` | n-ary numeric | common type | sum |
| `SUB` | 2 numeric | common type | difference |
| `MUL` | n-ary numeric | common type | product |
| `DIV` | 2 numeric | common type | quotient; integer ÷0 yields 0 (scan keeps running) |
| `MOD` | 2 INT | INT | remainder; ÷0 yields 0 |
| `MOVE` | 1 any | same | pass-through assignment (FBD wiring aid) |
| `GT`, `GE`, `LT`, `LE` | 2 comparable | **BOOL** | ordering (numeric or TIME) |
| `EQ`, `NE` | 2 comparable | **BOOL** | equality |

The comparison row is the ladder-relevant one: those six are the
functions that can gate power directly.

## Standard functions

Stateless, callable from ST, FBD blocks, and (where they return BOOL —
or inside arguments) ladder function contacts.

### Selection

| Name | Signature | Result | Behavior |
| --- | --- | --- | --- |
| `SEL` | `SEL(G: BOOL, IN0, IN1)` | type of IN0/IN1 | binary selector: G=FALSE → IN0, G=TRUE → IN1 |
| `MUX` | `MUX(K: INT, IN0, …, INn)` | common type | K picks the K-th input (0-based); an out-of-range K **faults the scan** — clamp K with `LIMIT` if it can wander |
| `MIN`, `MAX` | n-ary numeric | common type | smallest / largest |
| `LIMIT` | `LIMIT(MN, IN, MX)` | common type | IN clamped into [MN, MX] |

### Numeric

| Name | Signature | Result | Behavior |
| --- | --- | --- | --- |
| `ABS` | 1 numeric | same type | absolute value |
| `SQRT`, `LN`, `LOG`, `EXP` | 1 numeric | REAL | root, ln, log₁₀, eˣ |
| `EXPT` | `EXPT(base, exp)` | REAL | baseᵉˣᵖ |
| `TRUNC` | 1 REAL | INT | toward-zero truncation |
| `SIN`, `COS`, `TAN` | 1 numeric (radians) | REAL | trigonometry |
| `ASIN`, `ACOS`, `ATAN` | 1 numeric | REAL | inverse trig |
| `ATAN2` | `ATAN2(Y, X)` | REAL | quadrant-correct arctangent |

### Bit operations

| Name | Signature | Result | Behavior |
| --- | --- | --- | --- |
| `SHL`, `SHR` | `(IN: INT/WORD, N: INT)` | same as IN | shift left / logical shift right (zero-fill) |
| `ROL`, `ROR` | `(IN, N)` | same as IN | rotate left / right |

Caveat: the runtime's integers are 64-bit and declared widths aren't
tracked, so rotates operate over 64 bits — a `WORD` you think of as 16
bits rotates as a 64-bit value.

### Strings

All positions are **1-based** (IEC convention); length/position arguments
clamp to the string instead of faulting.

| Name | Signature | Result | Behavior |
| --- | --- | --- | --- |
| `LEN` | `LEN(IN)` | INT | length |
| `LEFT`, `RIGHT` | `(IN, L)` | STRING | first / last L characters |
| `MID` | `MID(IN, L, P)` | STRING | L characters starting at position P |
| `CONCAT` | n-ary STRING | STRING | concatenation |
| `INSERT` | `INSERT(IN1, IN2, P)` | STRING | IN2 inserted into IN1 after position P |
| `DELETE` | `DELETE(IN, L, P)` | STRING | L characters removed starting at P |
| `REPLACE` | `REPLACE(IN1, IN2, L, P)` | STRING | L characters at P replaced by IN2 |
| `FIND` | `FIND(IN1, IN2)` | INT | 1-based position of IN2 in IN1; **0** when absent or IN2 empty |

## Type conversions

Explicit, in the standard's `X_TO_Y` naming — there are no implicit
conversions across kinds:

| Conversion | Notes |
| --- | --- |
| `INT_TO_REAL`, `REAL_TO_INT` | REAL→INT rounds to nearest |
| `BOOL_TO_INT`, `INT_TO_BOOL` | 0 ↔ FALSE, nonzero → TRUE |
| `INT_TO_TIME`, `TIME_TO_INT` | the INT is **milliseconds** |
| `REAL_TO_TIME`, `TIME_TO_REAL` | milliseconds, rounded to nearest |
| `INT_TO_STRING`, `REAL_TO_STRING`, `BOOL_TO_STRING`, `TIME_TO_STRING` | formatting |
| `STRING_TO_INT`, `STRING_TO_REAL`, `STRING_TO_BOOL` | parse; a non-parsing string is a runtime scan fault, so validate upstream |

## Standard function blocks

Stateful — declare an instance (`VAR t1 : TON; END_VAR`, or inline in a
rung as `t1:TON(...)`), and each instance keeps its own state between
scans. Outputs read as `inst.Pin` from any language.

| Type | Inputs | Outputs | Behavior |
| --- | --- | --- | --- |
| `TON` | `IN: BOOL, PT: TIME` | `Q: BOOL, ET: TIME` | on-delay: Q rises after IN has been TRUE for PT; ET is elapsed |
| `TOF` | `IN, PT` | `Q, ET` | off-delay: Q stays TRUE for PT after IN drops |
| `TP` | `IN, PT` | `Q, ET` | pulse: rising IN produces a PT-wide TRUE pulse |
| `CTU` | `CU: BOOL, R: BOOL, PV: INT` | `Q: BOOL, CV: INT` | count rising CU edges; Q when CV ≥ PV; R resets |
| `CTD` | `CD: BOOL, LD: BOOL, PV: INT` | `Q, CV` | count down from PV (LD loads); Q when CV ≤ 0 |
| `CTUD` | `CU, CD, R, LD, PV` | `QU, QD, CV` | up/down counter |
| `R_TRIG` | `CLK: BOOL` | `Q: BOOL` | Q for exactly one scan on CLK's rising edge |
| `F_TRIG` | `CLK` | `Q` | one-scan pulse on the falling edge |
| `SR` | `S1: BOOL, R: BOOL` | `Q1: BOOL` | set-dominant latch |
| `RS` | `S: BOOL, R1: BOOL` | `Q1: BOOL` | reset-dominant latch |
| `PID` | see [below](#pid-closed-loop-control) | see below | closed-loop control — proportional/integral/derivative with anti-windup and bumpless auto/manual |

### Power pins in ladder

When a block sits in a rung, rung power drives one input and continues
from one output; every other pin is passed (or read) by name in the
parentheses:

| Type | Power in | Power out |
| --- | --- | --- |
| `TON`, `TOF`, `TP` | `IN` | `Q` |
| `CTU` | `CU` | `Q` |
| `CTD` | `CD` | `Q` |
| `R_TRIG`, `F_TRIG` | `CLK` | `Q` |
| `SR` | `S1` | `Q1` |
| `RS` | `S` | `Q1` |
| user FUNCTION_BLOCK | `IN` | `Q` |

Passing the power pin explicitly in the argument list (`t1:TON(IN := x)`)
is an error — power owns it.

### PID: closed-loop control

`PID` is a positional (non-velocity) three-term controller in the IEC/OSCAT
spirit — no ladder power pin (like `CTUD`, it has no single input that
means "run"), so instantiate it from ST or an FBD diagram, the way the
[heated-tank-nogo](../examples/heated-tank-nogo) example wires its own
hand-rolled PI today.

| Pin | Kind | Type | Meaning |
| --- | --- | --- | --- |
| `AUTO` | input | `BOOL` | `TRUE` = closed loop; `FALSE` = manual — `CV` tracks `CV_MAN` and the integral free-wheels for a bumpless return to AUTO |
| `PV` | input | `REAL` | process value |
| `SP` | input | `REAL` | setpoint |
| `KP` | input | `REAL` | proportional gain. **`KP = 0` disables the whole controller**, not just the P term — see below |
| `KI` | input | `REAL` | integral gain, **repeats/second** (`1/TI`); `0` disables integral action |
| `KD` | input | `REAL` | derivative gain, **seconds** (`TD`); `0` disables derivative action |
| `CV_MAN` | input | `REAL` | manual output, used when `AUTO = FALSE` |
| `CV_MIN` | input | `REAL` | output clamp floor. Default `0` (see below) |
| `CV_MAX` | input | `REAL` | output clamp ceiling. Default `100` (see below) |
| `DIRECT` | input | `BOOL` | `FALSE` = reverse acting, `error = SP − PV` (e.g. a heater — raise `CV` when `PV` is low); `TRUE` = direct acting, `error = PV − SP` (e.g. a cooling valve) |
| `DT` | input | `REAL` | seconds since the last call. Left at `0` (unbound), the block measures elapsed time itself from the scan clock — bind a task's `dt-tag` when one is available, same as any hand-written loop |
| `DB` | input | `REAL` | deadband on `error` — within `±DB` the P and I terms see zero error (chatter suppression); the D term still sees every `PV` move |
| `RESET` | input | `BOOL` | `TRUE` zeroes the integral this scan |
| `CV` | output | `REAL` | controller output, clamped to `[CV_MIN, CV_MAX]` |
| `ERR` | output | `REAL` | `error`, before the deadband |
| `SAT_HI`, `SAT_LO` | output | `BOOL` | `TRUE` when `CV` is clamped at its ceiling/floor |
| `P_TERM`, `I_TERM`, `D_TERM` | output | `REAL` | the three contributions to `CV`, for trending/diagnostics |

**Algorithm.** ISA standard form: `CV = KP·(error + KI·∫error·dt + KD·d(PV)/dt)`.
The derivative acts on `PV`, not on `error` (so a setpoint step never
"kicks" `D_TERM` — only a `PV` change does), through a fixed first-order
filter (time constant `KD/10`) that keeps sensor noise from being
amplified into a noisy `CV`. Anti-windup is **conditional integration**:
the integral only accumulates when doing so wouldn't push the unclamped
output further past a rail it has already reached — cheap, and unlike
back-calculation it needs no extra tracking-gain to tune. `AUTO`/`MANUAL`
is bumpless both ways: in `MANUAL`, the integral is continuously
back-solved every scan so `P_TERM + I_TERM + D_TERM` already equals
`CV_MAN`, so the instant `AUTO` goes `TRUE`, `CV` continues from exactly
where `CV_MAN` left off instead of jumping. Leaving `CV_MIN`/`CV_MAX` both
unbound (they default to `0`) falls back to the IEC `0..100` range, since
an explicit `0..0` clamp would otherwise pin `CV` at zero.

```iecst
(* LIC-101: tank level, reverse acting — open the inlet valve more as
   level falls below setpoint, close it as level rises to setpoint. *)
VAR lic : PID; END_VAR
lic(AUTO   := TRUE,     PV     := LevelPct, SP    := LevelSP,
    KP     := 1.5,      KI     := 0.05,     KD    := 0.0,
    CV_MAN := 0.0,      CV_MIN := 0.0,      CV_MAX := 100.0,
    DIRECT := FALSE,    DB     := 0.5,      DT    := ScanDtS);
InletValve := lic.CV;
IF lic.SAT_HI THEN InletMaxedAlm := TRUE; END_IF;
```

## User function blocks

User `FUNCTION`s and `FUNCTION_BLOCK`s written in library files
participate everywhere the built-ins do; see "Structuring logic" in the
main README. Two things about their pins are worth stating outright,
because they decide how a block's signature is shaped.

### A pin may be any type, including a user TYPE

`VAR_INPUT`, `VAR_OUTPUT`, `VAR_IN_OUT` and `VAR`
declarations inside a `FUNCTION_BLOCK` (and a `FUNCTION`'s inputs, locals,
and return type) resolve against the **whole compile**: this file's `TYPE`
block plus every project library joined ahead of it. So a block can take
the UDT its site model already defines, nested structs and all:

```iecst
FUNCTION_BLOCK FB_Scale
VAR_INPUT  IN  : AnalogInput; END_VAR   (* a user TYPE, nested structs fine *)
VAR_OUTPUT OUT : AnalogInput; END_VAR
OUT       := IN;
OUT.VALUE := IN.SCALE.LO + (IN.SCALE.HI - IN.SCALE.LO) * INT_TO_REAL(IN.RAW) / 32767.0;
END_FUNCTION_BLOCK
```

At the call site the struct output reads like any other pin, one field at a
time (`s.OUT.VALUE`) or whole (`Scaled := s.OUT`). A pin naming a type
nothing declares is still a compile error that names the type.

### VAR_IN_OUT is a reference pin

A `VAR_IN_OUT` pin is bound at the call site to a **variable**, and what
the block writes into it is visible to the caller when the call returns.
That is what collapses a block whose UDT already names its own inputs and
outputs from thirty scalar pins to one:

```iecst
FUNCTION_BLOCK FB_Starter
VAR_IN_OUT M : Motor; END_VAR
VAR edge : R_TRIG; END_VAR
edge(CLK := M.Cmd);
IF edge.Q THEN M.Starts := M.Starts + 1; END_IF;
M.Running := M.Cmd;
END_FUNCTION_BLOCK
```

```iecst
VAR_EXTERNAL P101 : Motor; END_VAR
VAR s : FB_Starter; END_VAR
s(M := P101);          (* P101 carries the block's writes afterwards *)
```

The rules, all of them enforced at compile time:

| Rule | Why |
| --- | --- |
| Bound with `:=`, like an input — `s(M := P101)` | it *is* an input; the `=>` form binds outputs, and an IN_OUT already writes back |
| The argument must be **assignable**: a variable, a struct field, an array element, or a `VAR_EXTERNAL` tag | the block writes back to it; an expression has nowhere to write |
| The argument's type must match the pin **exactly** | a reference cannot convert, so an `INT` variable does not stand in for a `REAL` pin |
| Every `VAR_IN_OUT` must be bound at **every** call site | there is no default for a reference |
| The pin's own type may not be a function block | an instance is retained state, not a value; nothing in the language copies one |

Mechanically the pin is copied in before the block's body runs and copied
back to the same variable after it — the observable behaviour of "by
reference" for scan code, and what makes a `VAR_IN_OUT` bound to a
`VAR_EXTERNAL` UDT round-trip through the tag store as one whole-struct
write. Two calls in one scan bound to different variables each see and
update their own.

`FUNCTION`s have no `VAR_IN_OUT` (or `VAR_OUTPUT`): an IEC function is a
single return value, and the compiler says so.

`VAR_IN_OUT` pins are ST/FBD-callable; the FBD and ladder editors expose a
block's `VAR_INPUT`/`VAR_OUTPUT` pins only, so a block meant to be wired
graphically should keep its interface on those.
