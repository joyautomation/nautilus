---
title: IT hardware (SNMP, Redfish, node_exporter)
description: Switches, PDUs, UPSes, server BMCs and commodity hosts as ordinary Nautilus tags — one UDT set, three drivers, generated manifests, credentials from the environment, and stand-ins that make the bench hardware-free.
---

Three `drivers:` types turn IT hardware into the same kind of tag a pump
is: `snmp` for switches, PDUs and UPSes, `redfish` for server BMCs, and
`prometheus` for hosts scraped straight from node_exporter. They deliver
**one shared UDT set**, so a switch port, a fan or a PSU is bound, alarmed,
trended, published and rendered in 3D exactly like plant equipment — the
brief is `docs/design/it-drivers.md`, and §2 there is the contract.

| UDT | What it is | Alarm member |
|---|---|---|
| `Server` | a machine: power, health, temps, CPU/memory, fan/PSU counts | `Fault`, `Warning` |
| `Fan`, `PSU`, `TempSensor` | children of a server or a switch | `Fault`; `High`, `HighHigh` |
| `Switch`, `SwitchPort` | a managed switch and each interface: link, speed, rates, errors, PoE | `Fault`; `Down` (enabled, no link) |
| `PDU`, `PDUOutlet`, `UPS` | rack power | `Overload`; `OnBattery`, `LowBattery`, `BatteryFault` |

Instances are flat, not arrays — `SW1_Port03 : SwitchPort`, `NODE1_Fan2 :
Fan`, `NODE1_Temp_Inlet : TempSensor` — because that is what alarm rules,
Sparkplug Templates and a scene file address by name.

## Import, then run

```sh
# a switch over SNMPv3 (v2c: --community-env SNMP_SW1_COMMUNITY instead)
naut snmp import --tag SW1 --host 192.168.1.2 --version 3 --user mon --auth sha256 --priv aes128
# a BMC (self-signed certificate: --insecure, or --ca-file)
naut redfish import --tag NODE1 --host https://bmc1 --user mon --password-env BMC_NODE1_PASSWORD --insecure
# a host running node_exporter
naut prometheus import --tag HOST1 --url http://host1:9100/metrics
```

Each importer writes `<proto>_manifest.yaml`, `tags/<proto>.yaml` and
`hw_types.st` — generated, never hand-edited, byte-identical on re-run.
All three write the same `hw_types.st`, so a project with a switch and a
host keeps one types file. The manifest is explicit — every member of every
tag names its OID, its Redfish resource and path, or its metric selector —
so a reviewer sees exactly what will be polled, and a device the profiles
have never met is handled by editing the manifest, not the driver.

```yaml
drivers:
  - type: snmp
    manifest: snmp_manifest.yaml
    scan-rate: 5s                       # a poll INTERVAL, in seconds
    scan-classes: { slow: 60s }
    tag-classes: { slow: ["*.Name", "*.Alias", "*.Model", "*.Serial"] }
  - type: redfish
    manifest: redfish_manifest.yaml
    scan-rate: 10s                      # BMCs rate-limit; 10 s is a safe floor
  - type: prometheus
    manifest: prometheus_manifest.yaml
    scan-rate: 15s
tag-files: [tags/snmp.yaml, tags/redfish.yaml, tags/prometheus.yaml]
```

`manifest:` is the only required key. `New` never dials: `naut check` and
`naut build` validate every binding offline, with no device in sight.

## Credentials

Never in the manifest, the tag file, a log line or an error. A source names
the **variable** (`community-env: SNMP_SW1_COMMUNITY`, `auth-env`,
`priv-env`, `password-env`) or the **file** (`community-file`,
`password-file`, for a mounted Secret) the driver reads where it runs.
`naut check` warns when a named variable is unset — a warning, never an
error, because check runs on laptops that have none of the controller's
secrets. SNMPv3 with SHA-256 and AES-128 is what a managed switch should
be on; MD5 and DES are accepted with a warning.

## Quality, rates and companions

- **`<id>__Online`** is true while the source answered within
  `stale-after` (default three intervals). A device that stops answering
  keeps its last values, marked Stale — never a silent zero — so interlock
  on `__Online` before trusting a device's tags, and use it as the alarm
  rules' `enable:` so a dark device's alarms are Suppressed instead of
  frozen lit. An unbound `Online` member on `Server`, `Switch`, `PDU` and
  `UPS` mirrors the same verdict. `<id>__LastPollMs` is the poll clock.
- **Rates:** `InBps`, `OutBps`, `ErrorRate` are per-second rates of wire
  counters, with 32-bit wrap and 64-bit reset handled (a step backwards on
  a 64-bit counter is a reboot, not a wrap). They read 0 for exactly one
  interval after a source connects.
