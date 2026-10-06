# logix-shaped: dogfood findings

The build is `build.sh` (beats in PLAN.md). It ran in the rig as
`RIG_NAME=nautilus-build-logix`, `G_PACE=fast`, 1600×1000 at zoom 2.5,
against origin/main e2e41d2 (vscode-iec 0.13.2 and naut built from the same
commit). The program was built the way a Studio 5000 programmer would build
it, and every edit was read back from disk. Evidence is under
`tools/rig/out/builds/logix-shaped/`: `NN-<row>.png`, `NN-<row>.mp4`,
`clips.html`, `checks/` and `built/`.

The final run had 125 PASS, 0 FAIL, 8 XFAIL, 0 XPASS and 6 PASTE rows
(139 rows in all, counting the 17 checks and the final three verdicts).
`naut check` was clean at all 17 beat boundaries, `naut test` passed 6/6,
and the gesture-built files match `reference/` modulo layout.

Each entry follows the ex01 dogfood-log form, **date · what I did ·
expected · happened · kind · where**, with kind one of **bug** (wrong
result or lost edit), **papercut** (works, but trips a user or a take),
**gap** (no gesture for it) or **docs** (behaviour nobody would guess).
Every OPEN entry links its issue, labelled `ux:<kind>`, `editor:ld|ext` and
`parity:logix`.

A rung is red while it is being built. "+ rung" seeds a `( _ )` coil and
the palette inserts `_` placeholders, so the rung carries a "1 problem"
badge until the last placeholder is retagged. Studio 5000's verify flags an
unfinished rung the same way, so this is not logged. The checks run at
rung boundaries.

## Ladder editor

1. **2026-10-05 · pasted `+M1_StartPB` (the ONS) into rung m1, ahead of
   `m1:MotorStarter(…)`.** Expected: an edge contact in front of the block.
   Happened: the diagram draws the block alone on the rail (see
   `82-ld_assert_rung-m2perm.png` and `135-diagram_zoom-fit.png`), so the
   motor's start looks unconditional. `naut ld graph` does send the element
   (`{"kind": "edge", "ref": "M1_StartPB", "mode": "P"}`), but
   `ladderLayout.ts` lays out only contact, fn, fb and branch, so the edge
   element is silently dropped. The program runs correctly; the picture is
   wrong. The same thing happens to `examples/lift-station/lib/motor.ld`'s
   `+FailToRun`. The XFAIL row is `69-lx_edge_drawn-M1_StartPB`. **bug (high: the diagram
   misstates the logic)** · `webview-ui/src/ladderLayout.ts`,
   `ladder.ts` (the element `kind` union has no `'edge'`). OPEN, #212.
