# codesys-shaped — a washer wash cycle, built the way a Codesys programmer builds it

Test plan §5.2's Codesys-shaped build: a small program a Codesys programmer
would write on day one — an SFC with timed qualifiers, a GVL, an enum, a
library FB — gesture-built in a real VS Code from `naut new --template
minimal`, every edit read back from disk. The point is the gesture path and
the habits, not a plant. FINDINGS.md is the dogfood log; this file lists the
beats and, for each part, whether it is **gestured** (a verb through the
extension's editors) or **pasted** (a file or block written to disk, because
no gesture can author it — the reason is given).

## The program (reference/)

`reference/` is the finished project, written first: `naut check` clean,
`naut test` 6/6.

| file | what | Codesys equivalent |
|---|---|---|
| `nautilus.yaml` | two tasks: `washer.sfc` (main, 100 ms), `sim.st` (100 ms) | task configuration |
| `tags/washer.yaml` | every global, with its `init:` (via `tag-files:`) | the GVL |
| `washer.sfc` | the wash cycle: `Idle → Fill → (Heat → HeatDone ‖ Wash) → Drain → Spin → Idle`, abort out of `Fill` and `Drain` into `Aborted → Idle` | the SFC POU (PLC_PRG) |
| `lib/reverser.st` | `FB_Reverser` — the drum's CW / pause / CCW / pause rhythm | a library POU |
| `sim.st` | the drum's level and temperature | a simulation POU |
| `washer_test.yaml` | 6 acceptance tests in virtual time | (CODESYS Test Manager) |

