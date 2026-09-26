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

**2026-09-25 · a block comment whose continuation line starts with the
literal keyword `PROGRAM` silently breaks project-library type
registration · bug (fixed on main)**

Adding a lockout counter to `motor.ld`'s `MotorStarter` (a project-library
`FUNCTION_BLOCK`, no `PROGRAM`) meant rewording its header comment.
Rewrapped it so a continuation line began "PROGRAM is a project library,
exactly like..." — the same sentence that, worded the original way,
never put that word first on a line. Expected a doc-comment reword to be
inert. Got every caller in the project failing with `unknown type
"MotorStarter"` — no error pointing at `motor.ld` itself, nothing about
the comment at all. Isolated with a two-file, two-line repro (a
`FUNCTION_BLOCK` with nothing but a leading block comment, instantiated
once from a `PROGRAM`): the type registers fine with the header worded
any other way, and stops registering the moment a wrapped continuation
line's first word is the bare, correctly-cased keyword `PROGRAM` —
reproduces in a project-library `.ld` file and in a plain `PROGRAM`'s own
leading comment alike (not tested for `TYPE`/`FUNCTION`; `FUNCTION_BLOCK`
itself in the same position did *not* trigger it). Consistent with a
comment-unaware, line-oriented pre-scan — this project's own convention
almost certainly relies on one ("a .ld/.st file with no PROGRAM is a
library," per the `lib/` finding above) — misreading the commented-out
word as a real POU boundary and mis-slicing the rest of the file. Worked
around by rewording so no continuation line starts with `PROGRAM`. Where:
likely the same library/prelude discovery pass as the `lib/` finding
above (`internal/project/project.go`) — not chased past the isolated
repro in the time available; worth its own issue, and a nastier one than
the ladder rung-comment parser bug above since this one has zero
symptoms pointing anywhere near the actual cause.

**Status: fixed on `main` by PR #42 (`PROGRAM` detection is lexical now,
not a line-oriented pre-scan) — ships in v0.13.0.** This example still
targets the released CLI (v0.12.0), where the bug is real, so `motor.ld`'s
header keeps the reworded wording above rather than reverting to the
original phrasing that happens to put `PROGRAM` mid-line — it has to keep
working on v0.12.0 until v0.13.0 ships, not just on `main`.

**2026-09-25 · a trip counter reset by the same signal that permits a
retry can never count past one · design clarification, not a bug**

The review asked for `MotorStarter`'s dead `CTU` to count fail-to-run
trips instead (`CU` on `FailToRun`'s rising edge, `R := Reset`, `PV := 3`,
`LockedOut` after three) — reusing the same operator `Reset` pulse that
already clears `FailToRun` for a retry. First cut wired `R := Reset`
literally, expecting three Reset-and-retry cycles to accumulate the
count to three. What happened: every retry needs its own `Reset` pulse
(nothing else clears the `FailToRun` latch blocking `Run`), and that
same pulse also zeroes the counter (`CTU`'s own `R`) right before the
next attempt even starts — so the count is provably always 0 or 1, never
higher, and `LockedOut` (`PV := 3`) is unreachable through any real
operating sequence, not a timing artifact of the acceptance test itself.
Caught by writing the three-trips test: it failed at trip two every
time, confirmed against a minimal isolated repro before suspecting the
test rather than the design. Fixed by gating the counter's own reset on
`AND(Reset, LockedOut)` instead of `Reset` alone, so an ordinary
fail-and-retry clears only `FailToRun` (the count keeps accumulating),
and the count clears only once `LockedOut` has actually latched — which
is also the one moment "clears on Reset" needs to be true. Worth a
general note for anyone wiring an N-strikes lockout in ladder: don't
share a trip tally's reset with the fault flag's own retry-reset, or the
tally can never move past one.

**2026-09-25 · a custom component's sidecar-only ports resolve in the
editor and silently collapse at runtime · docs gap / design constraint**

`SubmersiblePump.svelte` got a `SubmersiblePump.component.json` sidecar
(one `discharge` port) and nothing else, matching every other custom
component in this project at first. Expected the mimic's `P101`/`P102`
discharge pipes to route from that port up to their check valves.
Got pipes collapsed to a two-point stub sitting at the check valve, with
no visible line down to the pump at all — no error, no warning, just a
wrong-looking pipe. `hmi-demo`'s own README already says why, in the
"Pipe anchors" section: a `*.component.json` sidecar is a project/editor-
time convenience (`mimicComponentIndex.ts` aggregates them for the
editor), and it never ships with the built app — the runtime `<Mimic>`
has no sidecar to read, so a pipe anchored to a sidecar-only port
resolves to nothing at serve time even though the SAME instance looks
correct in the VS Code mimic editor (which does read the sidecar). The
fix (also already documented, for `HeatExchanger`/`E101`, in that same
README section — missed the first time through here) is to also carry
the identical `ports` array inline on the equipment entry in the
`.mimic.json` itself, which resolves identically in both places. Where:
inherent to the split between editor-time sidecars and the runtime
`<Mimic>` component (`hmi/src/lib/mimic.ts`/`components/Mimic.svelte`) —
not a bug, but worth a `naut check`-style diagnostic (or an editor
lint) for "this equipment's component has a sidecar with ports this
instance doesn't repeat inline," since the failure mode (a mimic that's
right in the editor and wrong once served) is exactly the kind of thing
this project's own dogfooding protocol exists to catch.

