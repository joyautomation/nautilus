---
title: Redundancy & retained state
description: Run replicas that elect one scanning leader, and keep operator setpoints and online edits across restarts — a ConfigMap and a Lease, no operator to install.
---

Two manifest sections turn a single controller into something you can
restart, reschedule, and replicate:

```yaml
retain: {}       # setpoints + online edits survive restarts
redundancy: {}   # replicas elect one scanning leader
```

Both run on plain Kubernetes primitives — a ConfigMap and a
`coordination.k8s.io` Lease, spoken directly with the pod's service
account — and both degrade gracefully outside a cluster, so the same
manifest runs on a bench laptop and as a three-replica Deployment.

## Retained state

A controller loses two kinds of state on restart: the values operators
have written (setpoints, tuning, mode selections) and any program edited
online but not yet pulled into git. `retain:` persists exactly those:

```yaml
retain:
  file: retain.json        # outside a cluster (default retain.json)
  configmap: line1-retain  # in a cluster (default <name>-retain)
```

- **What is saved:** every `role: setpoint` tag; every tag marked
  `retain: true` in the manifest or declared in a library's `VAR_GLOBAL
  RETAIN`; every program's `VAR RETAIN` variables; and the source of any
  program that differs from what shipped (an online edit). Nothing else:
  inputs re-read from the field and outputs re-derive from logic. Any other
  tag is not carried between replicas, and nothing re-seeds it at a
  takeover. A restart starts it from its seed, because the process is new;
  a failover leader keeps its own copy, which is its seed if it has never
  led and what it last held as leader if it has. A program `VAR` that is
  not `RETAIN` starts from its declared value at every takeover. The PID
  does not persist its integral; it warm-starts from live field values, the
  way a real changeover does.
- **Keeping state the logic owns:** run hours, start counts and a lead/lag
  pointer are state, not setpoints, so mark them. A tag:

  ```yaml
  tags:
    - { name: P1_Stats, role: state, type: PumpStats, retain: true }
  ```

  or a program variable, `VAR RETAIN runHours : REAL; END_VAR`. An input
  or an output cannot be retained: one comes back from the field and the
  other from the logic on the first scan.
- **Any shape:** a setpoint can be a scalar, a struct (UDT), or an array —
  an array of scalars, an array of structs, or a multi-dimensional array.
  A setpoint table in an `ARRAY` survives a restart or a failover like a
  single setpoint does. A struct or array comes back member by member
  onto the current tag, so if the project has since grown an array, the
  new elements keep their `init:` seeds; if it has shrunk one, the array
  keeps its seeds and the mismatch shows in `lastRetainError`. A setpoint
  cannot hold a function-block instance: the tag model refuses an `ARRAY
  OF TON`, or a struct with a TON member, as a tag's type, whether the
  manifest or a program's `VAR_EXTERNAL` declares it.
- **When:** every 2 seconds, only when something changed, and only by the
  leader. An orderly stop (Ctrl+C, or SIGTERM from systemd or Kubernetes)
  saves once more after the last scan, so what was written before the stop
  is kept. It waits up to 5 seconds for that save, and logs it when the
  save fails or does not finish in time. A hard kill can lose at most 2
  seconds of operator input. The file store writes the new file whole
  under another name, syncs it to disk, renames it over the old one and
  syncs the directory, so on a journaling file system (ext4, the overlay
  file systems controllers boot from) a power cut leaves the old file or
  the new one, never an empty or half-written one. FAT, on some SD cards,
  makes no such promise. A file that cannot be read is moved aside to
  `retain.json.corrupt-<time>`, the error is logged, and the tags start
  from their initial values.
- **Load order:** retained values are applied before the first scan and
  win over the manifest's `init:` seeds — what the operator set last
  outlives what the project file said first. A single controller applies
  them before its tag API, OPC UA server and HMI answer, so no client ever
  reads a seed in their place; a replica applies them when it takes over.
- A retained online edit still shows as *modified* in the dashboard and
  `naut pull`, so the git loop closes the same way it always did.
- **A deploy wins:** an online edit is reloaded only by the same build it
  was made on. A build whose program for that task differs replaces the
  edit (logged, and named in `retainDiscardedEdits`), so pull the edit into
  git before you deploy over it. The replaced source stays in the store
  under `discardedPrograms` (the last one per task, never applied) for
  recovery. A build that already contains the edit just clears it from the
  store.

Store failures surface in the dashboard's diagnostics (`retainErrors`,
`lastRetainError`) — a save that keeps failing is invisible exactly until
the restart that needed it, so it must be visible while it is fixable.

## Redundancy

```yaml
redundancy:
  lease: line1   # default: the project name
