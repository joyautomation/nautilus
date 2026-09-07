---
title: Structured Text (ST)
description: The IEC 61131-3 text language nautilus compiles directly, and the one the other three languages lower to.
sidebar:
  order: 1
---

Structured Text is IEC 61131-3's text language, and in nautilus it sits under
the other three. A `.st` file holds a program, a library of types and blocks,
or both. Ladder lowers to FBD, FBD lowers to ST, and an SFC file is an ST POU
with a chart body, so all four languages reach the same compiler and the
same typed IR. What ST accepts is what a controller runs.

## When to use it

ST fits arithmetic, loops, state machines, and string work: a PI calculation,
a totalizer, a mode word driving a `CASE`. Ladder reads better for interlocks
and permissives, FBD for signal flow, SFC for step sequences. ST is also the
only language that declares reusable units today, so a project's `FUNCTION`
and `FUNCTION_BLOCK` definitions are ST even when the callers are diagrams.

## A program, top to bottom

```iecst
PROGRAM Main
(* Heated surge tank: pump holds level by hysteresis, a PI loop holds
   temperature. VAR_EXTERNAL tags are bound by the runtime each scan —
   field inputs written before, outputs collected after. VAR is retained. *)
VAR_EXTERNAL
    LevelPct       : REAL;   (* field inputs *)
    TempC          : REAL;
    ScanDtS        : REAL;
    TempSP         : REAL;   (* operator setpoints *)
    Kp             : REAL;
    Ki             : REAL;
    PumpStartLevel : REAL;
    PumpStopLevel  : REAL;
    PumpRun        : BOOL;   (* field outputs *)
    Heater         : REAL;
END_VAR
VAR
    integral : REAL;
    err      : REAL;
END_VAR

(* P-101: level hysteresis latch *)
IF LevelPct <= PumpStartLevel THEN
    PumpRun := TRUE;
ELSIF LevelPct >= PumpStopLevel THEN
    PumpRun := FALSE;
END_IF;

(* TIC-101: temperature PI with anti-windup clamp *)
err := TempSP - TempC;
integral := LIMIT(0.0, integral + Ki * err * ScanDtS, 100.0);
Heater := LIMIT(0.0, Kp * err + integral, 100.0);
END_PROGRAM
```

That is `examples/heated-tank/program.st`, abridged. `PROGRAM Main …
END_PROGRAM` names the POU, and that name is the program's identity for
download, diff, and pull. `VAR` holds program-local state that persists between
scans. Statements evaluate top to bottom, so the `integral` written on one line
is what the next line reads in the same scan.

A `VAR_EXTERNAL` declaration binds a name in the tag store rather than creating
it, and reading a tag nothing has ever written faults the scan with `undefined
tag`. See [the tag model](/guides/tag-model/) for which role fits which name.

## Declarations

The sections are `VAR`, `VAR_INPUT`, `VAR_OUTPUT`, `VAR_IN_OUT`, `VAR_TEMP`,
`VAR_GLOBAL`, and `VAR_EXTERNAL`, each closed by `END_VAR`, with `RETAIN` or
`CONSTANT` allowed after the section keyword. `VAR_GLOBAL` and `VAR_EXTERNAL`
resolve identically: both name a tag in the store.

```iecst
TYPE
  Motor : STRUCT
    Running : BOOL;
    Fault   : BOOL;
    Speed   : REAL;
  END_STRUCT;
END_TYPE

VAR CONSTANT
    CapacityL : REAL := 1000.0;
END_VAR
VAR
    history : ARRAY[1..10] OF REAL;
    grid    : ARRAY[0..4, 0..4] OF REAL;
    i, j    : INT;
    dwell   : TIME := T#2m30s;
    tag     : STRING := 'TIC-101';
    m       : Motor;
END_VAR
```

Elementary types are `BOOL`; the integer kinds `SINT`, `USINT`, `INT`, `UINT`,
`DINT`, `UDINT`, `LINT`, `ULINT`, `BYTE`, `WORD`, `DWORD`, `LWORD`; `REAL` and
`LREAL`; `TIME` and `LTIME`; `STRING`, `WSTRING`, `CHAR`, and `WCHAR`. Integer
kinds share one 64-bit representation, so a declared width documents intent
without narrowing. `TIME` counts milliseconds.

`ARRAY[lo..hi] OF T` takes several comma-separated dimensions and any element
type, including a UDT or a function block. Bounds are integer literals.
`TYPE … END_TYPE` declares a `STRUCT` or an alias for another type. An initial
value after `:=` must be a single literal constant.

Literals cover decimals and exponents (`1.5e3`), based integers (`16#FF`,
`2#1010`, `8#777`), typed forms (`INT#42`, `BOOL#TRUE`, `STRING#'hi'`), and
durations (`T#5s`, `T#500ms`, `T#1h30m`). Comments are `(* … *)` or `//` to end
of line.

`RETAIN` parses and changes nothing at runtime today. Every program `VAR` and
function block instance already keeps its value from scan to scan, and a warm
swap carries that state across an online edit by name and type. Persistence
across a restart is configured per tag on the retain store.

## Statements and expressions

`IF … ELSIF … ELSE … END_IF`, `CASE … OF … ELSE … END_CASE`,
`FOR … TO … BY … DO … END_FOR`, `WHILE … DO … END_WHILE`,
`REPEAT … UNTIL … END_REPEAT`, plus `EXIT`, `CONTINUE`, and `RETURN`.
Assignment is `:=`, equality is `=`, and inequality is `<>`.

