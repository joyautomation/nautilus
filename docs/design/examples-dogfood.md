# Examples rebuild: dogfood log

Running log of friction hit while authoring the rebuilt `examples/`
projects against the released CLI (per `docs/design/examples.md`'s
dogfooding protocol). Every entry: date, what was done, what was
expected, what happened, a classification (bug / papercut / docs gap),
and where, if known. Entries are appended, oldest first, per project.

## examples/lift-station

**2026-09-25 · devicemap tag naming can't produce a bare/short tag name ·
docs gap / design constraint**

Built `devices.yaml` per the design's plan for a `rio` remote-I/O device
type carrying `LIT101_Level`, `FIT101_Flow`, `LSHH101`, `LSLL101`,
`P101_SealFail`, `P101_OverTemp`, `P102_SealFail`, `P102_OverTemp` under
one `RIO` instance. Expected the importer to have some way to land these
exactly on the tag model's names, since the design's own text hedged
("the RIO instance may need its registers named with the full tag and
the prefix suppressed... pick whatever produces the tag names above").
What happened: `modbus/codegen/generate.go`'s `Generate` composes every
tag name as `name := in.ID + "_" + r.Tag` unconditionally — both operands
required non-empty by `ParseDeviceMap`'s `identifier` check — so there is
no way to suppress the instance-id prefix or land on a name with no
underscore at all (`LSHH101`) from any instance id. Where: modbus/codegen/
devicemap.go and generate.go (no prefix-suppression option exists).
Worked around it: the `RIO` instance's points import as `RIO_Level`,
`RIO_Flow`, `RIO_LSHH101`, `RIO_LSLL101`, `RIO_P101_SealFail`,
`RIO_P101_OverTemp`, `RIO_P102_SealFail`, `RIO_P102_OverTemp`, and
`field_map.st` — a small program that only exists in `field.yaml` — renames
them onto the canonical tags every other program reads. The VFD points
needed no such workaround: naming the VFD device type's registers
`Running`, `VfdFault`, `SpeedHz`, `Amps`, `RunCmd`, `SpeedCmd` and the
instances `P101`/`P102` reproduces the tag model's names exactly, RunCmd
included — see the next entry.

**2026-09-25 · writable `bit:N` register format does read-modify-write ·
not a bug, a design question resolved**

The design's Files section wasn't sure whether the importer supports a
writable single bit of a control word (`P101_RunCmd` = bit 0 of the VFD's
control word) or whether the fallback (a whole UINT control word written
by ladder) would be needed. Read `modbus/driver.go`'s `flushWrites`/
`stageWrites`: a writable `bit:N` binding is staged as a `bitWrite` and
flushed with an explicit read-modify-write of that one register
(`bit:N writables read-modify-write their register one at a time — the
other 15 bits belong to the device or to sibling bindings`, driver.go
~line 930). So `RunCmd` at `address: 2100, format: "bit:0", writable:
true` is safe and exact — no separate `Control` word tag or ladder-side
bit-packing needed. Documented in devices.yaml's header.

**2026-09-25 · a manifest's first task's `name:` key is silently ignored
· papercut**

Gave the bench manifest's first task (`sequence.sfc`) `name: sequence`,
expecting `suspend: [sequence]` to work in tests. `naut check` passed
with 0 errors/warnings on this. `naut test` failed with `no task
"sequence" in this resource (have main, level, permissives, stats,
sim)`. Where: `internal/project/sources.go`'s `Sources` — `taskName :=
runtime.MainTaskName` unconditionally for `i == 0`, before even looking
at `t.Name`. The existing examples all follow this by convention (never
naming the first task), but nothing in `naut check` says the key is
ignored for that position — a project author who names the first task
gets no diagnostic, just a working manifest whose declared name silently
does nothing. A `naut check` warning ("task 1's name: key is ignored;
the first task is always \"main\"") would have caught this in seconds
instead of a `naut test` failure with an unfamiliar task name in the
error. Fixed by dropping the `name:` key on the first task in both
manifests and using `main` in the test file's `suspend:` lists.

**2026-09-25 · `task.local` addressing (docs/testing.md) doesn't resolve
· bug or docs gap**

Tried `main.postRunTmr.Q` and `sim.tSec` as ST test-expression matchers
to inspect program-local state without adding tags, per docs/testing.md
("Program locals are addressable as `task.local` — `main.integral`").
Expected the identifier to resolve against the named task's compiled
program locals. Got `undeclared identifier "main"` / `undeclared
identifier "sim"` from the test-expression compiler for both the
reserved main-task name and a manifest-declared task name. Never found
a working spelling in the time available. Either the feature doesn't
reach the acceptance-test expression compiler (a bug) or the doc's
example needs a working one (a docs gap) — not chased further; the
acceptance suite doesn't rely on it, but the dogfooding brief asked to
try each `docs/testing.md` verb.

**2026-09-25 · SFC `Pn` pulse qualifier timing: activation vs.
deactivation matters for a duty-swap that must outlive a post-run ·
bug in this project's own logic, not the platform**

First cut of `sequence.sfc` put `P1 SwapDuty` in `STEP Alternate`
(pulse on **activation** — fires the instant the Lead→(PostRun,
Alternate) simultaneous divergence takes effect). Since `Alternate` and
`PostRun` activate in the very same scan, `LeadIsP101` flipped *before*
the outgoing pump's 20 s pump-down had even started, and the ladder's
duty-mapping rung (`[ LeadIsP101 LeadReq | ... ]`) immediately stopped
recognizing that pump as the one `LeadReq` was called for — its `RunCmd`
dropped to `false` within one scan of entering `PostRun`, instead of
staying on for the whole post-run. Caught by running `naut test`: two of
the ten tests (`lead pump starts at LeadOn and post-runs after
LeadOff`, `duty alternates every completed cycle`) failed with
`P101_RunCmd = false, want true` a few hundred milliseconds into what
should have been a 20 s run-down — exactly the kind of thing the
acceptance criteria's "spot-check by breaking the logic" step is for,
except this one broke itself first. Fixed by using `P0` (pulse on
**deactivation**) instead: `SwapDuty` now fires when `Alternate` itself
deactivates, i.e. only once the simultaneous convergence to `Idle` has
already fired (after the post-run timer elapses) — so the duty-mapping
tag still shows the outgoing pump as lead for the full run-down, and
only flips for the *next* call. `STEP Failover` still uses `P1` (that
swap must be instant — the standby has to answer the same call this
scan). `P0`/`P1` are both documented, supported qualifiers
(`lang/sfc/check.go`'s `supportedQualifiers`); the platform behaved
exactly as specified — the bug was choosing the activation-edge
qualifier for a job that needed the deactivation edge.

**2026-09-25 · a `VAR CONSTANT` named in a FUNCTION_BLOCK whose body also
captures an output at the call site silently drops the write-back · BUG**

`lib/physics.st`'s `WetWellModel` (`VAR_IN_OUT Level`) and `VfdModel`
(`VAR_OUTPUT SpeedHz` captured via `=>`) each declared their tuning
numbers in a `VAR CONSTANT` block (`LPerPercent := 50.0`, `RampHzPerS :=
10.0`, `FullLoadA := 22.0`) and referenced them in the body. `naut check`
passed with 0 errors. Expected `sim.st`'s calls (`well(..., Level :=
LIT101_Level)`, `vfd101(..., SpeedHz => P101_SpeedHz)`) to update the
caller's tags every scan, the way `docs/functions.md`'s own `FB_Starter`/
`VAR_IN_OUT` example and the `RateOfChange` (`lib/pump.st`) block already
do elsewhere in this same project. What happened: `LIT101_Level` and
`P101_SpeedHz` stayed **exactly** at their seeded value for the whole
test window (five real minutes of `naut run`, or an `advance: 5s` in
`naut test` — no drift at all, not even by a rounding error), while
every other observable inside the same call — `P101_Running` (a plain
`VAR_OUTPUT` with no named constant in its formula), the seal-fail
pass-through, the alarm floats — updated correctly on schedule. Isolated
by commenting out the second `VfdModel` instance (ruled out a
two-instances-of-one-type aliasing bug — still broken with only one
instance called) and then by replacing the named `VAR CONSTANT` in the
ramp formula with the literal `10.0` inline: the write-back started
working immediately, byte-for-byte the same formula otherwise. Confirmed
the same fix on `WetWellModel`'s `LPerPercent`. **Workaround, now in both
FUNCTION_BLOCKs: don't name tuning constants in a `VAR CONSTANT` block
inside a FUNCTION_BLOCK whose output pins are captured at the call site
— inline the literal instead**, with a comment pointing here. This is a
real compiler/runtime bug (silent, no diagnostic from `naut check` or at
runtime — the caller's tag is simply never written), not a design
mistake; it deserves its own issue against `lang/st` or `runtime` before
the extension's stable release, since it would silently produce a "dead"
simulation with no error anywhere in the toolchain. Not yet root-caused
past "naming the constant is the trigger" — worth a minimal, single-file
repro (a two-line FUNCTION_BLOCK with one `VAR CONSTANT` and one
`VAR_OUTPUT`) in the issue.

**2026-09-25 · library files must sit at the project root — a `lib/`
subdirectory does not compose · docs gap / design constraint**

The design's Files section put the shared library code under `lib/`
(`lib/pump.st`, `lib/physics.st`, `lib/motor.ld`). With them there,
`naut check` failed on every file that references one of their types or
blocks: `unknown type "MotorStarter"`, `unknown type "WetWellModel"`,
`unknown type "PumpStats"` — three errors, one per consumer, nothing
about the `lib/` directory itself. Every existing example
(`heated-tank-nogo/blocks.st`, `ladder-subroutines/blocks.ld`,
`alarms/blocks.st`) already keeps its PROGRAM-less library files at the
project root, never in a subdirectory, which in hindsight was the
answer: library discovery is root-only. Moved `pump.st`, `physics.st`
and `motor.ld` to the project root (same as every other example);
`naut check` passed immediately with no other change. Worth a `naut
check` diagnostic when a project has a `lib/`-shaped subdirectory of
`.st`/`.ld`/`.fbd` files with no `PROGRAM` that never gets composed — a
name like `lib/` invites exactly this layout, and the failure mode
(three unrelated-looking "unknown type" errors, one per file that
happens to use the library) doesn't point at the real cause.

**2026-09-25 · a level-PID-modulated duty pump doesn't fully stop just
because inflow drops · design tuning, not a bug**

Design's test 9 wanted a storm inflow (`InflowLps: 40`) to cycle the
pumps, then a drop back to a low inflow (design said `5`) to bring
`P101_RunCmd`/`P102_RunCmd` back to `false` within 15 minutes. With
`InflowLps: 5`, they never went `false`: the level PID (`level.fbd`)
found a stable operating point near `LevelSP` (45 %) where the lead
pump's modulated speed exactly balances a 5 L/s inflow (around 32 Hz,
comfortably above `MinSpeedHz`'s ~30 Hz / ~3.6 L/s floor) and parks
there indefinitely — correct variable-speed behavior (that's what a
speed-modulated duty pump is *for*), but it means the level never falls
to `LeadOffLevel` (30 %) to trigger the sequence's stop. Lowered the
post-storm inflow to `2.0` L/s — below the pump's minimum-speed output,
so the floor-clamped pump necessarily outruns it and the level keeps
falling past `LeadOffLevel` into a real post-run and stop. Documented
inline in `lift-station_test.yaml`; this is the "adapt numbers if the
physics need it, keep the intent" allowance, not a platform issue.

**2026-09-25 · permissive-blocked lead does not trigger sequence
failover · design clarification, not a bug**

The design's test 6 wanted "a seal fail on the lead pump blocks its
start and the standby takes the lead call," hedging that the mechanism
("the sequence sees `LeadFailed`, or the availability logic re-maps")
was for the authoring session to resolve. `LeadFailed` is spec'd
explicitly as "the lead pump's `FailToRun`" (design line 125), and
`FailToRun` only latches after `Run` has been commanded for 5 s without
`RunFb` confirming — a permissive block (`SealFail`) prevents `Run`
from ever asserting in the first place, so the fail-to-run timer never
starts and `LeadFailed` never trips. The standby does **not** take an
instant, automatic handoff on a bad permissive in this implementation;
it only answers once the level independently justifies its own call
(e.g. rising to `LagOnLevel`). Implementing instant failover on any
availability loss would mean re-deriving duty from `P101_Avail`/
`P102_Avail` rather than the literal `LeadIsP101` pointer the design
specifies for duty-mapping — a larger, un-spec'd change. Documented in
`lift-station_test.yaml`'s "permissives block a start" test and left as
literal-spec behavior; flagging here in case the lead session wants
true instant failover on any availability loss as a follow-up.

**2026-09-25 · a multi-line `(* ... *)` comment right after a `RUNG` name
breaks the ladder parser · bug**

First cut of `permissives.ld` wrote a rung comment the way this file's
own header comments wrap across lines:

```
RUNG p101req (* duty mapping + high-high override, one rung: LSHH forces
               the call regardless of the sequence or who's lead *)
  [ LeadIsP101 LeadReq | /LeadIsP101 LagReq | LSHH101 ] ( P101_Req )
```

Expected the same block-comment wrapping every other language in this
project uses freely (SFC/FBD/ST headers all wrap `(* ... *)` across
several lines with no issue). Got `permissives.ld: ld: line 46: expected
a name, got "*"` from `naut check` — a hard parse error, not a
diagnostic pointing at the comment. Reproduced on two separate
occurrences in the same file (both rung-trailing multi-line comments).
Every working `.ld` example in the repo (`heated-tank-nogo/interlocks.ld`,
`ladder-subroutines/*.ld`) only ever uses a `(* ... *)` comment that
closes on the same line it opens, right after a `RUNG <name>` — in
hindsight, a pattern nobody had broken yet rather than a documented
rule. Worked around it by moving the explanation to a `//` line above
the `RUNG` line instead of an inline block comment trailing the rung
name. Where: the `.ld` parser's rung-header handling (`lang/ld`) — not
chased to the exact token-level cause in the time available, but the
failure is specific to the multi-line block-comment case immediately
after a rung name; a single-line `(* ... *)` in the same position works
fine.
