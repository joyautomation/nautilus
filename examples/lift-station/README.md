# lift-station — the flagship example

A duplex sewage lift station: the start-here project for `examples/`.
Generic water/wastewater pattern, deliberately small and fast-cycling —
no client names, tag conventions or logic. Gravity sewer flows into a wet
well (WW-101); two submersible pumps on VFDs, P-101 and P-102, lift the
wastewater into a force main. A level transmitter (LIT-101) and two float
switches (LSHH-101 high-high, LSLL-101 low-low) watch the well; a flow
meter (FIT-101) watches the discharge. The station alternates duty
between the two pumps every cycle, brings the standby pump in on a rising
level, and runs both on the high-high float regardless of anything else
in Auto.

```sh
naut check .                     # validate the bench build
naut check -m field.yaml .       # validate the field build
naut test .                      # the acceptance suite, virtual time
naut run .                       # dashboard + tag API on http://localhost:8080
naut build .                     # one deployable controller binary
```

**Needs naut >= 0.13.1.** `lib/` composition, a `FUNCTION_BLOCK`'s
declared initial values, and `naut compose` are on `main` but not in the
released v0.12.0 — build the CLI from source until 0.13.0 ships.

## What to open first

- `sequence.sfc` — the duty/standby state machine. Right-click →
  *Open With → SFC Diagram* to see the alternative divergence out of
  `Lead` (failover, lag, or post-run — priority by declaration order)
  and the simultaneous divergence into `(PostRun, Alternate)`.
- `level.fbd` — the level→speed PID and the high-level alarm seal-in.
  *Open With → FBD Diagram*.
- `permissives.ld` and `lib/motor.ld` — HOA, interlocks, the high-high
  override, and the `MotorStarter` ladder library block, instantiated
  once per pump. *Open With → Ladder Diagram*.
- `sim.st` / `lib/physics.st` — the bench-only plant: a wet-well level
  integrator and two pump/VFD models, in Structured Text.

## Tour the editor

Ten minutes against a running project — start `naut run .` in a terminal
and leave it running for all of this.

**Live values.** Open each of the four diagram files (`sequence.sfc`,
`level.fbd`, `permissives.ld`, `lib/motor.ld`) with *Open With → \<Diagram\>*
— every one shows live values on its pins/rungs/steps while `naut run .`
is up. Open `sim.st` as plain text (not a diagram — it has none) and the
same live values show as inline pills next to each variable.

