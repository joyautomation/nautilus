# tia-shaped — dogfood log

A TIA Portal (S7-1500) programmer's first Nautilus project, built from
`naut new --template minimal` by `build.sh` in the rig (PLAN.md): the ST
typed with the language server live, the FBD gestured. Found against
origin/main at e2e41d2 (naut + VSIX built from this branch), VS Code 1.139
in the rig container, `G_PACE=fast`, 1600x1000 at zoom 2.5. Evidence PNGs
and clips are in `tools/rig/out/builds/tia-shaped/` after a run (gitignored).
Row names below are `build.tsv` rows.

Dogfood-log form (ex01's): **date · did · expected · happened · kind ·
where**. Kinds: **bug** (wrong result / lost edit), **papercut** (works, but
trips a user or a take), **gap** (no gesture or no feature for it), **docs**
(behaviour nobody would guess). Each OPEN finding is a GitHub issue
labelled `ux:<kind>`, `editor:fbd|ext`, `parity:tia`, titled
`[tia-shaped #N] …`.

| # | kind | finding | status |
|---|---|---|---|
| 1 | bug | CASE labels that are named constants silently miscompile | FIXED #196 (PR #242: reference/dosing.st now names its states) |
| 2 | bug | identifiers are case-sensitive; a project FUNCTION is unreachable from FBD | FIXED #197 (PR #241) |
| 3 | bug | SCL `#` prefix: an error on the RHS, silently dropped on a target | FIXED #198 (PR #242: one message everywhere; row 05-diag-hash-prefix-target PASS) |
| 4 | papercut | one library error is reported on every file at 1:1 | FIXED #199 (PR #243: row 05-library-error-once) |
| 5 | papercut | tag `type: INT` refused with "no TYPE INT is declared" | FIXED #200 (PR #243: every tag in the table carries its type; row 03 now PASS) |
| 6 | gap | TIME members of a UDT tag cannot be seeded by `init:` | FIXED #201 (PR #243: the reference's recipe has `SettleTime : TIME`, seeded `T#2S`) |
| 7 | gap | `REGION … END_REGION` unsupported; reported as an undeclared identifier | FIXED #202 (PR #242: reference uses two regions; rows 05-region-outline, 05-region-folds) |
| 8 | gap | `VAR_TEMP` keeps its value between calls | FIXED #203 (PR #242: the `justDone := FALSE;` workaround is gone) |
| 9 | gap | no signature help on `LIMIT(` | FIXED (PR #168); row 04-signature-help-LIMIT PASS |
| 10 | gap | no document symbols (Ctrl+Shift+O, Outline) | OPEN, PR #172 |
| 11 | gap | no Find All References (Shift+F12) | FIXED (PR #171); row 05-find-references PASS |
| 12 | papercut | block → wire's function field never suggests project FUNCTIONs | FIXED #204 (PR #236) |
| 13 | gap | FB picker writes every input as `_`, an error even with a default | FIXED #205 (PR #236) |
| 14 | gap | no EN/ENO on standard blocks | FIXED #206 (PR #236) |
| 15 | gap | no network numbers, titles or execution order | FIXED #207 (PR #236) |
| 16 | bug | wired tag chips keep the ghost's 40,40 pin and stack | OPEN #81 (commented) |
| 17 | papercut | the `(* @layout *)` block lands mid-body | FIXED #208 (PR #236) |
| 18 | papercut | a blank `.fbd` seeds `PROGRAM main` | FIXED #209 (PR #236) |
| 19 | gap | every tag re-declared per program, one palette gesture each | FIXED #210 (PR #243: manifest tags in scope; 15 `06-declare-*` rows became one — MainDtS, the dt-tag the manifest names only at beat 12 — and `06-initialize` writes the skeleton) |
| 20 | gap | no force | FIXED #211 (PR #234: Force… / F badges / status bar; row 13 now PASS) |

20 findings: 4 bugs, 6 papercuts, 10 gaps. 16 new issues; 3 are covered by
open PRs and 1 was already filed (#81).

## The language (found writing reference/, then confirmed in the build)

1. **2026-10-05 · CASE state machine with named states** (`VAR CONSTANT
   S_IDLE : INT := 0; S_RUN : INT := 10;`, `CASE St OF S_IDLE: … S_RUN: …`).
   Expected: the same as literal labels. Happened: `naut check` clean; every
   clause after the first is parsed into the first one's body, so `S_RUN:`'s
   statements run in `S_IDLE` and never again (Cnt 1,1,1,1 where 0,1,2,3 is
   right). No diagnostic. **bug** · `lang/st/parser.go` `looksLikeCaseLabel`
   recognises literal labels only. The reference uses literal labels. #196
2. **2026-10-05 · FBD network 1: `ft = ScaleAnalog(FT101_Raw, 0.0, 120.0)`**
   (row `07-block-ScaleAnalog`, then `07-check-user-FUNCTION-from-FBD` XFAIL).
   Expected: the project FC is called. Happened: `unknown function
   "SCALEANALOG"`. The netlist upper-cases call names
   (`lang/fbd/netlist.go:386`) and user FUNCTIONs are looked up by exact
   name. Underneath, identifiers in general are case-sensitive: `STATE` for
   `state`, `startedge` for `startEdge`, `DOSING` for `Dosing`, and the
   function's own return variable `ScaleAnalog` inside `FUNCTION
   SCALEANALOG` are all rejected. IEC and every vendor IDE are
   case-insensitive. **bug** · workaround, typed in the build
   (`07-workaround-uppercase-FC`): rename the FUNCTION to `SCALEANALOG`. #197
   — **fixed** (PR #241): identifiers are case-insensitive throughout, the
   FBD block keeps the spelling it was given, and the row is PASS; the
   workaround row is gone.
3. **2026-10-05 · the SCL `#` habit** (`05-diag-hash-prefix-rhs` PASS,
   `05-diag-hash-prefix-target` XFAIL). Typed `Step := #state;` → a squiggle,
   `unexpected token "#"`, which is fine. Typed `#state := 10;` → no
   squiggle, `naut check` clean, `naut test` 6/6: the `#` is dropped on an
   assignment target. **bug** (inconsistent, and pasted SCL half-compiles) ·
   `lang/st` statement parsing. #198
4. **2026-10-05 · one typo, `Recipe.TargetLL`** (`05-diag-typo` PASS in the
   editor). The editor flags it on the right column (F8: `field "TargetLL"
   not found on DoseRecipe`). `naut check` then reports it again on
   `main.fbd`, `scale.st` and `types.st` at 1:1, "in project library files:
   …", so it says 4 files have errors. **papercut** · library composition in
   `naut check`/`naut lsp`. #199
5. **2026-10-05 · the tag table's Data type column** (`03-tag-elementary-type`
   XFAIL). Wrote `type: INT` on `FT101_Raw`. Expected an INT tag. Happened:
   `no TYPE INT is declared by this project's ST … declare it in a library
   .st file`, which asks for something impossible. `type:` only takes a UDT.
   **papercut** (+docs) · manifest tag loading. #200
6. **2026-10-05 · the recipe DB's start values** (`SettleTime : TIME` in
   `DoseRecipe`, `init: { SettleTime: 2000 }` and `'T#2S'`). Happened: `TIME
   members cannot be seeded by init:`. **gap** · `lang/ir/seed.go`. The
   reference carries `SettleSec : REAL` and converts with
   `REAL_TO_TIME(Recipe.SettleSec * 1000.0)`. #201
7. **2026-10-05 · `REGION init … END_REGION`** (`05-diag-region` PASS:
   diagnosed). The message is `undeclared identifier "REGION" (declare in
   VAR_* …)`, which reads as if REGION were a variable to declare.
   **gap** · ST parser. #202
8. **2026-10-05 · VAR_TEMP.** A temp counter `n := n + 1` gives 1,2,3,4,5
   over five calls; TIA Temp is per-call scratch. Documented under ST "Not
   supported", but nothing warns when a ported block depends on it. **gap** ·
   `lang/st`. The build's `Dosing` writes its temp (`justDone := FALSE;`)
   before any read, so it behaves the same either way. #203

## The ST editor (beats 04, 05)

9. **2026-10-05 · `ScaleAnalog := LIMIT(`, Ctrl+Shift+Space**
   (`04-signature-help-LIMIT` XFAIL). Expected TIA/Codesys-style parameter
   hints (`LIMIT(MN, IN, MX)`). Happened: no widget. **gap** · `naut lsp`.
   Open PR #168 adds signature help, and the row turns XPASS once it is
   merged. Completion itself works: Ctrl+Space after `LIM` (on a buffer that
   does not parse yet) listed `LIMIT`, `LTIME` (`04-type-ScaleAnalog`), and
   the hover on a pin shows `EngHi : REAL / VAR_INPUT — ScaleAnalog`
   (`04-hover-pin`).
10. **2026-10-05 · Ctrl+Shift+O in `dosing.st`** (`05-outline-symbols` XFAIL).
    Expected the block interface (Dosing, its VAR sections, state, settle…),
    which is how a TIA programmer moves around a block. Happened: "The active
    text editor does not provide symbol information." **gap** · `naut lsp`.
    Open PR #172.
11. **2026-10-05 · Shift+F12 on `state`** (`05-find-references` XFAIL).
    Expected TIA's cross-reference: every read and write of `state`.
    Happened: nothing (no peek, no message). **gap** · `naut lsp`. Open PR
    #171.

## The FBD editor (beats 06-11)

12. **2026-10-05 · + add → block → wire, typed `Sca` in the function field**
    (`07-palette-lists-user-FUNCTION` XFAIL). Expected `ScaleAnalog` offered,
    as the FB picker offers the project's `Dosing`. Happened: no list at all;
    `suggest.ts` `FUNCTIONS` is a fixed list of standard functions.
    **papercut** · `webview-ui/src/suggest.ts`. #204
13. **2026-10-05 · + add → function block → `Dosing` as `doseA`**
    (`08-check-with-open-pins` XFAIL, `08-disconnect-NoFlowTime` PASS). The
    picker writes all six inputs as `_`, and `naut check` stays red until
    each is wired (`unfilled placeholder _`). That includes `NoFlowTime`,
    which has a default (`T#5S`) and which a TIA programmer would leave
    unconnected. Removing it works (select its wire, Delete: the disconnect
    drops the named argument), but nothing on screen suggests it. **gap** ·
    `Palette.svelte` `openArgs` / the `_` rule. #205
14. **2026-10-05 · EN on `LIMIT`** (`09-EN-ENO-on-LIMIT`,
    `09-EN-input-by-text` XFAIL). The block draws `MN IN MX OUT`, and `LIMIT(EN
    := …, MN := …, …)` is refused (`unexpected ":=" in expression`). What the
    user does instead: a `SEL` in front, which is network 3's `spOut =
    SEL(doseA.ValveOpen, 0.0, spA)`. **gap** (documented under FBD "Not
    supported"). #206
15. **2026-10-05 · five commented "networks"** (`11-network-numbers-or-exec-order`
    XFAIL). There is no network construct; `// Network N: …` notes are the
    titles. Auto-layout places notes on their own, away from their
    statements (in the diff view, the notes for networks 4 and 5 sit under
    every block), and no block shows a number or its place in the execution
    order. **gap** · netlist format, `FbdNode.svelte`. #207
16. **2026-10-05 · six tag chips from + add → input reference (bare), each
    wired to a pin** (`08-wire-*`, `11-wire-ResetCount-to-R` PASS on the text).
    Expected each chip next to its pin once wired. Happened: every one keeps
    the ghost's `40,40` pin in `(* @layout *)`, as `v:StartA 40,40` etc., so
    all six are drawn stacked in the top-left corner with six wires fanning
    out of one spot (PNG `50-08-disconnect-NoFlowTime`). The text is right,
    but the drawing can't tell you which tag feeds which pin. The verb works
    only because the newest ghost is drawn on top. **bug** · already #81
    (commented with this run).
17. **2026-10-05 · the built `main.fbd`.** The `(* @layout … *)` block sits
    between `doseA : Dosing(...)` and the `XV101_Open` coil, where the body
    ended at the first layout write. Every statement added later went after
    it. **papercut** · `lang/fbd/layout.go`. #208
18. **2026-10-05 · the first palette gesture on a blank `main.fbd`**
    (`06-header-matches`). It seeded `PROGRAM main`; `naut new` makes
    `PROGRAM Mintest`, and TIA's OB1 is `Main`. **papercut** ·
    `lang/internal/seed` `PouName`. The reference now says `PROGRAM main`. #209
19. **2026-10-05 · the program's header, from the palette** (`06-declare-*`,
    15 rows, all PASS). Each tag the program touches costs one pass through
    + add → variable (external tag) → name → type → insert, retyping a type
    that the tag table and the libraries already settle. In TIA a PLC tag is
    usable in any block with no declaration. **gap** · palette /
    VAR_EXTERNAL model. #210