```iecst
CASE Mode OF
    0:    Running := FALSE;
    1, 2: Running := TRUE;
ELSE
    Running := FALSE;
END_CASE;

dwell(IN := Running, PT := T#5s, ET => Elapsed);

Total := 0.0;
FOR i := 1 TO 10 BY 1 DO
    IF history[i] < 0.0 THEN CONTINUE; END_IF;
    Total := Total + history[i];
END_FOR;
```

Operator precedence runs, tightest first: unary `NOT` and `-`; then `*`, `/`,
`MOD`; then `+` and `-`; then the comparisons `<`, `<=`, `>`, `>=`, `=`, `<>`;
then `AND`; then `XOR`; then `OR`. An `INT` widens to `REAL` where one is
expected; the other direction is a compile error, so narrowing goes through
`REAL_TO_INT`.

A function block instance is declared like a variable (`dwell : TON;`) and
invoked as its own statement with named arguments. `=>` copies an output into a
target at the call site, and `dwell.Q` reads one anywhere. Struct fields and
array elements use `.field` and `[i]` on either side of an assignment; an index
outside the declared bounds faults the scan.

Every built-in function and function block is in the
[language reference](/reference/functions/).

## Functions and function blocks

`FUNCTION` is stateless and returns one value, assigned to the function's own
name. `FUNCTION_BLOCK` is stateful, and each instance owns its slots.

```iecst
FUNCTION_BLOCK RateOfChange
(* Rate of change of IN in units/minute, from the previous scan's value.
   Each instance retains its own prev — across scans and online edits. *)
VAR_INPUT
    IN : REAL;
    DT : REAL; (* seconds since last scan *)
END_VAR
VAR_OUTPUT
    OUT : REAL; (* units per minute *)
END_VAR
VAR
    prev   : REAL;
    primed : BOOL;
END_VAR
IF NOT primed THEN
    prev := IN;
    primed := TRUE;
END_IF;
IF DT > 0.0 THEN
    OUT := (IN - prev) / DT * 60.0;
END_IF;
prev := IN;
END_FUNCTION_BLOCK
```

A `.st` file with no `PROGRAM` keyword is a library: `TYPE`, `FUNCTION`, and
`FUNCTION_BLOCK` declarations, in scope for every program in the same
directory. The runtime, the language server, `nautilus check`, download, and
pull all compose libraries the same way, so a program round-trips losslessly.
See [function blocks, libraries, and tasks](/guides/blocks-and-tasks/).

## In the editor

The VS Code extension highlights `.st` with no setup, from keyword lists
generated out of the compiler. With the CLI installed it also runs
`nautilus lsp`, the same compiler over stdio: diagnostics as you type for parse
and type errors, go-to-definition from an identifier to its declaration, hover
showing the declared type and var section, and completion over in-scope
variables, keywords, types, and the builtin registries.

Point `nautilus.runtimeUrl` at a running controller and the extension renders
live tag values inline beside every identifier, with a Live Values panel and a
**Set Live Value** command. The program commands work on `.st` directly:
**Download Program to Controller**, **Diff Program with Controller**, **Rollback
Controller Program**, and **Pull Program from Controller**.

## Tooling

```sh
nautilus check          # compile every .st, .fbd, .ld, .sfc under the path
nautilus test           # *_test.yaml acceptance tests, on a virtual clock
nautilus pull --host c1 # write a controller's running source back to the file
```

`nautilus check` prints gcc-style `file:line:col: message` diagnostics and exits
non-zero when it finds any, which is what CI gates on. Tests are YAML fixtures
asserting on tags over virtual time; see [Testing](/reference/testing/).
Downloading a program warm-swaps it into a running controller with state carried
across, a failed compile leaves the running program untouched, and `nautilus
pull --check` fails a build when a controller holds edits nobody pulled back.
[Online edits](/guides/online-edits/) has the whole loop.

## Not supported

- **Pointers and references.** No `REF_TO`, no dereference operator.
- **`VAR_ACCESS`, `CONFIGURATION`, `RESOURCE`, and `TASK` declarations.**
  Tasks come from `nautilus.yaml` or the Go composition.
- **Enumerated and subrange types.** `TYPE` declares a `STRUCT` or an alias.
- **Date and time-of-day.** `DATE`, `TOD`, `TIME_OF_DAY`, `DT`, and
  `DATE_AND_TIME` are rejected as unknown scalar types, and their literals do
  not evaluate. Durations are fine.
- **Exponentiation with `**`.** Use `EXPT(base, exp)` instead.
- **Length-qualified strings.** `STRING[80]` is a parse error; `STRING` holds
  any length.
- **Aggregate initializers.** `:= [1, 2, 3]` in a declaration is a parse error;
  assign the elements in the body.
- **`VAR_TEMP`.** It parses as an ordinary local, kept between scans rather than
  cleared per invocation.
- **`CONSTANT` enforcement.** `VAR CONSTANT` declares and initializes; assigning
  to one is not an error.

## See also

- [Language reference](/reference/functions/) — types, operators, and every
  built-in.
- [Function blocks, libraries, and tasks](/guides/blocks-and-tasks/)
- [The tag model](/guides/tag-model/)
- [Online edits](/guides/online-edits/)
- [Testing](/reference/testing/)
- [Ladder (LD)](/languages/ladder/), [Function Block Diagram
  (FBD)](/languages/function-block/), [Sequential Function Chart
  (SFC)](/languages/sfc/)
