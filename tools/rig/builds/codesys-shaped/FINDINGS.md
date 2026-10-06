# codesys-shaped — dogfood log

Found while building the washer (PLAN.md) from `naut new --template minimal`
in the rig: `RIG_NAME=nautilus-build-codesys G_PACE=fast RIG_CLIPS=1`, naut
and vscode-iec 0.13.2 built from origin/main e2e41d2 (`tools/rig/smoke/build.sh`),
VS Code 1.139 at 1600x1000, zoom 2.5. Rows and PNGs/clips named below are in
`tools/rig/out/builds/codesys-shaped/` after a run (gitignored).

Form: **date · did · expected · happened · kind · where**. Kinds: **bug**
(wrong result / lost edit), **papercut** (works, but trips a user or a take),
**gap** (no gesture / no feature for it), **docs** (behaviour nobody would
guess). Every OPEN finding is a GitHub issue labelled `ux:<kind>`,
`editor:sfc|ext` and `parity:codesys`, titled `[codesys-shaped #N]`.

Result of the last run: **77 PASS, 0 FAIL, 15 XFAIL, 0 XPASS**; `naut check`
clean after every beat but the one that probes the action-body gap; `naut
test` 6/6; the gesture-built chart equals the reference modulo layout and
association order (#14).

## The project and its globals

1. **2026-10-05 · a `gvl.st` holding one `VAR_GLOBAL … END_VAR` block (the
   Codesys GVL) beside `program.st`.** Expected: globals declared, or a
   message that library files hold TYPE/FUNCTION/FUNCTION_BLOCK only and
   globals are manifest tags. Happened: `program.st:1:9: undeclared
   identifier "Washer"` — on the program's own name, in a file that did not
   change; `gvl.st` reported clean. The composed prelude begins with a
   top-level VAR block and the program after it parses as a bare body.
   **bug** · `lang/st` library compose · row `habit-gvl-var-global` ·
   [#175](https://github.com/joyautomation/nautilus/issues/175)
2. **2026-10-05 · `VAR_GLOBAL CONSTANT tMaxFill : TIME := T#60S; …` in the
   same file.** Expected: project-wide constants. Happened: `an initial
   value is not applied to a tag — give it an init: in the manifest instead`,
   repeated on `program.st:1:1`. A constant is not a tag; the only home for
   one is `VAR CONSTANT` in each POU (the chart declares its 14 locally).
   **gap** · `lang/st` · row `habit-gvl-var-global-constant` ·
   [#176](https://github.com/joyautomation/nautilus/issues/176)
3. **2026-10-05 · referenced a manifest tag from a POU without a
   VAR_EXTERNAL line.** Expected: resolves (GVL variables are visible
   everywhere), or a quick fix "declare `Setpoint : REAL` as VAR_EXTERNAL".
   Happened: `undeclared identifier "Setpoint" (declare in VAR_* or
   VAR_GLOBAL block)`, no quick fix; the washer repeats 20 tags in the
   chart's header and 6 in sim.st. **papercut** · `lang/st/lower.go`
   (ImplicitGlobals not fed from the manifest), LSP · CLI-verified ·
   [#177](https://github.com/joyautomation/nautilus/issues/177)
4. **2026-10-05 · `TYPE E_WashState : (IDLE := 0, FILL := 1, …); END_TYPE`
   in `lib/types.st`.** Expected: "enumerated types are not supported".
   Happened: `type decl: line 2: expected 79, got ":="` — a token-kind
   number. Same `expected 79` for an SFC header comment containing `*)`.
   **bug** · `lang/st` parser errors · row `habit-enum-type` ·
   [#178](https://github.com/joyautomation/nautilus/issues/178)
5. **2026-10-05 · added the SFC POU as a new empty file.** Expected: a
   "new program" command, or `naut check` ignoring a file no task names.
   Happened: no such command; the Empty-file banner's "initialize" works
   (row `sfc_init`), but before it `naut check` fails: `source must contain
   an SFC ... END_SFC body`. **papercut** · extension commands, check's file
   discovery · CLI-verified ·
   [#179](https://github.com/joyautomation/nautilus/issues/179)

## The chart

6. **2026-10-05 · the chart's "vars" panel: StartPB : BOOL (ext), LevelPct :
   REAL (ext), drum : FB_Reverser (local) — all three PASS; then the
   constant `tMaxFill`, type `TIME := T#60S`.** Expected: a CONSTANT
   section and an initial value (the panel already shows `:= init` on
   existing rows). Happened: ext/local only, a bare type field; a toast
   `sfc edit: "TIME := T#60S" is not a valid type name`, and the name field
   is already cleared, so the attempt is lost. The rest of the header (17
   tags, 14 constants) is pasted. **gap** · `VarsPanel.svelte` `addVar`,
   `lang/sfc/edit.go` `opDeclareVar` · row `habit-vars-constant`, clip 23 ·
   [#180](https://github.com/joyautomation/nautilus/issues/180)
7. **2026-10-05 · main path first, then "+ alt branch" Fill → Aborted (the
   order a Codesys programmer draws in).** Expected: a way to give the
   abort priority. Happened: the branch lands after Fill → (Heat, Wash),
   lowest priority (drawn rightmost — priority does read left to right);
   no gesture reorders branches. Left as gestured, the test "the abort
   branch has priority when Stop and a full drum arrive together" fails.
   The build moves the TRANSITION block by text. **gap** ·
   `opInsertAlternativeBranch`, `SfcView.svelte` · rows
   `habit-abort-priority`, `paste-move-abort-first` ·
   [#181](https://github.com/joyautomation/nautilus/issues/181)
8. **2026-10-05 · Down with Fill selected; the "+ transition" form.**
   Expected: the selection moves; a transition name field. Happened: the
   selection stays; the form is `to step`, `condition`. ex01 finding 11,
   still OPEN. **gap** · `SfcView.svelte` · rows `habit-keyboard-nav`,
   `habit-transition-name` · re-verified on
   [#76](https://github.com/joyautomation/nautilus/issues/76) (comment and
   `parity:codesys` label added)
9. **2026-10-05 · "+ action" `N HeatCtl` on Heat before `ACTION HeatCtl`
   exists, then double-click the row.** Expected: the ST-body editor,
   creating the ACTION. Happened: a dangling association (`naut check`:
   `references neither an ACTION block nor a declared variable`); the
   double-click opens the one-line association field. Double-clicking an
   EXISTING ACTION's row does open the body editor (row
   `sfc_edit_action_body-SpinCtl`, PASS), and the Go op `setActionBody`
   already creates a missing ACTION — only the webview never offers it.
   ex01 04-sfc's action-body gap: still OPEN. All nine ACTION blocks are
   pasted. **gap** · `SfcView.svelte` `ondblclick` · rows
   `habit-create-action-body`, `check-action-gap` ·
   [#182](https://github.com/joyautomation/nautilus/issues/182)
10. **2026-10-05 · the simultaneous convergence `(Wash, HeatDone) → Drain`
    by "+ join".** ex01 finding 49 (no gesture widens a FROM): **FIXED** —
    `sfc_join_step` PASS, the text is exactly the reference's. (How it is
    drawn is #15.)
11. **2026-10-05 · "+ action", typed `D Detergent T#3S` (qualifier, name,
    time — Codesys's column order).** Expected: written, or "the form is
    `D Detergent(T#3S)`". Happened: nothing written, nothing shown;
    `parseAssoc` returns undefined and the edit is dropped. **papercut** ·
    `SfcView.svelte` `parseAssoc` · row `habit-assoc-time-syntax` ·
    [#183](https://github.com/joyautomation/nautilus/issues/183)
12. **2026-10-05 · "+ action" `D Detergent(T#3S)` on Fill, `SD
    AlarmLamp(T#45S)` on Drain, `L SpinMotor(T#10S)` on Spin.** Expected:
    the error to say how to get the behaviour. Happened: each is written;
    `naut check`: `timed qualifier "D" is not implemented; supported
    qualifiers are N, S, R, P, P0, P1`, and the chart puts a red "!" with
    that text on the step (row `chart-shows-timed-qualifier-error`, PASS).
    Clear, but nothing points at the `Step.T` recipe the washer uses (see
    the habits table). Each was retyped in place by double-click
    (`sfc_edit_action`, PASS). **docs** · `lang/sfc` check, `sfc.mdx` "Not
    supported" · rows `habit-D/SD/L-qualifier` ·
    [#184](https://github.com/joyautomation/nautilus/issues/184)
13. **2026-10-05 · the same D association, with `ACTION Dose` assigning
    `Detergent`.** The step marker adds `the association wins on its one
    pulse scan (and writes FALSE the scan after)` — semantics for a
    qualifier that has none here. **papercut** · `lang/sfc` association vs
    ACTION warning ·
    [#185](https://github.com/joyautomation/nautilus/issues/185)
14. **2026-10-05 · 25 associations through "+ action" (the row under each
    step's table), in the reference's order.** Expected: each appended.
    Happened: each inserted FIRST — every step's list is reversed against
    what was typed. The webview posts `addAssoc` with no index; Go's zero
    value means index 0. Tests pass (bodies run in ACTION order); the chart
    and the text read upside down. **bug** · `SfcView.svelte` `addAssoc`,
    `lang/sfc/edit.go` `opAddAssoc` · row `habit-assoc-typed-order`
    (`sfc_compare.py --assoc-order`) ·
    [#186](https://github.com/joyautomation/nautilus/issues/186)
15. **2026-10-05 · the join `(Wash, HeatDone) → Drain` on the chart.**
    Expected: both legs into a double bar above Drain. Happened: Drain
    under Wash, the transition a `↩ Drain: Wash.T >= tWash` glyph titled
    "a loop back", HeatDone with no outgoing edge (it reads as a dead end).
    Drain's BFS rank (3, via Wash) is not below HeatDone's (3), so
    `isForward` is false. **bug** · `webview-ui/src/sfc.ts` · row
    `sfc-join-drawn-as-convergence`, PNG `83-diagram_zoom-fit.png` ·
    [#187](https://github.com/joyautomation/nautilus/issues/187)
16. **2026-10-05 · the IEC 61131-3 textual association `Detergent(D,
    T#3S);`.** Expected: accepted (the docs open with "the IEC standard's
    own textual form"), or an error that shows `D Detergent(T#3S);`.
    Happened: `expected an action or variable name after qualifier
    "Detergent"`. **docs** · `lang/sfc` parser, `sfc.mdx` · CLI-verified ·
    [#189](https://github.com/joyautomation/nautilus/issues/189)

Seen again, not refiled: the zoom controls sit over the leftmost column
(Aborted / Heat, PNGs 36-38 — ex01 SFC 14); the fitted chart (39 %) is
unreadable at this frame (PNG 83 — the FBD analogue is #79).

What worked first time and is worth saying: every chart verb
(`sfc_add_step` chained, `sfc_add_transition` including loop-backs and the
"other… (new step)" path, `sfc_add_parallel_branch`, `sfc_join_step`,
`sfc_add_alt_branch` into an existing step, `sfc_add_transition_condition`,
`sfc_rename_step`), 25 association adds, three in-place association
retypes, the ST-body editor on an existing ACTION, and the vars panel for
plain declarations. `naut check` stayed clean through every one of them.

## Codesys habits with no Nautilus equivalent

| habit | Nautilus | what the user does instead | status |
|---|---|---|---|
| timed qualifiers `L` `D` `SD` `DS` `SL` | rejected, clear error (#12) | `N` + an ACTION on `Step.T`: D → `X := S.X AND S.T >= t;` (the final scan closes it), L → `X := S.X AND S.T < t;`, a step watchdog → see supervision | OPEN gap [#190](https://github.com/joyautomation/nautilus/issues/190) |
| SFCError / step maximum time | none | an ACTION `Supervise` (N on each supervised step) sets `FaultCode` from `Step.T`, an abort transition per supervised step, `P1 RecordFault` on the abort step | OPEN gap [#191](https://github.com/joyautomation/nautilus/issues/191) |
| enumerated types (DUT) | not supported, opaque error (#4) | `VAR CONSTANT ST_IDLE : INT := 0; …` in the POU, an INT tag (`StateNo`), one ACTION associated from every step | documented (structured-text.mdx "Not supported"); the error is #178 |
| GVL, GVL constants | tags in `nautilus.yaml` / `tag-files:`, `VAR_EXTERNAL` per POU; no global constants (#1–#3) | `tags/washer.yaml` with `init:`; `VAR CONSTANT` per POU | #175, #176, #177 |
| numeric transition priority | declaration order, drawn left to right | declare the abort first — and, since "+ alt branch" appends last, move it by text (#7) | declined by design (docs/design/sfc.md §7); the reorder gap is #181 |
| transition condition in LD/FBD | ST expression only | write it in ST; a complex one becomes a BOOL computed in an ACTION or another task | declined (test plan §6.4 "—") |
| keyboard navigation of the chart | none (#8) | the pointer | OPEN #76 |
| online force of a step / transition, set active step | Set Live Value writes tags only; a step is a program-local slot (`main.Fill`), readable by `naut test`, not writable from the IDE or the API | drive the transition's inputs with Set Live Value, or add a commissioning tag to the condition. Not exercised in the rig: read from `server/server.go` and docs/testing.md | OPEN gap [#192](https://github.com/joyautomation/nautilus/issues/192) |
| GVL browsing (online) | the Live Values view and inline pills; the chart's vars panel (header only) | open Live Values, or hover; `tags/*.yaml` has schema completion, no grid | equivalent exists (test plan §6.5 ◐) |
| library manager (versioned references) | `lib/` and root library files compose into every task | copy `lib/*.st` between projects (or a git submodule) | OPEN gap [#193](https://github.com/joyautomation/nautilus/issues/193) |
| Add Object → POU | no "new program" command (#5) | create the file, open as diagram, "initialize" | #179 |

## Issues filed

| # | kind | label | issue |
|---|---|---|---|
| 1 | bug | editor:ext | #175 |
| 2 | gap | editor:ext | #176 |
| 3 | papercut | editor:ext | #177 |
| 4 | bug | editor:ext | #178 |
| 5 | papercut | editor:ext | #179 |
| 6 | gap | editor:sfc | #180 |
| 7 | gap | editor:sfc | #181 |
| 8 | gap | editor:sfc | #76 (existing, re-verified) |
| 9 | gap | editor:sfc | #182 |
| 10 | — | — | FIXED (ex01 #49) |
| 11 | papercut | editor:sfc | #183 |
| 12 | docs | editor:sfc | #184 |
| 13 | papercut | editor:sfc | #185 |
| 14 | bug | editor:sfc | #186 |
| 15 | bug | editor:sfc | #187 |
| 16 | docs | editor:sfc | #189 |
| 17 | gap (timed qualifiers) | editor:sfc | #190 |
| 18 | gap (step supervision) | editor:sfc | #191 |
| 19 | gap (online force) | editor:ext | #192 |
| 20 | gap (library manager) | editor:ext | #193 |

17–20 are the habits-table rows that are open gaps rather than deliberate
declines.

**SFC editor parity (PR #233):** findings 5 (an empty `.sfc` is one
`naut check` warning; no "new POU" command yet), 6, 7, 8, 9, 11, 14 and 15
are fixed; their build rows (`check-empty-sfc`, `habit-vars-constant`,
`sfc-join-drawn-as-convergence`, `habit-abort-priority` — now after
`sfc_reorder_branch`, with the text move gone — `habit-keyboard-nav`,
`habit-transition-name`, `habit-create-action-body`, `check-action-gap`,
`habit-assoc-time-syntax`, `habit-assoc-typed-order`) expect PASS, and
`ACTION HeatCtl` is written by gesture (`sfc_create_action`).
