# Structure warnings

A ladder, FBD or SFC program that compiles can still draw as a knot. The
editors lay every diagram out from the text, so the text's structure alone
decides whether the diagram reads: a rung with three levels of nested
branches, an FBD statement nested four calls deep, or a chart whose
transitions all say `State = 2` is a correct program and a bad drawing.

`naut check` reports these shapes as **structure warnings**. Each has a
stable rule ID, a message that says what to write instead, and a position:

```
interlocks.ld:9:1: warning: rung r3 has no comment: write why the rung exists after its name, RUNG r3 (* … *), or in a // note on the line above — the reason, not a list of the contacts [LD005]
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
| **LD001** | One BOOL is driven by `( X )` (or `( P X )` / `( N X )`) coils on more than one rung, or by a plain coil and a `( S X )` / `( R X )` latch. The later coil overwrites the earlier one every scan, so the earlier rung is dead logic. | One rung per BOOL: OR the conditions there, or compute each into its own BOOL and OR those. A latch is a `( S X )` / `( R X )` pair and nothing else. | none |
| **LD002** | Branches nest deeper than the limit: `[ a [ b \| c ] \| d ]` is 2 deep. | Compute the inner branch into a named BOOL on its own rung (`PumpPermissive`) and use that BOOL as one contact. | `max-branch-depth`, default **1** |
| **LD003** | A rung has more condition elements in series than the limit. A branch counts as its longest leg, and coils do not count. | Compute part of the condition into a named BOOL on its own rung. | `max-series`, default **6** |
| **LD004** | The same contact (plain, negated or a comparison) is on every leg of a branch: `[ a /Stop \| b /Stop ]`. | Put it once, in series, before the branch: `[ a \| b ] /Stop`. | none |
| **LD005** | A rung has no comment: neither `RUNG name (* … *)` nor a `//` note on the line above it. | Say why the rung exists, not which contacts it has. | none |
| **LD006** | A comparison contact `GT`, `GE`, `LT` or `LE` has a nonzero numeric literal argument: `GT(Level, 90.0)`. | A setpoint tag (`role: setpoint`, with `init:` and `desc:`) or a named `VAR CONSTANT`: `GT(Level, HighLevelSP)`. | none |

LD004 leaves out edge contacts (`+X`, `-X`) and blocks. Each occurrence of
one is its own instance with its own state, so factoring it out would
change the program. LD006 leaves out `EQ`/`NE` (a mode or state compare,
`EQ(Mode, 2)`, is not a threshold), zero, and TIME literals (`PT := T#5S`
in the block that times it reads fine).

## Function Block Diagram

| ID | Fires when | Write instead | Threshold |
| --- | --- | --- | --- |
| **FBD001** | One statement nests blocks deeper than the limit. `AND(OR(a, b), c)` is 2 deep. An FB call counts as a level, so `t1(IN := AND(OR(a, b), c))` is 3. A wire name counts as nothing, and `NOT` is pin negation, not a block. | Name the inner results, one block per line: `hiLevel = GE(Level, HighLevelSP)`, then pass `hiLevel`. | `max-nesting`, default **2** |
| **FBD002** | A network with no title holds more than one function. That is either the statements before the first `NETWORK` line (or a body with none) or an untitled `NETWORK`. Functions are the separate pieces the diagram draws: statements joined by a wire or an FB instance's pins. Reading the same variable does not join two functions. | Start each function with its own `NETWORK 'what it does'` line, so it draws as its own titled band. A body that is one function needs no title, because the POU's name is its title. | none |
| **FBD003** | A comparison block `GT`, `GE`, `LT` or `LE` has a nonzero numeric literal argument. | A setpoint tag or a named `VAR CONSTANT`, as LD006. | none |

Only `.fbd` files get the FBD rules. A ladder file compiles through an FBD
netlist, but that netlist is the compiler's, and the ladder rules judge
the text you wrote.

## Sequential Function Chart

| ID | Fires when | Write instead | Threshold |
| --- | --- | --- | --- |
| **SFC001** | A transition's whole condition compares a variable with a constant (`State = 2`, `Step <> 3`, `Mode = Mode#Fill`, `SeqStep = 'Fill'`), **and** an `ACTION` of the same chart assigns that variable. That is a CASE state machine drawn as a chart: every transition shows `State = n`, and the real conditions hide in the actions' IFs. | The process condition in the transition (`LT101_Level >= FullSP`, `Fill.T >= T#30S`), outputs driven by step associations (`N FillReq;`), and no state variable. | none |
| **SFC002** | A step is named for its number: `S1`, `s_2`, `Step3`. | Name it for the process state (`Idle`, `Fill`, `Heat`), which is what the chart shows. | none |

SFC001 needs the chart to write the variable itself. A transition on an
operator's `Mode` setpoint, or on another program's state, is not reported.

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
the first POU covers the whole file. Prefer fixing the structure. A pragma
is for code you do not own, or for a rule that truly does not fit, and the
reason belongs in it.

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
