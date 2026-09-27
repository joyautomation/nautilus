# Redfish fixtures

DMTF mockup directories in the short form (`index.json` is the service root,
`Chassis/1/index.json` the chassis) — the layout DMTF's published bundle uses
and `naut redfish browse --record` writes. `naut redfish serve --mockup <dir>`
serves any of them; so does DMTF's Redfish-Mockup-Server with `--short-form`.

| Fixture | What it is |
|---|---|
| `supermicro-x14` | A Supermicro X14SBW-F (SYS-112B-WR, OpenBMC-based BMC firmware 01.06.07.00). `Systems/1`, `Chassis/1` and `Managers/1` are recorded bodies, **sanitised**: serial numbers (system, chassis, board, the Manager's `ServiceIdentification`), UUIDs/GUIDs and timestamps replaced with placeholders; no address or host name is in these bodies. Everything under `ThermalSubsystem`, `PowerSubsystem`, `Sensors` and `EnvironmentMetrics` is **modeled**, not recorded — the read-only walk the recording came from did not descend there — following the links the recorded `Chassis/1` carries (it links only the 2021+ resources) and OpenBMC's `<type>_<name>` sensor ids. Replace it with a real `browse --record` (then sanitise) once a read-only run is allowed. |
| `legacy-1u` | Authored: a pre-2021 server with only `Chassis/1/Thermal` and `Power` — an Absent CPU2 sensor, an Absent fan, a PSU with lost input (Health Critical, 0 V). |
| `subsystem-1u` | Authored: a 2021+ 1U with `ThermalSubsystem`/`PowerSubsystem`/`Sensors` only — 6 fans, 2 PSUs with `Metrics`, CPU/inlet/exhaust sensors with thresholds, an energy counter. |

The generated files for each live in `cmd/naut/testdata/redfish/<fixture>/`
(`go test ./cmd/naut -run Redfish -args -update` refreshes them).
