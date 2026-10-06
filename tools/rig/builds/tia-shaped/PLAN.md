# tia-shaped — a TIA Portal (S7-1500) programmer's first Nautilus project

Test plan §5.2's "TIA-shaped program": small (one task, a few files), built
from `naut new --template minimal` in a real VS Code by the rig, the way a
Siemens programmer would lay it out. The point is the gesture path and the
dogfood log (FINDINGS.md), not the plant.

What a TIA programmer reaches for, and where it lands here:

| TIA habit | in this build |
|---|---|
| SCL FB with Input/Output/InOut/Static/Temp | `dosing.st` `FUNCTION_BLOCK Dosing` (VAR_INPUT/VAR_OUTPUT/VAR_IN_OUT/VAR/VAR_TEMP) |
| FC with a return value (FC105 SCALE) | `scale.st` `FUNCTION SCALEANALOG : REAL` (typed as `ScaleAnalog`, see FINDINGS) |
| PLC data type + global DB of it | `types.st` `TYPE DoseRecipe : STRUCT`, tag `RecipeA` of that type |
| instance DB | the FBD instance `doseA : Dosing(...)` in `main.fbd` (instance data lives in the program) |
| OB1 with FBD networks | `main.fbd` `PROGRAM Main`, five numbered, titled networks (`NETWORK 'title'`) |
| TON/TOF, CASE state machine | inside `Dosing` (`noFlow`, `settle` TON; `doneHold` TOF; `CASE state OF`) |
| REAL math with LIMIT/SEL | network 3: `LIMIT` then `SEL` |
| PLC tag table | `tags/plc_tags.yaml` |
| acceptance tests | `tia-dosing_test.yaml` (6 tests) |

The finished project is `reference/` (`naut check` clean, `naut test` 6/6).
`build.sh` builds it again from the template and compares.

## Running it

```sh
export PATH=$HOME/.nvm/versions/node/v24.18.0/bin:/usr/local/go/bin:$PATH
eval "$(tools/rig/smoke/build.sh)"
RIG_NAME=nautilus-build-tia G_PACE=fast RIG_CLIPS=1 tools/rig/builds/tia-shaped/build.sh
```

Results land in `tools/rig/out/builds/tia-shaped/`: `build.tsv` (n, row,
PASS/FAIL/XFAIL/XPASS, detail), one PNG (and clip) per row, `built/` (the
project as the build left it), `check-NN.txt` (`naut check` after every
beat), and `compare.txt` (built vs reference). XFAIL rows probe something a
TIA programmer expects that Nautilus does not do (yet); an XPASS means it
started working.

## Beats

How each part is authored: **gestured** (diagram editor verbs),
**typed** (xdotool keystrokes into a VS Code text editor, with the language
server live), **pasted** (file copied in: nothing a person types or drags
would teach us anything new there).