`reference-timed/` is the same washer written the way a Codesys programmer
types it first, now that nautilus has them (#190, #191): `D Detergent(T#3S)`
on Fill, `SD AlarmLamp(T#45S)` on Drain, `L SpinMotor(T#10S)` on Spin, and
`STEP Fill (MAXTIME := T#60S, ERROR := FillOverrun):` / Drain likewise in
place of the `Supervise` action, with an `alarms:` definition on each
overrun tag. Its `washer_test.yaml` is reference/'s six tests plus a drain
overrun, 7/7, the fill-timeout test also asserting the alarm. The gesture
build still reproduces reference/; the variant is checked and tested at the
end (rows `reference-timed-check`, `reference-timed-test`).

The chart: 8 steps, 9 transitions — an alternative divergence out of `Fill`
(abort declared first, so it has priority), a simultaneous divergence
`Fill → (Heat, Wash)` and its join `(Wash, HeatDone) → Drain`, two
loop-backs to `Idle`. Associations use `N`, `S`, `R`, `P1`, `P0`. Nine
`ACTION` blocks, all ST. The Codesys habits it had to give up, and what it
does instead (the first three rows compile as written since #190/#191 —
reference-timed/ uses them; reference/ keeps the Step.T form the build
gestures):

| Codesys | here |
|---|---|
| `D Detergent(T#3S)` | `N Dose` — `Detergent := Fill.X AND Fill.T >= tDoseDelay;` (the body's final scan closes it) |
| `L SpinMotor(T#10S)` | `N SpinCtl` — `SpinMotor := Spin.X AND Spin.T < tSpin;` |
| `SD`/step max time + `SFCError` | `N Supervise` on Fill/Drain sets `FaultCode` from `Step.T`; an abort transition per supervised step; `P1 RecordFault` on Aborted (reference-timed/: `MAXTIME` + `Fill.ERR`) |
| `TYPE E_WashState : (IDLE, FILL, …)` | `VAR CONSTANT ST_IDLE : INT := 0; …` in the chart, `StateNo : INT` tag, one `TrackState` ACTION associated from every step |
| GVL (`VAR_GLOBAL … END_VAR` in its own object) | `tags/washer.yaml` + `tag-files:`, re-declared `VAR_EXTERNAL` in each POU |
| GVL constants (`VAR_GLOBAL CONSTANT`) | `VAR CONSTANT` in the POU that uses them |
| numeric / left-to-right transition priority | declaration order: the abort transition is declared first |

## The tests (washer_test.yaml)

1. a full cycle runs Idle to Idle with the plant simulated (closed loop)
2. Fill opens the valve, counts the cycle once (P1), and doses after 3 s (the D alternative)
3. Heat and Wash run together, the drum reverses (the library FB), and Drain waits for both (the join)
4. Stop during Fill aborts, latches the lamp (S), and Reset returns to Idle (R, P0)
5. a fill that runs past its maximum step time aborts with a fault code (supervision)
6. the abort branch has priority when Stop and a full drum arrive together

## The build (build.sh)

Run in the rig container, `G_PACE=fast`, one row per verb, paste, habit or
`naut check`. `naut check` runs after every beat and must be clean (exit 0;
warnings are counted in the row) except where a row says XFAIL.

| # | beat | rows | gestured / pasted | why pasted |
|---|---|---|---|---|
| B01 | project | `naut new washer --template minimal` | terminal | the extension's Create Project… flow is smoke check 17's; not re-proven here |
| B02 | the GVL habit | `gvl.st` with `VAR_GLOBAL`, then `VAR_GLOBAL CONSTANT` → `naut check` (habit rows, XFAIL); then `tags/washer.yaml` + `tag-files:` | pasted | a GVL is typed text in Codesys too; the tag file has YAML schema completion, no grid |
| B02 | sim task | `sim.st` + its task | pasted | a plain ST program, not under test |
| B03 | the enum habit, the library FB | `lib/types.st` with `TYPE … : (…)` → `naut check` (habit, XFAIL); `lib/reverser.st` | pasted | ST text; nothing to gesture |
| B04 | a new SFC POU | empty `washer.sfc` → `ed_open_diagram` → `sfc_init` → `sfc_rename_step Start Idle` | **gestured** | — |
| B04 | the declaration part | the chart's **vars** panel: `StartPB : BOOL`, `LevelPct : REAL` (ext), `drum : FB_Reverser` (local); a constant `tMaxFill : TIME := T#60S` (habit, XFAIL) | **gestured** | — |
| B04 | the rest of the header | 17 more `VAR_EXTERNAL`, the `VAR CONSTANT` block; the manifest's tasks switch to `washer.sfc` + `sim.st` | pasted | the panel declares one at a time and has no CONSTANT section or initial value (FINDINGS #6); Codesys's declaration editor is text too |
| B05 | the chart | `sfc_add_step` (chained) ×3, `sfc_add_transition_condition`, `sfc_add_transition_new_step`, `sfc_add_parallel_branch`, `sfc_add_transition` ×3, `sfc_join_step`, `sfc_add_alt_branch` ×2 | **gestured** | — |
| B05 | abort priority | habit row: is the abort declared before the normal transition? (XFAIL: "+ alt branch" always appends last); then the abort TRANSITION block moved up | pasted (a block move) | no gesture reorders transitions |
| B05 | chart habits | arrow-key navigation, a transition name in the add form | habit rows (XFAIL) | — |
| B06 | the action-body gap | `sfc_add_action Heat N HeatCtl` before `ACTION HeatCtl` exists, then double-click it hoping for a body editor (habit, XFAIL) | gestured attempt | — |
| B06 | ACTION blocks | all nine, `SpinCtl` as a stub | pasted | no gesture creates an `ACTION` block (only an existing one's body is editable) |
| B06 | associations | 25 `sfc_add_action` across 8 steps; the timed qualifiers typed first (`D Detergent(T#3S)`, `SD AlarmLamp(T#45S)`, `L SpinMotor(T#10S)`), `naut check` clean (habit rows, PASS since #190), the chart's marker read for the association-vs-ACTION warning worded for D (#185), the IEC form `Detergent(D, T#3S);` checked by CLI (#189), then each retyped in place with `sfc_edit_action`; a Codesys-ordered `D Detergent T#3S` (habit, XFAIL) | **gestured** | — |
| B06 | one body | `SpinCtl`'s body typed in the chart's ST-body editor | **gestured** | — |
| B07 | tests | `washer_test.yaml` | pasted | the Testing view runs it; writing YAML is typing |
| B05 | join drawing | is `(Wash, HeatDone) → Drain` drawn as a convergence? (XFAIL) | — | — |
| B07 | the timed variant | `naut check` + `naut test` 7/7 on `reference-timed/` | — | — |
| B07 | verdict | `naut test` 6/6; `sfc_compare.py` built vs `reference/washer.sfc` (associations as a multiset), and again with `--assoc-order` (XFAIL, FINDINGS #14) | — | — |

`sfc_compare.py` compares through `naut sfc graph`: program name, header
declarations, steps and their associations (order only with
`--assoc-order`), transitions as
(FROM, TO, condition), alternative-branch priority, and every ACTION body
(whitespace-normalised). Layout, comments and transition order outside a
shared-source group do not count.

## Running it

```sh
export PATH=$HOME/.nvm/versions/node/v24.18.0/bin:/usr/local/go/bin:$PATH
eval "$(tools/rig/smoke/build.sh)"
RIG_NAME=nautilus-build-codesys G_PACE=fast RIG_CLIPS=1 tools/rig/builds/codesys-shaped/build.sh
```

Results in `tools/rig/out/builds/codesys-shaped/` (gitignored): `build.tsv`,
a PNG and a clip per row, `clips.html`, and `built/` — the project as the
build left it, with `naut check`, `naut test -v` and the compare.