**2026-09-25 · `.component.json` port sidecars are read by the mimic
editor but never reach the built HMI app — two sources of truth for one
port list · bug (kit/extension design)**

Restating the finding above as what it actually is, not just a docs gap:
`resolvePorts()` (`tools/vscode-iec/webview-ui/src/mimic/ports.ts`) and
the kit's own `resolveRuntimePorts()` (`hmi/src/lib/mimic.ts`) resolve a
custom component's connection points on DIFFERENT precedence chains —
editor: instance override -> sidecar -> built-in; runtime: instance
override -> built-in, no sidecar tier, because a sidecar is a
project/editor-time aggregation (`tools/vscode-iec/src/
mimicComponentIndex.ts` + `mimicComponents.ts`; a project's own custom
`*.svelte` components are separately discovered and bundled for the
webview by `tools/vscode-iec/src/userComponents.ts`, which has the same
never-ships-with-the-built-app property) that never ships with a built
app. Both resolvers' own doc comments already say so in so many words —
this is a known, intentional tradeoff, not an oversight anyone missed —
but the practical effect is that a mimic authored with sidecar-only
ports is WRONG the moment it's built and served, with no diagnostic
anywhere (the editor shows it correctly right up until `npm run build`),
and the only fix is keeping the same port list in two places by hand
(the sidecar, for the editor's "Edit Component Ports…"/`p` gesture, and
an inline `ports` override on every equipment instance, for the runtime)
with nothing that checks they still agree after either one is edited.
Where: `hmi/src/lib/mimic.ts` (`resolveRuntimePorts`, the `<Mimic>`
registry) + `tools/vscode-iec/src/userComponents.ts` (and its sidecar
neighbors `mimicComponentIndex.ts`/`mimicComponents.ts`). Status: open —
worth either shipping the sidecar data as part of the built app (a small
generated JSON the build step could emit) or a lint/diagnostic for a
custom component's ports diverging between its sidecar and every mimic
that places it.

**2026-09-25 · a `.mimic.json` at the project root needs an explicit
`server.fs.allow` for the SvelteKit app's dev server to import it ·
friction, worked around**

