# booster-1 — a pressure-boosting station with a live Modbus device

Two pumps under one pressure PID (`pressure.fbd`): `Pump1` (lead) always
runs; `Pump2` (lag) joins on flow demand, with hysteresis, mirroring the
lead's speed while it's in. Unlike the two well sites, this station also
carries one real field device — a discharge flow meter over Modbus TCP —
whose tags ride along the Sparkplug B publish as a DEVICE (`plc1`).
Publishes to the `Water` group as edge node `Booster1`.

```sh
naut check .    # validate offline
naut test  .    # the acceptance suite, virtual time
naut run   .    # dashboard + tag API on http://localhost:8093
```

**Needs naut ≥ 0.12.0.** Runs clean on the released CLI — the
`drivers:` list form (pure sugar for this project's one Modbus driver)
and the Sparkplug DEVICE publish are both already released. See
`../../README.md` for the fleet-wide version check.

`naut run .` alone shows the Modbus task **degraded** — `FM1_FlowLps`
faulted, the driver panel red — until `naut modbus serve` (below) is
brought up alongside it; this is deliberate, not a rough edge (see
`sim.st`'s own header: "the flow meter is real either way"), the same
choice this codebase makes everywhere a field driver has nothing to talk
to yet. There is no memory-only `bench.yaml` here the way `field.yaml`
elsewhere in the fleet's own family (`examples/lift-station`) swaps a
live driver out — booster-1's whole point is the one real device, not a
bench stand-in for it.

## What to open first

- `pressure.fbd` — the discharge-pressure PID (`PIC-1`) and the lag
  pump's flow-demand call, with hysteresis. *Open With → Function Block
  Diagram*.
- `devices.yaml` — the one hand-written input to `naut modbus import`.
- `booster-1_test.yaml` — the acceptance suite; every test seeds
  `FM1_FlowLps` with `given:` since FBD can't short-circuit an unset
  input.

## What it demonstrates

| Feature | Where | Docs |
|---|---|---|
| FBD: a `PID` closed loop, a hysteresis seal-in for the lag pump's call | `pressure.fbd` | [Function blocks](https://nautilus.joyautomation.com/languages/function-block/) |
| Modbus TCP: a live flow meter, `naut modbus import`/`serve` | `devices.yaml`, `modbus_manifest.yaml` | [Modbus TCP](https://nautilus.joyautomation.com/guides/modbus/) |
| Sparkplug B: a Modbus driver's own tags riding along as a DEVICE | `nautilus.yaml` `sparkplug: {device: plc1}` | [Sparkplug](https://nautilus.joyautomation.com/guides/sparkplug/) |
| Acceptance tests: virtual time, a live device seeded with `given:` | `booster-1_test.yaml` | [Testing](https://nautilus.joyautomation.com/reference/testing/) |

## Files

| File | What |
|---|---|
| `nautilus.yaml` | the manifest: tasks, tags, `drivers: [{type: modbus, ...}]`, `sparkplug: {device: plc1}` |
| `types.st` | `TYPE Pump` — this site's own copy of the same shape `scada/sites.yaml` shares across all three sites |
| `pressure.fbd` | the lead's PID, the lag's flow-demand call |
| `sim.st` | bench-only plant: pump speed response, discharge/suction pressure, the shared distribution tank level |
| `devices.yaml` | the ONE hand-written input to `naut modbus import` — one flow meter, one instance |
| `modbus_manifest.yaml`, `tags/modbus.yaml` | generated — see below |
| `booster-1_test.yaml` | the acceptance suite |

## The flow meter

```sh
naut modbus import --map devices.yaml   # regenerate the committed files
```

Generates `FM1_FlowLps` (`role: input`), which `pressure.fbd` reads every
scan for the lag pump's call — FBD has no short-circuiting, so (like
`examples/sparkplug-host`'s host-side inputs) it must be seeded before the
first scan; `booster-1_test.yaml` does this with `given:` in every test,
no live device required.

**Live**, this is the one site in the fleet whose own field bus needs
standing up separately from the broker:

```sh
naut modbus serve --manifest modbus_manifest.yaml --listen 127.0.0.1:5021 --ramp
naut run .
```

Without a live meter, `FM1_FlowLps` reads as a task fault (the same
"reads fault until connected" contract every input tag has) — honest,
and why the fleet's live walkthrough (`../../README.md`) brings the meter
up alongside the broker and the three sites.

## Sparkplug

`sparkplug: {device: plc1}` attaches the Modbus driver's own tags
(`FM1_FlowLps`) to the edge node as a Sparkplug DEVICE — its DBIRTH/DDEATH
track the driver's connection health, the same mechanism
`examples/alarms`'s `client60` retransmit uses for its EIP-polled tags.
`Pump1`/`Pump2` (the shared `Pump` Template) and `TankLevel` publish at
node level, same as the two well sites' `Pump1`/`WellLevel` — the shape
`scada/sites.yaml` and a live `--broker` import agree on byte-for-byte.