**Set Live Value.** With `sim.st` open, find `InflowLps`, run *nautilus:
Set Live Value*, and set it to `60` — the "storm" inflow the file's own
comment describes. Watch `P102_RunCmd` (or `sequence.sfc`'s diagram) go
true within a few minutes: the well fills past `LagOnLevel` and the lag
pump joins.

**The Testing view.** Open the Testing view and *Test: Run All Tests* —
`lift-station_test.yaml`'s whole suite, green, in well under a second of
wall time (it's virtual time underneath). Now open `tags/station.yaml`,
change `LeadOnLevel`'s `init: 60.0` to `70.0`, save, and run all tests
again: 5 of 15 turn red, starting with "lead pump starts at LeadOn and
post-runs after LeadOff" — it drives the level to 65 %, which no longer
reaches the new 70 % call point. Click a failing test to see why it
failed. Revert the `70.0` back to `60.0`, save, and run once more —
green again.

**Download Program to Controller.** Open `lib/motor.ld` and change the
fail-to-run timer's preset — `t1`'s `PT := T#5S` — to `T#8S`. Notice the
toolbar's `≠ controller` pill (and the status bar's "nautilus: program
differs"): the workspace and the running controller have diverged.
`motor.ld` has no `PROGRAM` of its own (it's a library, instantiated by
`permissives.ld`), so running *nautilus: Download Program to Controller*
from here refuses — it names every program file in the project and asks
you to open one of them instead. Open `permissives.ld` (the program that
instantiates `MotorStarter`) and run *Download Program to Controller*
there instead: it composes `motor.ld`'s edit into `permissives.ld`'s
prelude, shows a confirmation naming what's about to ship, and the pill
clears once it's sent.

**Diff Ladder Diagram (vs Controller).** With `permissives.ld` still
active, run *nautilus: Diff Ladder Diagram (vs Controller)* — a diagram
overlay, not a text diff, showing exactly what changed between the
workspace and what the controller is running (nothing, right after a
download — edit the timer again to see it highlight the changed rung).

**Rollback.** Run *nautilus: Rollback Controller Program* (with
`permissives.ld` active — like Download, it resolves its target from the
active editor) to send the controller back to what it was running before
the download; the `≠ controller` pill returns to match.

**Diff … (vs git HEAD).** Make an edit and don't save it — e.g. rename a
step in `sequence.sfc`'s diagram — then run *nautilus: Diff SFC Diagram
(vs git HEAD)*: an overlay of the unsaved buffer against the last
committed revision, no controller involved.

**Diff Ladder Diagram (between git revisions…).** Commit a small change
to `permissives.ld`, make and commit another, then run *nautilus: Diff
Ladder Diagram (between git revisions…)*: two sequential quick-picks,
newest-first — type to filter down to the older side, then the newer —
and the diagram overlay shows what changed between exactly those two
commits, independent of both the working tree and the controller.

## What it demonstrates

| Feature | Where | Docs |
|---|---|---|
| SFC: alternative + simultaneous divergence, `N`/`S`/`R`/`P1`/`P0` qualifiers, a timer in an action body | `sequence.sfc` | [SFC](https://nautilus.joyautomation.com/languages/sfc/) |
| FBD: a `PID` closed loop, `SEL` chains, a hysteresis seal-in | `level.fbd` | [Function blocks](https://nautilus.joyautomation.com/languages/function-block/) |
| Ladder: interlocks, branches, a ladder-authored `FUNCTION_BLOCK` library called twice | `permissives.ld`, `lib/motor.ld` | [Ladder](https://nautilus.joyautomation.com/languages/ladder/) |
| ST: `TYPE`/`STRUCT` UDTs, a `VAR_IN_OUT` function block, plant physics | `lib/pump.st`, `lib/physics.st`, `sim.st` | [Structured Text](https://nautilus.joyautomation.com/languages/structured-text/) |
| Multiple tasks/scan rates, a project library in `lib/` | `nautilus.yaml` tasks: | [Function blocks, libraries, and tasks](https://nautilus.joyautomation.com/guides/blocks-and-tasks/) |
| Tag model: roles, units, descriptions, a UDT tag | `nautilus.yaml`/`field.yaml` tags: | [The tag model](https://nautilus.joyautomation.com/guides/tag-model/) |
| Modbus TCP: a device map, `naut modbus import`, a writable single bit | `devices.yaml`, `modbus_manifest.yaml`, `tags/modbus.yaml` | [Modbus TCP](https://nautilus.joyautomation.com/guides/modbus/) |
| Alarms: ISA-18.2 states, priorities, ack/shelve, explicit `defs:` | `alarms:` in both manifests | [Alarms](https://nautilus.joyautomation.com/guides/alarms/) |
| Retained state: setpoints and HOA modes survive a restart | `retain:` in both manifests | [Redundancy & retained state](https://nautilus.joyautomation.com/guides/redundancy/) |
| Online edits: warm-swap a running program from the editor | `server.online-edits` | [Online edits](https://nautilus.joyautomation.com/guides/online-edits/) |
| Acceptance tests: virtual time, `suspend:`, `advance:`/`until:`, alarm verbs | `lift-station_test.yaml` | [Testing](https://nautilus.joyautomation.com/reference/testing/) |
| HMI: a P&ID mimic (`*.mimic.json`), custom components + port sidecars | `lift-station.mimic.json`, `hmi/src/lib/*.component.json` | [HMI kit](https://nautilus.joyautomation.com/reference/hmi/) |
| HMI: faceplates, alarm banner/ack, live trend, driver status, scan diagnostics | `hmi/src/routes/+page.svelte` | [HMI kit](https://nautilus.joyautomation.com/reference/hmi/) |

## The control narrative

**Sequencing (`sequence.sfc`).** `PumpSequence` calls the lead pump
(`LeadReq`) once the level reaches `LeadOnLevel` (60 %), and calls the
standby in too (`LagReq`) if the level keeps climbing to `LagOnLevel`
(80 %) — both requests stay set until the level drops back through
`LagOffLevel` (55 %) then `LeadOffLevel` (30 %). Dropping below
`LeadOffLevel` doesn't stop the pump outright: it enters `PostRun`, a
fixed pump-down (`PostRunSec`) that finishes emptying the well before
the pump actually stops, running in parallel with `Alternate` — which
flips the duty pointer (`LeadIsP101`) so the *other* pump leads next
time, but only once `PostRun`'s timer has elapsed (a `P0`, not `P1`,
qualifier — see `docs/design/examples-dogfood.md` for why that distinction
matters). A lead pump that's commanded but never confirms running for
5 s (`FailToRun`, computed in the ladder) triggers an immediate
`Failover`: the standby takes the call this same scan, no waiting for a
post-run.

**Level and speed (`level.fbd`).** A single `PID` (`LIC-101`) turns wet-
well level into a speed reference (`SpeedRef`), targeting `LevelSP`
(45 %) and clamped to `[MinSpeedHz, MaxSpeedHz]`. It only runs closed-
loop while a pump is actually called (`AUTO := LeadReq`); otherwise it
free-wheels, bumpless, ready to resume. Each pump's own speed command
is zero when stopped, `SpeedRef` when running in Auto, and the operator's
`HandSpeedHz` when running in Hand — a small `SEL` chain, not a second
PID. A second seal-in latches `HighLevelAlm` at `HighLevelAlmSP` (90 %)
with 5 % of hysteresis, so it doesn't chatter sitting on the setpoint.

**Permissives and HOA (`permissives.ld`, `lib/motor.ld`).** Duty mapping
turns `LeadReq`/`LagReq` into a request per physical pump
(`P101_Req`/`P102_Req`) via the `LeadIsP101` pointer, with the high-high
float (`LSHH101`) OR'd into both regardless of the sequence or HOA mode
— except HOA **Off** still wins, because `MotorStarter`'s own `run` rung
never asserts on `Mode = 0`. Each pump's permissive is the AND of no
seal fail, no over-temp, no VFD fault and no low-low float; `MotorStarter`
(`lib/motor.ld`, a ladder-authored `FUNCTION_BLOCK`, instantiated once per
pump) turns `(Mode, request, permissive, running-feedback)` into
`(Run, InHand, FailToRun, LockedOut)`, with its own 5 s fail-to-run timer
and a trip counter: three fail-to-run trips latch `LockedOut`, blocking
`Run` (and `Avail`) until the operator pulses `ResetFaults` — which only
clears the trip count once `LockedOut` has actually latched, so an
ordinary fail-and-retry in between doesn't quietly reset the tally (see
`docs/design/examples-dogfood.md`).

**Stats and totals (`stats.st`).** `RuntimeMeter` (`lib/pump.st`, a
`VAR_IN_OUT` `FUNCTION_BLOCK`) accumulates each pump's starts and run
hours into a retained `PumpStats` UDT tag, called once per pump. A
running station flow totalizer integrates `FIT101_Flow` into
`StationFlowM3`.

**The bench plant (`sim.st`, `lib/physics.st`).** `WetWellModel` integrates
the wet well's level from an inflow/outflow balance (small on purpose —
5 m³ over the 4 m span, so a full cycle takes minutes); `VfdModel`
(instantiated twice) turns a run command and speed command into a
running feedback, a ramping speed, and a current draw, with a bench-only
fault-injection input. Inflow is `InflowLps` (12 L/s normally, a "storm"
is 40) plus a slow sinusoid, so the level never sits dead flat.

## Bench vs. field

`nautilus.yaml` is the bench build: `driver: { type: memory }` plus the
`sim` task, so `naut run .` cycles the whole plant with nothing wired up.
`field.yaml` is the same control programs (`sequence.sfc`, `level.fbd`,
`permissives.ld`, `stats.st` — byte-identical) pointed at real VFDs and a
remote-I/O rack over Modbus TCP instead: no `sim` task, and one extra
program, `field_map.st`, that exists only in this manifest — the device
map's importer can't produce a few of the tag model's bare names (see
`devices.yaml`'s header and `docs/design/examples-dogfood.md`), so this
small program renames the Modbus-imported `RIO_*` tags onto the
canonical ones every other program reads.

`retain: { file: retain.json }` means a stray `retain.json` left over from
an earlier run changes the bench's (or field's) start state on the next
`naut run`/`naut test` — setpoints and HOA modes load from it, not from
the manifest's `init:` values, the moment it exists. `.gitignore` already
excludes it; delete it to get back to the manifest's declared start state.

## Run it on real VFDs

Two terminals — a `naut modbus serve` slave standing in for the VFDs and
the remote I/O, then the field manifest polling it:

```sh
naut modbus import --map devices.yaml   # regenerate the committed manifest + tag file

# terminal 1
naut modbus serve --manifest modbus_manifest.yaml --values seed.json

# terminal 2
naut run -m field.yaml .
```

`curl localhost:8080/api/state` shows the driver connected to all three
sources (`P101`, `P102`, `RIO`) and the seeded values flowing through
`field_map.st` onto `LIT101_Level`, `FIT101_Flow`, `LSHH101`, `LSLL101`,
and the two pumps' seal-fail/over-temp contacts. A write — e.g. putting
`P101_Mode` to `1` (Hand) from the dashboard — reaches the slave: read
it back with `naut modbus browse -host 127.0.0.1 -port 5020 -unit 1
-table holding -from 2100 -count 2` and see the control-word bit and the
scaled speed register change.

Point `devices.yaml`'s instances at real hosts and it's the same command
against real drives.

The slave stood up by `naut modbus serve` doesn't emulate the drive — it
just holds whatever registers `seed.json` gives it, so writing `RunCmd`
doesn't make `Running` come back true on its own. `MotorStarter`'s own
fail-to-run timer (`motor.ld` → `lib/motor.ld`) doesn't know the
difference: if a pump is called (by the sequence, or Hand) and the slave's
`Running` bit hasn't gone true within 5 s, `FailToRun` latches, same as a
real stuck pump. Two ways to keep that 5 s window from tripping on a
bench: put P-101 in Hand and write its `Running` register back with
`naut modbus browse` before the timer elapses, or start it already true —
`seed.json` seeds `P101_Running: true` for exactly this, a static bench
where the point is exercising reads/writes, not a full run simulation.

## The operator screen

`hmi/` is a SvelteKit app built on the published
[`@joyautomation/nautilus-hmi`](https://www.npmjs.com/package/@joyautomation/nautilus-hmi)
kit (0.6.0) — one page, not a catalog: the wet-well mimic, an alarm banner
with an ack-all button, two pump faceplates (HOA selector, running/speed/
amps, starts/run-hours from `P101_Stats`/`P102_Stats`, fail-to-run/locked-
out pills, a Reset faults button), the five level setpoints
(`LeadOnLevel`/`LagOnLevel`/`LagOffLevel`/`LeadOffLevel`/`LevelSP`) as
number fields with write-back, a level trend, the field-driver status
panel (honestly empty here — this project runs the loopback/Modbus
drivers, not EtherNet/IP or Sparkplug), and scan diagnostics. It follows
`examples/hmi-demo`'s pattern; that example's README is the deeper
reference for the mimic format, custom components, faceplates and the
dev proxy.

```sh
cd hmi
npm install
npm run dev             # http://localhost:5173, proxying /api to :8080 —
                         # run `naut run .` in the project root first
```

To see it served by the controller itself (no proxy, one origin, exactly
how it ships):

```sh
cd hmi && npm run build      # -> hmi/build (adapter-static)
cd .. && naut run .           # server.hmi: hmi/build serves it at "/";
                               # the built-in dashboard moves to /_nautilus/
```

`server.hmi` tolerates the build directory being absent (`naut check`
and a fresh `naut run` before the first `npm run build` don't fail —
a request against a missing build just 404s), so both manifests set it
unconditionally.

### The mimic

`lift-station.mimic.json` lives at the **project root**, not under
`hmi/src/routes/` — the VS Code mimic editor discovers any `*.mimic.json`
anywhere in the workspace, and this way the SvelteKit app and the editor
share exactly one file (`+page.svelte` imports it three directories up).
That import crosses `hmi/`'s own project boundary, which Vite's dev
server refuses by default (files outside its root 403 the moment
anything tries to read/transform them) — `hmi/vite.config.ts` adds
`server.fs.allow` for the parent directory to fix it; see the dogfood log
for the friction. Open the file itself with **Open With → Mimic Editor**.

The wet well (`WW101`, a built-in `Tank`) is bound to `LIT101_Level`; two
float switches (`LSHH101`/`LSLL101`, both instances of the custom
`FloatSwitch` component below) sit at the high-high/low-low marks; P-101
and P-102 (both instances of the custom `SubmersiblePump` component) sit
submerged near the bottom, each with one `discharge` port on top; each
discharge line runs through a check valve (`CV101`/`CV102`, the built-in
`Valve`) to a `ToProcess`-style off-page connector labeled "FORCE MAIN".
Pipe runs carry `flowing` bound to `P101_Running`/`P102_Running`; labels
show `LIT101_Level` (%) and `FIT101_Flow` (L/s). `SeqStep` is a **string**
tag (`'Idle'`/`'Lead'`/`'Lag'`/…) — the kit's built-in `MimicLabel.bind`
only formats numeric tags, so it rides a small custom `SeqStepTag`
component instead, bound the same way an equipment prop is (`bind: {
text: "SeqStep" }`), which passes any tag value through untouched.

### Custom components

Three components live in `hmi/src/lib/`, each with a
`{Name}.component.json` port sidecar so the extension's **Nautilus: Edit
Component Ports…** command (and the mimic editor's `p` shortcut on a
selected instance) has something to edit. The mimic editor's equipment
palette lists a custom component whether or not it has a sidecar — a
sidecar is still what gives an instance ports to wire a pipe to, so a
ports-free drop from the palette is a component with nothing to connect
until one is added:

- **`SubmersiblePump.svelte`** — `running`/`speedHz`-driven, one
  `discharge` port on top (`dir: "up"`). Because a custom component's
  ports live in its sidecar, which is an editor-time convenience that
  never ships with the built app, the mimic doc also carries the SAME
  port as an inline `ports` override on each `P101`/`P102` equipment
  entry — the same trick `examples/hmi-demo` uses for `HeatExchanger`.
  Skipping that (sidecar only, no inline override) is what a pipe with no
  visible run looks like in the built app: the discharge pipes silently
  collapse to a stub at the check valve with no line down to the pump —
  see the dogfood log.
- **`FloatSwitch.svelte`** — one component, two mimic instances
  (`LSHH101`/`LSLL101`), a `tripped` prop swinging the float's pivot arm.
  No pipe anchors it, so its sidecar's one port (`mount`) exists only so
  the extension has something to open — nothing binds to it.
- **`ToProcess.svelte`** (the force-main connector) — one `in` port on
  its left edge (`dir: "left"`), matching the same port the mimic doc
  already carries inline on the `TOPROC` equipment entry. Same idiom as
  `examples/hmi-demo`'s `Supply`/`ToProcess`.

`SeqStepTag.svelte` (the `SeqStep` readout) is custom too, but ports-free
and unanchored — nothing pipes to a text label, so it has no sidecar.
