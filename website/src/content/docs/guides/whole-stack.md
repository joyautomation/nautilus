---
title: nautilus as the runtime and the SCADA
description: A walk through running a small plant on nautilus alone, with no separate PLC runtime and no SCADA package. One step at a time, what you can do at each, which guide has the detail, and where nautilus stops.
---

This page is a map, not a manual. It follows one small plant from a blank
folder to two controllers running it, and at each step says **what you can
do** and **which guide has the how**. The plant is the kind the examples
use: a lift station, a bin-fill line, a batch skid. A few pumps, a drive or
two, some relay and IO-Link I/O, a screen for operators, alarms, and a
second controller for when the first one fails.

Read the last step before you decide. nautilus does not do everything a PLC
vendor and a SCADA vendor do together, and the list of what it does not is
on this page as plainly as the list of what it does.

## 1. Describe the plant as tags and logic

**You can** declare every value the plant has as a tag with a type, a role
(what the tag is for: `input` from the field, `output` to it, an operator's
`setpoint`, or internal `state`), a unit and limits, write the logic
in Structured Text, Ladder, Function Block or SFC, and run it on a scan loop
(read the inputs, run the logic, write the outputs, over and over) from your laptop with `naut run`.

- [Your first program](/guides/first-program/): a pump with a level input.
- [The tag model](/guides/tag-model/): roles, and the one rule that bites.
- [Blocks and tasks](/guides/blocks-and-tasks/): reusable function blocks
  and more than one scan rate.
- [Languages](/languages/).

## 2. Talk to the field

**You can** read and write field devices from a committed device map, with a
quality on every tag, and see a lost device as `stale` or `notConnected` and
not as a frozen number.

- Modbus TCP: [Modbus](/guides/modbus/).
- Allen-Bradley Logix tags over EtherNet/IP: [EtherNet/IP](/guides/ethernet-ip/).
- A plant OPC UA server, such as an Ignition gateway or a PLC's own server:
  [Reading OPC UA](/guides/opcua-client/).
- IO-Link sensors on a master's ports, decoded from their IODD:
  [IO-Link](/guides/io-link/). This one is a preview: it has run against
  virtual masters only.
- Switches, PDUs, UPSes and server BMCs as ordinary tags:
  [IT hardware](/guides/it-hardware/).
- A PLC that configures nautilus as a PROFINET IO device (the device side
  only): [PROFINET](/guides/profinet/), experimental.
- A Sparkplug B host that consumes a group of edge nodes:
  [Sparkplug host](/guides/sparkplug-host/).

## 3. Prove it with no hardware

**You can** run acceptance tests on a virtual clock, so a ten-second
interlock delay is asserted exactly and in milliseconds, and run the same
sequence against simulated devices that answer your real Modbus and OPC UA
bindings.

- [Testing](/reference/testing/): `naut test`, virtual time.
- [Virtual devices](/guides/virtual-devices/): drives, relay boards and
  IO-Link masters as simulators, in tests and on real sockets with
  `naut devices up`.

## 4. Change I/O and setpoints while it runs

**You can** move a device to a new address or a register to a new place
without a restart, tune setpoints live and keep the tuning in git, and swap
logic on a running controller.

- [Changing I/O while it runs](/guides/live-io-configuration/): stage,
  validate, apply, then save as a commit.
- [Parameter sets](/guides/parameters/): export, diff and import tuned
  setpoints.
- [Online edits](/guides/online-edits/), [forcing](/guides/forcing/) and
  [program history](/guides/program-history/).

## 5. Run on a bad sensor, in the open

**You can** let an operator run the program on a chosen value for a bounded
time while a sensor is bad (a **substitute**), flagged everywhere as
substituted, and expired by the controller. Which inputs may be substituted,
for how long, and whether the start must be acknowledged are rules in
`nautilus.yaml`.

- [Substitutes](/guides/substitutes/).

## 6. Give operators screens, alarms and history

**You can** serve operator screens from the controller (a built-in set, or
your own), raise alarms from tags with acknowledge, shelve and a journal,
page people through an escalation and an on-call roster, keep notes on
assets, sign off an I/O checkout, show a plant camera, and keep history in
Postgres.

- [Operator screens](/guides/operator-screens/) and the
  [installable HMI](/guides/installable-hmi/).
- [Alarms](/guides/alarms/) and [alarm management](/guides/alarm-management/).
- [Notes](/guides/notes/), [I/O checkout](/guides/checkout/) and
  [cameras](/guides/cameras/).
- [Historian](/guides/historian/).

## 7. Serve the rest of the plant

**You can** let another system read the controller's tags over OPC UA, with
the types, quality and timestamps intact, or publish them over Sparkplug B
to a broker.