```

Run the same controller as a Deployment with `replicas: 2` (or 3). The
replicas elect a leader through a Lease; the leader scans, the standbys
do nothing at all — no field I/O, no logic, no output writes. Suppression
by absence is the safety story: a stale replica cannot write an output it
never computes.

- **Failover** is bounded by the lease: 4 s worst case, and a clean
  shutdown (SIGTERM) hands the lease over so a standby takes it on its
  next 1 s tick.
- **A Lease created ahead of time is taken over.** If the Lease already
  exists (made by hand or by another tool), each replica writes its own
  4 s duration on every renew and takeover. A standby judges a holder by
  the longer of the stored duration and its own, so a Lease stored short,
  or with no duration, never lets a standby take over from a live
  leader. A long stored duration still holds back a takeover from a
  holder that never renewed it under naut: a Lease left at 3600 s by a
  holder that is gone keeps every replica standby for up to an hour. Let
  naut create the Lease. If you pre-create it, leave its duration out,
  and keep it out of anything that re-applies it (a GitOps controller
  with self-heal on): each re-apply writes the stored duration back.
- **The lease needs live scans.** A leader renews only while its scans
  are completing, so a leader that is still running but whose scan is
  hung (logic that never returns, a driver call that blocks) hands over
  too. A scan counts as hung once one scan has been in progress for
  longer than twice its task's period, at least 1 s and at most 2 s,
  whatever the period: a fast task that overruns now and then is not
  hung. A task that is **starved** between scans counts too: its next
  slot comes due and it doesn't run (a higher-priority thread on its
  core, say). It is flagged once that slot is overdue by the same bound.
  A task that runs every 10 s is fine between its scans until its slot
  is due. A standby's scans gate out but still tick on schedule, so a
  healthy standby is never starved. A hung
  leader stops renewing and closes its own gate (its drivers stop
  writing) on its first tick more than 2 s after its last renew, without
  waiting on the Lease API, so a call to it that fails or hangs cannot
  keep the gate open. The gate also closes by itself once the last renew
  is older than the lease (2 s while scans are hung), however late the
  elector's own tick is. The standby takes the lease once it lapses, 4 s
  after that renew: about 5 to 8 s from the start of the hang, and the
  two never lead at once. A hung scan that finally returns after the
  lease is gone writes nothing. A replica whose own scans are hung does
  not take the lease either, so a broken replica never takes over; it
  contends again once its scans complete. Each change is logged once,
  naming the stuck task. At start-up a replica may take the lease before
  its first scan, but its scan loops must be running within 8 s (twice
  the lease) or it lets the lease go.
- **A scan over about 2 s pauses the outputs.** A single scan that takes
  longer than about 2 s can close the gate before it completes, even when
  no other replica takes the lease. While it is closed, the drivers send
  nothing of their own. When the scan completes, the replica renews the
  same lease and opens the gate again: the program carries on where it
  was (no takeover: frames, timers, SFC steps and retained state are
  kept), and the drivers get every output once. Forces are dropped when
  the gate closes, as on any step-down. The scan clock is not zeroed on a
  resume, unlike a takeover, so the first scan's `dt` spans the stall.
  Only a lease another replica held in between makes the next opening a
  takeover.
- **A lease holder whose gate is closed answers its own API.** The lease
  still names it, so its leader address is its own: rather than forward a
  request to itself, it answers reads from its own store and refuses
  writes with 503 until its gate opens or another replica takes the lease.
- **Outside a cluster** (no service account), a `redundancy:` manifest
  runs standalone: it is always the leader, with nothing to hand over
  to, except that a hung scan closes its gate: 4 s after the last check
  that found its scans live, about 5 to 8 s into the hang. Drivers then
  stop asserting outputs on their own (a Modbus `rewrite:` keep-alive,
  for one), so a device's communication watchdog sees the silence and
  acts. The gate opens again once scans complete, and the program
  carries on as above.
- **Without `redundancy:`** there is no lease, but `naut run` still gates
  the Modbus, EtherNet/IP and OPC UA client drivers on live scans, on
  the same timing. A healthy project sees no difference: the gate is
  open from the start and the runtime is not coordinated (no takeover,
  forces kept). The groov EPIC (OptoMMP) driver and a Sparkplug host take
  no gate here, because they read any gate as "one replica of a
  redundant set".
- **On takeover**, the new leader re-reads the retain store (the old
  leader may have accepted retunes or edits while this replica idled),
  discards program frames so warm-start logic runs against live field
  values, and zeroes its scan clock so the first `dt` is one scan, not
  the hours it spent standing by. Process state is deliberately **not**
  replicated — configuration travels through the retain store, process
  state re-derives from the field.
- **A `READ`/`WRITE` channel write leaves only from the leadership that
  posted it.** Each channel sends its requests one at a time, so a write
  can wait seconds behind another. It is checked against the write gate
  and the leadership (the lease's term) at the moment it is sent, not
  when it was posted:
  - if its turn comes while the gate is closed, it is dropped;
  - if it was posted in an earlier term, it is dropped, even with the
    gate open again: another replica may have led in between;
  - if the gate closed and reopened in the same term (a stalled scan that
    resumed, with nobody else leading), a write still waiting is sent.

  A dropped write ends with `ERROR` and `STATUS` -6 (lost), as on a
  restart. Reads are not gated.
- **A replica that takes over from standby sends every output once**
  through the Modbus, OPC UA client, EtherNet/IP and OptoMMP drivers,
  with the program's value, changed or not: the runtime hands the
  drivers the whole output set on the first scan as leader, and those
  drivers send each one even if it equals what this replica sent the
  last time it led, since the leader in between may have written
  something else. A replica that started as a
  standby has no start-up baseline, so its first takeover commands every
  output, as the new primary of a hot-standby PLC pair does; a replica that
  leads from its first scan keeps the baseline. `naut run` starts the
  drivers only after the elector's first decision (at most 5 s; a Lease
  read that fails at start is retried, not taken as standby), so a
  replica that acquires the lease at once is a plain start. One whose
  lease is still held by a predecessor that has not yet expired (a pod
  replaced under a new name) is a standby until it takes over, and so
  commands every output when it does. A Modbus `rewrite:`
  keep-alive re-asserts nothing until that push has given it the
  program's value. A driver sees leadership only by looking at its write
  gate, so a loss and regain between two looks is not seen (see
  [Modbus](/guides/modbus/#redundancy),
  [EtherNet/IP](/guides/ethernet-ip/#redundancy) and
  [groov EPIC](/guides/groov-epic/#redundancy)). EtherNet/IP and OptoMMP
  have no start-up baseline at all, so a plain start commands every
  output too. The hw drivers (SNMP, Redfish, Prometheus) do not do this:
  their commands are often pulses (a ForceOff sent twice powers a server
  off twice), so the first value after their gate opens is a baseline.
- **A standby leaves a device's communication watchdog to the leader.**
  Where the manifest sets a watchdog that any client's request feeds (a
  groov EPIC's `watchdog:`), the standby does not talk to that device at
  all, so a leader that keeps the lease but cannot reach the device lets
  the watchdog trip instead of the standby's polls feeding it. The
  standby loses its live view of just that device until it takes over
  (see [groov EPIC](/guides/groov-epic/#redundancy)), so its takeover
  waits for live inputs: the driver reports them not ready until its
  first read of the device, and the runtime holds the takeover (frame
  reset, logic and every driver's outputs) until then, at most 2 s
  (`runtime.Options.TakeoverInputsWait`), its scan loop still ticking.
  One hiccup in the dial can use up that wait: a first connect that
  fails waits about 1 s for its retry, and a lost SYN costs as much in
  the kernel. Past the wait the logic and every other driver's outputs
  run on stale inputs (the groov EPIC's own outputs stay held until it
  has read the unit).
  A watchdog the manifest does not name is still fed by a polling
  standby; the groov EPIC driver warns when it finds one. No other driver
  has a manifest-set watchdog of that kind; a Modbus `rewrite:`
  keep-alive is a write, and a standby sends none.
- **The HTTP API follows the leader.** Every replica serves the
  dashboard, and a standby reverse-proxies API traffic to the leader
  (the leader's address rides inside the Lease), so a Service in front
  of the replicas works. The replicas answer only `localhost`, an IP
  address or a name in `NAUTILUS_ALLOWED_HOSTS` unless a request carries
  the token, so the
  Service's names go in that list: a deployment from `naut new --deploy`
  sets `NAUTILUS_ALLOWED_HOSTS` to `name`, `name.<namespace>`,
  `name.<namespace>.svc` and `name.<namespace>.svc.cluster.local`
  (change the last for another cluster domain). The standby keeps the
  request's Host when it proxies, so the leader checks the same name.
  `GET /api/cluster` reports each replica's own view — mode, leader,
  transitions.
- The identity shown in `/api/cluster` comes from `POD_NAME`, and the
  proxy target from `POD_IP` — both from the downward API:

```yaml
env:
  - name: POD_NAME
    valueFrom: {fieldRef: {fieldPath: metadata.name}}
  - name: POD_IP
    valueFrom: {fieldRef: {fieldPath: status.podIP}}
  # The Service's names, answered without the token (#819):
  - name: POD_NAMESPACE
    valueFrom: {fieldRef: {fieldPath: metadata.namespace}}
  - name: NAUTILUS_ALLOWED_HOSTS
    value: "water-scada,water-scada.$(POD_NAMESPACE),water-scada.$(POD_NAMESPACE).svc,water-scada.$(POD_NAMESPACE).svc.cluster.local"
