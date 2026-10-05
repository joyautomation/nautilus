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
| OB1 with FBD networks | `main.fbd` `PROGRAM Main`, five commented "networks" |
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
| 03 | tag table | `tags/plc_tags.yaml` pasted (TIA users import a tag table); the `tag-files:` entry typed into `nautilus.yaml` | pasted + typed | check clean (warnings: tags no program binds yet) |
| 04 | FC | `scale.st`: `FUNCTION ScaleAnalog : REAL`, VAR_INPUT, VAR_TEMP, body; completion on `LIMIT(`, signature help probe, hover a pin | typed | completion lists LIMIT; signature help (XFAIL until #168); hover shows the type; check clean |
| 05 | FB | `dosing.st`: the whole `Dosing` FB typed, including the TIA habit `#state` (diagnostic, then fixed) and a deliberate typo (`Recipe.TargetLL`, diagnostic, then fixed) | typed | squiggle + status-bar error appear and clear; Outline (document symbols) probe XFAIL; Find All References probe XFAIL; check clean |
| 06 | main.fbd header | blank `main.fbd`, opened as the FBD diagram; every `VAR_EXTERNAL` declared from the palette's "variable (external tag)" | gestured (`fbd_declare` ×15) | header == reference; check clean |
| 07 | network 1 | comment; `ft = ScaleAnalog(FT101_Raw, 0.0, 120.0)`; coil `FT101_Flow := ft` | gestured (`fbd_add_comment`, `fbd_add_block`, `fbd_add_coil`) | user FUNCTION in the palette suggestions (XFAIL); `naut check` on the user FUNCTION call (XFAIL: FBD upper-cases call names); workaround typed in `scale.st` (rename to `SCALEANALOG`), check clean |
| 08 | network 2 | comment; `doseA : Dosing(…)` from the FB picker; five pins wired from tag chips; the `NoFlowTime := _` open pin removed; four output coils | gestured (`fbd_add_block Dosing`, `fbd_add_tag_ref` ×5, `fbd_add_coil` ×4); the open pin by text (see FINDINGS) | check red while pins are open, clean once wired |
| 09 | network 3 | comment; `spA = LIMIT(0.0, RecipeA.FlowSP, MaxFlowLpm)`; `spOut = SEL(doseA.ValveOpen, 0.0, _)`; wire `spA → spOut.IN3`; coil | gestured (`fbd_add_block` ×2, `fbd_wire`, `fbd_add_coil`) | EN/ENO on a standard block probe (XFAIL); check clean |
| 10 | network 4 | comment; `hiFlow = GT(…)`; `tHi : TON` from the picker; wire `hiFlow → tHi.IN`; coil | gestured | check clean |
| 11 | network 5 | comment; `cDoses : CTU`; wire `doseA.Done → cDoses.CU`; tag chip on `R`; coil `DosesToday := cDoses.CV`; `fbd_move_node` one block | gestured | network-number / execution-order display probe (XFAIL); check clean |
| 12 | go live | the task switched to `main.fbd` (typed in `nautilus.yaml`), the template's `program.st` and test removed, `tia-dosing_test.yaml` pasted | typed + pasted | `naut check` clean; `naut test` 6/6 |
| 13 | compare | built files vs `reference/` modulo whitespace and `@layout`; the TIA "compare blocks" habit: `Diff FBD Diagram (vs git HEAD)` after one more edit; the "force" habit: is there a Force command | gestured + palette probes | compare == ; diff overlay opens; force XFAIL |
