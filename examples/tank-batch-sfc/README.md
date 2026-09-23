# tank-batch-sfc — the SFC toolchain, end to end

A simulated batch tank, but the control logic is an IEC 61131-3
**Sequential Function Chart** (`program.sfc`) — nautilus's fourth and last
IEC language, alongside ST, FBD, and LD. Where FBD and LD are *dataflow*
languages, SFC is a *coordination* language: a chart of **steps** connected
by **transitions**, with **actions** (`N`/`S`/`R`/`P1`, …) driving outputs
while a step is active. The chart transpiles to ST through `lang/sfc` at
startup and runs on the same runtime as every other language — one
compiler, four source languages.

## The chart

`program.sfc` is the `docs/design/sfc.md` §1.2 worked example, wired up to
cycle for real against a simulated plant instead of sitting as prose:

```
Idle ──(Start AND NOT Abort)──▶ Fill ──(Level ≥ FillSP)──▶ ┬─▶ Heat ─┐
  ▲                              │                          └─▶ Mix ──┤
  │                       (Abort)│                                    │
  │                              ▼                    (TempC ≥ HeatSP AND
  │                           Aborted                   mix timer done)
  │                              │                                    ▼
  └───(NOT Abort)────────────────┘                                  Drain
  ▲                                                                    │
  └────────────────────────(Level ≤ EmptySP, BatchCount++)────────────┘
```

- **`Fill`** opens `FillValve` until the level reaches `FillSP`.
- Reaching `FillSP` is a **simultaneous divergence** (`FROM Fill TO (Heat,
  Mix)`): the token splits and `Heat` and `Mix` run concurrently. `Heat`
  runs the heater as a real thermostat (drops out at `HeatSP` rather than
  cooking the batch for the rest of the mix time); `Mix` runs the mixer and
  a `T#3S` timer (shortened from the doc's `T#30S` so a batch cycles in
  seconds, not half a minute).
- A **simultaneous convergence** (`FROM (Heat, Mix) TO Drain`) only fires
  once *both* legs finish — `TempC >= HeatSP` **and** the mix timer's `Q`.
- **`Drain`** opens `DrainValve` until the level reaches `EmptySP`, pulsing
  `BatchCount` up by exactly one (`P1`, a rising-edge pulse — the body runs
  once, the scan `Drain` activates) before returning to `Idle`.
- `Fill`'s **alternative divergence** carries the abort path: `Fill ->
  Aborted` (declared first, so abort wins any race) versus `Fill -> (Heat,
  Mix)` on level. `Aborted` is a small addition beyond the doc's minimal
  example, added so the `S`/`R` qualifier pair on `AbortLamp` does
  something observable: `S AbortLamp` latches on entry to `Aborted`,
  `R AbortLamp` clears it back in `Idle`. `Abort` is a real, writable tag —
  see "Poke the plant" below — but the auto-operator never asserts it, so
  `Aborted` is reachable but not visited in the unattended demo.

Every qualifier used is slice 1 (`N`, `S`, `R`, `P1`) — the set
`naut sfc check` currently supports.

## Run it

```sh
go run ./examples/tank-batch-sfc
# port 8080 taken? pick another and point the extension at it:
#   NAUTILUS_API=localhost:8081 go run ./examples/tank-batch-sfc
```

No operator needed: `plant.go` watches its own field outputs and asserts
`Start` whenever the batch is idle (all four field outputs off), clearing
it the instant the chart leaves `Idle` and energizes `FillValve` — the
batch cycles forever unattended. You should see `BatchCount` climb by
exactly one every cycle, level and temperature ramp and fall between
`EmptySP`/`FillSP` and ambient/`HeatSP`, and the console line's `step`
column show `Heat+Mix` during the simultaneous branch.

Open <http://localhost:8080> for the built-in dashboard: PLC-style scan
diagnostics and a live tag table with the descriptions/units this example
registers via `runtime.Options.Tags`. `GET /api/state` also exposes the
chart's retained step-activity slots (`_S_Idle_X`, `_S_Fill_X`,
`_S_Heat_X`, `_S_Mix_X`, `_S_Drain_X`, `_S_Aborted_X`, …) inside `locals` —
the same watch surface a graphical editor's inline live values read, so you
can literally see the token move scan to scan.

## Try the SFC tooling (nautilus CLI; VS Code editor 0.9.7, may still be shipping)

```sh
naut sfc check examples/tank-batch-sfc/program.sfc   # structural checks (§5.1)
naut check examples/tank-batch-sfc                    # + the ST-level hop
naut sfc graph examples/tank-batch-sfc/program.sfc | jq .   # render model
```

The graph model reports each transition's derived `kind` —
`normal`/`alt`/`simDiverge`/`simConverge` — which is what a graphical
editor uses to draw the chart's divergence/convergence bars instead of
plain arrows. A VS Code SFC diagram preview and structural-op editor
(mirroring the FBD/LD graphical editors) is the next piece of tooling to
land on top of this same render model.

Online edits are on (`server.Options{OnlineEdits: true}`): change the
chart while it runs and download it back — step activity, the mix timer,
and the `S`/`R` abort latch are all retained VAR slots, so **the live token
survives the swap** (docs/design/sfc.md §2.7). Renaming a step is the
honest exception: a renamed step's activity slot is a new name, so its
token resets — surfaced in the swap report the same way an FB instance
rename resets today.

## Poke the plant

```sh
curl -X POST localhost:8080/api/tags -d '{"name": "HeatSP", "value": 80}'
```

`Abort` only takes effect while `Fill` is the active step (`t_abort FROM
Fill`) — `t_start`'s guard is `Start AND NOT Abort`, so setting it while
`Idle` is active just blocks re-entry rather than driving `Aborted`. Watch
`GET /api/state`'s `locals._S_Fill_X` and fire it the moment `Fill` goes
true:

```sh
curl -X POST localhost:8080/api/tags -d '{"name": "Abort", "value": true}'   # while Fill is active -> Aborted
curl -X POST localhost:8080/api/tags -d '{"name": "Abort", "value": false}'  # -> t_resume back to Idle
```