- [Serving OPC UA](/guides/opcua-server/): a preview, and read
  [Writes](/guides/opcua-server/#writes) before you open a setpoint to a client.
- [Sparkplug](/guides/sparkplug/).

## 8. Decide who may do what, and keep the record

**You can** give people roles and areas, sign them in with local passwords
or your company's OIDC login, give a script a token, and read an audit
journal that names who did what, with a copy somewhere the controller cannot
rewrite so `naut audit verify` can tell if it was changed.

```sh
naut auth init --user alice:operator --user bob:engineer   # adds an auth: block to nautilus.yaml
naut users add alice       # asks for a password; on a box, run it with sudo
naut users token alice     # a token for a script or VS Code
```

```yaml
audit:             # in nautilus.yaml
  sink: postgres   # its connection string in NAUTILUS_AUDIT_DB_URL
```

```sh
naut audit verify --anchor postgres   # exit 0 verified, 1 BROKEN, 3 UNPROTECTED
```

- [Users, roles and sign-in](/guides/auth/).
- [Audit trail](/guides/audit-trail/).

## 9. Run two controllers

**You can** run two controllers and a witness (a small third program,
`naut witness`, that breaks a tie so the two never both scan) so that one
scans and the other stands by with its state. A takeover keeps what operators
entered, including setpoints, substitutes and alarm acknowledgements, and an
optional floating address lets clients keep one URL.

```yaml
redundancy:
  peers:
    a: ctl-a:9450
    b: ctl-b:9450
  witness: witness-host:9460
  floating-ip: {address: 10.0.0.50/24, interface: eth0}
```

`naut check` loads that block. Commissioning the pair, the witness and the
takeover time you can expect are in [Redundancy](/guides/redundancy/): with
the default 1 s lease the new leader was in place in about a second, on a
test bench. And
the sign-in behaviour across a takeover is in
[Users, roles and sign-in](/guides/auth/#a-redundant-pair). It is a hot
standby with state streaming (a takeover is cold when the standby's image
is stale or from another build), not lockstep (step 11).

## 10. Ship it and keep it running

**You can** have every change merged to git that passes its tests built into
one controller program (an image), install it on a Linux box over SSH,
update and roll back by commit, measure how late a scan runs and give a fast
task its own CPU core, and read what is running back as a git commit.

- [Deployment](/guides/deployment/).
- [Real time](/guides/real-time/).
- Hardware targets, each tested on one bench unit and marked experimental:
  [PLCnext](/guides/plcnext/), [Opto 22 groov EPIC](/guides/groov-epic/),
  [Toradex Verdin](/guides/verdin/).

## 11. What nautilus does not do

Over-claiming is the commonest mistake about a tool like this, so here is
the list.

- **Safety.** A safety function stays in the hardwired chain: the safety
  relay, a contactor that drops out, a drive's safe torque off, a burner's
  flame management. nautilus monitors it and stops the plant in order. It
  is not a safety controller, and the hardware guides say so.
- **Lockstep redundancy.** A pair is a hot standby. A takeover has a
  measured gap (the lease and the takeover time are in the redundancy
  guide), and a takeover is not bumpless. Some drivers are refused under
  `redundancy:`: [PLCnext](/guides/plcnext/), and
  [PROFINET](/guides/profinet/) until the pair can name and check a
  floating station address.
- **Vendor PLCs as targets.** Logix and Siemens S7-1500 targets let
  nautilus be the IDE and the vendor's PLC the runtime. Both are
  experimental and name what does not translate yet: see
  [Authoring for Logix](/guides/logix-authoring/#what-it-does-not-do-yet)
  and [Siemens S7-1500](/guides/siemens/#what-does-not-translate-yet).
  The Siemens download needs a Windows machine with TIA Portal.
- **Importing a CODESYS project.** There is no importer. A CODESYS project
  is rewritten by hand, and [Coming from Codesys](/coming-from/codesys/)
  lists what carries over and what does not.
- **OPC UA reads under `auth:`.** Writes are gated by role; reads, browsing
  and subscriptions are not yet (see [Users, roles and sign-in](/guides/auth/#limits)).
- **Field I/O without a driver.** PROFINET is a device side only, so
  nautilus can be an IO device to a PLC, not a PROFINET controller. IO-Link
  has not been verified against a real master.
- **Your application's logic.** Route solving and the like stay in your
  project. nautilus supplies the runtime, not the plant's rules.

If a claim on this page and the code disagree, the code is right; tell us
and we will fix the page. The nightly record of what the tools have been
shown to do is on the [verified](/verified/) pages.
