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
- [EN/ENO: execution control](#eneno-execution-control)
- [User function blocks](#user-function-blocks)
- [Function blocks in ladder](#function-blocks-in-ladder)

## How a scan evaluates

Every task runs the classic PLC cycle on its interval:

```
read inputs → evaluate the program top to bottom → write outputs
```

All four languages end up as the same intermediate form, one hop at a
time:

```
 .st  ─────────────────────────►  IR (compiled, type-checked)
 .fbd ── transpile ──► ST ──────►  IR
 .ld  ── transpile ──► FBD ──► ST ─►  IR
 .sfc ── transpile ──► ST ──────►  IR
```

Because each hop preserves line maps, a diagnostic anywhere in that chain
lands back on **your** source — an `.ld` error points at the rung, an
`.fbd` error at the statement. The text file is always the source of
truth; the graphical editors are projections that edit it structurally.

Statements evaluate **top to bottom within a scan**, so a value written by
one rung/statement is visible to the ones below it in the same scan, and
to everything on the next scan (that's what makes the seal-in idiom work).

### FBD networks

An `.fbd` body can be divided into numbered **networks**, the way TIA
Portal and CODESYS draw an FBD block: a `NETWORK` line starts one, with an
optional title, and it runs to the next `NETWORK` line.

```
FBD
  NETWORK 'FT-101 raw to L/min'
  ft = ScaleAnalog(FT101_Raw, 0.0, 120.0)
  FT101_Flow := ft
  NETWORK 'high-flow alarm'
  // a comment after the NETWORK line is the network's note
  hiFlow = GT(FT101_Flow, MaxFlowLpm)
  tHi : TON(IN := hiFlow, PT := T#2S)
  HighFlowAlm := tHi.Q
END_FBD
```

- Networks are numbered by position (1, 2, …): the number is what the
  diagram draws and what you call it ("network 2"); the text carries only
  the order, so inserting or moving a network renumbers nothing in the file.
- **Execution order is network order.** Network 1 runs completely, then
  network 2, and so on. Within a network the FBD rule holds: an FB call runs
  before the statements that read its outputs, otherwise source order. A
  read of a *later* network's FB output sees the value from the previous
  scan, as on any PLC.
- Statements before the first `NETWORK` line form an untitled first
  network. A body with no `NETWORK` line is one network — exactly the
  program it always was.
- `NETWORK` stands alone on its line; the title is a `'…'` string (no
  quotes inside it). A tag that happens to be called `Network` still works
  as one (`Network := x` is a coil).

The diagram draws each network as a numbered band with its title, its
notes and its logic, and a small badge on every statement (FB calls and
coils) with its execution order within the network. A read that crosses a
network boundary (an FB output or a wire from another network) draws as a
variable box in the reading network, never as a line across bands. The
band header is the network's handle: click it and the palette's *+ add*
inserts into that network; ▲ / ▼ move it, + adds a network after it, ✕
removes its `NETWORK` line (its statements join the network above;
nothing else is deleted), and a double-click on the title renames it. The
palette's *network* adds one at the end (or after the picked network).

## Ladder power flow

A rung is a boolean expression that reads left to right:

| Element | Text | Meaning |
| --- | --- | --- |
| NO contact | `Tag` | passes power when `Tag` is TRUE |
| NC contact | `/Tag` | passes power when `Tag` is FALSE |
| Rising-edge contact | `+Tag` | one scan TRUE on `Tag`'s 0→1 transition (an implicit `R_TRIG`) |
| Falling-edge contact | `-Tag` | one scan TRUE on `Tag`'s 1→0 transition (an implicit `F_TRIG`) |
| Parallel branch | `[ a \| b ]` | OR of its legs; legs are series (AND) and nest freely |
| Function contact | `FN(args)` | passes power when the call yields TRUE — **the function must return BOOL** |
| Negated function contact | `/FN(args)` | passes power when the call yields FALSE — `/` negates any BOOL-yielding contact term: a plain ref, an accessor chain (`/t1.Q`), or a call (`/GT(a, b)`) |
| Function block | `inst:TYPE(args)` | power drives the block's power-in pin; power continues from its power-out pin (table [below](#standard-function-blocks)) |
| Coil | `( Tag )` | `Tag :=` the rung condition, every scan |
| Set coil | `( S Tag )` | latch: `Tag := Tag OR condition` |
| Reset coil | `( R Tag )` | unlatch: `Tag := Tag AND NOT condition` |
| Rising-edge coil | `( P Tag )` | `Tag :=` TRUE for one scan when the rung condition rises (an implicit `R_TRIG`) |
| Falling-edge coil | `( N Tag )` | `Tag :=` TRUE for one scan when the rung condition falls (an implicit `F_TRIG`) |
| Assignment | `{ Tag := expr }` | made when the rung has power at that point; power passes through unchanged. Several with `;`: `{ Count := Count + 1; Last := Now }` |

Series elements AND together; a rung with only a coil is driven by the
rail (`TRUE`). Multiple coils on one rung share the same condition. A
rung's only output may be a function block instance — no trailing coil is
required — its own output (`inst.Q`, `inst.DN`, …) is read elsewhere, the
same way an `inst.Q` fed to a coil would be: `A B t1:TON(PT := T#5S)` is a
complete, legal rung.

Edge instances (`+Tag`, `-Tag`, and the `P`/`N` coils) are unnamed in the
text — the compiler derives a stable instance name from the rung name,
the reference, and the occurrence's position among repeats of the same
(rung, ref, edge kind) within that rung (contacts key on the watched tag,
`P`/`N` coils key on the coil's own tag), so the name — and the R_TRIG's
retained state — survives an unrelated edit elsewhere in the program.
Two edge contacts on the same tag in one rung are independent instances.

A rung's trailing output zone (coils, or a rung-final function block) must
be contiguous at the rung's right end — coils ahead of a later function
block in the same rung aren't supported (`coils must sit at the rung's
right end`). Split such a rung into one nautilus rung per output leg
instead.

**The BOOL rule.** Power is boolean, so anything that gates it must yield
BOOL. Comparisons do: `GT(TempC, 90.0)` is a fine contact. Numeric
functions don't: `ADD(TempC, 0.0)` as a contact is a **compile error**
(`operator AND on BOOL and REAL`, or `cannot assign REAL to BOOL` when
it's alone on the rung) — ADD does not "pass through" its input. Numeric
functions belong *inside* a comparison's arguments — `GE(ADD(Base, Bias),
Limit)` — or in an **assignment**: `Run { Hours := Hours + ScanH }` adds
while `Run` is true and leaves power as it found it, which is the IEC
function box with its `EN` wired to the rung. The value is any IEC
expression (operators, functions, members, indexes); it is compiled as a
`SEL` on the rung condition, so it is evaluated every scan like any block
— an array index in it must stay valid while the rung is false. On a
Logix target an assignment becomes the matching instruction (MOVE, ADD,
SUB, MUL, DIV, MOD, NEG, ABS, SQR, XPY) or a CPT; see
`docs/design/logix-authoring.md`.

Reading FB outputs: any instance output is addressable as `inst.Pin`
everywhere — `GE(t2.ET, T#2S)` as a contact, `t2.Q` as an operand, or
from another task's FBD/ST program. To capture an output **into a
variable** at the call site, use the standard's output binding:
`t2:TON(PT := T#5S, ET => Elapsed)` — the one way ladder stores a
non-BOOL (coils only assign BOOL). `=>` works in ST and FBD calls too.

## SFC action qualifiers and step supervision

Every IEC 61131-3 action qualifier compiles. An association is written
`Q Target;` / `Q Target(T#3S);`, or in the standard's own textual form
`Target(Q);` / `Target(Q, T#3S);`. The duration of a timed qualifier is a
`TIME` literal or a `TIME` variable.

| Qualifier | Active |
| --- | --- |
| `N` | while the step is active |
| `S` / `R` | set / reset once, on the step's activation scan |
| `P`, `P1` / `P0` | one scan, on the step's activation / deactivation |
| `L` | from activation, for the duration (or until the step is left) |
| `D` | once the step has been active the duration, until it is left |
| `SD` | the duration after activation, even if the step was left; until an `R` |
| `DS` | once the step has been active the duration; then until an `R` |
| `SL` | from activation for the duration, even after the step is left; an `R` cuts it short |

An `R` acts on its step's activation scan and clears the `S`, `SD`, `DS` and
`SL` latches on its target. A timed `SL` that has run out stays latched until
an `R`, as the standard specifies. Every timed association has its own timer,
retained across an online edit.

A step can carry a maximum time:
`STEP Fill (MAXTIME := T#60S, ERROR := FillOverrun):`. When the step has
been active longer than that, `Fill.ERR` goes TRUE (legal in conditions and
action bodies), stays TRUE after the step is left, and clears on its next
activation. `ERROR :=` mirrors it into a BOOL variable, a manifest tag an
`alarms:` definition can watch. Pinned in `lang/conformance/sfc-timed-qualifiers`
and `lang/conformance/sfc-step-maxtime`; the full description is on the
[SFC page](/languages/sfc/).

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
| `AND`, `OR`, `XOR` | n-ary BOOL (or INT for bitwise) | same | logical on BOOL; bitwise on INT, over all 64 bits (`-8 AND 6` = 0) |
| `NOT` | 1 BOOL/INT | same | negation on BOOL; bitwise complement on INT (`NOT 0` = -1, `NOT 12` = -13) |
| `ADD` | n-ary numeric | common type | sum |
| `SUB` | 2 numeric | common type | difference |
| `MUL` | n-ary numeric | common type | product |
| `DIV` | 2 numeric | common type | quotient; integer and REAL ÷0 yield 0 and the scan keeps running. This is a deliberate deviation (the standard calls it an error; Codesys and TIA yield IEEE ±Inf) so a bad divisor cannot propagate Inf/NaN through a control loop — and because every seam on the tag bus (`/api/state`, the SSE stream, retained state, `*_test.yaml`) is JSON, which cannot carry Inf or NaN. The fault is not silent: every ÷0 bumps the program's **`divZero`** counter in the scan stats (`/api/state` → `stats.divZero`, controller-wide, counted since start), the way Logix raises `S:V` and keeps running |
| `MOD` | 2 INT | INT | remainder; ÷0 yields 0 and counts in `divZero` |
| `MOVE` | 1 any | same | pass-through assignment (FBD wiring aid) |
| `GT`, `GE`, `LT`, `LE` | 2 comparable | **BOOL** | ordering (numeric or TIME) |
| `EQ`, `NE` | 2 comparable | **BOOL** | equality |

The comparison row is the ladder-relevant one: those six are the
functions that can gate power directly.

## Standard functions

Stateless, callable from ST, FBD blocks, and (where they return BOOL —
or inside arguments) ladder function contacts.

A call is positional — `LIMIT(0.0, x, 100.0)` — or **formal**, every input
by its standard name: `LIMIT(MN := 0.0, IN := x, MX := 100.0)`. The names
are the ones the signatures below use (`IN` for one input, `IN1`…`INn` for
the extensible ones, `G`/`IN0`/`IN1` for `SEL`, `K`/`IN0`… for `MUX`); one
call is one or the other, never a mix. A formal call is also how a function
takes [EN/ENO](#eneno-execution-control).

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
| `AND`, `OR`, `XOR` | two integers | integer | bitwise, when both operands are integers (on BOOLs they are the logical operators) |

**Bit access.** `Word.3` is bit 3 of an integer, as a BOOL — readable
anywhere a BOOL is (`IF Status.0 THEN`, a ladder contact `Status.0`, a
compare argument) and assignable (`Cmd.4 := TRUE`, a ladder coil
`( Cmd.4 )`), which reads the word, sets or clears the bit, and writes
the word back. Bits are numbered from 0 at the least significant end, up
to 63. This is the spelling Logix uses and the one IEC 61131-3 ed. 3
writes `Word.%X3`; nautilus takes the shorter one. A bit of an array
element or a structure member works the same way: `Words[2].15`,
`P101.Status.12`.

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
| `INT_TO_REAL`, `REAL_TO_INT` | REAL→INT rounds to nearest, ties to even (IEC 60559: 2.5 → 2, 3.5 → 4) |
| `BOOL_TO_INT`, `INT_TO_BOOL` | 0 ↔ FALSE, nonzero → TRUE |
| `BOOL_TO_REAL`, `REAL_TO_BOOL` | 0.0 ↔ FALSE, nonzero → TRUE |
| `INT_TO_TIME`, `TIME_TO_INT` | the INT is **milliseconds** |
| `REAL_TO_TIME`, `TIME_TO_REAL` | milliseconds, rounded to nearest, ties to even |
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
| `TP` | `IN, PT` | `Q, ET` | pulse: rising IN produces a PT-wide TRUE pulse; a rising edge during the pulse is ignored; afterwards ET holds at PT while IN stays TRUE and returns to 0 the scan IN is FALSE |
| `CTU` | `CU: BOOL, R: BOOL, PV: INT` | `Q: BOOL, CV: INT` | count rising CU edges; Q when CV ≥ PV; R resets |
| `CTD` | `CD: BOOL, LD: BOOL, PV: INT` | `Q, CV` | count down from PV (LD loads); Q when CV ≤ 0 |
| `CTUD` | `CU, CD, R, LD, PV` | `QU, QD, CV` | up/down counter |
| `R_TRIG` | `CLK: BOOL` | `Q: BOOL` | Q for exactly one scan on CLK's rising edge |
| `F_TRIG` | `CLK` | `Q` | one-scan pulse on the falling edge |
| `SR` | `S1: BOOL, R: BOOL` | `Q1: BOOL` | set-dominant latch |
| `RS` | `S: BOOL, R1: BOOL` | `Q1: BOOL` | reset-dominant latch |
| `PID` | see [below](#pid-closed-loop-control) | see below | closed-loop control — proportional/integral/derivative with anti-windup and bumpless auto/manual |

## EN/ENO: execution control

Every function and function-block call may bind **EN** (a BOOL input,
TRUE when unbound) and read **ENO** (a BOOL output), as IEC 61131-3
§6.6.1.2.4 defines them and every FBD editor draws them:

- **EN FALSE: the call does not execute.** A function's result is **not
  assigned**: the variable it drives keeps its value. A function block's
  body does not run: its outputs, every `=>` binding and its internal state
  (a counter's edge memory, a timer's start) keep their values until it runs
  again.
- **ENO = EN AND no error.** A call that errors faults the scan here (a `MUX`
  index out of range, an unparseable `STRING_TO_*`), so a call that returns
  has ENO = EN — except FBD's `DIV` and `MOD`, whose ENO is FALSE on a zero
  divisor (the result itself follows the ÷0 rule: 0, and the `divZero`
  counter).

In ST, EN and ENO are formal arguments:

```iecst
t1(EN := Enable, IN := Start, PT := T#5S, ENO => t1Ok);
Out := LIMIT(EN := Enable, MN := 0.0, IN := Raw, MX := 100.0, ENO => ok);
```

A function's EN/ENO needs its call to be a statement of its own — the whole
right-hand side of an assignment (or a user `FUNCTION` called as a
statement) — because "not assigned" has to name what is not assigned;
inside a larger expression it is a compile error that says so.

In FBD, any block takes them the same way — `sp = LIMIT(EN := Enable, MN :=
0.0, IN := Raw, MX := 100.0, ENO => ok)` — and a block with EN/ENO must drive
a variable (a coil, directly or through its named wire), never another
block's input. ENO also reads as a wire: `t1.ENO`, or `sp.ENO` for a named
block, feeds a coil or the next block's EN (TIA's EN/ENO chain). The diagram
draws EN first and ENO last, but only when bound; the **EN** toggle on a
block (or FB) shows both as open pins to drop a wire on or drag one from.

A user block that **declares** its own `EN` input or `ENO` output (the
ladder convention, [below](#power-pins-in-ladder)) keeps them as ordinary
pins: its body decides what they mean. `lang/conformance/fbd-en-eno` runs
the same EN/ENO logic in ST and FBD and asserts them together.

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
| user FUNCTION_BLOCK | `EN`, else the first **BOOL** `VAR_INPUT` the call doesn't bind by name | `ENO`, else the first **BOOL** `VAR_OUTPUT` |

Passing the power pin explicitly in the argument list (`t1:TON(IN := x)`)
is an error — power owns it.

A user block's pins are resolved from the **whole compile** — this file's
own `FUNCTION_BLOCK`s plus every project library — so the rung lands power
where the block actually declares it. Two shapes have no pin to use, and
both are meaningful:

- **No power-in** — every BOOL input is bound by name, or the block has
  none. The block is still called, unconditionally, so it may only sit on
  a rung whose condition is the rail itself. A rung with contacts ahead of
  it is a compile error (`no free BOOL input for the rung's power`): say
  which pin the gate lands on, or give the block an `EN`.
- **No power-out** — the block has no BOOL output. Power **passes
  through** unchanged, so whatever conditioned the block still conditions
  the coils to its right.

A block whose type nothing in the compile declares falls back to `IN`/`Q`.

### PID: closed-loop control

`PID` is a positional (non-velocity) three-term controller in the IEC/OSCAT
spirit — no ladder power pin (like `CTUD`, it has no single input that
means "run"), so instantiate it from ST or an FBD diagram, the way
[`examples/lift-station`](../examples/lift-station)'s `level.fbd` wires a
direct-acting level loop (`lic : PID(AUTO := LeadReq, …, DIRECT := TRUE, …)`)
and [`examples/batch-skid`](../examples/batch-skid)'s `dosing.fbd` wires a
reverse-acting one (`tic : PID(AUTO := HeatingActive, …, DIRECT := FALSE,
…)`, the "heater" case below). In the FBD editor, *+ add → function block →
PID* places `pid1 : PID(AUTO := _, PV := _, …)` with every input an open pin
to drag a tag onto; unwire (select the wire, Del) the ones you leave at
their defaults.

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

### Arrays of instances

A function-block instance can be declared in an array — `Timers : ARRAY
[0..3] OF TON;` — and each element is its own instance with its own
retained state. Call an element by index and read its outputs by index:
`Timers[2](IN := Run, PT := T#5S);` and `Timers[2].Q` in ST, or
`Run Timers[2]:TON(PT := T#5S)` in a rung. The index may be a variable.
On a Logix target the array is one `TIMER[4]` tag; an element called
with a literal index carries its preset in the tag's data, one called
with a computed index takes a `MOVE` to its `.PRE` ahead of the rung.

## Dialects

A project runs on the nautilus runtime by default, and that is the only
semantics the standard library has. A project that targets a vendor's
controller can opt into that vendor's idioms with `dialect:` in
`nautilus.yaml`:

```yaml
dialect: logix
```

A dialect is a library of blocks with the vendor's semantics, written in
nautilus and compiled into every program of the project like a `lib/`
file — so the nautilus runtime runs them, the vendor writer emits the
native instruction for them, and the vendor import folds the native idiom
back into them. One definition, three uses. The names are `nautilus` (the
default, adds nothing), `logix`, and the reserved `siemens` and `codesys`.

The `logix` dialect today:

| Block | Pins | Semantics | On a Logix controller |
| --- | --- | --- | --- |
| `TONR` | `IN`, `PT`, `Reset` → `Q`, `ET` | a TON whose `Reset` clears the accumulated time and `Q` while TRUE; the free-running pulse is `t:TONR(PT := T#1S, Reset := t.Q)` | a `TON` with a `RES(t)` rung ahead of the timer's rung |

**Tag aliases.** A manifest tag may carry `alias:`, the vendor-side
binding of the tag — on Logix the alias tag's target, which is how a Logix
program names a rack point or another tag:

```yaml
tags:
  - { name: StartPB, role: input, alias: "Local:1:I.Data.3" }
```

The program names `StartPB`; the Logix writer emits it as an alias tag;
the L5X importer turns an export's alias tags, and rack points the logic
named directly, into tags with `alias:`. The nautilus runtime ignores the
binding — the tag is a tag — so a simulation runs the same source. It is
the one place a hardware address lives in a project.

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

### An input left unbound keeps its value

A call may leave any `VAR_INPUT` out: it keeps its value — the declared
initial value (`NoFlowTime : TIME := T#5S`) until something writes it,
exactly an unconnected FB input in TIA or CODESYS. A `VAR_IN_OUT` must be
bound at every call. The FBD palette's *function block* picker follows
this: an input **with a declared initial value** arrives unbound (it
already has its value), an input without one arrives as an open `_` pin
(the compiler flags it until something is wired, since nobody decided
what it reads), and so does every `VAR_IN_OUT`. EN is never written by the
picker: unbound, it is TRUE.

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

## Function blocks in ladder

A `.ld` file may **define** `FUNCTION_BLOCK`s as well as (or instead of) a
`PROGRAM`. Each one is an ordinary IEC POU whose body happens to be rungs:

```
FUNCTION_BLOCK PumpSeq
VAR_INPUT  Start : BOOL; Stop : BOOL; Level : REAL; StopLevel : REAL; END_VAR
VAR_OUTPUT Run : BOOL; Warm : BOOL; END_VAR
VAR        t1 : TON; END_VAR
LD
  RUNG seal  [ Start | Run ] /Stop /GE(Level, StopLevel) ( Run )
  RUNG warm  Run t1:TON(PT := T#5S) ( Warm )
END_LD
END_FUNCTION_BLOCK
```

This is what ladder has instead of a JSR: a subroutine with a real
interface — pins, not shared tags — and its own retained state **per
instance**. Two pumps are two instances of one block, each with its own
seal-in and its own `t1`.

The `VAR_*` sections are ordinary POU declarations, `VAR_IN_OUT` included,
so a ladder block can take a UDT by reference the same way an ST one does
(see [VAR_IN_OUT is a reference pin](#var_in_out-is-a-reference-pin)). The
LD body lowers through the usual single hop — LD → FBD netlist → ST — so
by the time the compiler sees it, it is an ordinary
`FUNCTION_BLOCK … END_FUNCTION_BLOCK`. There is no special case anywhere
downstream.

### Calling one

From ladder, with the rung's power on the block's power-in pin and `=>`
capturing outputs (the same `inst:TYPE(args)` syntax the standard blocks
use — see [Power pins in ladder](#power-pins-in-ladder) for where power
lands on a user block):

```
RUNG lead
  P101Start p101:PumpSeq(Stop := P101Stop, Level := LevelPct,
                         StopLevel := StopLevel,
                         Run => P101Run, Warm => P101Warm)
```

From ST or FBD, like any other block:

```iecst
VAR p : PumpSeq; END_VAR
p(Start := Cmd, Stop := Halt, Level := LevelPct, StopLevel := 80.0);
PumpRun := p.Run;
```

An FB instance a rung declares inline (`t1:TON(...)`) needs no separate
`VAR` entry — but writing one, as the block above does, is fine and often
clearer: a declaration in the POU's own header wins, and the rung is then
a call on it rather than a second declaration.

### A `.ld` library

A `.ld` file with **no PROGRAM** is a project library, exactly like a
PROGRAM-less `.st` file. Its blocks join the prelude ahead of every task,
so any program in the project — in any language — can instantiate them.
`.fbd` libraries work the same way. A library may sit in the project root or
anywhere under `lib/` (e.g. `lib/motor.ld`); `lib/` holds libraries only, so
a `PROGRAM` there is an error, and no other subdirectory composes.

**Composition order.** Every `.st` library first, then every transpiled
`.ld` / `.fbd` library, each group sorted by project-relative path (root
files and `lib/` files interleaved: `lib/motor.ld` sorts before `pump.ld`). ST leads
because that is where a project's `TYPE` declarations live and a graphical
block's pin may name a UDT. Order never decides whether a call *resolves*:
the ST front-end registers every `FUNCTION_BLOCK` signature in the composed
source before it lowers any body, so blocks may reference each other in
either direction, across files. What order decides is only which
declaration a duplicate-name collision reports.

Two names, one error: declaring the same `FUNCTION_BLOCK` twice in one
`.ld` file is refused with the second declaration's line, before anything
is transpiled.

`examples/lift-station`'s `lib/motor.ld` (a `MotorStarter` block,
instantiated twice from `permissives.ld`) is the whole feature.

### What the editors do

The ladder view renders a file's blocks as rung groups, each under its own
`FUNCTION_BLOCK` heading with its pins, and every rung in them edits like
any other. The palette's *FB…* picker lists every block a rung can call —
the standard ones, then the file's and the project libraries' — and inserts
one with the rung's power already on its power pin, `_` placeholders on its
non-BOOL inputs and in-outs, and an instance name you can edit; double-click
a block's header to rename the instance (its declaration and every
reference in the POU follow). The FBD palette's *function block* picker
lists the same catalog (`naut fbd graph` sends it too) and inserts
`inst : TYPE(pin := _, …)`, every required input an open pin (an input with
a declared initial value stays unbound — [above](#an-input-left-unbound-keeps-its-value));
its *block → wire* function field offers the project's own `FUNCTION`s, by
their declared names, beside the standard functions; its *output reference*
reads an instance output (`Speed := m1.Run`). Two limits worth knowing: an edit op addresses a rung by
**name**, so two rungs with the same name in different POUs of one file
resolve to the first — name them distinctly; and `addRung` with no `after`
appends before the file's **first** `END_LD`. The language server analyses
a multi-POU `.ld` fully, diagnostics landing on the offending rung.