2. **2026-10-05 · wanted the ONS: double-clicked contact M1_StartPB and
   typed `+M1_StartPB`.** Expected: a rising-edge contact (`+Tag` is in the
   grammar and in ladder.mdx's element table). Happened: a toast, `ld edit:
   "+M1_StartPB" is not a valid reference`
   (`55-lx_edge_retag-M1_StartPB.png`). The palette has no P/N contact
   either, although `ld edit` already accepts `insert` with `kind: "edge"`
   (mode P/N). Both edges were pasted. The template's own rung-grammar
   comment does not mention `+x` either. **gap** · `LadderView.svelte`
   `PALETTE`, `lang/ld/edit.go` `refValid` for setRef. OPEN, #213.
3. **2026-10-05 · started the MotorStarter "AOI".** Expected a gesture that
   creates a function block and its parameters, the way Studio 5000's New
   Add-On Instruction dialog and Parameters tab do. Happened: no gesture
   makes a `FUNCTION_BLOCK`. A blank `.ld` seeds a `PROGRAM`, and #179
   already covers POU creation. Inside an FB, nothing declares a pin: the
   variables panel's section badge toggles only `ext` (VAR_EXTERNAL) and
   `local` (VAR), and the amber declare offer skips FB rungs
   (`if (r.pou) continue`). The `FUNCTION_BLOCK`/`VAR_INPUT`/`VAR_OUTPUT`
   header was pasted. After that, all four FB rungs, the TON included,
   were gestured. **gap** · `VarsPanel.svelte` (sections),
   `LadderView.svelte` `undeclared`. OPEN, #214.
4. **2026-10-05 · the CPT/MOV habit: a coil on the REAL `M2_SpeedRef` in
   rung speed.** The retag lands (it is text). `naut check` then fails with
   `program.ld:51:1: cannot assign BOOL to REAL`, and the rung goes red
   (`112-lx_real_coil_checks-M2_SpeedRef.png`). Ladder has no output
   instruction for a non-BOOL write (no MOV, ADD or CPT box, and `MOVE` is
   FBD-only). What the product offers works: an ST `FUNCTION_BLOCK` with
   EN/ENO (`lib/speed.st`), which the FB… picker lists alongside the
   project's own blocks with EN as its power pin, called from the rung with
   `Hz => M2_SpeedRef`. The cost is a library file for every assignment.
   The diagnostic does not point at that route. **gap** · ladder grammar
   (coil zone), `LadderView.svelte` palette. OPEN, #215.
5. **2026-10-05 · looked for M1_StartPB's description (`desc: "M1 start
   pushbutton (momentary)"` in tags/io.yaml) on the contact.** Studio 5000
   draws a tag's description above the instruction. Here, neither the
   element nor its tooltip shows it
   (`56-lx_desc_on_element-M1_StartPB.png`). The only place the desc
   appears is the amber declare offer's row title. **gap** · `LadderView.svelte`
   node `<title>`/operand, the model's `tags` already carries `desc`. FIXED (#216):
   the tooltip ends with the desc and a second line under the operand draws
   it (`nautilus.diagram.showDescriptions`); the row is PASS now.
6. **2026-10-05 · rung m1 finished, wanted m2 = copy of m1 with M1→M2.**
   Selected rung m1 by its name, then Ctrl+C, Ctrl+V. Expected a copy below
   it. Happened: nothing changed (`71-lx_copy_rung-m1.png`). `doCopy`
   refuses a whole-rung selection, but the palette's ⧉ button stays enabled
   while a rung is selected. m2 took the same nine gestures again: rung,
   contact, declare, FB picker with typed args, four declares, coil, and
   another declare. **gap** · `LadderView.svelte` `doCopy`/`doPaste`.
   OPEN, #217.
7. **2026-10-05 · wanted "where else is M1_Run used?" from its contact in
   m2perm.** Studio 5000 offers Cross Reference on any tag (Ctrl+E), with
   every read, write and coil listed. Here, the ladder element has no such
   action and the language server has no `references` provider on main.
   The LSP half is in flight on other branches; the ladder half needs its
   own entry point (the contact's context menu or a key). **gap** ·
   `LadderView.svelte`, `naut lsp`. FIXED (#218): Shift+F12 or right-click →
   Find All References on the element opens the References view (smoke 14,
   X44).
8. **2026-10-05 · declared the main routine's tags.** Every manifest tag a
   rung names needs its own VAR_EXTERNAL line, made by opening the amber
   offer and clicking that tag's row: 29 declarations in this build (28
   `ld_declare` rows, plus M1_Starts through the variables panel; see #9),
   including 5 after each MotorStarter insert. In Studio 5000,
   controller-scoped tags are simply in scope. This is a **papercut**
   already filed from the other builds: #210 (tia-shaped #19) and #177
   (codesys-shaped #3). The logix evidence was added to #210.
9. **2026-10-05 · `CV => M1_Starts` on the CTU, then the amber offer for
   M1_Starts.** Expected `VAR_EXTERNAL : INT`, since the tag is a count with
   `init: 0`. The offer reads `VAR_EXTERNAL : REAL`
   (`101-lx_declare_offer_type-M1_Starts.png`): with no program
   declaring the tag yet, the model types an integer seed as REAL, and the
   INT capture then widens into a REAL without a word. The counter was
   declared `INT` through the variables panel's type field instead
   (`102-lx_vars_declare-M1_Starts`). **papercut** ·
   `naut ld graph` `tags[].type` for an untyped integer seed,
   `LadderView.svelte` declare offer. OPEN, #219.
10. **2026-10-05 · looked for the AOI backing tags (m1, m2, cStarts, spd,
    tFail) in the variables panel.** It lists only header declarations:
    right after the m1 insert it shows "variables 9", all VAR_EXTERNAL, with
    `m1 : MotorStarter` on the rung beside it
    (`63-lx_vars_lists_instance-m1.png`). The FB picker declares an instance
    by its call (`m1:MotorStarter(…)`), not in VAR, so a programmer looking
    in the panel for an instance never finds it. The panel also ignores
    Escape and closes only on a click outside it
    (`64-lx_vars_escape_closes`). **papercut** ·
    `VarsPanel.svelte`, `App.svelte` `varList`. OPEN, #220.

## Project and language

11. **2026-10-05 · wrote the tag database with `type: REAL` /
    `type: INT`, as a Logix tag editor has a Data Type column.** `naut
    check`: `tag M2_SpeedRef: no TYPE REAL is declared by this project's ST
    … declare it in a library .st file`. An elementary type is refused, and
    the message sends you off to declare a TYPE. The workaround is `init:`
    with the program's declaration deciding the type. **papercut**, already
    filed as #200 (tia-shaped #5). The logix evidence (REAL as well as INT)
    was added there.
12. **2026-10-05 · started the project the way the walkthrough does.** The
    extension's *Create Project…* asks for a name and a template (Demo,
    Minimal, SDK, SDK demo) but not a language, so Minimal is always
    `program.st`. Ladder needs the CLI's `naut new --template minimal
    --language ld`, which this build used. For an AB programmer, ladder is
    the first thing to look for. **gap** · `tools/vscode-iec/src/newProject.ts`,
    `newProjectLogic.ts` `newProjectArgs`. OPEN, #221.
13. **2026-10-05 · the alarm word: a Logix programmer packs alarm bits
    into one DINT (`Alarms.3`).** Nautilus has no bit-of-word access:
    `w.3` is a parse error ("expected 1, got "3""), and `w.%X3` gives
    `member access on non-struct type INT` for a `DINT` (also for a `WORD`),
    so the message names the wrong type. The build uses one BOOL tag per bit
    (`Alm_*`). **gap** (with a papercut message) · `lang/st` member access,
    the type name in the diagnostic. OPEN, #222.
14. **2026-10-05 · JMP/LBL and MCR.** ladder.mdx lists "Not supported:
    jumps and labels, MCR zones". Test plan §6.2 marks the row "decide
    (§8)". JSR/SBR/RET maps cleanly onto a FUNCTION_BLOCK (this build's
    MotorStarter), but nothing covers a skipped section or an MCR zone; the
    user has to put the condition in front of every rung, or move the
    section into an FB and gate its call. **gap (a decision)** ·
    `lang/ld`, ladder.mdx. OPEN, #223.
15. **2026-10-05 · translated the Logix instructions by hand: XIC→`Tag`,
    XIO→`/Tag`, OTE/OTL/OTU→`( )`/`( S )`/`( R )`, ONS→`+Tag`, TON
    `.DN`/`.ACC`/`.PRE` (ms)→`.Q`/`.ET`/`PT` (TIME), CTU `.ACC`→`.CV`,
    MOV/CPT→an ST block, JSR→an FB call, AOI→`FUNCTION_BLOCK`, `AOI.Member`
    →`inst.Pin`.** No page has this map. The Logix guide's only mention is
    the GEQ/GE caption note, and ladder.mdx explains JSR → FB but nothing
    else. **docs** · `website/src/content/docs/languages/ladder.mdx` (a
    "Coming from Studio 5000" section). OPEN, #224.

## What worked (no finding)

- **Nested branches by gesture.** `B` around `M1_Run` gives
  `[ M1_Run | Maint ]`. A contact added after `M1_Run` lands inside leg 1,
  and `B` on that contact nests a second OR inside the leg:
  `/EStop [ M1_Run [ M1_Aux | M1_AuxBypass ] | Maint ]`, all gestures. A
  3-leg OR is `B` plus "+ leg".
- **The FB… picker lists the project's own blocks**, both the ladder
  `MotorStarter` from `lib/motor.ld` and the ST `SpeedCalc` from
  `lib/speed.st`, with their power pins (Start→Run, EN→ENO). Typed argument
  lists, `=>` captures included, land as written, and every name they
  introduce gets the amber declare offer, which is ex01's finding 56,
  fixed.
- **`m1.Faulted` as a contact** (the `AOI.Member` habit) retags, checks
  and runs.
- Rung comments by double-click, a drag that reorders (E-stop ahead of
  AirOk), instance rename (`c1` → `cStarts`), and deleting the last coil
  of a rung that calls a block (ex01's finding 53, fixed) all worked.
- **Emulate → `naut test`.** Six acceptance tests in virtual time,
  including a 3 s fail-to-start timer. A mutation (the ONS removed, or the
  seal-in removed) fails the right test.

## Parity: test plan §6.2 (Ladder) against this build

| §6.2 capability | matrix says | this build found | finding |
|---|---|---|---|
| NO/NC contacts, coils, S/R, P/N | ✓ | NO/NC/S/R ✓ by gesture. **P/N edge contacts ✗**: no gesture, and once in the text they are not drawn | #212, #213 |
| Parallel branches, nested | ✓ | ✓ by gesture (B inside a leg) | none |
| Timers, counters, any FB in rung | ✓ | ✓ TON, CTU, a ladder FB and an ST FB, all through FB… | none |
| Compare as contact | ✓ | not exercised | none |
| Output boxes: MOV/ADD/CPT | ✗ | ✗ confirmed (`cannot assign BOOL to REAL`); the route is an ST FB with EN/ENO | #215 |
| JMP/LBL, JSR/RET, MCR | ✗ (decide) | JSR → FB works well; JMP/LBL/MCR ✗ | #223 |
| Rung comments | ✓ | ✓ by gesture | none |
| Tag description on the element | ✗ | ✓ since #216 (tooltip + second line; the probe row is PASS) | #216 |
| Keyboard rung entry | ✓ (the text file) | the `.ld` text is the ASCII rung editor; without a map from Logix mnemonics it is a new grammar to learn | #224 |
| Drag to reorder | ✓ | ✓ | none |
| Copy/cut/paste, multi-select | ✓ (elements) | elements only; **a rung cannot be copied** | #217 |
| Power flow online | ✓ | not exercised (no controller in this build) | none |
| Cross-reference from a contact | ✗ | ✓ since #218 (Shift+F12 / right-click → References view) | #218 |
| Rung-level diff | ✓✓ | not exercised | none |

## More Studio 5000 day-one habits, and what to do instead

| habit | in nautilus | what the user does instead | finding |
|---|---|---|---|
| New Add-On Instruction, Parameters tab | no gesture | paste the `FUNCTION_BLOCK` + `VAR_INPUT`/`VAR_OUTPUT` header; gesture the rungs | #214 (and #179) |
| AOI backing tag in the tag database | the instance is declared by its call | read it in the text (`m1:MotorStarter(…)`) | #220 |
| Controller-scoped tags | VAR_EXTERNAL per POU, one declare per tag | 29 declarations, one gesture each | #210 (#177) |
| Data Type column (DINT, REAL) | `type:` takes UDTs only | `init:` plus the program's declaration | #200 |
| Alarm DINT with aliased bits | no bit-of-word access | one BOOL tag per bit | #222 |
| ONS / OSR | `+Tag` / `( P Tag )` in text only | paste `+` into the rung | #212, #213 |
| Copy rung, paste, Find/Replace M1→M2 | no rung copy | rebuild the rung, or copy it in the text view | #217 |
| CPT / MOV | no output box | ST FB with EN/ENO, called from the rung | #215 |
| Cross Reference (Ctrl+E) | none | Shift+F12 on the element (References view) | #218 |
| Ladder on New Project | `--language ld` on the CLI only | `naut new … --language ld` in a terminal | #221 |
| JMP/LBL, MCR | none | gate each rung, or move the section into an FB | #223 |
| Verify routine | `naut check`, live diagnostics | none needed | ✓ |
| Emulate + toggle bits | `naut test` in virtual time | none needed | ✓✓ |