| # | beat | what | how | checks |
|---|---|---|---|---|
| 01 | scaffold | `naut new tia-dosing --template minimal`, committed | CLI | `naut check` clean |
| 02 | types | `types.st`: `TYPE DoseRecipe : STRUCT … END_STRUCT; END_TYPE` | typed | file == reference; check clean |
| 03 | tag table | `tags/plc_tags.yaml` pasted (TIA users import a tag table); the `tag-files:` entry typed into `nautilus.yaml`; the Data-type-column habit (`type: INT`) probed | pasted + typed | check clean (warnings: tags no program binds yet); `type: INT` XFAIL |
| 04 | FC | `scale.st`: `FUNCTION ScaleAnalog : REAL`, VAR_INPUT, VAR_TEMP, body; completion on `LIMIT(`, signature help probe, hover a pin | typed | completion lists LIMIT; signature help (XFAIL until #168); hover shows the type; check clean |
| 05 | FB | `dosing.st`: the whole `Dosing` FB typed; then four edits typed over it, each read back (squiggle, status-bar count, F8's problem text) and undone: the SCL habit `#state` on the right-hand side and on an assignment target, a typo (`Recipe.TargetLL`), and `REGION … END_REGION` | typed | diagnostics appear and clear (the `#` on a target is not diagnosed: XFAIL); Outline (Ctrl+Shift+O) XFAIL; Find All References (Shift+F12) XFAIL; check clean |
| 06 | main.fbd header | blank `main.fbd`, opened as the FBD diagram; every `VAR_EXTERNAL` declared from the palette's "variable (external tag)" (the first one seeds `PROGRAM Main`, #209) | gestured (`fbd_declare` ×15) | header == reference; check clean |
| 07 | network 1 | `NETWORK` with its title; `ft = ScaleAnalog(FT101_Raw, 0.0, 120.0)`; coil `FT101_Flow := ft` | gestured (`fbd_add_network`, `fbd_add_block`, `fbd_add_coil`) | user FUNCTION in the palette suggestions (#204); `naut check` on the user FUNCTION call (XFAIL: FBD upper-cases call names); workaround typed in `scale.st` (rename to `SCALEANALOG`), check clean |
| 08 | network 2 | `NETWORK`; `doseA : Dosing(…)` from the FB picker (`NoFlowTime`, which has a default, arrives unbound — #205); five pins wired from tag chips; four output coils | gestured (`fbd_add_network`, `fbd_add_block Dosing`, `fbd_add_tag_ref` ×5, `fbd_add_coil` ×4) | check red while the required pins are open (and only on those), clean once wired |
| 09 | network 3 | `NETWORK`; `spA = LIMIT(0.0, RecipeA.FlowSP, MaxFlowLpm)`; `spOut = SEL(doseA.ValveOpen, 0.0, _)`; wire `spA → spOut.IN3`; coil | gestured (`fbd_add_network`, `fbd_add_block` ×2, `fbd_wire`, `fbd_eno_pins`, `fbd_add_coil`) | EN/ENO pins on LIMIT after the pin gesture, and `EN :=` by text (#206); check clean |
| 10 | network 4 | `NETWORK`; `hiFlow = GT(…)`; `tHi : TON` from the picker; wire `hiFlow → tHi.IN`; coil | gestured | check clean |
| 11 | network 5 | `NETWORK`; `cDoses : CTU`; wire `doseA.Done → cDoses.CU`; tag chip on `R`; coil `DosesToday := cDoses.CV`; `fbd_move_node` one block | gestured | network numbers and execution order drawn (#207); check clean |
| 12 | go live | `program: main.fbd` and `dt-tag: MainDtS` typed into `nautilus.yaml`, the template's example tags selected and deleted, the template's `program.st` and test removed, `tia-dosing_test.yaml` pasted | typed + pasted | `naut check` clean; `naut test` 6/6 |
| 13 | compare | built files vs `reference/` modulo indentation, blank lines and `@layout` (`nautilus.yaml` from `server:` on, comments dropped: the template's header stays); the TIA "compare blocks" habit: commit, retype `T#2S` → `T#3S` on the diagram, `Diff FBD Diagram (vs git HEAD)`, put it back; the "force" habit: is there a force command | gestured + palette probes | compare ==; the diff tab opens; force XFAIL |

## Last run

2026-10-05, origin/main e2e41d2 + this branch, `G_PACE=fast`, clips on:
**84 rows: 72 PASS, 0 FAIL, 12 XFAIL, 0 XPASS, no FALLBACK.** Every built
file equals `reference/` modulo layout; `naut check` clean and `naut test`
6/6 at the end. 20 findings (FINDINGS.md): 4 bugs, 6 papercuts, 10 gaps.

The 12 XFAIL rows, and what turns each one XPASS:

| row | finding |
|---|---|
| 03-tag-elementary-type | #200 |
| 04-signature-help-LIMIT | PR #168 |
| 05-diag-hash-prefix-target | #198 |
| 05-outline-symbols | PR #172 |
| 05-find-references | PR #171 |
| 07-check-user-FUNCTION-from-FBD | #197 |
| 13-force-command | #211 |

PASS since PR #236 (the FBD parity batch): `07-palette-lists-user-FUNCTION`
(#204), `09-EN-ENO-on-LIMIT` and `09-EN-input-by-text` (#206),
`11-network-numbers-or-exec-order` (#207). `08-check-with-open-pins` became
`08-defaulted-input-unbound` (#205: `NoFlowTime`, which has a default, now
arrives unbound, so the `08-disconnect-NoFlowTime` workaround row is gone; an
open `_` on a pin with no default is still an error, by design). The five
`// Network N:` comments became `NETWORK 'title'` lines, added with the
palette's "network" (`fbd_add_network`), and `reference/main.fbd` seeds
`PROGRAM Main` (#209).

When #197 is fixed, `07-check-user-FUNCTION-from-FBD` turns XPASS and
`07-workaround-uppercase-FC` (the rename to `SCALEANALOG`) can go, along
with the capitals in `reference/scale.st`.
