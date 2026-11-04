# Structure warnings

A ladder, FBD or SFC program that compiles can still draw as a knot. The
editors lay every diagram out from the text, so the text's structure alone
decides whether the diagram reads: a rung with three levels of nested
branches, an FBD statement nested four calls deep, or a chart whose
transitions all say `State = 2` is a correct program and a bad drawing.

`naut check` reports these shapes as **structure warnings**. Each has a
stable rule ID, a message that says what to write instead, and a position:

```
interlocks.ld:9:8: warning: rung r3 has no comment: write why the rung exists after its name, RUNG r3 (* … *), or in a // note on the line above — the reason, not a list of the contacts [LD005]
control.fbd:3:1: warning: this statement nests blocks 4 deep (the limit is 2): give the inner results named wires, one block per line (hiLevel = GE(Level, HighLevelSP)), and pass the wire names [FBD001]
```

They are warnings: `naut check` still exits 0, and the program runs. The
VS Code extension shows the same warnings as you type, from the language
server, with the rule ID as the diagnostic's code. They appear in the
Problems panel and on the rung, network or step in the diagram editors.
Treat them as bugs. Every project under `examples/` and every `naut new`
scaffold passes with none, and a test keeps it that way.

The rules are deterministic and cheap: one parse of the file, no
compilation. Each rule was chosen to be precise, so it fires on a shape
that draws badly or hides the logic, not on a matter of taste. Rules that
could not be made precise are listed under
[considered and left out](#considered-and-left-out).

## Ladder

| ID | Fires when | Write instead | Threshold |
| --- | --- | --- | --- |
| **LD001** | One BOOL is driven by `( X )` (or `( P X )` / `( N X )`) coils on more than one rung, or by a plain coil and a `( S X )` / `( R X )` latch. Two plain coils: the later one overwrites the earlier every scan, so the earlier rung is dead logic. A plain coil after the latch overwrites it, so the latch never holds. A latch after the plain coil overrides the coil's result while its rung is true (`( R X )` forces X off, `( S X )` forces it on), and the coil writes X again next scan, so it still never latches. The message says which of these the rungs' order gives. | One rung per BOOL: OR the conditions there (for a later reset, put its condition into the coil's rung as a negated contact), or compute each into its own BOOL and OR those. A latch is a `( S X )` / `( R X )` pair and nothing else. | none |
| **LD002** | Branches nest deeper than the limit: `[ a [ b \| c ] \| d ]` is 2 deep. | Compute the inner branch into a named BOOL on its own rung (`PumpPermissive`) and use that BOOL as one contact. | `max-branch-depth`, default **1** |
| **LD003** | A rung has more condition elements in series than the limit. A branch counts as its longest leg, and coils do not count. | Compute part of the condition into a named BOOL on its own rung. | `max-series`, default **6** |
| **LD004** | The same contact (plain, negated or a comparison) is on every leg of a branch, and it can move out without changing the program: `[ a /Stop \| b /Stop ]`. | Put it once, in series, before the branch: `[ a \| b ] /Stop`. Or after it, when the legs start with a block: `[ t1:TON(PT := T#5S) a \| b a ]` is `[ t1:TON(PT := T#5S) \| b ] a`. | none |
| **LD005** | A rung has no comment: neither `RUNG name (* … *)` nor a `//` note on the line above its `RUNG` header (blank lines between are fine). A note further up, inside the previous rung's body, is that rung's note. An `@allow` pragma in the header is not a comment. | Say why the rung exists, not which contacts it has. | none |
| **LD006** | A comparison contact `GT`, `GE`, `LT` or `LE` has a numeric literal argument other than 0, 1 or -1: `GT(Level, 90.0)`. | A setpoint tag (`role: setpoint`, with `init:` and `desc:`) or a named `VAR CONSTANT`: `GT(Level, HighLevelSP)`. | none |

LD004 leaves out edge contacts (`+X`, `-X`) and blocks. Each occurrence of
one is its own instance with its own state, so factoring it out would
change the program. A common contact also moves only past plain and
comparison contacts: in `[ a t1:TON(PT := T#5S) | t2:TON(PT := T#1S) a ]`,
`a` comes before a timer on one leg and after one on the other, so no
single place keeps the program and the rung is not reported.

LD006 leaves out `EQ`/`NE` (a mode or state compare, `EQ(Mode, 2)`, is not
a threshold), zero in any spelling, the integer literals 1 and -1 (a count
compare: `GE(Count, 1)` is `GT(Count, 0)`, "at least one"), and TIME
literals (`PT := T#5S` in the block that times it reads fine). A real
`1.0` is reported: it is a threshold like any other.

LD005 is the most opinionated rule. A rung such as `RUNG reset` with
`ResetPB ( Fault_Reset )` explains itself to most readers, and a project
that would rather not comment rungs like that switches LD005 off for the
whole project:

```yaml
check:
  disable: [LD005]
```

The examples keep it on, so every rung there says why it exists.

Ladder findings point at the rung's name on its `RUNG` line.

## Function Block Diagram

| ID | Fires when | Write instead | Threshold |
| --- | --- | --- | --- |
| **FBD001** | One statement nests blocks deeper than the limit. `AND(OR(a, b), c)` is 2 deep. An FB call counts as a level, so `t1(IN := AND(OR(a, b), c))` is 3. A wire name counts as nothing, and `NOT` is pin negation, not a block. | Name the inner results, one block per line: `hiLevel = GE(Level, HighLevelSP)`, then pass `hiLevel`. | `max-nesting`, default **2** |
| **FBD002** | A network with no title holds four or more statements and more than one function. That is either the statements before the first `NETWORK` line (or a body with none) or an untitled `NETWORK`. Functions are the separate pieces of logic: statements joined by a wire, by an FB instance's pins, or by a variable one statement assigns and another reads (`Scaled := MUL(Level, 2.0)` then `Valve := GE(Scaled, SP)` is one function). Two statements that only read the same input are not joined. | Start each function with its own `NETWORK 'what it does'` line, so it draws as its own titled band. A body that is one function needs no title, because the POU's name is its title, and a network of three statements or fewer reads without one. | none |
| **FBD003** | A comparison block `GT`, `GE`, `LT` or `LE` has a numeric literal argument other than 0, 1 or -1. | A setpoint tag or a named `VAR CONSTANT`, as LD006. | none |

Only `.fbd` files get the FBD rules. A ladder file compiles through an FBD
netlist, but that netlist is the compiler's, and the ladder rules judge
the text you wrote.

## Sequential Function Chart

| ID | Fires when | Write instead | Threshold |
| --- | --- | --- | --- |
| **SFC001** | A transition's whole condition compares a variable with a constant (`State = 2`, `Step <> 3`, `Mode = Mode#Fill`, `SeqStep = 'Fill'`), **and** the `ACTION`s of the same chart assign that variable two or more different constants (`State := 1;` … `State := 2;`). That is a CASE state machine drawn as a chart: every transition shows `State = n`, and the real conditions hide in the actions' IFs. | The process condition in the transition (`LT101_Level >= FullSP`, `Fill.T >= T#30S`), outputs driven by step associations (`N FillReq;`), and no state variable. | none |
| **SFC002** | A step is named for its number: `S1`, `s_2`, `Step3`. | Name it for the process state (`Idle`, `Fill`, `Heat`), which is what the chart shows. | none |

SFC001 needs the chart to step the variable through constants itself. These
are data the sequence branches on, not a hidden state machine, and are not
reported:

- an operator's `Mode` setpoint, or another program's state, which the
  chart does not write;
- a snapshot latched once, such as the recipe a batch starts with
  (`P1 LatchRecipe;` doing `Recipe := RecipeReq;`, then
  `TRANSITION ToB FROM Idle TO ChargeB := Recipe = 2;`);
- a counter (`Pass := Pass + 1;`, then `Pass = 3`);
- a variable the actions set to one constant only, such as a reset to 0.

## Thresholds and switching rules off

A project changes the thresholds, or switches rules off, in `nautilus.yaml`:

```yaml
check:
  disable: [LD005]      # rules not reported in this project
  max-series: 6         # LD003
  max-branch-depth: 1   # LD002
  max-nesting: 2        # FBD001
```

A field left out keeps its default. An unknown rule ID or a negative
threshold is a manifest error. `naut check` and the editor both read this
section.

One POU opts out of a rule with an `@allow` pragma: a comment in its
header, next to its description, naming the rule IDs and, after them, why.

```
FUNCTION_BLOCK VendorSequence
(* @allow LD003 LD005: the vendor's rungs, kept as imported *)
VAR_INPUT …
```

The pragma covers the POU it is written in, from its `PROGRAM` or
`FUNCTION_BLOCK` line to its `END_` line, and no further. One written above
the first POU covers the whole file. One written in a rung's header covers
that rung only:

```
  RUNG trip (* @allow LD006: the OEM's trip point, fixed by the vendor *)
    GT(Level, 95.0) ( Trip )
```

The rule IDs come first, separated by spaces or commas. A colon, or the
first word that is not shaped like a rule ID, ends the list and starts the
reason. An ID that is not a rule, such as a typo (`LD0O5`), switches
nothing off, and `naut check` reports it as a warning that names it,
tagged `[@allow]`. That warning cannot be disabled: fix the typo.

Prefer fixing the structure. A pragma is for code you do not own, or for a
rule that truly does not fit, and the reason belongs in it.

## Imported ladder

`naut logix import` carries the vendor's rungs across as they were
written: two coils on one tag, literal trip points, rungs with no comment.
So that an imported project checks clean, the import writes one line above
the `PROGRAM` (or `FUNCTION_BLOCK`) of every ladder file it creates: a
file-wide pragma for every ladder rule, naming the export it came from.

```
(* @allow LD001 LD002 LD003 LD004 LD005 LD006: imported from Line.L5X by `naut logix import`; delete this line to lint it *)
PROGRAM MainProgram
```

Delete the line when you start to own the routine, and `naut check` lints
it like any other. The pragma is a comment, so `naut logix write` leaves it
out of the L5X. Imported ST routines get no pragma, because the structure
rules do not cover ST.

## Considered and left out

These were candidates and are not rules, because they would fire on
programs that are fine:

- **An SFC step with no actions.** Wait steps have none, and neither do
  the steps that wait at a simultaneous convergence (`ChargeADone` in
  `examples/batch-skid`) or for an operator's confirm. The chart shows what
  the process is waiting for, and that is the point of the step.
- **One variable feeding many FBD blocks.** Reading the same tag in many
  places is ordinary. The diagram gives each network its own copy of the
  variable box, so the crossing wires the AGENTS.md investigation saw came
  from nesting and missing networks, which FBD001 and FBD002 catch.
- **Magic numbers everywhere.** Scaling factors (`DIV(dt, 60.0)`), clamps
  (`LIMIT(0.0, x, 100.0)`), PID limits and mode codes are constants by
  nature. Only an ordering comparison against a literal (LD006, FBD003) is
  reliably a threshold someone will want to tune.
- **Rung and wire names such as `r1` and `w1`.** An unnamed rung already
  gets a generated name, and short wire names are often clear in context.
  Step names are different: they are the chart's content, so SFC002 checks
  them.
