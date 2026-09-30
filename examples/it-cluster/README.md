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

## Faults are tags

Each is an input: it bends what it should, composes with the others, and
clearing it restores the recording.

| Tag | Effect |
|---|---|
| `Sim_SW1_Port25.Down` | link lost: `OperUp` false, no traffic, `AdminUp` stays true, so the monitor raises "link down" |
| `Sim_SW1_Port25.AdminDown` | shut by an operator: `AdminUp` and `OperUp` false, no alarm |
| `Sim_SW1_Port25.SpeedMbps` | negotiated speed override (10000 → 1000), traffic capped to the line; 0 = as recorded |
| `Sim_SW1_Port25.ErrorRate` | errors per second added to the recording |
| `Sim_SW1.Dark` | the switch goes dark: every port down, and its stand-in stops answering |

```sh
curl -X POST localhost:8087/api/tags -d '{"name":"Sim_SW1_Port25.Down","value":true}'
```

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

Servers, fans, PSUs and temperatures are recorded (`Rec_NODE1_Fan3`, …) but
not yet served: `naut redfish serve --from` is next, then a thermal model and
the named scenarios.