## Project level (beat 13)

20. **2026-10-05 · the command palette, `nautilus: force`**
    (`13-force-command` XFAIL). No force command. Set Live Value writes once,
    and the driver rewrites an `input` tag on the next scan. **gap** ·
    extension + controller API. #211

These worked the way a TIA programmer would hope:

- The FB picker lists the project's `Dosing` with its named pins, including
  a VAR_IN_OUT `Recipe` pin wired to a UDT tag (`08-fb-picker-Dosing`,
  `08-wire-RecipeA-to-Recipe`).
- A VAR_IN_OUT UDT round-trips through the instance: `RecipeA.DoneCount`
  counts in the tests.
- An output coil from an FB pin is one gesture (`fbd_add_coil`).
- `fbd_wire` from an FB output pin into a CTU works
  (`11-wire-doseA.Done-to-CU`).
- `TON` and `CTU` come from the picker with typed args.
- **Diff FBD Diagram (vs git HEAD)** after a constant edit (`13-diff-vs-HEAD`)
  is TIA's compare editor, and it works between any two git revisions too.
- `naut check` was clean after every beat except where a row says otherwise.

## TIA habits with no direct Nautilus equivalent: what the user does instead

| TIA habit | Nautilus | what the user does instead |
|---|---|---|
| **Instance DB** (`"Dosing_DB"`, a DB per call, visible and monitorable on its own) | the instance is a variable of the calling program, `doseA : Dosing(...)` in `main.fbd`; no separate DB object | name the instance after the DB. Its state lives in the program and survives a warm swap by name; monitor it through the live values on the FB's pins. Multi-instances (an FB inside an FB) work the same way (`noFlow : TON` in `Dosing`) |
| **Global DB of a UDT** (recipe DB) | a tag of a `TYPE … STRUCT` in the tag table, `RecipeA : DoseRecipe` | as here; a TIME member takes its start value as `T#2S` (#201) |
| **VAR_TEMP** = per-call scratch | retained between calls (#203) | write every temp before reading it |
| **`#local`, `"Global"` quoting, REGION** | none of them (#198, #202) | plain names; `(* *)` comments for structure |
| **CASE on named state constants** | silently wrong today (#196) | literal labels plus a comment per state |
| **EN/ENO on every box** | none on standard blocks (#206) | `SEL(cond, hold, new)` in front of the block, or the logic in ST with `IF` |
| **Network numbers and titles** | one netlist per body; `//` notes (#207) | `// Network N: title` notes; statement order is the execution order |
| **Cross-reference** | none yet (#171 in review) | text search; Go to Definition (F12) works |
| **Block interface / Outline navigation** | none yet (#172 in review) | Ctrl+G to a line, F12 to a declaration; the FBD **vars** panel lists a program's declarations |
| **Compare blocks** (online/offline compare editor) | **Diff FBD/Ladder/SFC Diagram** vs git HEAD, vs the controller, or between two revisions; text diffs | git holds the project's history; the overlays colour added, removed and changed blocks |
| **Force table** | none (#211) | Set Live Value on setpoint/state tags; for a test, `given:` with `suspend:` in `*_test.yaml` |
| **Tag table usable from any block** | every program sees the tag table as it is (#210); a function block names what it uses in its own `VAR_EXTERNAL` | nothing |
| **Data type column in the tag table** | `type:` takes any elementary type, a UDT, or an ARRAY (#200) | nothing |

## Rig notes (not product findings)

- **R1 · 2026-10-05 · beat 12, End+Return after `scan: 100ms` in
  `nautilus.yaml`, then typed `    dt-tag: MainDtS`.** The new line came
  out at 8 spaces (a YAML error). VS Code's built-in `[yaml]` defaults keep
  the line's indentation on Return whatever `editor.autoIndent` says, so the
  rig's "autoIndent none, type the indentation" convention from ex01's ST
  beats does not hold in YAML. build.sh now types the new line at column 1
  of the line below, and never ends a typed YAML block with Return.
- **R2 · 2026-10-05 · several build agents on one host share the session
  scratchpad.** A sibling's `build.sh` env file overwrote this build's
  `NAUT=`/`VSIX=` lines between writing and reading them, so the first run
  went out against another worktree's artifacts. Keep per-build files in a
  per-build directory, or pass `NAUT`/`VSIX` straight from the worktree's
  `tools/rig/out/build/`.
- **R3 · `fbd_add_block` routes any CamelCase name to the FB picker**, so a
  project FUNCTION cannot go through it; `fbd_add_function` (new) forces
  block → wire. `_fbd_assert_pin` assumed `IN<n>` is argument n, which is
  wrong for SEL (`G, IN0, IN1`) and LIMIT (`MN, IN, MX`); it now reads the
  block's pin list from `naut fbd graph`.
- **R4 · a `while read …; do row …; done < <(…)` loop fed the beat's own
  stdin to the verbs** (cdp's node, python), which ate the first character
  of every name after the first (`T101_Flow`, `tartA`). The loop reads fd 3.