- **Bad, per tag:** a refused OID, a 404 resource or a metric the exporter
  no longer carries marks that tag Bad and leaves its siblings Good. A
  transport failure (timeout, refused, bad credentials) climbs a backoff
  ladder from 1 s to 60 s and shows on `/api/drivers` with the reason —
  a wrong SNMPv3 key reads as `wrong credentials`, not as a timeout.

## Alarms by rule

```yaml
alarms:
  site-from: "^([A-Za-z0-9]+?)(?:_|$)"   # SW1_Port03 → SW1, NODE1_Fan2 → NODE1, SW1 → SW1
  rules:
    - { match: { type: SwitchPort, member: Down },     priority: medium,   on-delay: 15s, enable: "{site}__Online" }
    - { match: { type: Fan,        member: Fault },    priority: high,     on-delay: 30s, enable: "{site}__Online" }
    - { match: { type: PSU,        member: Fault },    priority: high,                    enable: "{site}__Online" }
    - { match: { type: TempSensor, member: HighHigh }, priority: critical, on-delay: 10s, enable: "{site}__Online" }
    - { match: { type: UPS,        member: OnBattery },priority: high,                    enable: "{site}__Online" }
```

One rule per `(type, member)` covers every port, fan and sensor on every
device — now, and after the next import adds a device.

## Writes are off

The importers generate no writable binding. A PDU outlet command or a
server power command is added by hand, in a reviewed diff, as its own
scalar output tag bound to the member it acts on:

```yaml
writes:
  - { name: PDU1_Outlet03_Cmd, tag: PDU1_Outlet03, member: On, oid: 1.3.6.1.4.1.3808..., set: { true: 1, false: 2 } }
  - { name: NODE1_PowerCmd, tag: NODE1, member: PowerOn, target: /redfish/v1/Systems/1/Actions/ComputerSystem.Reset }
```

`NODE1_PowerCmd` is an INT: 0 none, 1 on, 2 graceful shutdown, 3 force
off, 4 restart — posted once on the change to a non-zero value; the program
returns it to 0. Every command goes through the runtime's existing gates:
the leader only, the write token, the HMI's confirm dialog. The first value
seen after a start is a baseline that is never written, so `init: false` on
an outlet command cannot switch the outlet off at boot. Automatic power
actions from control logic are deliberately out of scope: a controller
that can power-cycle the node it runs on is a loop, and fencing belongs to
the cluster's own tooling.

## The bench: recordings and stand-ins

```sh
naut snmp browse --host 192.168.1.2 --record switch.snmpwalk       # then import --walk
naut redfish browse --host https://bmc1 --record bmc1/              # then import --mockup
naut prometheus browse --url http://host1:9100/metrics --record host1.prom

naut snmp serve --walk switch.snmpwalk --ramp &        # a v2c agent on 127.0.0.1:1161
naut redfish serve --mockup bmc1/ &                    # a BMC on 127.0.0.1:8000
naut prometheus serve --file host1.prom --ramp &       # an exporter on 127.0.0.1:9100
```

An import from a recording gives the same bytes as from the live device,
so the recording is the fixture the bench, the golden tests and CI all
share. A Redfish recording carries serial numbers and addresses: review
it before committing. `examples/it-rack` is the whole story runnable on a
laptop — a switch and a host, rules, rollups, an acceptance suite in
virtual time — and the same project pointed at real devices.

## What each driver knows

- **snmp** — IF-MIB/ifXTable for ports (64-bit counters, falling back to
  32-bit), ENTITY-MIB for the chassis, ENTITY-SENSOR-MIB and
  POWER-ETHERNET-MIB where present; RFC 1628 UPS-MIB and CyberPower's
  CPS-MIB for the PDU and UPS profiles (written from the published MIBs,
  marked unverified until a device confirms them). One GetBulk walk per
  table column per poll, not one Get per port. v2c and v3.
- **redfish** — both generations: legacy `Chassis/{id}/Thermal` and
  `Power`, and 2021+ `ThermalSubsystem`, `PowerSubsystem` and `Sensors`;
  sessions with basic-auth fallback; paths select array members by
  `MemberId` or `Name`, never by position, because BMCs reorder arrays
  across firmware.
- **prometheus** — the exposition text format from node_exporter (pinned
  to a release; the profile reports a bound metric the scrape no longer
  carries); `CpuPct`, `MemPct`, `RootDiskPct` and uptime as small
  expressions over named selectors; fans and temperature sensors from
  hwmon with their own thresholds. Scrapes the exporter directly — no
  Prometheus server needed.
