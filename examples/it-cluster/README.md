# it-cluster — a server cluster as a plant simulation

Three 28-port switches and three 1U servers, recorded for 68 hours and then
switched off. This project plays them back as a **plant**: every device tag
is the recording bent by fault inputs you switch at run time. A monitoring
project polls stand-ins that answer from these tags, so its drivers, alarms
and 3D view run against the plant exactly as they ran against the hardware.

```
 this project (the plant, :8087)             stand-ins                         monitoring project
 replay driver ─▶ Rec_SW1_Port25             naut snmp serve --from :8087 ◀─poll─ snmp driver
 Sim_SW1_Port25.Down (fault input) ─▶        answers each OID the monitor's       alarms, rollups,
 PortPlant (lib/switch.st) ─▶ SW1_Port25     manifest binds from SW1_Port25       3D, unchanged
```

The manifest the monitoring project polls with is the one description of the
device in both directions: its driver reads an OID forwards into
`SW1_Port25.OperUp`, and `naut snmp serve --from` reads it backwards, from the
plant's `SW1_Port25.OperUp` to the OID's wire value.

## The recording is not in this repository

`./data` is a link to the dataset: `history.json.gz` (846 series, 60 s
samples) and the switches' SNMP walks. Without it everything still checks,
tests and runs: the replay driver reports the recording missing, `Replay_At`
stays 0, and each device holds its snapshot (`tags/switches.yaml`'s `init:`).

```sh
naut check .   # no recording needed
naut test .    # the fault logic, virtual time (it-cluster_test.yaml)
naut run .     # the plant on http://localhost:8087
```

### Where the recording is not the plan

The plant's normal is the site as planned, so a fault-free plant raises no
alarm. Where the recording differs (while it ran, the bench trunk was in
SW1 g0/23 and the site uplink's g0/1 sat with no link), `baseline.yaml`
has a port play another port's series; `gen.py` applies it to the replay
manifest and the snapshot. The recording itself is untouched.

## Faults are tags

Each is an input (all in `tags/faults.yaml`): it bends what it should,
composes with the others, and clearing it restores the recording. A cable is
worked out from both ends, so a fault at either end, or the cable itself,
takes both ends down.

| Tag | Effect |
|---|---|
| `Sim_SW1_Port25.Down` | link lost: `OperUp` false, no traffic, `AdminUp` stays true (the monitor raises "link down"), and the server port at the other end loses link too |
| `Sim_SW1_Port25.AdminDown` | shut by an operator: `AdminUp` and `OperUp` false, no alarm; the far end loses link |
| `Sim_SW1_Port25.SpeedMbps` | negotiated speed override (10000 → 1000) at both ends, traffic capped to the line; 0 = as recorded |
| `Sim_SW1_Port25.ErrorRate` | errors per second added to the recording |
| `Sim_Cable_SW3_Port25.Pulled` | the cable itself: both ends down |
| `Sim_SW1.Dark` / `.Reboot` | the switch goes dark (its stand-in stops answering) / reboots: dark for 90 s, back with its uptime reset |
| `Sim_NODE2.CpuLoad` | % busy, 0 = as recorded: power and the thermal model follow |
| `Sim_NODE2.PowerOff` / `.Dark` | host off (standby power, fans stop, temperatures fall to the inlet, links drop) / the BMC stops answering |
| `Sim_NODE1_Fan3.Fail` | the fan stops; the others ramp; temperatures climb |
| `Sim_NODE1_PSU2.InputLost` / `.Fail` | output 0, the other supply carries the load, the server stays up |
| `Sim_NODE3_Drive_NVMe2.Pulled` / `.Failing` | `Present` false / `PredictedFailure` and health Warning |
| `Sim_NODE1_Nic_Slot2_P1.Down` | the server's port loses link (and the far end) |
| `Sim_Room.InletDeltaC` | the room runs hot: every inlet, and everything behind it, rises |

```sh
curl -X POST localhost:8087/api/tags -d '{"name":"Sim_SW1_Port25.Down","value":true}'
```

The thermal model (`lib/server.st`) is first order: each sensor heads for its
recording plus its share of the CPU's rise (load, failed fans, less what the
ramping fans take back) plus the room, with a 60 s time constant, so a
failure makes temperatures climb rather than jump. Every number that shapes
it is a named constant there, to tune against a real box.

## Named scenarios

`Scenario` (a STRING) sets a preset of fault inputs, so a demo is one write
(`scenarios.st`). Presets only set inputs, so they compose; `normal` clears
every fault (so does the `ClearFaults` pulse).

| Scenario | Sets |
|---|---|
| `port-down` | `Sim_SW2_Port26.Down` (node1 slot 3 port 2 at the far end) |
| `cable-pull` | `Sim_Cable_SW3_Port25.Pulled` |
| `mesh-cable-pull` | `Sim_Cable_NODE1_Nic_Slot2_P1.Pulled` (node1 to node2) |
| `speed-degrade` | `Sim_SW1_Port25.SpeedMbps := 1000` |
| `error-burst` | `Sim_SW2_Port25.ErrorRate := 50` |
| `switch-down` / `switch-reboot` | `Sim_SW3.Dark` / `Sim_SW3.Reboot` |
| `cpu-overheat` | `Sim_NODE2.CpuLoad := 100`, fans 3 and 4 fail: HighHigh in about 3 min |
| `fan-fail` | `Sim_NODE1_Fan3.Fail` |
| `psu-loss` | `Sim_NODE1_PSU2.InputLost` |
| `drive-pull` / `drive-failing` | `Sim_NODE3_Drive_NVMe2.Pulled` / `Sim_NODE3_Drive_NVMe1.Failing` |
| `hot-room` | `Sim_Room.InletDeltaC := 15` |
| `node-off` / `bmc-dark` | `Sim_NODE2.PowerOff` / `Sim_NODE3.Dark` |

```sh
curl -X POST localhost:8087/api/tags -d '{"name":"Scenario","value":"cpu-overheat"}'
```

`it-cluster_test.yaml` runs every scenario in virtual time.

## The replay clock is tags too

`Replay_Speed` (default 60: the 68 h in 68 min), `Replay_Pause` and
`Replay_SeekS` (unix seconds; a change jumps there) steer it; `Replay_At`,
`Replay_From` and `Replay_To` report it. The node-3d HMI's clock writes them.

## Serving a monitoring project

For each switch, point a stand-in at the plant with the monitoring project's
own manifest (it listens where that manifest says the switch is):

```sh
naut snmp serve --walk data/snmp/sw1.snmpwalk --from http://127.0.0.1:8087 \
  --manifest ../monitor/snmp_manifest.yaml --source SW1
```

OIDs the plant has no tag for keep answering from the walk. Rate members
(`InBps`) steer the counters, which stay monotonic.

## Regenerating

`gen.py` writes `replay_manifest.yaml`, `tags/recorded.yaml`,
`tags/switches.yaml` and `plant.st` from the monitoring project's manifests
and the dataset. Device identities come from `naut snmp read` (the walk read
through the manifest, offline); hostnames and serials are replaced.

```sh
python3 gen.py --dataset <dataset> --monitor <monitoring project>
```

`naut redfish serve --from` serves the servers the same way, through the
monitoring project's `redfish_manifest.yaml`.
