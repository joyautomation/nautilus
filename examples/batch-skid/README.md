# batch-skid — recipes, phases, and a line's own EtherNet/IP handshake

A two-ingredient batch mixing skid. Generic batching pattern, deliberately
small — no client names, tag conventions or logic. A jacketed mix tank
MT-201 (500 L) is charged from two dosing lines, A and B, each with a
flow meter (FIT-201A/B) and a dosing valve (XV-201A/B). An agitator
AG-201 runs during the charge and the mix, for recipes that call for
one. Hot water on the jacket, through control valve TCV-201, brings the
batch to its recipe temperature (TT-201, a PID loop); a hold timer runs;
then transfer pump P-202 sends the batch downstream through XV-203 to
**the line** — an existing Logix PLC that owns the receiving tank and
answers a ready/accept handshake over EtherNet/IP. A CIP flush follows
every third batch. The operator picks one of three recipes, presses
Start, and can Hold, Resume or Abort.

```sh
naut check .                     # validate the bench build
naut check -m line.yaml .        # validate the line build
naut test .                      # the acceptance suite, virtual time
naut run .                       # dashboard + tag API on http://localhost:8080
naut build .                     # one deployable controller binary
```

**Needs naut >= 0.13.0.** `naut eip` shipped in v0.12.0 already; it's
`naut logix emulate` and `lib/` composition that are on `main` but not in
that release — build the CLI from source until 0.13.0 ships.

## What to open first

- `phases.sfc` — the batch sequence. Right-click → *Open With → SFC
  Diagram* to see the simultaneous divergence into `(ChargeA, ChargeB,
  Agitate)`, the matching convergence into `Heat`, the alternative
  Hold/Resume branch, and the Abort branch reachable from every active
  phase. Every qualifier: `N`, `S`, `R`, `P1`, `P`, `P0`.
- `dosing.fbd` — the two dosing-line totalizers and the jacket PID
  (TIC-201). *Open With → FBD Diagram*.
- `transfer.ld` — the line handshake interlocks, the transfer pump's
  seal-in permissive, the CIP-due check, and the handshake-timeout
  alarm. *Open With → Ladder Diagram*.
- `line/Line.L5X` — the "existing line" PLC, read-only. *Open With →
  Ladder Diagram*; see `line/README.md` for what it is and how to diff
  it between revisions.
- `lib/recipes.st` / `lib/physics.st` — the recipe/batch UDTs and the
  bench-only plant model, in Structured Text.
