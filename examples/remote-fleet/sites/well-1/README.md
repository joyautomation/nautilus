# well-1 — a Sparkplug B edge node

One well pump (`Pump1`), called on rising wet-well level with hysteresis
(`PumpOnLevel`/`PumpOffLevel`) and run at a speed clamped to
`[MinSpeedHz, MaxSpeedHz]`. The plant itself is simulated (`sim.st`), so
`naut run .` cycles the whole thing with nothing wired up — no field
build ships in this fleet; see `../booster-1` for the Modbus-backed site
instead. Publishes to the `Water` Sparkplug group as edge node `Well1`.

```sh
naut check .    # validate offline
naut test  .    # the acceptance suite, virtual time
naut run   .    # dashboard + tag API on http://localhost:8091
```

See `../../README.md` for the fleet story — bringing up a broker, the
other two sites, and the SCADA host that consumes all three.

## Files

| File | What |
|---|---|
| `nautilus.yaml` | the manifest: tasks, tags, `driver: {type: memory}`, `sparkplug:` |
| `types.st` | `TYPE Pump` — a root `.st` with no `PROGRAM` composes as a library. The **same shape** `scada/sites.yaml` declares as its shared `Pump` Template for all three sites, but this is this site's own copy, not a shared package: each site deploys alone. |
| `well.st` | the hysteresis call and the speed clamp; a run-hours meter (`PumpStarts`/`PumpHours`) |
| `sim.st` | bench-only plant: a wet-well level integrator and the pump/VFD's speed response |
| `well-1_test.yaml` | the acceptance suite |

## Sparkplug

`nautilus.yaml`'s `sparkplug:` section: `group-id: Water`, `edge-node:
Well1`, `store-forward: 2000` (buffers data through a broker outage,
replayed as historical on reconnect), a `default-class` plus a `fast`
class with no deadband for `WellLevel` (the level is what the SCADA
host's dispatch logic watches; see `../../scada/overview.fbd`).

`Pump1` (the Template) and `WellLevel` publish under those exact tag
names — no rename between the edge's local tag and the Sparkplug metric
name, which is why `scada/sites.yaml`'s offline description and a live
`--broker` import produce byte-identical generated files (see the fleet
README's diff proof).

Writing `Pump1.SpeedSP` — from this project's own dashboard, or from the
SCADA host's generated `Well1_Pump1_SpeedSP` tag over an NCMD member
write — reaches the same struct member either way: the member-write
machinery routes `Tag.MEMBER` regardless of which side initiates it.
