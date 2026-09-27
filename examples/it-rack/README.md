# it-rack — the office rack as a controller project

A 28-port switch over SNMP and a workstation over node_exporter, every port,
fan and temperature sensor an ordinary Nautilus **UDT tag**: `SW1_Port03 :
SwitchPort`, `MIRA1_Fan2 : Fan`, `MIRA1_Temp_Package_id_0 : TempSensor`. A
port with no link is an ISA-18.2 alarm, a stopped fan is an alarm, the rack's
hottest reading is a rollup, and all of it publishes, trends and renders in
3D exactly like a pump skid — no special cases anywhere. The `Server`,
`Switch` and `SwitchPort` UDTs are the contract the 3D scene binds to
(`docs/design/it-drivers.md` §2).

Two builds of the same project:

| | `nautilus.yaml` (bench) | `live.yaml` |
|---|---|---|
| the switch | `naut snmp serve` replaying `fixtures/switch.snmpwalk` on 127.0.0.1:1161 | the real switch (`live/snmp_manifest.yaml`, imported with `--host <its address>`) |
| the host | `naut prometheus serve` replaying `fixtures/workstation.prom` on 127.0.0.1:9100 | the workstation's own node_exporter, same port, **same manifest** |
| tags, rules, `rack.st`, tests | identical | identical |

## Run it on a laptop

```sh
naut check .     # validate offline — warns that $SNMP_SW1_COMMUNITY is unset here
naut test  .     # the acceptance suite, virtual time, no devices

naut snmp serve --walk fixtures/switch.snmpwalk --ramp &          # the "switch": counters move at a few Mb/s
naut prometheus serve --file fixtures/workstation.prom --ramp &   # the "host"
SNMP_SW1_COMMUNITY=public naut run .                              # http://localhost:8080
```

The stand-in answers community `public`; the manifest never holds the
community, it names the variable the driver reads (`SNMP_SW1_COMMUNITY`)
where it runs. Then:

```sh
curl -s localhost:8080/api/state | jq '.tags.SW1_Port03'   # AdminUp, OperUp, Down, InBps, InPct, ErrorRate …
curl -s localhost:8080/api/drivers | jq '.[].devices'      # SW1 and MIRA1 rows: fresh, tag counts, request counts
curl -s localhost:8080/api/alarms | jq '.summary'          # the synthetic switch has 17 enabled ports with no link
```

## What is where

- `snmp_manifest.yaml`, `tags/snmp.yaml`, `hw_types.st` — **generated** by
  `naut snmp import` from the recorded walk. `prometheus_manifest.yaml` and
  `tags/prometheus.yaml` by `naut prometheus import` from the recorded
  scrape. Both importers write the same `hw_types.st`. Byte-identical on
  re-run (the header of `nautilus.yaml` has the exact commands); regenerate,
  never hand-edit.
- `fixtures/` — the recordings: a synthetic 28-port switch walk (24×1G +
  4×10G, mixed up/down) and a real workstation scrape with its identifiers
  scrubbed. A real device's recording (`naut snmp browse --record`,
  `naut prometheus browse --record`) drops in the same way.
- `rack.st` — the rollups no device carries: `PortsUp`/`PortsDown`,
  `DevicesOnline`, `RackFault`, `RackMaxTempC`. Every read is under its
  source's `__Online` companion: a device that stops answering keeps its
  last values (Stale), so the guard, not the data, zeroes the rollup.
- `nautilus.yaml` — the drivers (poll **intervals**, in seconds, with the
  inventory strings on a slow class) and the alarm **rules**: one line per
  `(type, member)` covers every port, fan and sensor on every device, with
  `enable: "{site}__Online"` moving a dark device's alarms to Suppressed.
- `it-rack_test.yaml` — virtual time: a port goes down → alarm after 15 s,
  suppressed while the switch is dark; a fan stops and a sensor crosses its
  critical threshold; the fault rollup. `given:` writes the driver's input
  image directly, one member per line.

## Pointing it at real devices

```sh
naut snmp browse --host <switch> --record fixtures/switch.snmpwalk         # community from $SNMP_COMMUNITY
naut snmp import --tag SW1 --host <switch> --out live --tags-out ../tags/snmp.yaml
SNMP_SW1_COMMUNITY=… naut run -m live.yaml .
```

SNMPv3 (what a managed switch should be on): add `--version 3 --user <u>
--auth sha256 --priv aes128` to the import; the manifest then names
`SNMP_SW1_AUTH` and `SNMP_SW1_PRIV`. For the host, run node_exporter and
stop the stand-in — `prometheus_manifest.yaml` already points at `:9100`.

## Known limits (2026-09-26)

- The synthetic switch has no vendor MIB, so `SW1.CpuPct/MemPct` stay 0 and
  `Switch.Fault` has nothing to bind; the FS S3900 the office runs exposes
  no CPU, temperature or fan objects without FS's MIB pack either.
- A workstation is a zoo of hwmon sensors (48 here, some with chip-id names
  such as `MIRA1_Temp_i2c_10_10_0050_temp1`). A server with a BMC is the
  cleaner story (`naut redfish import`); a naming/filter pass for hwmon is
  on the list.
- `Server.Fault` from node_exporter is a coarse proxy (the chip alarm bits
  when the exporter has them, else the hottest reading against the box's
  highest critical threshold). The per-sensor `TempSensor.HighHigh` rules
  are the precise signal.
