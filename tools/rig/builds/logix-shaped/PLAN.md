# logix-shaped: a Studio 5000-shaped program, gesture-built

Test plan §5.2's third build. The question is what an Allen-Bradley Logix
programmer reaches for on day one, and how far the ladder editor gets them
with gestures alone. The plant is deliberately small: two conveyors, one
task, five source files. What the build exercises is the gesture path.

- `reference/` is the finished project, written as text. `naut check` is
  clean on it and `naut test` passes 6/6.
- `build.sh` gesture-builds the same project in the rig, starting from
  `naut new conveyor --template minimal --language ld`. It reads every edit
  back from disk and runs `naut check` at each beat boundary. It finishes
  with `naut test` and with `compare.py`, which compares the built project
  against `reference/` modulo layout.
- `FINDINGS.md` is the dogfood log, followed by the Studio 5000 habits that
  have no Nautilus equivalent.

```sh
eval "$(tools/rig/smoke/build.sh)"
RIG_NAME=nautilus-build-logix G_PACE=fast RIG_CLIPS=1 tools/rig/builds/logix-shaped/build.sh
# → tools/rig/out/builds/logix-shaped/{build.tsv, NN-*.png, NN-*.mp4, clips.html, checks/, built/}
```

## The program

| Studio 5000 habit | here | file |
|---|---|---|
| A `MotorStarter` AOI: Start, Stop (NC), Permit, Aux feedback, Fault, Reset → Run, FailToStart, Faulted | a user `FUNCTION_BLOCK` written in ladder: a seal-in rung, a fail-to-start `TON` on the aux, S/R fault latches | `lib/motor.ld` |
| MainRoutine calls the AOI (or JSRs a subroutine) once per motor | one rung per motor, `+Mx_StartPB mx:MotorStarter(…) ( Mx_Run )` | `program.ld` |
| `ONS` on the start pushbutton | the rising-edge contact `+M1_StartPB` | `program.ld` m1, m2 |
| `CTU` counting starts | `M1_Run cStarts:CTU(R := CountReset, PV := 9999, CV => M1_Starts)` | `program.ld` starts |
| Interlock rungs: XIO, and a nested branch | `/EStop AirOk`, `/EStop [ M1_Run [ M1_Aux \| M1_AuxBypass ] \| Maint ]` | m1perm, m2perm |
| An alarm DINT whose bits are aliased | one BOOL tag per bit (`Alm_*`) and an `Alm_Any` summary | almestop … almany |
| `AOI.Member` used as a contact | `m1.Faulted` | almm1, almm2 |
| `CPT`/`MOV` writing a REAL setpoint | not possible in ladder (coils are BOOL), so an ST block with EN/ENO is called from the rung and `Hz => M2_SpeedRef` captures the result | `lib/speed.st`, rung speed |
| Controller tags with descriptions | `tags/io.yaml`, `tags/plant.yaml`, each tag with `desc:` | `tags/` |
| Proving it in Emulate | 6 acceptance tests in virtual time | `conveyor_test.yaml` |

## Beats: gestured vs pasted

