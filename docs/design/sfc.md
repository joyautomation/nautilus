# Design: IEC 61131-3 Sequential Function Chart (SFC) support

Status: implemented (landed 2026-07-27 — language, runtime wiring, LSP, VS Code editor, example; §6's staging plan is historical)
Author: design pass, 2026-07-23
Scope: adds SFC as the fourth (and last) IEC 61131-3 language in nautilus, beside ST, FBD, and LD.

---

## 0. Context and grounding

SFC is a *coordination* language: a chart of **steps** (states) connected by **transitions** (conditions), with **actions** associated to steps. Exactly one region of the chart holds a token at a time along any given path; the token moves when a transition below an active step becomes true. It is the last IEC language nautilus needs.

The three existing languages establish a hard architectural spine this design follows rather than reinvents:

- **Everything transpiles toward ST and reuses `st.Parse` + `st.Lower`.** FBD (`lang/fbd/fbd.go`) transpiles its netlist to ST. LD (`lang/ld/ld.go`) transpiles to an FBD netlist, which then transpiles to ST — a two-hop chain. Neither language touches the IR or the VM. They inherit the whole type system, the standard function/FB library, arrays, UDTs, and every diagnostic for free. **SFC will be a third front-end that emits ST.**
- **Text is canonical; the graphical view is a projection.** FBD/LD keep a git-diffable text form; the diagram is produced by a `graph` function (`lang/fbd/graph.go`, `lang/ld/graph.go`) and every editor gesture is a *structural op* (`lang/fbd/edit.go`) resolved in Go against a fresh parse, returning minimal `TextEdit`s. No consumer computes source spans itself.
- **Stateful runtime state already exists and already migrates.** `runtime/program.go` keeps a retained `ir.Frame` per program; `SwapWarm`/`ir.MigrateFrame` carries retained VAR slots across online edits by name+type. FBD/LD already put stateful FB instances (a `TON`) into VAR slots that persist between scans and survive online edits. **SFC step-activity flags and step timers are just more retained VAR slots — no VM or runtime-core change is required.** This is the single most important finding of this design: SFC does *not* need a new stateful execution mechanism.

The line-map discipline (`TranspileWithLines` in FBD/LD; `check.go`, `analysis.go`) that projects ST diagnostics back onto the original graphical source is a hard requirement SFC inherits.

### Files read to ground this design

`lang/fbd/{fbd,transpile,graph,edit,editparity,layout}.go`, `lang/ld/{ld,graph}.go`, `lang/ir/{node,program}.go`, `lang/ir/builtins_fb.go` (TON), `lang/st/time.go`, `runtime/{runtime,program}.go`, `internal/stproject/stproject.go`, `internal/lsp/{server,analysis}.go`, `cmd/naut/{main,check,fbd,new}.go`, `tools/vscode-iec/src/{fbdPreview,scan}.ts`, `tools/vscode-iec/package.json`.

---

## 1. Canonical textual format

**Recommendation: a new file extension `.sfc`, holding an ST POU whose body is an `SFC … END_SFC` block — byte-for-byte the same integration shape as `.fbd` and `.ld`.** The block uses IEC 61131-3's *standard textual SFC* production (`INITIAL_STEP`/`STEP`/`END_STEP`, `TRANSITION … END_TRANSITION`, `ACTION … END_ACTION`), so this is standard-only with no vendor dialect.

Rationale:

- A dedicated extension is what FBD and LD already do, and every integration seam (`stproject.ComposeAll`, `check.go`, `lsp/server.go`, `runtime.Language`) is already an extension switch. A POU-body-form-inside-`.st` was considered and rejected: it would force the `PROGRAM`↔language mapping (one program = one language, keyed by file) to break, and it would make the "open this file as a diagram" custom-editor binding (`filenamePattern: "*.sfc"`) impossible. The whole toolchain assumes *file extension ⇒ language*; honour it.
- Using the standard's own `STEP/TRANSITION/ACTION` keywords keeps us honest against the "standard-only" constraint while staying diffable. The one concession to concision (below) is allowing an action association to name a plain BOOL variable instead of always requiring a separate `ACTION` block — this is *also* in the standard (a Boolean variable is a legal action).

### 1.1 Grammar (the subset nautilus parses)

```
sfc-body   := "SFC" newline { element } "END_SFC"
element    := initial-step | step | transition | action | comment | layout-comment
initial-step := "INITIAL_STEP" ident ":" { assoc } "END_STEP"
step         := "STEP" ident ":" { assoc } "END_STEP"
assoc        := qualifier ident [ "(" time-literal ")" ] ";"      (* action association *)
qualifier    := "N" | "S" | "R" | "P" | "P0" | "P1"               (* slice 1 set *)
transition := "TRANSITION" [ ident ] "FROM" step-set "TO" step-set
              ":=" st-expression ";" "END_TRANSITION"
step-set   := ident | "(" ident { "," ident } ")"
action     := "ACTION" ident ":" st-statement-list "END_ACTION"
```

- `FROM`/`TO` take a **single** step for ordinary flow and a **parenthesised list** for simultaneous divergence (`TO (A, B)`) or convergence (`FROM (A, B)`). This is exactly the standard's textual form.
- An **alternative divergence** is *not* special syntax: it is simply two or more `TRANSITION FROM S …` sharing the same single source step `S`. Priority is **declaration order, top to bottom** (see §2.3). An optional transition name (`TRANSITION t3 FROM …`) is allowed for diagnostics/UI labels and has no semantic effect.
- An **alternative convergence** is two or more transitions with the same single `TO` step.
- The transition condition after `:=` is an arbitrary **ST boolean expression**, reusing the ST expression grammar verbatim (it is emitted straight into generated ST). Step attributes `Step.X` (BOOL, active) and `Step.T` (TIME, elapsed since activation) are legal inside conditions and action bodies.
- Action bodies (`ACTION … END_ACTION`) are **ST statement lists**, emitted verbatim. A Boolean-variable action (`N FillValve;`) needs no `ACTION` block.
- Comments (`//` line, `(* *)` block) pass through; the `(* @layout … *)` block is reused verbatim from FBD (`lang/fbd/layout.go`) for optional pinned node positions.

### 1.2 Complete worked example

A heated-tank batch sequence with all required features: 5 steps, an **alternative divergence** (`Fill → Idle` on abort *vs.* `Fill → Heat/Mix` on level), a **simultaneous divergence** (`Fill → (Heat, Mix)`) and matching **simultaneous convergence** (`(Heat, Mix) → Drain`), action associations with `N`/`S`/`R`/`P1` qualifiers, and transitions using ST expressions.

```
PROGRAM TankBatch
VAR_EXTERNAL
  Start     : BOOL;
  Abort     : BOOL;
  Level     : REAL;
  TempC     : REAL;
  FillSP    : REAL;
  EmptySP   : REAL;
  HeatSP    : REAL;
  FillValve : BOOL;
  DrainValve: BOOL;
  Heater    : BOOL;
  Mixer     : BOOL;
  RunLamp   : BOOL;
  AbortLamp : BOOL;
  BatchCount: INT;
END_VAR
VAR
  mixT : TON;   (* referenced by the Mix action body *)
END_VAR
SFC

  INITIAL_STEP Idle:
    N  RunLamp;              (* boolean action: RunLamp := Idle.X ... see §2.5 *)
    R  AbortLamp;            (* clear the abort lamp on return to Idle *)
  END_STEP

  STEP Fill:
    N  RunLamp;
    N  FillValve;            (* valve open while Fill active *)
  END_STEP

  STEP Heat:
    N  RunLamp;
    N  Heater;
  END_STEP

  STEP Mix:
    N  RunLamp;
    N  Stir;                 (* ACTION block, below *)
  END_STEP

  STEP Drain:
    N   RunLamp;
    N   DrainValve;
    P1  CountBatch;          (* pulse: run its body once on activation *)
  END_STEP

  (* --- transitions --- *)

  TRANSITION t_start FROM Idle TO Fill := Start AND NOT Abort;
  END_TRANSITION

  (* alternative divergence out of Fill: abort has priority (declared first) *)
  TRANSITION t_abort FROM Fill TO Idle := Abort;
  END_TRANSITION
  TRANSITION t_full  FROM Fill TO (Heat, Mix) := Level >= FillSP;   (* simultaneous divergence *)
  END_TRANSITION

  (* simultaneous convergence: fires only when BOTH Heat and Mix are active *)
  TRANSITION t_done FROM (Heat, Mix) TO Drain := (TempC >= HeatSP) AND mixT.Q;
  END_TRANSITION

  TRANSITION t_empty FROM Drain TO Idle := Level <= EmptySP;
  END_TRANSITION

  (* --- action blocks (ST bodies) --- *)

  ACTION Stir:
    Mixer := Mix.X;                  (* track the step: drops to FALSE on the final scan *)
    mixT(IN := Mix.X, PT := T#30S);  (* mixing timer; falling edge resets it (§2.5.1) *)
  END_ACTION

  ACTION CountBatch:
    BatchCount := BatchCount + 1;     (* runs exactly once, on Drain activation *)
  END_ACTION

  (* S/R stored-action demo lives on the lamp: an operator-visible latch *)
  ACTION HoldAbort:
  END_ACTION

END_SFC
END_PROGRAM
```

(The `S AbortLamp` / `R AbortLamp` pair would latch an abort indicator across steps; shown as `R AbortLamp` on `Idle` here and would pair with an `S AbortLamp` on a fault step in a fuller chart. Left minimal to keep the example readable.)

---

## 2. Semantics

nautilus implements a **declared conformant subset** of IEC 61131-3 §6.7 SFC semantics: the token-passing evolution rules in full, and a minimal-but-honest set of action qualifiers. Where the standard leaves an implementation choice (transition priority resolution, one scan's evolution atomicity) the choice is stated explicitly here and is the common soft-PLC choice.

### 2.1 The scan model and where SFC runs

An SFC program is one nautilus program. On each scan, `ir.Run` executes the program's lowered body once (`runtime/program.go` `Run`). SFC's generated ST body performs, **in this fixed order, once per scan**:

1. **Evaluate transitions** against the *pre-scan* step-activity snapshot.
2. **Resolve firing** (alternative-branch exclusivity, simultaneous gating).
3. **Deactivate** the source steps of every firing transition.
4. **Activate** the target steps of every firing transition. (Sets run after clears — see §2.4.)
5. **Update step timers** (`Step.T`) from the *new* activity.
6. **Compute actions** (drive action variables / run action bodies) from the new activity + qualifier state.

This ordering is a single deterministic pass. It corresponds to the standard's rule that within one evolution all enabled transitions clear simultaneously; steps 3–4 realize "simultaneous" by snapshotting in step 1. Actions are evaluated *after* the token has moved so a step activated this scan already drives its `N` actions this scan (standard behaviour: a newly-active step's non-stored actions are active in the same cycle).

The scan itself is unchanged runtime plumbing: read inputs → `prog.Run` → write outputs, with the same `ScanStats` phase breakdown (`ReadMs`/`ExecUs`/`WriteMs`). SFC adds no new phase — the whole chart evolution is part of `ExecUs`. Diagnostics need no schema change; §5 adds an *optional* step-activity watch surface built from the retained step BOOLs, which `runtime.AllLocals` already exposes.

### 2.2 Step activation / deactivation per scan

Each step `S` owns a retained `BOOL` slot `S.X` (its activity). A step is active iff `S.X` is TRUE at the end of the scan. `Step.X` read in a condition/action reads that slot. Deactivation and activation happen only in steps 3–4 above, driven by firing transitions; a step never changes activity for any other reason.

### 2.3 Transition evaluation order and alternative-divergence priority

All transitions are evaluated in step 1 from the pre-scan snapshot, so evaluation order does **not** affect *which conditions are read* — every transition sees the same consistent snapshot. Order matters only for **alternative divergence** (multiple transitions competing for the same token), where the standard requires mutual exclusion (a single token cannot take two paths).

**Alt-group definition (recommended: share-any-source).** Two transitions are in the same priority group iff their source sets **share at least one step**. Rationale: the invariant SFC must protect is that a single active step's token is consumed by at most one transition per scan. A step shared between two transitions is exactly a token that two transitions contend for — whether or not their *full* source sets are identical — so grouping on any shared source is the safe superset. Grouping only on *identical* source sets would miss a genuine contention: a plain `FROM S` transition competing with a convergence `FROM (S, other)` both want `S`'s token, but their source sets differ. Share-any-source catches it; identical-sets would not. (This makes the grouping relation non-transitive: A can share a step with B and B with C while A and C share nothing. That is not ambiguous, because firing is resolved in declaration order and a transition is suppressed only by a higher-priority sharer that itself **fires** — see the rule below. Such groups therefore need no diagnostic; until 2026-09 they drew an "ambiguous alternative-priority group" warning.)

**Priority rule (= standard default):** within a group, transitions are prioritised **in source order, first = highest**. A transition `tᵢ` *fires* only if it is **enabled** and **no higher-priority transition sharing a source with it fires**. Crucially the guard never reads the raw condition — only `enabled` (and, through `fire`, `enabled`) of the higher-priority sharers:

```
enabled(tⱼ) = (AND over s in Src(tⱼ): s.X) AND cond(tⱼ)          (* all sources active AND condition true *)
fire(tᵢ)    = enabled(tᵢ) AND NOT( OR over j<i, Src(tⱼ)∩Src(tᵢ)≠∅ : fire(tⱼ) )
```

`fire` is computed in declaration order, so every `fire(tⱼ)` a guard reads is already final. Mutual exclusion is exact: of two transitions sharing a step, the later one is guarded by the earlier one's `fire`, so at most one takes that step's token per scan.

**Why `enabled`, not `cond`.** If a higher-priority transition in the group is a simultaneous **convergence** (its source set is this step plus others), its `cond` can be TRUE while the transition is *not enabled* because its other sources are inactive. Guarding on the raw `cond` would then suppress every lower-priority branch even though the convergence cannot fire — deadlocking the token at the step forever. Guarding on `enabled` (which ANDs in every source's `.X`) suppresses a lower-priority branch only when the higher-priority one can actually take the token. This is the difference that makes mixed alt-divergence/convergence groups correct.

**Why `fire`, not just `enabled`.** Slice 1 guarded on `enabled(tⱼ)`. That is right for two transitions, but in a non-transitive group it can suppress `tᵢ` because of a `tⱼ` that is enabled yet does not fire (a still-higher sharer took *its* token). Example: `t1 FROM A`, `t2 FROM (A, B)`, `t3 FROM B`, all enabled. `t1` takes A; `t2` is suppressed by `t1`; an `enabled` guard would still suppress `t3` because of `t2`, leaving B's token uncontested but untaken for one scan. The `fire` guard lets `t3` take it in the same scan. (Nothing could deadlock under the old guard either: whenever `t2` is enabled, `t1` or `t2` fires and empties A, so `t3` fires a scan later.)

The standard permits explicit numeric priorities; slice 1 uses source order only (numeric-priority annotation is a documented non-goal, §7). This is deterministic and diffable — reordering the text reorders priority, visible in the diagram as top-to-bottom branch order.

### 2.4 Simultaneous divergence / convergence and token rules

A transition carries a **set** of source steps and a **set** of target steps:

- **Simultaneous divergence** (`FROM S TO (A, B)`): one source, many targets. On fire, `A.X` and `B.X` both become TRUE — the token *splits* into concurrent active steps.
- **Simultaneous convergence** (`FROM (A, B) TO C`): many sources, one target. The transition is **enabled only when *all* sources are active**: `enabled = A.X AND B.X AND cond`. On fire, all sources clear and `C` sets — the tokens *merge*.

General firing for any transition `t` with source set `Src(t)` and target set `Tgt(t)`:

```
enabled(t) = (AND over s in Src(t): s.X) AND cond(t)
fire(t)    = enabled(t) AND altGuard(t)
             (* altGuard(t) = NOT( OR of fire(t') for every higher-priority t'
                sharing a source with t ) — see §2.3; identically TRUE when no
                other transition shares a source with t *)
```

**Set-dominates-clear (the one-scan atomicity rule).** A step that is simultaneously a source of one firing transition and a target of another firing transition in the *same* scan must end the scan **active**. This is guaranteed by ordering: emit *all* clears (step 3) before *all* sets (step 4). A tight self-loop (`FROM S TO S`) therefore leaves `S` active, matching the standard's resolution. This is stated as the declared conformant choice.

**Token conservation is not machine-enforced in slice 1.** A malformed chart (e.g. a simultaneous branch whose legs are re-merged by an alternative convergence) can in principle duplicate or lose a token. `naut check` flags the structurally detectable cases (convergence arity mismatch, unreachable steps — §5); full token-conservation proof is a non-goal (§7).

### 2.5 Action qualifiers — slice 1 set

**Recommendation: ship `N`, `S`, `R`, `P1` in slice 1; add `P`/`P0` in the same slice if cheap, and stage the timed qualifiers `L`/`D`/`SD`/`DS`/`SL` to a later slice.**

| Qualifier | Meaning | Slice |
|-----------|---------|-------|
| `N` | Non-stored: action active exactly while the step is active. | 1 |
| `R` | Reset: deactivates a stored (`S`) action. Acts **once**, on the step's activation. | 1 |
| `S` | Set (stored): action becomes active and **stays** active until an `R` on the same target. Acts **once**, on the step's activation. | 1 |
| `P1` | Pulse on rising activation: body runs **once**, the scan the step becomes active. | 1 |
| `P` / `P0` | Pulse (P ≡ P1 in most vendors) / pulse on deactivation. | 1 if cheap, else 1b |
| `L` `D` `SD` `DS` `SL` | Time-limited / delayed / stored-delayed variants. | **later slice** |

Rationale for staging: `N`/`S`/`R`/`P1` cover the overwhelming majority of real sequences and require **no new timer state** beyond a one-scan edge memory for `P1`. The timed qualifiers each require a per-association timer and a more elaborate action-control block; deferring them keeps slice-1 semantics small and *fully honest* (we ship a declared subset, not a broken superset). The deferral is called out in `naut check`: an unsupported qualifier is a clear diagnostic, never silently mis-executed.

**Action-control model (simplified conformant subset).** The standard defines an "action control" function block per action with a `Q` output. nautilus computes, for each *(step, action, qualifier)* association, a Boolean "active" signal, then combines per action:

- **Boolean-variable action** `Q Var`: each association writes `Var` **only on the scans it acts**, never recomputing it from scratch every scan, so an association on an inactive step never touches the variable:
  - `N` (and the pulses `P`/`P1`/`P0`): every association's active signal is OR-combined (the standard combines multiple actions on one Boolean variable by OR). While that OR is TRUE, `Var := TRUE` every scan; on the one scan it falls (the final scan), `Var := FALSE` — or `Var :=` the `S`/`R` stored value, when the variable also carries `S`/`R` associations (the standard's `Q = N OR stored`). So `N RunLamp` on all five steps holds `RunLamp` TRUE while any of them is active and drops it once when the chart leaves them (e.g. into `Aborted`).
  - `S` / `R`: `Var := TRUE` / `Var := FALSE` **once**, on the scan the step activates. A reset dominates a set on the same scan. An `N` that is active on that scan still wins over a reset (N is applied last).
  - Outside those scans the variable is not written by the chart's associations: an ACTION body, another task, or an HMI write stands.
- **Body action** `Q ActName` (an `ACTION` block): the body executes each scan the association's active signal is TRUE, **plus one final scan on the falling edge** (the final-scan rule, §2.5.1). For `N`, the active signal is "while the step is active"; for `P1`, "the one scan the step just activated" (a pulse, no final scan — see §2.5.1).
- **`S`/`R` on a body action**: `S Act` on step `Sa` sets a retained `_act_<Act>_stored := TRUE` when `Sa` becomes active; `R Act` on step `Sb` sets it FALSE. The action's `N`-equivalent active signal is then `_act_<Act>_stored`. (Store/reset combine across steps; a reset dominates a set on the same scan.)

**Association vs ACTION writes to the same variable.** A variable can be both an association target (`N X`, `S X`, `R X`, …) and assigned inside an `ACTION` body. The rule: within a scan, body actions run first and boolean-variable associations are applied last, and an association only writes on the scans it acts (above). So **the association wins while it acts** — every scan its `N` step is active plus that step's final scan, the one activation scan of an `S`/`R`, the one scan of a pulse — **and the variable is the ACTION's otherwise.** IEC 61131-3 has the action-control block's `Q` drive the variable outright; this is the same while the association is active, and keeps the variable free (instead of pinned FALSE) when it is not — which is what lets an `R X` on a never-reached abort step coexist with an ACTION that sets `X`. `naut check` warns on every such pairing (§5.1) because it reads as two owners, and a test sampling a few scans cannot tell which one produced a value.

#### 2.5.1 The final-scan rule (body actions)

**Rule (declared conformant subset, = the standard's "final scan"): a body action executes one *additional* scan on the falling edge of its active signal.** Implementation: a retained one-scan memory `_act_<A>_prev` holds the previous scan's active signal; the body is wrapped `IF active OR _act_<A>_prev THEN <body> END_IF; _act_<A>_prev := active;`. On that final scan the driving step's `.X` is already FALSE, so an idiomatic body written against step activity naturally shuts its outputs down.

**Why it exists — the re-entry hazard.** A body action that only ran *while* active would strand every output it drives. Consider `N Stir` on `Mix` with body `Mixer := TRUE; mixT(IN := TRUE, …);`. When `t_done` fires and `Mix` deactivates, the body simply stops running: `Mixer` is never assigned FALSE (it latches TRUE forever) and `mixT` is never called with `IN := FALSE`, so it freezes with `ET` at `PT` and `Q = TRUE`. If the chart later re-enters `Mix`, `mixT.Q` is *already* TRUE, so `t_done` fires instantly — zero actual mixing. The final-scan rule closes this: the idiomatic body is written to *track* the step (`Mixer := Mix.X; mixT(IN := Mix.X, …);`), and the one guaranteed final execution — with `Mix.X` already FALSE — drives `Mixer := FALSE` and `mixT(IN := FALSE)`, resetting the timer for a clean re-entry. Authors should therefore write body outputs as functions of the step's `.X` (or use boolean-variable `N` actions, which get the OR-combine-to-FALSE for free), not as bare `:= TRUE` latches.

**`P1` and the final scan.** `P1` is a rising-edge *pulse*: its body runs exactly once, the scan the step activates. It has **no** final scan — a pulse has no "while active" interval to fall off of, and adding a second execution would defeat "run once." (`P0`, when shipped, is the symmetric falling-edge pulse — one execution on deactivation, which subsumes the final-scan behaviour for that qualifier.) So the `_act_<A>_prev`/final-scan wrapper is emitted only for **level** qualifiers (`N`, and the `N`-equivalent signal of an active `S` store); pulse qualifiers keep their edge-memory (`_..._p1`) and fire-once semantics.

### 2.6 `Step.X` and `Step.T`

- **`S.X`** — the retained BOOL activity slot. Free to read anywhere.
- **`S.T`** — TIME elapsed since `S` last became active; `T#0s` while inactive. **Implementation: reuse the existing `TON`.** For each step whose `.T` is actually referenced, generate a hidden `_S_<step>_t : TON` and emit `_S_<step>_t(IN := <step>.X, PT := T#<cap>);`, then compile `S.T` to `_S_<step>_t.ET`. `TON.ET` (see `lang/ir/builtins_fb.go`) is driven by the runtime clock (`ctx.NowMs`), resets to 0 when `IN` falls, and is clamped to `PT`; choosing `<cap>` ≥ the largest bound the chart compares against makes the clamp invisible. This reuses a real, tested, clock-based FB and inherits its warm-restart migration for free. Steps whose `.T` is never read generate no timer (zero cost).

### 2.7 First scan and warm restart

- **Cold start / cold `Swap`.** `ir.NewFrame` zero-inits every slot except those with a declared `Init`. The transpiler emits the **initial step's** activity slot with `:= TRUE` (`_S_Idle_X : BOOL := TRUE;`) and all other step slots default FALSE. So the first scan finds exactly the initial step active — the standard's cold-start rule — with no "first scan" flag needed.
- **Warm restart / online edit (`SwapWarm`).** Step activity, `S/R` stored flags, `P1` (and any pulse) edge memories, body-action final-scan memories (`_act_<A>_prev`, §2.5.1), boolean-variable drive memories (`_act_<Var>_lvl`, §2.5), and step `TON`s are all retained VAR slots; `ir.MigrateFrame` carries them across by name+type. So an online edit **preserves the live token** and every timer — the running batch does not restart. Renaming a step is the honest exception: the renamed step's slot is a new name, so its token resets to FALSE (surfaced in the `SwapReport.Resets` list the UI already shows). This matches how renaming an FB instance resets its state today.
- **Redundancy takeover.** `Program.ResetFrame` re-inits to the initial step — the existing behaviour, correct for SFC.

---

## 3. Compilation strategy

**Recommendation: transpile the chart to per-scan ST (the FBD/LD posture), not interpret a chart structure at runtime.** SFC → ST → `st.Parse` → `st.Lower` → IR. No new IR node, no VM change, no runtime-core change.

Why transpile, not interpret:

- It reuses the entire compiled pipeline and every diagnostic, exactly as FBD/LD do. An interpreter would duplicate type-checking, scoping, the FB library, and the diagnostic/line-map machinery.
- **Step state is naturally VAR-slot state.** The activity BOOLs, `S/R` stored flags, pulse edge memories, body-action final-scan memories, and step `TON`s become ordinary retained slots — which already persist across scans (`ir.Frame`) and already migrate across online edits (`ir.MigrateFrame`). There is nothing an interpreter would give us here that the existing retained-frame machinery does not already provide. This is why **no refactor of the VM or the runtime scan loop is required.**
- Live values / online-edit observation come for free: step activity is `AllLocals()` state, so the existing inline-live-values path (`tools/vscode-iec/src/liveValues.ts`, `runtime.AllLocals`) shows a step's `.X` the same way it shows any local today. The diagram editor highlights the active step by reading the `_S_<step>_X` locals — no new controller API.

### 3.1 What the generated ST looks like (excerpt for the §1.2 example)

The transpiler injects a `VAR` block for hidden state and emits a single ordered statement body. Sketch (names illustrative; real output is deterministic and line-mapped):

```
VAR
  _S_Idle_X  : BOOL := TRUE;   _S_Fill_X : BOOL;   _S_Heat_X : BOOL;
  _S_Mix_X   : BOOL;           _S_Drain_X: BOOL;
  _drain_p1  : BOOL;           (* P1 edge memory: previous Drain.X *)
  _act_Stir_prev : BOOL;       (* final-scan memory for the N Stir body (§2.5.1) *)
  _act_RunLamp_lvl : BOOL;     (* final-scan memory for RunLamp's N drive (§2.5) *)
END_VAR
(* 1. evaluate transitions from the pre-scan snapshot *)
  _f_start := _S_Idle_X AND (Start AND NOT Abort);
  _f_abort := _S_Fill_X AND (Abort);
  _f_full  := _S_Fill_X AND (Level >= FillSP) AND NOT (_f_abort);
             (* alt-guard = NOT fire(t_abort), resolved in declaration order;
                fire ANDs in every source's .X through enabled, which is what
                makes a higher-priority convergence guard correct (§2.3) *)
  _f_done  := _S_Heat_X AND _S_Mix_X AND ((TempC >= HeatSP) AND mixT.Q);  (* sim. convergence: both sources *)
  _f_empty := _S_Drain_X AND (Level <= EmptySP);
(* 3. clear sources *)
  IF _f_start THEN _S_Idle_X := FALSE; END_IF;
  IF _f_abort THEN _S_Fill_X := FALSE; END_IF;
  IF _f_full  THEN _S_Fill_X := FALSE; END_IF;
  IF _f_done  THEN _S_Heat_X := FALSE; _S_Mix_X := FALSE; END_IF;
  IF _f_empty THEN _S_Drain_X := FALSE; END_IF;
(* 4. set targets (after all clears: set dominates) *)
  IF _f_start THEN _S_Fill_X := TRUE; END_IF;
  IF _f_abort THEN _S_Idle_X := TRUE; END_IF;
  IF _f_full  THEN _S_Heat_X := TRUE; _S_Mix_X := TRUE; END_IF;
  IF _f_done  THEN _S_Drain_X := TRUE; END_IF;
  IF _f_empty THEN _S_Idle_X := TRUE; END_IF;
(* 6. actions — bodies first (gated by their active signal), then boolean
      variables, each written only on the scans its associations act *)
  (* N Stir body — final-scan wrapper (§2.5.1): while Mix active + one falling-edge scan *)
  IF _S_Mix_X OR _act_Stir_prev THEN Mixer := _S_Mix_X; mixT(IN := _S_Mix_X, PT := T#30S); END_IF;
  _act_Stir_prev := _S_Mix_X;
  IF _S_Drain_X AND NOT _drain_p1 THEN BatchCount := BatchCount + 1; END_IF; (* P1 CountBatch: pulse, no final scan *)
  (* N RunLamp on five steps: held TRUE while any is active, FALSE once on the scan they all drop *)
  IF _act_RunLamp_lvl AND NOT (_S_Idle_X OR _S_Fill_X OR _S_Heat_X OR _S_Mix_X OR _S_Drain_X) THEN RunLamp := FALSE; END_IF;
  IF _S_Idle_X OR _S_Fill_X OR _S_Heat_X OR _S_Mix_X OR _S_Drain_X THEN RunLamp := TRUE; END_IF;
  _act_RunLamp_lvl := _S_Idle_X OR _S_Fill_X OR _S_Heat_X OR _S_Mix_X OR _S_Drain_X;
  IF _act_FillValve_lvl AND NOT (_S_Fill_X) THEN FillValve := FALSE; END_IF;
  IF _S_Fill_X THEN FillValve := TRUE; END_IF;
  _act_FillValve_lvl := _S_Fill_X;
  (* … Heater, DrainValve likewise … *)
  IF _S_Idle_X AND NOT _idle_prev THEN AbortLamp := FALSE; END_IF;   (* R AbortLamp: once, on Idle's activation *)
  _drain_p1 := _S_Drain_X;  _idle_prev := _S_Idle_X;
```

The `_f_*` transition flags are `VAR_TEMP`-style scratch (compiled as ordinary locals — nautilus has no temp class, so they are locals reset by assignment before use each scan, never read stale). Because the `_f_*` values are computed entirely from the pre-scan `_S_*_X` snapshot before any clear/set, the evolution is atomic within the scan.

### 3.2 The line map

Like `fbd.TranspileWithLines` / `ld.TranspileWithLines`, `sfc.TranspileWithLines` returns `(stSrc, lineMap, err)` where `lineMap[i]` is the 1-based `.sfc` source line that generated-ST line `i+1` came from. A transition condition maps to its `TRANSITION` line; an action body maps to the corresponding `ACTION` line; a step's action associations map to the step. This is what projects an ST type error in a condition back onto the offending transition in `check.go`, the LSP, and diagnostics.

---

## 4. Render model + edit ops

This is the third instance of the graph/edit seam. It follows **LD's posture more than FBD's**: SFC structure (which steps, which transitions, which branches) is *canonical in the text* — the drawing is a function of the source — so the render model is derived, not stored. SFC's one genuine 2-D need (horizontal placement of parallel branches) is met by **reusing FBD's optional `(* @layout *)` pin block** as an aesthetic override, not as the structural source of truth.

### 4.1 Render model (`sfc.Model`, emitted by `naut sfc graph`)

```
Model {
  Name    string
  Vars    []VarDecl        // reused shape from lang/ld VarDecl (header decls)
  Steps   []Step           // { Name, Initial bool, Line, EndLine, Actions []Assoc }
  Assoc   inline in Step    // { Qualifier, Target, Time?, Line }
  Trans   []Transition     // { Name, From []string, To []string, Cond, Line, EndLine, Kind }
  Actions []ActionBlock    // { Name, Body string, Line, EndLine }
  Comments []Comment       // reused shape from lang/ld / fbd editparity
  Layout  map[string]xy    // parsed from (* @layout *) — optional pins
}
```

`Transition.Kind` is derived (`"normal" | "alt" | "simDiverge" | "simConverge"`) from `len(From)`/`len(To)` and shared-source grouping, so the webview can draw the double-bar (simultaneous) vs. single-bar (alternative) convergence/divergence glyphs without re-deriving structure. Stable ids mirror FBD/LD conventions: `st:<stepName>`, `tr:<transName-or-line>`, `ac:<actionName>`, plus `g:`/`cm:` reused from FBD for ghosts/comments.

Auto-layout is topological (steps flow top-to-bottom along transitions; parallel legs fan into columns) — computed in the webview from the model, exactly as the FBD webview lays out from topology. The `@layout` block only overrides x/y for nodes the user dragged.

### 4.2 Edit ops (slice 1)

Addressed by render-model ids, resolved against a fresh parse in Go, returning minimal `TextEdit`s — the identical contract to `fbd.ApplyEdit` (`lang/fbd/edit.go`) and reusing its `TextEdit`/`EditOp` JSON shape:

| Op | Effect |
|----|--------|
| `addStep` | insert a `STEP name: END_STEP` (optionally spliced into a transition, see below) |
| `deleteStep` | remove a step and dangling references (leaves compile diagnostics as breadcrumbs, per the FBD editparity philosophy) |
| `renameStep` | rename step + fix `FROM`/`TO` references + remap `@layout` id (reuse `remapLayout`) |
| `addTransition` | insert `TRANSITION FROM a TO b := ; END_TRANSITION` |
| `deleteTransition` | remove a `TRANSITION` block |
| `setCondition` | replace a transition's `:= expr` |
| `addAssoc` / `setAssoc` / `deleteAssoc` | add/edit/remove an action association line inside a step |
| `setActionBody` | replace an `ACTION` block body |
| `insertAlternativeBranch` | add a second `TRANSITION FROM <same source>` (priority = insertion order) |
| `insertSimultaneousBranch` | turn a `TO x` into `TO (x, y)` and add step `y` |
| `joinSimultaneousBranch` | turn a `FROM x` into `FROM (x, y)` for an existing step `y` — a simultaneous convergence (Check, not the op, verifies the sources share a divergence) |
| `setLayout` / `clearLayout` | reuse FBD's layout ops verbatim |
| `setComment` | reuse FBD's comment op verbatim |

**The minimal editing unit (LD's "rung rewrite IS the edit" analog).** LD's insight is that a rung is a self-contained unit whose whole text can be rewritten. SFC's analog is the **step-plus-its-single-outgoing-transition** for the common *linear* case: appending to a sequence is "insert a new step and re-point one transition," a bounded local rewrite. Branches (alternative/simultaneous) are the explicit exceptions that need dedicated ops because they touch multiple transitions. So: linear sequence edits are the easy, mostly-mechanical majority; branch edits are the handful of ops that carry structural care.

Layout representation decision — **recommend: structure is implied by the text (canonical, like LD); position is an optional `@layout` pin block (like FBD).** This is the honest hybrid: SFC is more grid-structured than FBD (so we don't *need* stored coordinates for a readable default), but genuinely 2-D at parallel branches (so we *want* FBD's escape hatch). Do not invent a new layout mechanism — reuse `lang/fbd/layout.go`'s block and ops.

---

## 5. LSP + CLI + validation

### 5.1 `naut check` (validation)

The base pipeline is inherited: `.sfc` → `sfc.TranspileWithLines` → the existing ST parse+lower, with positions mapped back (the exact pattern already in `cmd/naut/check.go` for `.ld`/`.fbd`). On top, SFC-specific structural checks (in `lang/sfc/check.go`, run before transpile):

- **Unreachable step** — a non-initial step that is no transition's `TO` target (warn; it never activates, and an in-progress edit — a step just added or pasted — is unreachable until wired).
- **Dead-end step** — a step that is no transition's `FROM` source (warn; a terminal step may be intentional).
- **Transition with an empty/missing condition** (`:=` with nothing) — error.
- **Convergence/divergence arity** — a `FROM (A,B)` whose steps aren't all real; a simultaneous convergence whose sources can never be concurrently active (structurally: not reachable from a common simultaneous divergence) — warn.
- **Duplicate / missing initial step** — exactly one `INITIAL_STEP` required.
- **Action reference to an undefined action or variable** — an association naming neither an `ACTION` block nor a declared variable → error (the variable case is *also* caught by ST lowering; catching it structurally gives a better message).
- **Unsupported qualifier** (e.g. `L`, `SD` in slice 1) — clear "not yet supported" diagnostic, never silent.
- **Reference to `Step.X`/`Step.T` of an unknown step** — error.
- **Association vs ACTION write** — a variable targeted by a bare qualifier association (`N X`, `S X`, `R X`, `P1 X`, …) and also assigned (`X := …`, at statement level) inside an `ACTION` body → warning, naming the step, the qualifier, the ACTION(s), and the rule that decides between them (§2.5: the association wins while it acts, the ACTION otherwise).
- *(Removed 2026-09: "ambiguous alternative-priority group" for a non-transitive shared-source group. With the `fire`-based guard of §2.3 such a group has one well-defined outcome, so the warning only ever flagged correct charts.)*

All emitted through the same gcc-style `path:line:col: message` channel, positions on the offending `.sfc` construct via the line map.

### 5.2 LSP

Add `analyzeSFC` to `internal/lsp/analysis.go`, dispatched by the `.sfc` extension in `internal/lsp/server.go` (beside the existing `.fbd`/`.ld` branches). It transpiles to ST and runs the existing `analyze` over the result, projecting diagnostics back through the line map — identical to `analyzeFBD`/`analyzeLD`. Because conditions and action bodies **are** ST, this gives, for free inside them: undeclared-identifier diagnostics, type errors, hover (variable types, UDT expansion), go-to-definition on tags, and member completion after `.` (`PIT_001.` → members). SFC-specific completion additions: step names after `FROM`/`TO`, qualifiers after the start of an association line, and `.X`/`.T` after a step name. Header `VAR` hover/completion works unchanged (declarations map 1:1, as in FBD).

### 5.3 CLI verbs

Mirror `naut fbd` (`cmd/naut/fbd.go`) exactly, in a new `cmd/naut/sfc.go`:

- `naut sfc graph <file>` — emit the render model JSON (`-` reads stdin).
- `naut sfc edit` — read `{"source", "op"}`, return `{"edits":[…]}`.

Plus wire `.sfc` into: `naut check` (walk + transpile hop), `naut new --language sfc` (scaffold a blank chart from a template), and `main.go`'s verb switch.

---

## 6. Staging plan (for parallel background agents)

Slices are cut so the parallel ones touch **disjoint files**. The shared contract is the AST (slice A) and the wiring (slice 0); once those land, B/C/E proceed in parallel with no file overlap, and D follows C.

### Slice 0 — Language wiring (PREREQUISITE, serial, mechanical)

Land the "new language exists" seams as thin pass-throughs so nothing else is blocked. **Must merge before A–E start.**

- Files: `runtime/program.go` (`Language`, `lowerSource`), `internal/stproject/stproject.go` (`.sfc` in the `ComposeAll` ext allowlist), `cmd/naut/check.go` (ext allowlist + transpile hop), `internal/lsp/server.go` (`.sfc` → `analyzeSFC` dispatch), `cmd/naut/main.go` (`sfc` verb), plus a new `lang/sfc/sfc.go` exposing `HasBlock`, `Compile`, `Transpile`, `TranspileWithLines` as stubs (returning "not implemented" until B fills them).
- Depends on: nothing.
- Done when: `.sfc` files are discovered/compiled through the pipeline (erroring cleanly as "not implemented"), and every existing test still passes.
- Tier: **mechanical**.

### Slice A — Lexer / parser / AST + structural check (foundation, mostly serial)

- Files: `lang/sfc/{lexer,ast,parser,check}.go` + tests. Defines the AST every other slice imports.
- Depends on: slice 0 (package skeleton).
- Done when: the §1.2 example and a corpus of charts (alt divergence, sim divergence/convergence, all slice-1 qualifiers, `.X`/`.T` refs) parse to a stable AST; `check.go` produces the §5.1 diagnostics with correct positions.
- Tier: **standard**.

### Slice B — Transpile / lowering + runtime semantics + tests  ← **highest remaining risk**

- Files: `lang/sfc/transpile.go` (+ semantics helpers) + heavy tests. Consumes slice A's AST; owns the SFC→ST generation and the line map.
- Depends on: slice A (AST).
- Done when: the §3.1 generated ST compiles and *runs* correctly under a scan-by-scan test harness (see §9 trace); token evolution, enabled-based alt-priority exclusivity (including a mixed divergence/convergence group), simultaneous split/merge, set-dominates-clear, `N`/`S`/`R`/`P1` qualifiers, the body-action final-scan rule (§2.5.1, with the Mixer/`mixT` re-entry case as a regression test), `Step.T` via `TON`, cold-start initial step, and warm-restart token preservation all verified against expected activity per scan.
- Tier: **subtle**.

### Slice C — Render model + edit ops + CLI

- Files: `lang/sfc/{graph,edit,layout}.go` (layout reuses/imports FBD's), `cmd/naut/sfc.go`, `cmd/naut/fbd.go`-parallel wiring. Consumes slice A's AST/parser; **no overlap with B** (B owns transpile, C owns graph/edit).
- Depends on: slice A (AST). Independent of B.
- Done when: `naut sfc graph` emits a stable model for the corpus; each slice-1 edit op round-trips (op → text edit → re-parse → expected model), reusing FBD's layout/comment ops.
- Tier: **standard**.

### Slice D — VS Code editor + preview

- Files: `tools/vscode-iec/src/sfcPreview.ts` (+ webview UI), `tools/vscode-iec/src/extension.ts` (register `iec-sfc` language + `nautilus.sfcDiagram` custom editor + preview command), `tools/vscode-iec/package.json` (language contribution, `*.sfc` custom editor, commands), grammar/`language-configuration`. References the existing `fbdPreview.ts` host pattern; does not redesign it.
- Depends on: slice C (graph/edit JSON contract).
- Done when: opening a `.sfc` file shows the chart, live-updates on text change, highlights the active step from live values, and every gesture routes through `naut sfc edit`.
- Tier: **frontend** (standard).

### Slice E — LSP analysis

- Files: `internal/lsp/analysis.go` (`analyzeSFC` + SFC-specific completion). Server dispatch already stubbed in slice 0.
- Depends on: slice A (parser) + slice B (transpile, for lowering diagnostics).
- Done when: diagnostics land on the right `.sfc` construct; hover/completion/definition work inside conditions and action bodies; step-name and qualifier completion work.
- Tier: **standard**.

**Parallelism:** 0 → A serial; then **B, C, E in parallel** (disjoint files, all read the AST); **D after C**. E's diagnostic-quality tests want B done, but E can be developed against A and finalised once B lands.

**Highest-risk slice: B (transpile + semantics).** Everything else is a well-trodden path (parser, graph/edit, LSP, webview all have two prior instances to copy). B is where the genuinely new design decisions concentrate and where a wrong choice is a *runtime* wrong-answer, not a compile error: the atomic pre-snapshot evolution, enabled-based alternative-branch exclusivity guards, simultaneous split/merge gating, set-dominates-clear ordering, the `S/R`-combine-by-OR / `P1`-edge / body-action final-scan action-control model (§2.5.1), and `Step.T`-via-`TON` clamping. It needs the scan-by-scan trace harness (§9) as its acceptance oracle before it can be called done.

---

## 7. Explicit non-goals for v1

- **Macro steps** and **SFC-in-SFC nesting** (a step whose body is itself a chart). Slice 1 is flat charts only.
- **Timed action qualifiers** `L`, `D`, `SD`, `DS`, `SL` (deferred to a follow-on slice; flagged, not silently accepted).
- **Explicit numeric transition priorities** — source order only in v1.
- **IL action bodies** — action bodies are ST only (nautilus has no IL front-end).
- **Indicator variables** on transitions and the full standard **action-control block** with all edge cases — v1 ships the declared simplified subset of §2.5.
- **Machine-checked token conservation** for pathological branch topologies — v1 checks the structurally obvious cases only (§5.1).
- **Vendor SFC import** (Rockwell `.L5X` SFC routines, Siemens GRAPH, CODESYS `.sfc`). This is deliberately deferred and noted as a **future moat item** consistent with the project's language strategy: standard-only canonical form now; vendor-format import later as the migration on-ramp.

---

## 8. Refactors that must land FIRST (serial, before parallel slices)

Everything SFC needs is *additive* — the crucial finding is that **no existing code assumes stateless program bodies**, so there is no invasive refactor. The runtime already retains and migrates VAR-slot state, and FBD/LD already carry stateful FB instances through it. The "refactors" are therefore the thin **Slice 0 wiring** touchpoints; they must merge serially first because A–E all build on them:

1. `runtime/program.go` — `Language(src)` (add an `sfc.HasBlock` branch returning `"sfc"`) and `lowerSource(src)` (add an SFC→ST transpile hop *before* the ST parse; note SFC transpiles **directly to ST**, not through FBD, so it is a sibling of the LD/FBD hops, not a stage in their chain). Small, but shared and load-bearing — every program-compile path funnels through here.
2. `internal/stproject/stproject.go` — `ComposeAll` file-extension allowlist (`ext != ".st" && ext != ".fbd" && ext != ".ld"` → add `.sfc`), so `.sfc` program files are discovered by `naut pull`, the LSP prelude, and the runtime composer.
3. `cmd/naut/check.go` — the walk-dir extension filter and the per-file transpile hop (add the `.sfc` branch beside `.ld`/`.fbd`, composing the line map).
4. `internal/lsp/server.go` — `setDocument` dispatch: `.sfc` → `analyzeSFC` (beside the `.fbd`/`.ld` suffix checks).
5. `cmd/naut/main.go` — add the `sfc` verb to the switch and the usage text.
6. `server/program.go` — already calls `runtime.Language(...)`; once (1) returns `"sfc"` it works unchanged (verify, no edit expected).
7. `cmd/naut/new.go` — `--language` validation currently rejects anything but `st|fbd|ld`; extend to `sfc` and add a `program_blank.sfc.tmpl` template. (Scaffolding only — can land with slice C rather than strictly first.)

None of these is risky; all are the same shape as an already-present `.ld`/`.fbd` branch. The reason they are "first" is *ordering*, not difficulty: they are the shared files, so doing them once up front prevents five parallel agents from colliding in `runtime/program.go`, `check.go`, and `server.go`.

`internal/lsp/analysis.go` (`analyzeSFC`) is **not** in this serial prefix — it is slice E, because `server.go` can dispatch to a stub that slice E fills, exactly as slice 0 stubs `lang/sfc/sfc.go`.

---

## 9. Worked scan-by-scan token-flow trace

Using the §1.2 chart. Inputs are driven by the operator/field each scan; the table shows the **pre-scan** input read and the **post-scan** step activity (`S.X` after step 4 of §2.1). `_drain_p1` is the `P1` edge memory. Initial state (cold start, scan 0 not yet run): `Idle.X=TRUE`, all others FALSE.

Assume `FillSP=100`, `HeatSP=80`, `EmptySP=5`, `mixT` PT `30s`.

| Scan | Pre-scan inputs | Transitions that fire | Post-scan active steps | Notes |
|------|-----------------|-----------------------|------------------------|-------|
| 1 | `Start=F` | none (`t_start` needs Start) | `Idle` | `RunLamp:=TRUE` (Idle N). Chart idles. |
| 2 | `Start=T, Abort=F` | `t_start` (`Idle.X ∧ Start ∧ ¬Abort`) | `Fill` | Token Idle→Fill. `FillValve:=TRUE`, `RunLamp:=TRUE`. |
| 3 | `Level=40` | none (`t_full` needs Level≥100; `t_abort` needs Abort) | `Fill` | Still filling. |
| 4 | `Level=100, Abort=F` | `t_full` (`Fill.X ∧ Level≥100 ∧ ¬Abort`) | **`Heat` AND `Mix`** | **Simultaneous divergence**: one transition sets *both* targets. Token splits. Fill clears → `FillValve:=Fill.X=FALSE`. `Heater:=Heat.X=TRUE`. Stir wrapper runs (`Mix.X∨_act_Stir_prev`): `Mixer:=Mix.X=TRUE`, `mixT(IN:=Mix.X=TRUE)` starts; `_act_Stir_prev:=TRUE`. `mixT.Q=FALSE` (just started). |
| 5 | `TempC=60`; `mixT.Q=F` (t<30s, from scan 4's action call) | none (`t_done` = `Heat.X ∧ Mix.X ∧ TempC≥80 ∧ mixT.Q`) | `Heat`, `Mix` | Both legs run concurrently. Convergence gated: both sources active but condition false. Stir keeps running (`Mix.X=T`): `Mixer=TRUE`, `mixT(IN:=TRUE)` counts. |
| 6 | `TempC=85`; `mixT.Q=F` (still <30s) | none (`mixT.Q` still false) | `Heat`, `Mix` | Temp reached but mixer timer not done — convergence correctly waits for **all** conditions, not just both steps active. (Transitions read `mixT.Q` from the prior scan's action call — a one-scan pipeline.) |
| 7 | `TempC=85`; `mixT.Q=T` (≥30s) | `t_done` (`Heat.X ∧ Mix.X ∧ 85≥80 ∧ mixT.Q`) | `Drain` | **Simultaneous convergence**: both sources clear (step 3), single target sets (step 4). Tokens merge. `Heater:=Heat.X=FALSE` (N boolean drops). **Stir final scan** (`Mix.X=F` but `_act_Stir_prev=T`): body runs once more → `Mixer:=Mix.X=FALSE`, `mixT(IN:=Mix.X=FALSE)` **resets** (`ET=0, Q=F`); `_act_Stir_prev:=FALSE`. `DrainValve:=Drain.X=TRUE`. `P1 CountBatch` fires (`Drain.X ∧ ¬_drain_p1(F)`) → `BatchCount:=1`; `_drain_p1:=TRUE`. |
| 8 | `Level=60` | none | `Drain` | Draining. `CountBatch` does **not** re-run (`Drain.X ∧ ¬_drain_p1(T)` = FALSE — pulse fired once). Stir wrapper idle (`Mix.X=F, _act_Stir_prev=F`); `Mixer` stays FALSE, `mixT` stays reset. |
| 9 | `Level=4` (≤5) | `t_empty` (`Drain.X ∧ Level≤5`) | `Idle` | Token Drain→Idle. `DrainValve:=Drain.X=FALSE`. `R AbortLamp` acts once, on Idle's activation (`Idle.X ∧ ¬Idle_prev` → `AbortLamp:=FALSE`). `_drain_p1` recomputed to FALSE (Drain now inactive) — re-armed for the next batch's pulse. Cycle complete; `BatchCount=1`, `mixT` clean for re-entry. |

Every output claimed above now follows from the §3.1 compilation: `Heater`/`FillValve`/`DrainValve`/`RunLamp` are `N` boolean actions (OR of step activity, so they drop to FALSE automatically), while `Mixer` and `mixT` are driven by the `Stir` body whose final-scan execution (scan 7) drives them FALSE on the falling edge — closing the re-entry hazard of §2.5.1.

**Alternative-divergence trace** (replaying from scan 3 with an abort): if at scan 4 the inputs were `Level=100, Abort=T`, then `enabled(t_abort) = Fill.X ∧ Abort = TRUE`, so `fire(t_abort) = TRUE` and `fire(t_full) = enabled(t_full) ∧ NOT enabled(t_abort) = TRUE ∧ NOT(TRUE) = FALSE` — the enabled-based alt-guard suppresses the lower-priority transition. Only `t_abort` fires → token Fill→**Idle**, not Fill→Heat/Mix. Exactly one path is taken even though `Level≥100` also held — demonstrating source-order priority (`t_abort` declared before `t_full`).

**Set-dominates-clear trace** (illustrative self-loop, not in the example chart): a transition `FROM S TO S` firing emits `S.X:=FALSE` in step 3 then `S.X:=TRUE` in step 4, leaving `S` active — the token stays, per §2.4.

---

## 10. Summary of recommendations

1. **Format:** new `.sfc` extension, ST POU with an `SFC…END_SFC` body using standard `STEP`/`TRANSITION`/`ACTION` keywords; `FROM/TO` lists for simultaneous branches; alternative divergence = shared-source transitions in priority order.
2. **Semantics:** atomic per-scan evolution from a pre-scan snapshot; source-order alternative priority guarded on the higher-priority sharers' `fire` (never raw `cond`, so a higher-priority convergence can't deadlock the group), alt-groups defined by shared source; simultaneous split/merge via source/target sets; set-dominates-clear; slice-1 qualifiers `N/S/R/P1` (P/P0 if cheap) with the body-action final-scan rule (§2.5.1) so outputs shut down on a step's falling edge; `Step.T` via a reused `TON`; cold start via `INITIAL_STEP := TRUE`; warm restart preserves the token through frame migration.
3. **Compilation:** transpile SFC→ST and reuse `st.Parse`/`st.Lower`; step state is retained VAR slots — **no VM or runtime-core change**.
4. **Render/edit:** derived render model (structure canonical in text, LD-style) + optional reused FBD `@layout` pins; slice-1 ops for step/transition/action/branch edits; the step+its-outgoing-transition is the LD-rung-analog minimal unit.
5. **LSP/CLI/validation:** `analyzeSFC` transpile-and-remap like `analyzeFBD`; conditions/actions are ST so hover/completion/diagnostics come free; SFC-structural checks in `check.go`; `naut sfc graph|edit` verbs mirroring `naut fbd`.
6. **Staging:** Slice 0 wiring (mechanical, serial) → A parser/AST (standard) → **B transpile/semantics (subtle, highest risk) ∥ C graph/edit+CLI (standard) ∥ E LSP (standard)** → D VS Code editor (frontend).
7. **Non-goals:** macro/nested SFC, timed qualifiers, numeric priorities, IL bodies, full action-control edge cases, machine-checked token conservation, vendor import (future moat).
</content>
</invoke>
