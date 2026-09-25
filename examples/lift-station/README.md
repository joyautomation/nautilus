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

## What to open first

- `sequence.sfc` — the duty/standby state machine. Right-click →
  *Open With → SFC Diagram* to see the alternative divergence out of
  `Lead` (failover, lag, or post-run — priority by declaration order)
  and the simultaneous divergence into `(PostRun, Alternate)`.
- `level.fbd` — the level→speed PID and the high-level alarm seal-in.
  *Open With → FBD Diagram*.
- `permissives.ld` and `motor.ld` — HOA, interlocks, the high-high
  override, and the `MotorStarter` ladder library block, instantiated
  once per pump. *Open With → Ladder Diagram*.
- `sim.st` / `physics.st` — the bench-only plant: a wet-well level
  integrator and two pump/VFD models, in Structured Text.

## What it demonstrates

| Feature | Where | Docs |
|---|---|---|
| SFC: alternative + simultaneous divergence, `N`/`S`/`R`/`P1`/`P0` qualifiers, a timer in an action body | `sequence.sfc` | [SFC](https://nautilus.joyautomation.com/languages/sfc/) |
| FBD: a `PID` closed loop, `SEL` chains, a hysteresis seal-in | `level.fbd` | [Function blocks](https://nautilus.joyautomation.com/languages/function-block/) |
| Ladder: interlocks, branches, a ladder-authored `FUNCTION_BLOCK` library called twice | `permissives.ld`, `motor.ld` | [Ladder](https://nautilus.joyautomation.com/languages/ladder/) |
| ST: `TYPE`/`STRUCT` UDTs, a `VAR_IN_OUT` function block, plant physics | `pump.st`, `physics.st`, `sim.st` | [Structured Text](https://nautilus.joyautomation.com/languages/structured-text/) |
| Multiple tasks/scan rates, a project library composed at the root | `nautilus.yaml` tasks: | [Function blocks, libraries, and tasks](https://nautilus.joyautomation.com/guides/blocks-and-tasks/) |
| Tag model: roles, units, descriptions, a UDT tag | `nautilus.yaml`/`field.yaml` tags: | [The tag model](https://nautilus.joyautomation.com/guides/tag-model/) |
| Modbus TCP: a device map, `naut modbus import`, a writable single bit | `devices.yaml`, `modbus_manifest.yaml`, `tags/modbus.yaml` | [Modbus TCP](https://nautilus.joyautomation.com/guides/modbus/) |
| Alarms: ISA-18.2 states, priorities, ack/shelve, explicit `defs:` | `alarms:` in both manifests | [Alarms](https://nautilus.joyautomation.com/guides/alarms/) |
| Retained state: setpoints and HOA modes survive a restart | `retain:` in both manifests | [Redundancy & retained state](https://nautilus.joyautomation.com/guides/redundancy/) |
| Online edits: warm-swap a running program from the editor | `server.online-edits` | [Online edits](https://nautilus.joyautomation.com/guides/online-edits/) |
| Acceptance tests: virtual time, `suspend:`, `advance:`/`until:`, alarm verbs | `lift-station_test.yaml` | [Testing](https://nautilus.joyautomation.com/reference/testing/) |
| HMI: a P&ID mimic (`*.mimic.json`), custom components + port sidecars | `lift-station.mimic.json`, `hmi/src/lib/*.component.json` | [HMI kit](https://nautilus.joyautomation.com/guides/hmi/) |
| HMI: faceplates, alarm banner/ack, live trend, driver status, scan diagnostics | `hmi/src/routes/+page.svelte` | [HMI kit](https://nautilus.joyautomation.com/guides/hmi/) |

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

**Permissives and HOA (`permissives.ld`, `motor.ld`).** Duty mapping
turns `LeadReq`/`LagReq` into a request per physical pump
(`P101_Req`/`P102_Req`) via the `LeadIsP101` pointer, with the high-high
float (`LSHH101`) OR'd into both regardless of the sequence or HOA mode
— except HOA **Off** still wins, because `MotorStarter`'s own `run` rung
never asserts on `Mode = 0`. Each pump's permissive is the AND of no
seal fail, no over-temp, no VFD fault and no low-low float; `MotorStarter`
(`motor.ld`, a ladder-authored `FUNCTION_BLOCK`, instantiated once per
pump) turns `(Mode, request, permissive, running-feedback)` into
`(Run, InHand, FailToRun, LockedOut)`, with its own 5 s fail-to-run timer
and a trip counter: three fail-to-run trips latch `LockedOut`, blocking
`Run` (and `Avail`) until the operator pulses `ResetFaults` — which only
clears the trip count once `LockedOut` has actually latched, so an
ordinary fail-and-retry in between doesn't quietly reset the tally (see
`docs/design/examples-dogfood.md`).

**Stats and totals (`stats.st`).** `RuntimeMeter` (`pump.st`, a
`VAR_IN_OUT` `FUNCTION_BLOCK`) accumulates each pump's starts and run
hours into a retained `PumpStats` UDT tag, called once per pump. A
running station flow totalizer integrates `FIT101_Flow` into
`StationFlowM3`.

**The bench plant (`sim.st`, `physics.st`).** `WetWellModel` integrates
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

Two components live in `hmi/src/lib/`, each with a `{Name}.component.json`
port sidecar so the extension's **Nautilus: Edit Component Ports…**
command (and the mimic editor's `p` shortcut on a selected instance) has
something to edit:

- **`SubmersiblePump.svelte`** — `running`/`speedHz`-driven, one
  `discharge` port on top (`dir: "up"`). Because a custom component's
  ports live in its sidecar, which is an editor-time convenience that
  never ships with the built app, the mimic doc also carries the SAME
  port as an inline `ports` override on each `P101`/`P102` equipment
  entry — the same trick `examples/hmi-demo` uses for `HeatExchanger`.
  Skipping that (sidecar only, no inline override) is what a pipe with no
  visible run looks like in the built app: leaving it out was the first
  cut here, and the discharge pipes silently collapsed to a stub at the
  check valve with no line down to the pump — see the dogfood log.
- **`FloatSwitch.svelte`** — one component, two mimic instances
  (`LSHH101`/`LSLL101`), a `tripped` prop swinging the float's pivot arm.
  No pipe anchors it, so its sidecar's one port (`mount`) is there purely
  so the extension has something to open, per the review's ask.

`ToProcess.svelte` (the force-main connector) and `SeqStepTag.svelte` (the
`SeqStep` readout) are custom too, but ports-free — same idiom as
`hmi-demo`'s `Supply`/`ToProcess`, with the port declared inline in the
doc instead of a sidecar.