The rule is ex01's: a beat gestures everything the editor can author and
pastes only what no gesture reaches (each paste is a finding too; #179 for the FB itself — the edges (#213) and the FB's pins (#214) are gestures now). A paste is a PASTE row in
`build.tsv`. A gesture the editor cannot make yet is an XFAIL row, and the
build carries on past it.

| # | beat | gestured (verbs) | pasted (why) | check |
|---|---|---|---|---|
| B0 | scaffold | none (`naut new --template minimal --language ld`) | none | clean |
| B1 | clear the template | `ed_open_diagram`; `ld_delete_rung high`; `ld_vars_delete` ×3 (variables panel ×) | none | none |
| B2 | tag database | none | `tags/io.yaml`, `tags/plant.yaml`, `nautilus.yaml` `tag-files:` (YAML only: no tag-grid gesture) | clean |
| B3 | the AOI, `lib/motor.ld` | the six inputs from the variables panel's block scope (`ld_vars_declare … VAR_INPUT MotorStarter`, the Parameters tab), then 4 rungs: `ld_add_rung` ×4, `ld_rung_comment` ×3, `ld_add_contact [nc]` ×9, `ld_add_branch` ×2 (seal-in, fault OR), `ld_add_block TON tFail` (FB picker; the call `tFail:TON(…)` declares the instance), `ld_add_coil [set\|reset]` ×5, and the three outputs from the amber offer (`ld_declare … VAR_OUTPUT`) | the POU shell: its doc comment, `FUNCTION_BLOCK`, an empty `LD` (no gesture creates a POU, #179) | clean after the inputs and after every rung |
| B4 | the CPT math | none | `lib/speed.st` (ST is typed text) | clean |
| B5 | MainRoutine, `program.ld` | 10 rungs: `ld_add_rung`, `ld_add_contact [nc]`, `ld_edge_retag` (the ONS typed as `+M1_StartPB`), `ld_move_element` (E-stop dragged ahead of AirOk), `ld_add_block MotorStarter m1`, `ld_copy_rung m1 m2perm` (m2 = a copy of m1 below m2perm, instance m2) + `ld_retag` ×2 + `ld_edit_fb_args`, `ld_add_block SpeedCalc spd`, `ld_add_block CTU` + `ld_rename_block c1 cStarts`, `ld_add_branch` (nested: around `M1_Run`, then around `M1_Aux` inside the leg), `ld_add_contact_after`, `ld_add_leg` + `ld_retag_placeholder` (3-leg OR), `ld_add_coil`, `ld_delete_last_coil` ×2, and `ld_declare` for every tag (the amber offer; `M1_Starts` typed INT from the CTU's CV pin) | none | clean after every rung |
| B6 | tests | none | `conveyor_test.yaml` | clean; `naut test` 6/6 |

XFAIL probes (Studio 5000 habits), each logged in FINDINGS.md:

| row | habit | probe | issue |
|---|---|---|---|
| `lx_desc_on_element-M1_StartPB` | the tag description drawn above the instruction | the element's text/title includes the `desc:` | #216 |
| `lx_real_coil_checks-M2_SpeedRef` | MOV/CPT: write a REAL from the rung | a coil on the REAL tag, then `naut check` | #215 |

Probes that pass since #232 (rows now PASS, verbs in `verbs/gestures.sh`):

| row | habit | issue |
|---|---|---|
| `ld_edge_retag-M1_StartPB` | ONS, or `+Tag` typed into a contact | #213 |
| `ld_edge_drawn-M1_StartPB` | the ONS visible on the rung | #212 |
| `ld_copy_rung-m1` | copy rung, paste, edit M1→M2 (builds m2) | #217 |
| `ld_vars_lists_instance-m1` | the AOI backing tag in the tag list | #220 |
| `ld_vars_escape_closes` | Escape closes a popup | #220 |
| `ld_declare_offer_type-M1_Starts` | the counter tag is an INT | #219 |
| `ld_vars_declare-*`, `ld_declare-* VAR_OUTPUT` | the AOI's Parameters tab | #214 |

The build-local verbs (`lx_*` in `build.sh`) follow the gestures.sh
contract: save the file, then read it back. The generally useful ones moved
to `verbs/gestures.sh` (`ld_delete_rung`, `ld_vars_delete`,
`ld_vars_declare`, `ld_rung_comment`, and the probes above, with
selftest.sh rows); the two left are probes of habits nautilus lacks.

## Done means

- every row is PASS, XFAIL or PASTE (none FAIL, none XPASS)
- `naut check` is clean at every beat boundary
- `naut test` passes 6/6 on the built project
- `compare.py`: the built files match `reference/` modulo layout. Any
  allowed difference is named in its `ALLOW` table and in FINDINGS.md.