```

### A redundant pair without Kubernetes (experimental)

Two controllers and a small third voter, `naut witness`, can elect a
leader without a cluster. The full operator guide (commissioning,
re-commissioning by hand, the takeover time) comes with the rest of the
work (#710). For now the pair is an explicit opt-in:

```yaml
redundancy:
  experimental: true      # required for now: see below
  peers:
    a: ctl-a:9450         # each controller's peer port
    b: ctl-b:9450
  witness: witness-host:9460
  lease: 2s
```

:::caution[Retained values are not replicated yet]
Until state streaming lands, each controller keeps its own retained
values. A takeover starts from the **new** leader's own retain file, so
a setpoint the old leader took is lost at failover. A pair without
`experimental: true` is refused for that reason, and a running pair
says so at start, in `GET /api/cluster` (`warnings`) and on the
dashboard. Live I/O applies are refused on a pair.
:::

**Writes leave in time.** The leader stops writing a little before its
lease ends, not at the end: the fence margin, `net-delay` (the plant
network's delivery time) plus `send-margin` (scheduling slack), 70 ms at
the default 2 s lease. Every write to a device checks leadership just
before it goes out and must be done within the lease left minus
`net-delay`; a write still unanswered then has its connection reset, so
nothing it queued arrives late. Modbus TCP, EtherNet/IP, Redfish, SNMP
and Prometheus commands, OPC UA client writes, OptoMMP outputs and
READ/WRITE channel writes work this way now; the PROFINET device checks
leadership before every frame it sends (its raw Ethernet frames have no
connection to reset); the rest follow. On a pair the OPC UA client's
connection is made so that every close of it is a reset, so a server sees
a reset, not a clean close, whenever the client reconnects. A Sparkplug
host publishes a command only under the leadership it was computed in,
but the broker forwards it to the edge on its own timing: the edge's comm
watchdog covers that last hop.
Leave both keys unset unless the plant network is slow: then set them,
and keep their sum under a quarter of the lease (a longer one is
refused).

**A slow device costs a write.** A write may take as little as three
quarters of the lease minus `net-delay` (1.45 s at the default 2 s
lease). A device that answers slower than that has the write abandoned
and its connection reset, even with the leader healthy. A Modbus or
EtherNet/IP write is sent again after the reconnect: a Modbus write sets
a value and EtherNet/IP writes a tag, so a second one does the same. An
SNMP or Redfish command is never queued again: the source reports its
**outcome unknown**, because the device may have carried it out (a slow
BMC's reset) after nautilus stopped waiting. A Redfish action is never
retried, and an SNMP set goes out once (`retries:` applies to polls
only), so an action-valued object (an outlet reboot) never runs twice
because an answer was lost.

A source's `timeout:` is kept as it is (it bounds its polls too): on a
pair, each write is cut at its own send deadline whatever the timeout
says. `naut check` warns when a timeout a source that takes writes sets
is longer than the budget, since that device's slower writes will be
cut: lower it if the device answers well within it, or lengthen the
lease. EtherNet/IP has no
timeout setting, so the same applies to a controller slower than the
budget. A command queued under one leadership is never sent after the
controller lost the lease and won it back: it is dropped and the source
says why.

```yaml
redundancy:
  # ...
  net-delay: 50ms         # default: 50 ms, less on a lease under 400 ms
  send-margin: 20ms       # default: 20 ms, less on a lease under 320 ms
