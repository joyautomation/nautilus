# go-sdk — the advanced path

nautilus's one Go-tier example. Everywhere else in `examples/` the plant is a
manifest project — ST/FBD/LD/SFC, run with `naut run`, no Go toolchain. This
one is a plain Go program that embeds the same kind of IEC 61131-3 control
logic and hosts it itself, because sometimes you need real code around the
runtime: a stateful field protocol, a simulation richer than a manifest's
`sim.st` can express cleanly, or logic that genuinely doesn't fit a
declarative project file.

**Default to the manifest.** If you don't need Go, run `naut new` and pick
the manifest form — see the no-Go examples (the lift-station-style projects)
for that path. Reach for this one when you've hit something the manifest
can't do.

## What it is

A heated surge tank: one tank, one pump, one heater.

- `program.st` — the control: a level hysteresis latch (pump seal-in) and a
  temperature PI loop with an anti-windup clamp. Plain Structured Text — the
  graphical languages (FBD/LD/SFC) are the manifest examples' job.
- `plant.go` — the physics, as a nautilus `io.Driver`. It consumes the
  controller's outputs (`PumpRun`, `Heater`) and produces the field inputs
  (`LevelPct`, `TempC`) from a small mass/energy balance. A real field-bus
  driver (Modbus, EtherNet/IP, OPC-UA) has this exact shape: `ReadInputs`
  reports the transmitters, `WriteOutputs` applies the commands, and
  `program.st` never knows the difference.
- `main.go` — wires them together and serves the tag API.
- `program_test.go` — the acceptance test, in virtual time.

## The four seams

The public API is the seams. You implement small interfaces; nautilus
provides a reference implementation of each and never requires it:

| Interface | Plugs in as | This example |
|---|---|---|
| `io.Driver` | `runtime.Options.Driver` | `plant.go` — the whole point of this example |
| `retain.Store` | `runtime.Options.Retain` | not wired here — operator setpoints just live in memory for the demo; add a `retain.File` for restart survival |
| `runtime.Coordinator` | `runtime.Options.Coordinator` | not wired here — nil means "always leader," which is correct for a single instance; add a `leader.Elector` for redundant pairs |
| `hist.Sink` | consumed by the `naut historian` collector, not `runtime.Options` | not wired here — the historian runs as its own process against a project's tag API |

## Run it

```sh
go run ./examples/go-sdk
```

```
nautilus · go-sdk (heated tank) — tag API on http://localhost:8080 — Ctrl+C to stop
level  60.0%  temp  60.0°C  pump off  heater  61%  scans 9
level  59.9%  temp  60.0°C  pump off  heater  62%  scans 19
level  59.9%  temp  60.0°C  pump off  heater  62%  scans 29
```

`GET /api/state` gives the same picture as JSON:

```json
{"ts":1790386853452,"scans":38,"tags":{"Heater":63.23,"Ki":0.15,"Kp":12,
"LevelPct":59.84,"PumpRun":false,"PumpStartLevel":40,"PumpStopLevel":75,
"ScanDtS":0.100246183,"TempC":59.97,"TempSP":65}, ...}
```

## Open it in VS Code

Same experience as a manifest project: open `program.st` with the nautilus
IEC 61131-3 extension installed and, while `go run ./examples/go-sdk` is
running, you get inline live tag values next to each `VAR_EXTERNAL`, exactly
as if this were `naut run`. `OnlineEdits` is on, so "Download Program to
Controller," the running-vs-workspace diff, and rollback all work here too —
the extension talks to the tag API, not to `naut`, so it can't tell this is
hand-rolled Go underneath.

## Test it

```sh
go test ./examples/go-sdk/...
```

`program_test.go` is what the Go tier has instead of a manifest project's
`*_test.yaml` acceptance suites: it drives the same `acceptance` package
`naut test` uses — a virtual clock and a `Scheduler` that lands scans
exactly on each tick — so a settling time measured in simulated minutes
checks in milliseconds of wall-clock CPU. It asserts the pump seal-in
(hysteresis, by scan) and the PI loop settling to a setpoint step (by
simulated duration), both against the real `Plant`, not a re-implementation
of its physics.

## When you've outgrown this too

`naut new` scaffolds this same shape — pick the **SDK** template for a bare
`io.Driver` stub to fill in, or **SDK demo** for this plant model as a
starting point:

```sh
naut new
```