Put `lift-station.mimic.json` at the project root (not `hmi/src/routes/`)
so the VS Code mimic editor and the SvelteKit app share one file, per
the design brief's stated preference. Expected `hmi/src/routes/
+page.svelte`'s `import mimicDoc from '../../../lift-station.mimic.json'`
to just work, the same as any other relative import. `npm run build`
did work (Rollup reads straight off disk, no project-root fence); `npm
run dev` did not — Vite's dev server refuses to read/transform anything
outside its own project root by default, so the first request touching
that import path would have 403'd. Worked around with `server.fs.allow:
[path.resolve(__dirname, '..')]` in `hmi/vite.config.ts`, which is a
one-line, well-supported Vite option — this is expected Vite behavior
for a file living outside the SvelteKit project's root, not a nautilus
bug, but worth calling out for the next example that wants its mimic at
the project root: `npm run build` alone will not tell you `npm run dev`
is broken.

## examples/batch-skid

**2026-09-25 · a bare S/N/R qualifier association silently discards an
ACTION block's write to the same variable, from anywhere else in the
chart · BUG**

`phases.sfc`'s `Agitate` step conditionally commands the agitator —
`P1 SetAgitate` runs an `ACTION` body (`IF Active.Agitate THEN
AG201_Cmd := TRUE; END_IF;`), since an unconditional `S` can't express
"only for recipes that agitate." The design's own `Aborted` step also
resets it, first written the literal way the design phrases it: a bare
`R AG201_Cmd`. `naut check` passed with 0 errors. Expected `naut run .`
to show `AG201_Cmd` staying `TRUE` through `Charge`/`Heat`/`Hold` on an
agitate recipe. What happened: the pulse fired correctly — `AG201_Cmd`
read `TRUE` for exactly the one scan `SetAgitate` ran — and then reverted
to `FALSE` on the very next scan, forever, even though `Aborted` had
never been active (`Abort` stays `FALSE` the whole run). Isolated with a
minimal repro — one `INITIAL_STEP`, one step with a `P1` ACTION doing
`X := TRUE`, a second, **never-reached** step (`FROM ... TO Aborted :=
FALSE`, structurally dead) with nothing but a bare `R X` — reproduces in
3 scans: `X` goes `TRUE` once, then `FALSE` forever after. Replacing the
bare `R X` with an equivalent ACTION (`ClearX: X := FALSE;`) makes the
repro pass; replacing the *other* side's ACTION with a bare `S X` also
avoids it. So the trigger is specifically **one write via a bare
qualifier and the other via an ACTION**, targeting the same variable,
anywhere in the same chart — not simultaneity, not reachability
(the repro's `Aborted` is provably dead per `naut check`'s own "dead
end" warning, and it still corrupts `X`). Consistent with a compiler
pass that, once it sees ANY bare qualifier for a variable, treats that
variable as fully SFC-owned and recomputes it every scan from the bare
associations alone, blind to an ACTION block's independent write to the
same name. Not chased past the isolation (`lang/sfc`, the ST lowering
that combines qualifier associations, most likely). Worked around here
by making `Aborted`'s reset an ACTION too (`StopAgitator: AG201_Cmd :=
FALSE;`) — no bare qualifier touches `AG201_Cmd` anywhere now. Caught by
watching `naut run .` rather than the acceptance suite (which happened
to pass either way, since the buggy reset produced the same *externally
observable* FALSE the fix does in every test's exact scan windows) —
worth a `naut check` diagnostic ("variable X is targeted by both a bare
qualifier and an ACTION body — undefined which wins") since this is
exactly the kind of thing that looks fine in every test and is wrong on
the running plant.

**Status: fixed on `main` by PR #54.** Associations now write their
target only on the scans they act (every scan an `N`/pulse step is
active, plus the one scan it drops; once, on an `S`/`R` step's own
activation), after the ACTION bodies run — so the association wins while
it acts and the ACTION owns the variable otherwise, and `naut check` now
warns whenever a variable is targeted both ways, naming the step,
qualifier and ACTION(s) so the precedence is visible rather than
inferred. Reverted the workaround: `Aborted` is back to a bare `R
AG201_Cmd` (the design's own literal spelling), `SetAgitate`'s ACTION is
unchanged, and `naut check` shows exactly the one expected warning —
accepted rather than designed around, since avoiding it would mean
splitting the simultaneous divergence's `Agitate` branch into two
conditional paths for no behavioral gain now that the precedence is
exact. `naut test` — still 10/10 with no changes needed.

**2026-09-25 · what looked like a permanent three-way convergence
deadlock was a test-authoring bug, not the platform — plus one real,
narrower scheduling flaw PR #54 found and fixed while checking ·
CORRECTED (see the original, wrong write-up this replaces, below)**

First cut of the simultaneous charge/agitate branch gave every one of
its three steps (`ChargeADone`, `ChargeBDone`, `Agitate`) its own abort
transition to `Aborted`, alongside the simultaneous convergence
`(ChargeADone, ChargeBDone, Agitate) -> Heat`. `naut check` passed with
a warning naming exactly those four transitions as a non-transitive
alternative-priority group. Several acceptance tests then got stuck:
`PhaseName` stayed `"Charge"` no matter how many additional scans a step
budgeted, even with `Abort` FALSE throughout. **This write-up first
concluded the convergence itself was deadlocked** — a real bug in the
alternative-priority engine — and worked around it by dropping the abort
transitions on `ChargeADone`/`ChargeBDone`, keeping only `Agitate`'s.
That conclusion was wrong, and the fix was cosmetic: those tests set
`Batch.ChargedA_L`/`ChargedB_L` (via `given:`) in the **same test step**
as `Start: false` — the step whose one scan fires `Prep`'s own
simultaneous divergence into `(ChargeA, ChargeB, Agitate)`. `Prep`'s `N
ResetTotals` is still active during that scan (it self-clears once
`Prep` itself clears, which happens in the very same scan the
divergence fires — see `docs/languages/sfc.md`'s "Set-dominates-clear"
rule), and `dosing.fbd` — a separate 100 ms task — was ticking somewhere
in the same virtual-time window and saw `ResetTotals` still `TRUE`,
zeroing the totals the `given:` had just set before the SFC's own
`t_a_done`/`t_b_done` transitions ever got a chance to see them cross
the threshold. The charges never actually finished; nothing downstream
of that was ever going to fire, convergence included, independent of
which abort transitions existed. Splitting the `given:` into its own,
later step (after `Prep`'s divergence has already run once) is the real
fix, and it was already sitting in this file for unrelated reasons by
the time this was caught — the wrong conclusion above got written
against an earlier, since-corrected version of these tests.

What the review that caught this **did** find, checking the same
scenario properly: on `main`, the convergence fires correctly, in either
declaration order, once the charges actually complete — no deadlock, and
no restructuring needed. It also found a real, narrower flaw one layer
down, now fixed by PR #54: the alternative-priority guard suppressed a
lower-priority transition whenever a higher-priority sharer was merely
*enabled*, not only when it actually *fired* — so a non-transitive group
like this one (`t1 FROM A`, `t2 FROM (A,B)`, `t3 FROM B`, all enabled)
could suppress `t3` for one extra scan even though `t1`'s firing that
scan had nothing to do with `B`'s token. The guard now reads whether the
higher-priority sharer *fired* (`_f_`, resolved in declaration order)
instead of whether it was merely enabled (`_en_`), so `t1` and `t3` fire
together, exactly once. This is a genuine, previously-existing
scheduling imprecision — one scan, not a deadlock — and it's the reason
`naut check`'s "ambiguous alternative-priority group" warning is gone
entirely now rather than narrowed: the fixed guard makes every such
group exact, with one well-defined outcome, so there is nothing left to
warn about.

Reverted the workaround: the abort transitions on `ChargeADone` and
`ChargeBDone` are back (`t_abort_adone`, `t_abort_bdone`, alongside
`t_abort_agitate`), the acceptance suite's `given:`/`scans:` steps are
unchanged from the already-correct split, and all ten tests pass with no
other edits. The lesson, restated: a test that looks stuck deserves a
minimal repro of the *engine* behavior in isolation (no totalizers, no
`ResetTotals`, just the steps and transitions in question) before
concluding the compiler is wrong — the SFC-only repro that would have
shown the convergence firing fine was never actually built for this one,
only for the separate (and real) `R`/ACTION bug above.

**2026-09-25 · no struct/array literal initializer, and no way to
declare data outside a POU · docs gap / design constraint**

The design's plan for the three ship-with recipes — `Recipes : ARRAY
[1..3] OF Recipe` "declared in `lib/recipes.st` with three initialised
entries (constants)" — assumes some IEC literal-initializer syntax for a
struct or an array of structs. `docs/languages/structured-text.md` is
explicit that there isn't one ("An initial value after `:=` must be a
single literal constant"), and there is no `VAR_GLOBAL`-with-initializer
outside a POU either (`VAR_GLOBAL` and `VAR_EXTERNAL` both just resolve a
tag-store name — a real initial value comes from the manifest's `init:`,
which in turn has no array-of-struct literal form beyond per-member
nesting for a single struct tag, per `docs/guides/tag-model.md`). Worked
around with a `FUNCTION_BLOCK RecipeTable` (`lib/recipes.st`) whose
`VAR_OUTPUT Recipes : ARRAY[1..3] OF Recipe` is populated by plain field
assignment on its own first call (an internal `loaded` latch), called
once from `phases.sfc`'s `LoadRecipe` action. Not a bug — the language's
"a value is either a tag with a manifest `init:` or a POU-scoped
declaration with a scalar literal, nothing in between" is a deliberate,
documented boundary — but it's exactly the kind of thing a design
written before touching the compiler gets wrong, and worth a
`docs/design/examples.md`-style callout for the next session that reads
for "constants" in a design brief.

**2026-09-25 · cross-task step-activity flags don't exist as a thing to
read, even by convention · design clarification, not a bug**

The design's FBD section wanted the jacket PID's `AUTO` pin driven by
`Heat.X OR Hold.X` — `dosing.fbd` and `phases.sfc` are different tasks
(100 ms and 250 ms), and a step's `.X`/`.T` are retained **program**
locals (`docs/languages/sfc.md`: "Each step owns a retained BOOL slot"),
never tags, so there is no `VAR_EXTERNAL` spelling for another task's
step activity — consistent with lift-station's own finding that
`task.local` doesn't reach the ST-expression compiler (an acceptance
test's matcher form, `task.local: value`, does resolve it fine; it's
only unavailable as a value inside a program's own ST/FBD/LD
expressions). **Status: `docs/testing.md`'s `task.local` section was
reworded by PR #43** to say exactly that, rather than leaving it
ambiguous. Worked around the same way `examples/tank-batch-sfc`'s
`RunLamp` already demonstrates the fix for: `HeatingActive` is a state
tag, `N`-qualified identically from both `STEP Heat` and `STEP Hold` in
`phases.sfc` — two associations targeting the same BOOL OR-combine, so
it reads exactly `Heat.X OR Hold.X` a cross-task reader needs, with no
new mechanism. (`AgitateReq`, added when the agitator-never-stops bug
below was fixed, uses the identical combine across `Agitate`/`Heat`/
`Hold`.) Documented here mainly to save the next session from
re-discovering `task.local`'s scope the hard way.

**2026-09-25 · the agitator never stopped: a bare qualifier's RESET and an
ACTION's SET, on the same variable, don't compose the way "R wins on
Aborted, the ACTION owns it otherwise" reads · BUG, found by running the
plant, not by the acceptance suite**

`phases.sfc`'s original design commanded `AG201_Cmd` two ways: `Agitate`'s
`P1 SetAgitate` ACTION set it TRUE once, conditionally on
`Active.Agitate`; `Aborted`'s bare `R AG201_Cmd` reset it, once, on
Abort's own activation scan. `naut check` warned about exactly this pair
(a variable targeted by both a qualifier association and an ACTION body)
and PR #54's own fix made the precedence exact — but exact precedence
between two writers is not the same as **correct** behavior when the
design never gives the RESET writer a reason to fire on the path that
needed it. A normal batch never visits `Aborted` at all: Charge -> Heat
-> Hold -> Transfer -> Idle, so the ACTION's one-scan SET was the only
write `AG201_Cmd` ever saw across a whole batch, and it stayed TRUE —
through `Transfer`, through `Idle`, into the next batch, agitated or not.
Every acceptance test in this file happened to pass anyway: the tests
that check the agitator turns off all abort out of `Charge`/`Heat`
first, so they exercise exactly the one path where the bare `R` does
fire — none of them ran a batch to completion without aborting and then
checked `AG201_Cmd` afterward. A **good** acceptance test for "the
agitator stops after the batch" has to do that specifically: run recipe
1 (agitates) through a real `Transfer` and back to `Idle` with no abort
anywhere, and check `AG201_Cmd` is `FALSE` once it gets there — see
"recipe 1 keeps the agitator on through Charge/Heat/Hold, and it stops
after Transfer" in `batch-skid_test.yaml`, which fails against the
original design and passes against the fix below. A second test starts
recipe 2 (no agitate) immediately after recipe 1 aborts, to check nothing
is left latched to relapse onto the next, unrelated batch. **Fix:** drop
`AG201_Cmd` as something `phases.sfc` writes at all. Add a state tag
`AgitateReq`, `N`-qualified on `Agitate`, `Heat`, and `Hold` — the same
OR-combine `HeatingActive` already uses, so it's true exactly while an
agitate-relevant step is active and self-clears everywhere else,
`Transfer`/`Cip`/`Held`/`Aborted` included, with nothing to reset
explicitly. `transfer.ld` ANDs it with `Active.Agitate` to produce
`AG201_Cmd` — the same "the sequence only ever asks, a permissive/command
program decides" division `TransferReq`/`P202_Cmd` already keep. The
`naut check` warning is gone (nothing targets `AG201_Cmd` two ways
anymore), and there's no more RESET writer that needs a reason to fire.

**2026-09-25 · `naut logix emulate` serves the tag surface; it does not
execute the L5X's own ladder · significant docs gap**

The design's plan for `line/Line.L5X` was a `Receive` routine (ready/
accept handshake, a `TON`) that the bench could point `naut logix
emulate` at and watch answer requests autonomously, the same way a real
line's own PLC would. `naut eip import`/`naut logix import` both parsed
it cleanly, and `naut run -m line.yaml .` connected, polled, and read
back `Line_Ready`/`Line_TankLevel` correctly from the emulator. What did
NOT happen: writing `Line_Request` never advanced `AcceptTmr.ACC` (read
directly off the emulator, independent of this project's own driver —
stayed `0` indefinitely) and `Line_Accept` never went `TRUE` — because
the emulator never runs `Receive` at all. `docs/guides/ethernet-ip.md`
does say the emulator "stands a ControlLogix up ... from the project's
own L5X export" and describes tag serving, seeding, and `--ramp`, but
never states outright that program logic is not executed — a reasonable
reading of "stands a ControlLogix up" is "runs the program," and it does
not. Confirmed by reading a tag directly off the emulator with a
throwaway CIP client (bypassing this project's own driver entirely):
`Line_Request` correctly read back `TRUE` after the skid wrote it (the
protocol round trip genuinely works), while `AcceptTmr`'s `ACC` member
stayed `0` and `Line_Accept` stayed `FALSE` no matter how long
`Line_Request` held. Not a bug — `docs/guides/ethernet-ip.md`'s own
"Under the CLI is `eip/logixserver`, an in-repo ControlLogix **target**"
and the CI section's framing ("driver conformance," "no build tags, no
env gating") describes a CIP-protocol conformance target, never a logic
simulator, and nothing elsewhere claims otherwise — but a design brief
planning an autonomous emulated-line demo is an easy way to arrive at the
wrong expectation, and the guide's "No PLC?" section would benefit from
one sentence saying so explicitly. Consequence for this project:
`nautilus.yaml`'s `sim.st` answers the handshake itself (the same 2 s
settling shape `Receive` uses) so the bench demo is fully autonomous;
against the real emulator (`line.yaml`), demonstrating `Line_Accept`
responding means either running a real controller, or writing it
directly on the emulator to stand in for what `Receive` would have done
— both documented in `line/README.md`.

**2026-09-25 · `naut eip import --host` does not take a combined
`host:port` · papercut, docs-adjacent**

Tried `naut eip import --host 127.0.0.1:44818 --format yaml ...` against
`naut logix emulate --l5x line/Line.L5X --listen 127.0.0.1:44818`,
expecting the same `host:port` form `docs/guides/ethernet-ip.md`
documents for a manifest's `driver.host:` field to also work on the CLI
flag. Got `dial tcp: lookup 127.0.0.1:44818: no such host` — the
combined string was passed straight to DNS resolution as a hostname,
port included. The guide's own examples are consistent (`--host
127.0.0.1` with no port suffix, `--port` separate, when a non-default
port is needed) — this is a real distinction between the CLI flag and
the manifest field, just one this session's own initial phrasing
("`naut eip import --host 127.0.0.1:<port>`") glossed over. Fixed by
using `--host 127.0.0.1 --port 44818` instead. Worth a one-line note in
the CLI's own `--host` flag help text, since the manifest's `host:port`
form is documented prominently enough to invite the same shorthand here.

## CLI and extension (found while building)

Cross-cutting findings from this session not specific to one
`examples/` project.

**2026-09-25 · `naut eip import` emits an IEC-keyword UDT member name
verbatim · bug (fixed)**

Ran `naut eip import` against `naut logix emulate --l5x variety.L5X`,
expecting a valid generated types file. Got a UDT member named `retain`
— an IEC keyword — emitted verbatim into the generated ST, which `naut
check` then rejected as invalid ST; `naut logix import` already renames
these on the sibling code path, `naut eip` codegen did not. A real
controller triggers the same collision, not just the emulator. Where:
`modbus`-adjacent EtherNet/IP codegen. Status: fixed, PR #39, merged
2026-09-25.

**2026-09-25 · the extension's manifest schema doesn't describe
`host:port` for an EtherNet/IP driver · papercut (extension)**

Wrote `host: 127.0.0.1:44818` in a manifest's `eip` driver block,
expecting the extension's manifest schema to accept and describe the
form. `host:port` support landed in PR #38, but the schema's field
description still only says "IP or hostname" — the extension doesn't
know about its own CLI's feature yet. Where: `tools/vscode-iec` manifest
schema. Status: open, fix tracked under `[Unreleased]`.

**2026-09-25 · `naut logix emulate --ramp` moves every numeric leaf,
handshake DINTs included · papercut (CLI)**

Used `naut logix emulate --ramp` for a demo carrying handshake tags,
expecting process values to drift while handshake DINTs stayed put.
`--ramp` ramps every numeric leaf, undiscriminated — process values and
handshakes alike. Where: `cmd/naut/logixemulate.go`. Status: open —
wants a `--ramp-tags <globs>` flag for when a batch skid's demo needs
some numerics held still.