```

**No Sparkplug edge node on a pair yet.** Both controllers would publish
as the same edge node (#798), so a pair whose project has `sparkplug:`
is refused at load. Run the edge on a controller outside the pair until
#798 lands.

Each controller also needs, in its environment (`/etc/nautilus/env`
under `naut deploy`):

- `NAUTILUS_NODE`: `a` or `b`, which of the two it is. **It differs per
  box**: never copy one controller's env file to the other unchanged;
- `NAUTILUS_PEER_TOKEN`: the secret every peer and witness request
  carries, the same on all three boxes;
- `NAUTILUS_CLUSTER`: the pair's cluster ID, drawn when the pair is
  commissioned, the same on all three boxes.

The voter state, `quorum.state`, lives in the systemd state directory
(`/var/lib/nautilus` under `naut deploy`) or in `redundancy: state-dir:`.
Every `naut deploy` step keeps it, and a backup never contains it. It
records which controller wrote it, and on which box (its machine-id and
the network cards the pair's links use): a controller refuses to start
over the other one's file, and a file from another box is treated as
lost. So a **cloned disk or VM** starts uncommissioned and never votes,
and says so (`redundancy.box-changed`).

**Replacing a controller's network card, or migrating it** to other
hardware, is fine while the **other** controller is healthy: the changed
one starts uncommissioned, reports `redundancy.box-changed`, and
re-adopts from the other within seconds. If it stays uncommissioned
while the other controller is up, it is a copy of a controller: stop it.
**Never change both controllers at once**: both would start
uncommissioned, and only an operator can re-commission the pair.

:::danger[Never clone a controller's disk]
A clone that keeps both the original's machine-id and its network
card's MAC address is not caught by the box check, and the voters' own
check is not complete yet (tracked in
[#1029](https://github.com/joyautomation/nautilus/issues/1029), which
blocks lifting `experimental`). Build the second controller from a fresh
install, never from a copy of the first.
:::

Operator intent (substitute starts, stops and acknowledgements, alarm
acknowledgements and shelves) goes through a log with **three copies**:
one on each controller and one on the witness. A change is made once two
of the three hold it, so a takeover keeps it, even after the leader
died: the new leader catches the log up from the witness before its
first scan. **With one controller down,** the other keeps controlling
the plant and operators can still start, stop and acknowledge
substitutes and acknowledge and shelve alarms. **With both the other
controller and the witness down,** these are refused with
"operator-intent log has no majority: neither the other controller nor
the witness answers" until one of them returns; a substitute's expiry
still happens on time. A change sent just as a copy went away can be
answered "outcome unknown": it may still take effect, so re-read before
sending it again.

A copy counts only once it is known to hold everything committed
("synced"). A controller whose copy was lost (its file deleted or
damaged, a disk replaced, a cloned disk, which is moved aside) starts
**unsynced** and says so in `GET /api/cluster` (its `log` block) and on
the dashboard's banner. An unsynced controller still takes over when it
must, so control never waits for operator data, but while it leads,
substitute and alarm changes are refused with "operator log not synced",
and its takeover carries nothing from the log (as below). It syncs as
soon as both other copies count (answer, synced), or, if it was not
leading, at the leader's next heartbeat. While the other controller is reachable and
synced, an unsynced one doesn't campaign; if it leads and can't catch up
while the other is synced and able to lead, it hands the lead over
(`yielded to a: own operator log unsynced, a synced` in
`GET /api/cluster`), at most once every 10 minutes.

**A new pair's logs are vouched for at commissioning.** All three copies
start empty, so none is synced yet: the re-commission that commissions
the pair vouches for that controller's copy, and its first takeover
syncs the other two. A vouch is refused while any other copy doesn't
answer (it may hold entries the vouched one lacks: bring it back, or
rebuild it), when another copy holds a later log (named in the refusal),
and for a copy that was lost (re-commission the other controller
instead). Logs from before this release need no vouch.

:::caution[The witness holds plant operator data]
The witness's state directory keeps a copy of the operator-intent log:
who started or stopped a substitute (and the tag and the value), who
acknowledged or shelved which alarm, and when. Guard the witness host
like a controller; its files are readable by the service user only.

The peer token authenticates every peer and witness request, but it
travels as an HTTP header, and peer traffic is **not encrypted**: on an
open network it can be read, and the token replayed. Run the pair's and
the witness's peer ports only over a **trusted link**: a dedicated
network or VLAN, or a VPN such as WireGuard or a tailnet.
:::

**Rebuilding the witness loses nothing while both controllers are up**:
`naut witness --rebuild`, or a fresh state directory, is a new
incarnation that both controllers adopt after a quiet period, and the
leader then copies the log to it again. **Never restore the witness's
state directory from a backup**: an older copy of the log under the same
incarnation can't be told from a current one, and could cost an
acknowledgement. Rebuild it instead. **Upgrade the witness first**: a
controller sees a witness from before this release as an unreachable
copy, which is safe, but leaves the log on the two controllers until the
witness is upgraded.

**Sign-in is not on the log.** Sessions are kept by the leader, so a
takeover signs everyone out, as before.

Alarm acknowledgements and shelves go through the same log. When the
other controller takes over and catches the log up (from the old leader,
the witness, or both), an alarm acknowledged
before the takeover stays acknowledged, and a shelf stays until its own
end. An acknowledgement is of an occurrence: once an acknowledged alarm
leaves that occurrence (it returns to normal, a latched alarm returns
unacknowledged, its tag goes dark, or it activates anew), the leader
clears the acknowledgement in the log, so a new occurrence annunciates
after a takeover. **The window:** if the leader dies after that and
before the clear is committed, the next leader can still carry the old
acknowledgement to the new occurrence. With two copies of the log
answering that is a few tens of milliseconds; while only the leader's
answers, the clear waits for another, so the window lasts as long as
that outage plus about 2 s.
An acknowledgement or a shelve made just before a controller died and
committed more than 60 s later is dropped, not applied to whatever
occurrence is live by then. While a new leader's takeover is still
waiting (for its inputs to come live, or for the log to catch up), an
acknowledgement or a shelve sent to it is refused with "this controller
is taking over: its alarm state is not restored yet" (503); retry it a
moment later.

**When the takeover could not catch the log up** (the new leader's own
copy is unsynced and only one other copy answers, or neither does), no
acknowledgement is carried at all: acknowledged alarms annunciate again
on the new leader (the safe direction), and the alarm journal says
"acks not carried: takeover unsynced" when it held any. Shelves still
carry. Substitutes it held stay, marked "catch-up failed" until the log
confirms them.

**One set of credentials on both.** The pair is one controller to its
clients, so both need the same `NAUTILUS_TOKEN`, the same users file and
the same tag API certificate. They compare them at every handshake (a
few seconds apart) and say so in `GET /api/cluster` and on the
dashboard's banner when they differ; it never stops either from leading.
`naut deploy` leaves the state directory alone, so it does not copy the
users file: copy the same `users.json` to both controllers yourself (for
example with scp, into each one's state directory; each reloads it
within a second), and never add users separately on each box, since each
box salts its own password hashes and the same password still makes a
different file. A certificate renewal shows as a difference for the few
minutes it takes to reach both. "Could not be verified" means the two
controllers hold different `NAUTILUS_PEER_TOKEN`s (or something else
answers in the peer's place), not that the files differ.

Both controllers must run the same `redundancy:` section (`peers:` and
`witness:`). If they don't (an edited manifest deployed to one box
only), both report it in `GET /api/cluster` and on the dashboard's
banner, and the one that isn't leading won't take over until they match
again; a leader keeps leading. **That can stop control after a second
fault**: if the leader's lease lapses while the other controller still
runs a different section, neither leads until the sections match. A live I/O apply is refused while the other
controller runs an older build that can't keep it: finish the upgrade on
both, then apply.

Each controller's tag API must be reachable by the other (a standby
forwards writes to the leader). The manifest's default address is
`localhost:8080`, so set `server.addr` (for example `:8080`) or
`NAUTILUS_ADDR` in `/etc/nautilus/env`; a pair member bound to localhost
only is refused at start.

## RBAC

The service account needs exactly this — no CRDs, no operator:

```yaml
rules:
  - apiGroups: [""]
    resources: [configmaps]
    verbs: [get, create, update]
  - apiGroups: [coordination.k8s.io]
    resources: [leases]
    verbs: [get, create, update]
```

(No `delete`: a releasing leader expires its own lease by writing an
epoch renew time, which is why the takeover is immediate.)

## From Go

The same seams are the library API — bring your own implementations or
use the provided ones:

```go
rt, _ := runtime.New(runtime.Options{
    // ...
    Retain:      retain.New("line1-retain", "retain.json"),
    Coordinator: elector, // anything with IsLeader() bool; leader.New(...) provides it
})
```

`runtime.Coordinator` is one method — `IsLeader() bool` — so a different
election scheme (a hardware keyswitch, a modbus arbiter) plugs in with a
few lines.

`elector.SetHealth(rt.Liveness)` makes the lease need live scans, as
`naut run` does; call it before `elector.Run`. `rt.Liveness(now, max)` is
nil while the runtime's scans are completing and starting on schedule,
else an error naming the stuck or starved task, so another election
scheme can ask it too. The same verdict drives the systemd watchdog
heartbeat and, standalone, the local gate.

`rt.Run(ctx)` returns after the last scan and, with a retain store, the
last save. Wait for it to return before the process exits, or the writes
since the saver's last tick are lost.