- `batch-skid.mimic.json` — the P&ID: MT-201, both dosing valves, the
  agitator, the transfer pump and valve, and labels for the recipe name,
  phase, and batch count, built entirely from the HMI kit's built-in
  components (no `hmi/` app here — see lift-station for that shape
  instead). Right-click → *Open With → Mimic Editor*; run `naut run .`
  alongside it and the bindings go live. Kit labels only format a
  *numeric* bind (`Mimic.svelte`'s own `toFixed`), so the `Active.Name`
  and `Batch.Phase` labels — both `STRING` tags — render as the kit's
  usual `—` rather than the text today; `Batch.Count` (an `INT`) reads
  live. Left the two string binds in anyway, since the tags genuinely
  exist and the gap is in the kit's `Label`, not this project — drop them
  if that's confusing before the kit grows a text-bind mode. `IdleLamp`,
  `HoldDone`, and `Line_Status` (a struct, and line-only — it isn't
  declared on the bench build at all) aren't represented here; everything
  else the skid's own field values cover is bound to something on the
  canvas.

## What it demonstrates

| Feature | Where | Docs |
|---|---|---|
| SFC: simultaneous divergence/convergence, an alternative Hold/Resume branch, an Abort branch off every active phase, every qualifier (`N`/`S`/`R`/`P1`/`P`/`P0`) | `phases.sfc` | [SFC](https://nautilus.joyautomation.com/languages/sfc/) |
| FBD: totalizers, a `PID` closed loop, a hysteresis seal-in | `dosing.fbd` | [Function blocks](https://nautilus.joyautomation.com/languages/function-block/) |
| Ladder: handshake interlocks, a seal-in permissive, a compare contact, a `TON`-gated alarm | `transfer.ld` | [Ladder](https://nautilus.joyautomation.com/languages/ladder/) |
| ST: `TYPE`/`STRUCT` UDTs, a `FUNCTION_BLOCK` with a `VAR_OUTPUT` array pin, `FUNCTION`, plant physics | `lib/recipes.st`, `lib/physics.st`, `sim.st`, `status.st` | [Structured Text](https://nautilus.joyautomation.com/languages/structured-text/) |
| Multiple tasks/scan rates, `lib/` shared code | `nautilus.yaml`/`line.yaml` tasks:, `lib/` | [Function blocks, libraries, and tasks](https://nautilus.joyautomation.com/guides/blocks-and-tasks/) |
| Tag model: roles, units, a UDT tag, shared tag files | `nautilus.yaml`/`line.yaml` tags:, `tags/` | [The tag model](https://nautilus.joyautomation.com/guides/tag-model/) |
| EtherNet/IP: `naut eip import` against a live Logix controller, UDTs as ST types, scan classes | `line.yaml`, `eip_manifest.yaml`, `eip_types.st`, `tags/eip.yaml` | [EtherNet/IP](https://nautilus.joyautomation.com/guides/ethernet-ip/) |
| Rockwell `.L5X`: a hand-written export, opened read-only, diffed between two git revisions | `line/Line.L5X` | [Logix](https://nautilus.joyautomation.com/guides/logix/), [diffing between revisions](https://nautilus.joyautomation.com/guides/program-history/) |
| Alarms: ISA-18.2 states, priorities, `enable:`, explicit `defs:` | `alarms:` in both manifests | [Alarms](https://nautilus.joyautomation.com/guides/alarms/) |
| Retained state: recipe select and tuning survive a restart | `retain:` in both manifests | [Redundancy & retained state](https://nautilus.joyautomation.com/guides/redundancy/) |
| Acceptance tests: virtual time, `suspend:`, `advance:`/`until:`, alarm verbs | `batch-skid_test.yaml` | [Testing](https://nautilus.joyautomation.com/reference/testing/) |

## The control narrative

**Sequencing (`phases.sfc`).** `BatchSequence` copies the selected recipe
(`Active := recipes.Recipes[RecipeSel]`) and counts the batch on `Start`,
then diverges into three simultaneous branches: `ChargeA` and `ChargeB`
(each opens its own dosing valve; `dosing.fbd` totalizes the flow into
`Batch.ChargedA_L`/`ChargedB_L`) and `Agitate` (bare `N AgitateReq` —
true only while `Agitate`, and later `Heat`/`Hold`, are active; an
unconditional `S` can't express "only for recipes that agitate", so
`transfer.ld` ANDs `AgitateReq` with the recipe's own `Active.Agitate`
flag to get `AG201_Cmd`, rather than commanding it from here at all).
Each dosing branch waits in a "done" step once its target volume is
reached; the convergence back to one token — `Heat` — fires only once
both charges and the agitate branch have all reached that point. `Heat`
runs until the jacket reaches setpoint, `Hold` runs a fixed dwell, and
`Transfer` asks for a transfer (`TransferReq`) without ever commanding
the pump, the agitator, or the line directly — the same division
lift-station keeps between its sequence and its permissives.
`AgitateReq` isn't asserted in `Transfer`, `Cip`, `Held`, or `Aborted`,
so `AG201_Cmd` drops the moment the batch leaves `Heat`/`Hold`, with
nothing left to reset explicitly. `Hold` (the operator command, not the
step) diverts either active phase to `Held`, ahead of the normal
progression in declaration order, and parks there until `Resume`;
`Abort` similarly diverts every active phase (excepting the brief window
between one dosing line finishing and the other/the agitator settling —
see `docs/design/examples-dogfood.md` for why) straight to `Aborted`,
which latches `AbortLamp` and waits for the tank to drain before
returning to `Idle`.

**Dosing and temperature (`dosing.fbd`).** Two totalizers integrate
`FIT201A_Flow`/`FIT201B_Flow` into `Batch.ChargedA_L`/`ChargedB_L`,
held at zero for the one scan `Prep` sets `ResetTotals`. A single `PID`
(TIC-201) turns jacket temperature into `TCV201_Pos`, reverse acting (the
PID doc's own "heater" case) and gated `AUTO` on `HeatingActive` — a
state tag `N`-qualified identically from both `Heat` and `Hold` in
`phases.sfc`, which OR-combine onto one tag the same way an SFC's own
N-qualified "running" lamp does (see `examples/tank-batch-sfc`'s
`RunLamp`), since a step's activity flag isn't otherwise readable from
another task. `AgitateReq` (also on `Agitate`, `Heat`, and `Hold`) uses
the same combine to gate `AG201_Cmd` in `transfer.ld`. A hysteresis
seal-in latches `OvertempAlm` five degrees above setpoint, clearing five
below it.

**The line handshake (`transfer.ld`).** `P202_Permissive` is the line
ready, not faulted, and the transfer valve confirmed open; `P202_Cmd`
seals in on `TransferReq AND P202_Permissive` and holds until the tank
drains to 2 %. `CipDue` is a plain `MOD(Batch.Count, CipEveryN) = 0`
compare contact. A `TON` alarms `HandshakeTimeout` if the line never
accepts a request within 30 s.

**Batch bookkeeping (`status.st`).** Mirrors the sequence's phase name
into `Batch.Phase`, accumulates `Batch.ElapsedSec` since the batch left
`Idle`, and stamps `Line_BatchId` on a rising edge of `Line_Request` — a
"move a value on an edge" that ladder can't express directly (coils only
assign `BOOL`), so it lives here instead of `transfer.ld`.

**The bench plant (`sim.st`, `lib/physics.st`).** `JacketedTank`
integrates level from a dose-in/transfer-out balance and temperature
toward the jacket supply at a rate set by the valve position;
`DoseLine` (two instances) models 20 L/min flow with a 0.5 s valve-travel
delay and feedback. `sim.st` also stands in for **the line**: ready while
its own receiving tank has headroom, accepting a request after a 2 s
settling delay — the same shape `line/Line.L5X`'s own `Receive` routine
uses, so the fully autonomous bench demo and the (protocol-only) emulated
line agree on the narrative even though only one of them actually runs
the ladder (see `line/README.md`).

## Bench vs. line

`nautilus.yaml` is the bench build: `driver: {type: memory}` plus the
`sim` task, so `naut run .` cycles the whole plant — including the line's
side of the handshake — with nothing wired up. `line.yaml` is the same
control programs (`phases.sfc`, `dosing.fbd`, `transfer.ld`, `status.st`
— byte-identical) pointed at a real Logix line controller over
EtherNet/IP instead: no `sim` task, `tags/eip.yaml` (generated) supplies
`Line_Ready`/`Line_Accept`/`Line_TankLevel`/`Line_Fault`/`Line_Status` as
inputs and `Line_Request`/`Line_BatchId` as outputs. The skid's own field
values (`LT201_Level`, `TT201_Temp`, `FIT201*_Flow`, and the valve/pump
feedbacks) stay `role: state` in both manifests — this example's
EtherNet/IP story is specifically the line handshake, so `line.yaml`
doesn't wire a second, local I/O driver for the skid's own instruments; a
reasonable follow-up, not built here (see
`docs/design/examples-dogfood.md`).

## Run it against the emulated line PLC

Two terminals — `naut logix emulate` standing in for the line, then the
line manifest polling it:

```sh
# terminal 1
naut logix emulate --l5x line/Line.L5X --listen 127.0.0.1:44818

# terminal 2
naut run -m line.yaml .
```

`curl localhost:8080/api/drivers` shows the `eip` driver connected,
polling 7 tags with 0 poll errors. `curl localhost:8080/api/state` shows
`Line_Ready`/`Line_TankLevel`/`Line_Status` reading straight from the
emulator's own seeded values. Run the sequence far enough to reach
`Transfer` (drive `Batch.ChargedA_L`/`ChargedB_L`/`TT201_Temp` directly
if you don't want to wait out the dosing lines with no `sim` task
running) and `Line_Request` goes `TRUE` — reachable independently of this
project's own driver with a second read (`naut eip browse --host
127.0.0.1`, or any CIP client against port 44818).

**What the emulator does and doesn't do.** It serves the tag surface —
every tag `Receive` declares, at the export's own initial values — and
writes land in its tag store, so the round trip above genuinely proves
the wire protocol. It does **not** execute `Receive`'s ladder: nothing on
the emulator side ever runs `Receive`'s `TON` or flips `Line_Accept` on
its own, confirmed by reading `AcceptTmr`'s accumulator directly off the
emulator and watching it sit at `0` indefinitely. For a fully autonomous
demo, run the bench build (`nautilus.yaml`), whose `sim.st` answers the
handshake itself; against the emulator, seeing `Line_Accept` respond
means writing it directly (standing in for what a real controller's own
`Receive` would have done) or pointing `line.yaml`'s `host:` at a real
line controller. See `line/README.md` and
`docs/design/examples-dogfood.md` for the finding.

## The line's own program

`line/Line.L5X` is a hand-written Rockwell export — controller
`LineController`, the handshake tags, one UDT (`LineStatus`), and one
ladder routine (`Receive`) — checked in **read-only**: this project only
ever talks to it over the wire, never edits it. Open it in the ladder
editor (right-click → *Open With → Ladder Diagram*) the same as any
`.ld` file. It has two commits in this project's history — the second
adds a high-level cutoff interlock rung — so **nautilus: Diff Ladder
Diagram (between git revisions…)** has something to show: pick the two
commits and the added rung renders in cyan against the four that didn't
change. See `line/README.md` for the routine's own shape and the two
revisions' exact diff.
