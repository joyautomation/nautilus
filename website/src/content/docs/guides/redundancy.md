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
  On a redundant pair with `redundancy.public-urls`, it also lists
  `peers` (`name`, `url`, `leader`): the controllers as a browser opens
  them, never their peer-link addresses. The HMI kit remembers them, and
  when its own controller stops answering it goes to the one that does,
  the leader first (once per 30 s at most, so two dead boxes cannot make
  it bounce). Without `public-urls` the page stays where it is.
- **OPC UA clients of a pair.** With an `opcua:` section, each controller is its
  own OPC UA server: its ApplicationUri is the configured (or default) one with
  `-a` or `-b` appended. Each reports `RedundancySupport` (Cold), `ServerUriArray`
  (both URIs) and a live `ServiceLevel`: 250 for the leader, 200 for a leader
  with a standing warning (a pair always has one until state streaming), 100 for a
  standby that knows the leader and has a synced log, 1 for any other standby, and
  0 for a spare or a copy held by a duplicate mark. A client that holds a session
  to each keeps the higher. A write to a standby stays refused (`Bad_InvalidState`).
- **Browser sign-ins across a takeover.** A signed-in operator stays signed in when
  the other controller takes over, because the sign-in's record (a hash of the
  cookie, who it is, when it ends; never the cookie itself) goes through the
  operator log. Elevation is not carried: an elevated action asks for the password
  again. A sign-out, a changed password or a removed user reach the other controller
  through the log too, and a sign-out waits briefly for that, so a takeover right
  after cannot bring the session back. Signing in never waits for the log: with
  the log read-only or without a majority the session works on that controller only
  (the account chip says "local session", and the journal records it), and is
  replicated once the log returns. A copy of the log that is not known complete
  honours only local sessions.
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
leader without a cluster. Commissioning and re-commissioning are below;
the rest of the operator guide (the takeover time) comes with the rest
of the work (#710). For now the pair is an explicit opt-in:

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
- `NAUTILUS_CLUSTER`: the pair's cluster ID, the same on both
  controllers (the witness keys each pair by it as they first vote).

#### Commissioning a new pair

1. **Draw the pair's identity**, on any machine, before anything runs:

   ```sh
   naut redundancy new-pair --dir ./pair-env
   ```

   It writes `a.env`, `b.env` and `witness.env` (mode 0600) and prints
   their paths and the cluster ID, never the token. It refuses to write
   over an existing file. Copy each file to its box's `/etc/nautilus/env`
   and delete the copies here: `NAUTILUS_PEER_TOKEN` is the pair's shared
   secret. (`--print-secrets` prints all three to the terminal instead,
   and writes nothing.)
2. **Start** both controllers and the witness. A new pair waits for the
   operator: it elects no leader until it is commissioned.
3. **Commission**, on either controller:

   ```sh
   naut redundancy commission
   ```

   Every member must answer, and neither controller may have been
   commissioned before. A member that started less than a lease and a
   quarter ago can't answer yet: run it again a moment later. It takes
   the witness's incarnation and a floor above every term the three
   members report, and prints the pair's leader once one is elected.
   It needs the `redundancy` permission (administrator by default)
   under an `auth:` block, else `NAUTILUS_TOKEN`; it is journalled as
   `redundancy.commission`, accepted or refused.

The witness can't tell a new pair from one whose **two controllers both
lost their state**: commission would go ahead there too, with its floor
above every term the three still report. A term the two controllers
granted each other while the witness was down is lost with both files,
and no command can know it, the re-commission below included. That is
safe: a term is held only by a controller's running process, and both
controllers lost their files, so neither still runs under one. Both
controllers losing their state is exactly the case where only an
operator can commission the pair again.

#### Re-commissioning a controller by hand

A controller that lost its state normally re-adopts from the other one
within seconds. When it can't (the alarm says "re-commission by hand":
the other controller is down too, or its witness incarnation is gone),
run on it:

```sh
naut redundancy recommission
```

Without `--floor` it changes nothing and prints the evidence: the
highest term each member knows, and a floor above them all. Then:

```sh
naut redundancy recommission --floor <n>
```

The floor must be above every term a member that answered knows (not
only this controller's: one that lost its file knows little). A member
that did not answer is named; the floor must then also be above any term
it ever used. The witness must answer. `--incarnation` is the witness's
current one, the default. A controller that reports
`redundancy.box-changed` needs `--box-replaced`: you replaced its box,
it is not a copy of a running controller. Each flag is named in the
journal entry (`redundancy.recommission`).

The voter state, `quorum.state`, lives in the systemd state directory
(`/var/lib/nautilus` under `naut deploy`) or in `redundancy: state-dir:`.
Every `naut deploy` step keeps it, and a backup never contains it:
restoring a voter's state from an old file, the witness's included, is
not supported. A witness restored from a file older than the last
`recommission --from-spare` doesn't know that command's fence, and the
replaced box could lead beside it in a split. If the witness's state is
lost, rebuild it (a new incarnation, which learns the fences from the
controllers). It
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

**Cloning a controller is unsupported.** Build a second or replacement
controller from a fresh install, never from a copy of the first. If a
clone does run, here is what the pair does.

A clone that keeps both the original's machine-id and its network card's
MAC address passes the box check. The voters catch it instead (#1029):

- Each vote message carries the promise its sender's voter has made, and
  every voter notes the boots it hears under each name.
- A name heard under two live boots gets no new grant.
- A voter that hears **both copies running at once** (each keeps
  sending after the other has started) **marks** the name. The mark stays
  until it is cleared: a marked name gets no new grant from that voter, and
  its vote counts for nobody that voter's controller counts.
- A controller told it is marked stops campaigning, and its own voter
  grants the other controller nothing new. Only the voter that marked it
  can lift that, and only while that voter has not restarted.
- `redundancy.duplicate-member` is raised by the voter that marks it,
  naming the duplicated controller ("two processes claim to be `a`: this
  voter heard both running at once"). A controller that is marked logs
  `redundancy.duplicate-marked`.

So **there are never two leaders once a voter has heard both copies
running**, also when a later split separates them, or heals one copy back
first, and also when **any one** member restarts inside such a split
while the copy still runs. In the simulator, which also lets the other
controller's votes reach the copy, that held in 48,000 split runs with one
member restarting, and in 21,600 with two members restarting. Only the
single restart is claimed.

A plain restart, and `naut deploy`'s update or rollback, never marks
anything: the old process is gone before the new one starts.

**Marks survive restarts.** A member writes a mark into its
`quorum.state` right after it tells the marked controller, so members
restarted inside a split, while the copy still runs, still hold it when
they come back. Before marks were kept, such restarts made the voters
pick again, and often picked the copy: the **real** box went spare in
307 to 348 of 3,600 simulator runs where the peer and the witness both
restarted within two seconds of the clone's start, and 309 of 3,600 with
the peer and the witness alone. With marks kept, 0 or 1. A member that
crashes between telling the mark and writing it (a few milliseconds)
forgets it: the controller it held stays held, and the clear below frees
it.

**A running clone costs redundancy, not the leader**, until the network
also fails:

- **With no other fault the pair keeps a leader.** A clone of the healthy
  leader costs no takeover. If a controller restarts or the leader's scans
  hang while the clone runs, the other controller takes over within a
  takeover's time.
- **During a split with a clone, usually nobody leads.** If a cut
  separates the clone from its original, or one of the pair's own links
  (A–B, A–witness or B–witness) fails while the clone runs, usually
  neither side can lead until the cut heals and the marks are cleared.
  Never two.
- **After a mark, a witness outage or a cut leaves no leader until the
  mark is cleared**, even once the clone is stopped: the controller that
  still leads needs both voters that are not marked, and a marked
  controller can't take over.
- **Operator writes that go through the operator-intent log are refused**
  (substitutes, alarm acknowledgements and shelves) while a copy runs in a
  member's place or a spare stands in for one: neither the copy's log nor
  a silent spare's counts, so the leader has too few copies that count.
  Control goes on; those writes come back once the member is a member
  again (the copy stopped and the original back, or the spare made the
  member with `recommission --from-spare` and restarted).
- **A restart doesn't clear a mark.** Each member writes its marks into
  its `quorum.state`, so a mark outlives restarts, upgrades and `naut
  deploy`, also a mark made by mistake (below). Only the clear-duplicate
  command ends it.

With no leader, no controller drives outputs. The drivers stop asserting
them, so each device's communication watchdog sees the silence and puts
that device in its own safe state, as for a hung scan (above). An output
on a device with no communication watchdog keeps its last value.

**The copy makes itself a spare.** Once both other members have marked
the name, each tells it which process it holds as that member: the one
it heard **first**, among those still sending. A process of that name
the members first heard only after they marked it (the original,
restarted beside a running copy, or a copy restarted before it went
spare) is told nothing, and stays held. A member that restarted tells a copy it is a spare only once it hears
the process it holds send again. A copy that both other
members name another process for a full lease writes "spare" into its
`quorum.state` and goes silent: it no longer votes, campaigns, probes,
answers the other controller or takes part in the operator-intent log,
its tag API refuses writes (it knows no leader), and its `/api/cluster`
says `duplicate of a - spare`. It also raises `redundancy.spare`, at the
decision and every time it starts again. A spare stays one across
restarts and a box change: only an operator turns it back.

- In the simulator, a clone that came up beside a running pair went spare
  in every run, 3.4 s after it started on average and 4.0 s at most (2 s
  lease). The original never did, also with 30 to 80% message loss on
  its links. A real controller never went spare where a restart's late
  messages marked it by mistake on one member: one member's word is
  never enough, it takes both.
- **"First" means who was running first, not which box is real.** The
  pair can't tell a copy from its original. A copy that boots together
  with its original (a site-wide power cycle), comes up while the
  original is down, or is heard before the original's restart within
  about a second of the copy's start, may keep the name, and the
  **real** box then goes spare: 87 of 300 same-time boots in the
  simulator, and 300, 257 and 3 of 300 with the original restarting a
  quarter, half and one second after the copy started (none later).
  Never both: the other copy carries on held, as above.
- **A leader is never demoted under its own lease.**

What a spare costs:

- **If the process the voters hold dies while the spare runs**, the pair
  runs on the other controller and the witness alone until an operator
  acts: redundancy is lost, not the leader.
- **After a spare, one controller leads on two members until the marks
  are cleared.** The copy goes quiet before any split can take the
  original's lease, so the original keeps leading, while the member that
  marked it gives it nothing new. One more fault (the witness down, or a
  cut) then leaves no leader until the marks are cleared.

**What to do:** the alarm names the duplicated controller ("two processes
claim to be `a`") and the member that raised it.

1. **Stop the copy, and confirm it is down**: powered off, or its process
   ended. Unplugging its cable, pausing or suspending a VM is not stopping
   it: it comes back as the same process.
2. **On either controller, run the clear:**

   ```sh
   naut redundancy clear-duplicate a --copy-stopped
   ```

   `--copy-stopped` is your word that step 1 is done, and the audit
   journal records it under your name (`redundancy.clear-duplicate`). The
   command then:
   - **checks every member** (this controller, the other one and the
     witness). All three must be up (a member that started less than a
     lease and a quarter ago can't answer yet: try again shortly; one that
     is down must be brought back first: it keeps its marks on disk), and
     together they must hear **one**
     process named `a` in the last lease and a quarter. Otherwise it
     clears nothing and says who is silent, or who still hears two. One
     member alone can't tell: a split can put the copy where one member
     hears it and the original where another does. So the members' answers
     are taken together;
   - **clears the mark at each member**, each checking its own voter again
     first. Each member also keeps the process the check heard and sets
     aside every other process of that name any member knew, on disk too.
     If one of those comes back (a copy that was only unplugged, paused
     or hung after all), its first message, or its first answer to the
     other controller's vote, marks the name again before that counts
     for anything, whichever member it reaches. If no member hears any process of
     the name, there is nothing to keep, and the clear is refused: start
     the real controller (or heal its links), then clear;
   - **checks again a lease later** and prints what each member says. A
     mark that came back, or a second process heard by any member, means a
     copy still runs.

   The controller that was held campaigns again on its own: **no restart,
   no hand-over.** If a member couldn't be reached during the clear, it
   keeps its mark (never worse than before) and the command says so: run
   it again once that member answers.

`naut redundancy status` shows what this controller's voter marks, and
whether this controller is held or a spare. The same is in `GET
/api/cluster` (`marked`, `held`, `spare`).

**Who may run it.** Under an `auth:` block, the `redundancy` permission
(`administrator` by default). In a browser session it asks for your
password again, like the other elevated actions. A personal access token
or `NAUTILUS_TOKEN` counts as fresh, so on the command line it is the
role alone. While nobody leads, signing in through a browser waits for
an election, so use the command line with a token then. Without an `auth:` block, only `NAUTILUS_TOKEN` will do, as a
bearer token. These commands decide which process the pair counts as a
member, so a same-origin request with no credential is refused, unlike
other writes. The command line reads the credential from
`NAUTILUS_TOKEN` or `--token-file`, never from its arguments.

**Restarts don't clear marks**, since they are kept on disk: the command
is the only way. Each clear also raises a number the members keep for
that name, so a member that comes back without the mark (wiped, rebuilt,
restored from a backup, or a copy of one) can't end a hold by forgetting
it; only a clear does, and every clear does, also when one member is
behind. Once a member has marked or cleared a name, it keeps that record
(the number, and the processes the clear set aside) in a newer
`quorum.state` format, also after the clear. An earlier release refuses
to start on that file and says how to go on: move the file aside, and
that member starts uncommissioned and re-adopts from the other
controller (the witness, as a new incarnation the controllers adopt).
Its records go with the file, so do that only with the copy stopped.

In the simulator (two-second lease, 300 runs for each of eight split
shapes):
- **No clear gave two leaders**, also when it reached only one or two of
  the members.
- **Cleared once the split had healed and the copy was stopped**, the pair
  was never without a leader on average, and for 0.7 s at most. The
  restarts, measured the same way, left it without one for 3.2 to 4.5 s on
  average, and 5.3 s at most.
- **Cleared during the split**, the longest time without a leader after
  the split healed averaged 0.1 to 0.5 s (1.0 s at most). A clear that
  reached only some members averaged up to 1.5 s (4.0 s at most).
- **With the copy still running behind a split** (step 1 skipped), the
  check refused every clear. Asking each member alone instead let
  through clears that gave two leaders in 7 of 300 runs.
- **With the copy only cut off at the clear** (unplugged, not stopped)
  and coming back later into a split, no two leaders in 3,000 runs: its
  first message marked it again. Without that, 26 of 500, more than the
  restarts' 25.
- **With the copy only paused, or its scans hung, at the clear**, and
  coming back straight into a split beside the other controller: two
  leaders in 4 of 1,000 runs (paused) and up to 13 of 1,000 (hung), for
  36 ms to 0.8 s, against 56 and 57 of 1,000 with the restarts. Until the
  copy's own first message arrives, its voter's answers can still count
  for the other controller. This is why step 1 means powered off or its
  process ended.

**When a spare is involved:**

- **The spare is the copy** (`redundancy.spare` on a box that is not the
  real controller) and the real controller still runs: stop the copy,
  and keep it stopped. That is step 1 above.
- **The spare should become the member**: the real box made a spare
  because its copy ran first, or a copy you mean to keep in place of a
  dead original (on the original's addresses). First stop the controller
  the other members hold under that name, confirm it is down (powered
  off, or its process ended), **and keep it from starting again**: leave
  it off, or wipe its disk. Wait a lease and a quarter, then run on the
  spare:

  ```sh
  naut redundancy recommission --from-spare --copy-stopped
  ```

  `--copy-stopped` is your statement, journalled under your name. On top
  of it, the other controller must be a commissioned member (the spare
  re-adopts from it), and its voter and the witness must both answer and
  both hear no process under that name, else nothing changes. The
  command then clears the name at both, keeping no process: every
  process of that name they knew, the one they held included, is set
  aside, so if it was only cut off or paused it is marked the moment it
  is heard again. Last, it fences the name at both: from then on they
  refuse the state the stopped controller carries on its disk, at any
  term. It moves this box's `quorum.state` aside
  (`quorum.state.previous-spare-<time>`). Then **restart naut on the
  spare**: it starts uncommissioned and re-adopts from the other
  controller within seconds, as after a network card change, and is a
  standby again. A spare answers no other command; its only messages are
  these requests.

  If the command prints **INCOMPLETE: the peer is fenced, the witness is
  not**, run it again: nothing was moved aside, and the box is still a
  spare. Until it completes, the stopped controller's state is refused
  at the other controller only, so if it runs after all it can still
  lead with the witness.

  If the stopped controller is switched back on later with its old disk
  anyway, both other members refuse it, even inside a network split
  where neither hears it beside the member. It raises
  `redundancy.superseded`: *this box's state is from before a was
  replaced (recommission --from-spare): it can no longer vote*, and stops
  campaigning. The other members keep that refusal even if one of them
  later loses its own state file. Wipe its state (move its `quorum.state`
  aside) **only with the network whole**: wiped, it starts as a copy of
  the member, is heard beside it and made a spare. Started inside a
  network split that later moves, where a member hears it beside the
  member for less than about a lease and a quarter, it is a copy the
  members can't catch in time (see "What the voters can't catch"
  below). Or keep it off.

Do not run an older naut release on a spare. A release before the spare
can't read the spare's `quorum.state`: it sets the file aside and starts
uncommissioned, so the box would forget it is a spare. It can't vote with
the newer pair (`redundancy.version-mismatch`: the vote protocol changed
with the spare), and that is the only thing that keeps it out. From this
release on, naut refuses to start over a `quorum.state` written by a newer
release, and says to run that release. A controller that is not a spare
writes the same file format as before, so it can go back to an earlier
release.

**What the check costs:** one more vote round, at most, on a takeover
after the leader dies. That is at most 0.95 s plus one round trip
between the controller and the witness at the default 2 s lease
(`MaxRho × lease + 3/8 × lease + RTT`). In our measurements it came to
0.3 s at worst on a LAN, 0.6 s with 100 ms links, and 1.2 s with links
near the largest delay a 2 s lease allows.

:::danger[What the voters can't catch]
- **A clone booted on the far side of a split that already exists** is
  never seen by a voter that knows the original. Only the box check
  stops it, so it is caught only when the clone's machine-id or MAC
  differs.
- **A split within about a second of the clone's start**, before any
  voter has heard both copies running, leaves nothing marked either.
  For a controller's old box switched on again after `recommission
  --from-spare` with its disk wiped, the window is a lease and a quarter
  (2.5 s at the default lease): the mark comes in time, but it is made a
  spare only about a lease later, and a split that moves it to the other
  controller's side before then gives two leaders. In the simulator that
  happened in every run from an eighth to three quarters of a lease, 41
  of 100 at one lease, and none from a lease and a quarter. Keep the old
  box off, or wipe it only with the network whole. In
  the simulator (2 s lease) that gave two leaders in 52, 21 and 1 of
  3,600 runs with the split in the first, second and third half second
  after the clone started, and none later.
- **A member that crashes between telling a mark and writing it**, or
  whose disk is wiped, forgets the mark: until it marks again, it
  counts the copy as a member. In the simulator, with the disk slowed to
  0.3 to 1.5 s per write and both voters crashing inside that window,
  that gave two leaders in 13 to 24 of 300 runs (22 to 29 when marks
  were not kept at all). Real writes take milliseconds.
- **A message delayed past a quarter of a lease** (beyond the delay a
  lease allows) from a controller that has just restarted can mark that
  controller by mistake. It costs that
  controller's redundancy until the mark is cleared (a restart no longer
  does), never the leader.
  Late to one member, it never makes that controller a spare: that takes
  both other members. Late to **both**, by more than about 1.25 leases,
  the restarted controller can go spare: in the simulator 0 of 300 up to
  a lease late, 24, 150 and 299 of 300 at 1.25, 1.5 and 2 leases. Then
  that box is silent until an operator acts; the other controller leads.
  A controller resets its peer connections when it closes them, so an
  exiting process's unsent messages die with it rather than being
  retransmitted after a link outage: this takes more than a lease of
  delay inside the network itself.
- **A copy that boots with its original, or while the original is down**,
  may be the one the voters hold, and then the real box goes spare (see
  above).

Stop a clone as soon as `redundancy.duplicate-member` names it.
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
sending it again. The journal records it with result `unknown`. If the
controller that answered later sees that change in the log, it adds an
`oplog.resolved` entry: `detail.resolves` names the unknown entry and
`detail.action` its action. `detail.outcome` is one of:

- `committed`: the log has it and this controller applied it in order.
  That doesn't prove it took effect: an alarm engine or the runtime can
  still refuse it when applying it. Read the state.
- `lapsed` (result `refused`): committed too late, so it was never carried out.
- `stays unknown (evicted)`: the controller stopped tracking it.

It does that only on evidence. These stay `unknown`:

- a change that never commits;
- one whose controller restarts first;
- a substitute start sent without a `requestId`;
- a start retried with the same `requestId` while the first is still tracked (both stay unknown). An operator's alarm acknowledgement or shelve
that lapses is journalled as `alarm.lapse`, one entry per alarm, under
the operator's name.

Each change in the log carries who made it (`user:alice`,
`token:historian`), so the journal names the same principal whichever
controller carries it out. During a rolling upgrade a controller on an
older version drops that field. Changes it passes on are then journalled
under the operator's display name.

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
while the other is synced, able to lead and able to sync the others (a
vouch of its own pending, or another copy that counts), it hands the
lead over (`yielded to a: own operator log unsynced, a synced and able to
sync the others` in `GET /api/cluster`), at most once every 10 minutes.

**A synced controller alone among the copies can't decide either.** If
both other copies were lost (the other controller's and the witness's),
changes committed while it lagged may have been on them, so it leads
read-only and says "operator log read-only: no second counting copy".
Bringing back a copy that still holds its log ends it; otherwise the way
past is a person's decision: `naut redundancy vouch` (below).

**A new pair's logs are vouched for at commissioning.** All three copies
start empty, so none is synced yet: `naut redundancy commission` vouches
for the copy of the controller it runs on, and its first takeover
syncs the other two (it waits until every copy answers). A vouch is
refused while any other copy doesn't answer (it may hold entries the
vouched one lacks: bring it back, or rebuild it), when a copy that
doesn't count holds a later log (its entries would be lost; named in the
refusal), and for a copy that was lost (re-commission the other
controller instead, or accept it: below). **A refused vouch never stops
the re-commission:** the pair is commissioned and controls the plant, its
log stays read-only, and `GET /api/cluster` says why. A later log on a copy that counts is no
refusal: it is taken. So if both controllers lose their disks while the
witness's copy is whole, re-commissioning one brings the witness's
entries back. Logs from before this release need no vouch.

#### Accepting a lost operator log: `naut redundancy vouch`

When the log stays read-only and the copies that would end it can't come
back (a box dead for good, a witness rebuilt, a disk lost on both
controllers), a person decides what is lost. Run on the controller
whose copy you keep:

```sh
naut redundancy vouch
```

It changes nothing in the election. It prints each copy (does it
answer, does it count, its last entry), what accepting would lose, a
short digest of that picture, and the exact command that accepts it,
for example:

```
operator-intent log copies:
  this controller  counts; last entry term 7, seq 41
  controller b     DOES NOT ANSWER: connection refused
  the witness      not synced (does not count); last entry term 0, seq 0
would be lost: controller b does not answer: it may hold entries this log lacks; ...
picture digest 3f9c0a17be. If that loss is acceptable, run on this controller:
  naut redundancy vouch --accept-loss=3f9c0a17be
```

- `--accept-loss=<digest>` accepts what the other copies may hold: a
  copy that doesn't answer, a later log that doesn't count, or every
  other copy lost.
- An override needs one other copy answering. With both the other
  controller and the witness silent, the catch-up refuses ("a loss
  accepted with every other copy silent would leave the new history on
  this copy alone"): bring one back or rebuild it (a rebuilt witness
  answers), then vouch again. Alone, the controller could commit nothing
  anyway.
- `--accept-lost-log=<digest>` accepts that this controller's own log was
  lost (`oplog-lost`). With the witness's copy whole it gets the
  witness's entries back.
- The acceptance is for that picture only. If the copies changed since
  it was printed, the command refuses and prints the new one. If, when
  this controller next catches up as leader, a copy that answered is now
  silent (or holds more than was shown), the catch-up waits and says "not
  what was accepted: naut redundancy vouch again". Running `vouch` again
  prints the new picture to accept.
- A vouch holds until naut next restarts on that controller. If it
  restarts before the vouch was used, the copy stays synced, but you
  vouch again against the picture as it is then.
- `commission` and `recommission` take the same two flags for the vouch
  they run. A refused vouch there prints the picture and the `vouch`
  command to run next.
- Each run is journalled as `redundancy.vouch`, accepted or refused, with
  the digest and what you accepted.

**A copy whose loss was accepted never counts again.** When the catch-up
actually overrides a copy (one that didn't answer, or a later one that
doesn't count), the log starts a new history (`history 1` in `naut
redundancy status`). A copy at an older history counts for nothing. When
it comes back (the dead box repaired, say), it is told so on first
contact and the banner says "superseded". At its next start naut moves its
log aside, before replaying anything, to `oplog.jsonl.superseded-<time>`
(the newest one is kept), drops what its retain store keeps of the old
history (substitutes, acknowledgements, shelves), and syncs the
new history. It also stops running the old history's substitutes at once,
and its acknowledgements and shelves go (its alarms re-annunciate), even
while it leads: it then leads read-only for control and hands over to the
other controller once that one can take it. Under systemd (with
`Restart=on-failure`, as `naut deploy`'s unit has) it restarts itself
once, as soon as it is the
standby and the other controller leads synced at the new history
(journalled as `redundancy.oplog-superseded-restart`). Otherwise the banner
says why it waits, and you restart naut on it. If it has to take over
before that restart, it runs none of the old history's substitutes.

- Once a pair has a history above 0, a controller whose state was wiped or
  whose box was replaced also restarts once when it first joins. This is
  automatic under systemd.
- Right after such an override, if the leadership moves before the
  witness has synced (it normally syncs within half a lease), the new
  leader is alone again. Run `vouch --accept-loss` once more; it
  overrides nothing.
- The same half lease is the one window left: if the controller that
  accepted the loss dies before its first heartbeat reaches the witness,
  the other controller and the witness can go on at the old history, and
  what they commit then is superseded when it returns. Keep it running
  until `naut redundancy status` shows the witness synced.
- **A fork.** If people accepted losses on both controllers, each seeing
  the other controller silent and the witness not yet moved (the window
  above), both logs start the same history number with different
  contents. Neither counts the other,
  neither moves aside, and both say "operator log forked at history N
  (accepted by ...)". Choose one: run `naut redundancy vouch` on the
  controller whose log you keep and accept with `--accept-loss`. Its next
  catch-up as leader starts history N+1 (if the other controller leads,
  hand the lead over first), and the other side is superseded as above.
  A fork is the only case where the vouch acts on a synced copy that
  another copy counts beside.
- At a history above 0, a copy on an older naut build counts for nothing:
  upgrade all three members together. At history 0 nothing changes.

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
Each acknowledgement names the occurrence it was for, and is applied
only to that one: an acknowledgement committed after its occurrence
ended acknowledges nothing (the journal notes it), and one made just
before a controller died and committed more than 60 s later is dropped.

**Equipment out of service** ([suppression by state](/guides/alarms/#suppression-by-state))
goes through the log too, and a synced takeover keeps it until its own
end. One made just before a controller died and committed more than 60 s
later is dropped; a return to service never is. A takeover that could
not catch the log up does **not** carry it: a window the log cannot vouch
for would hide alarms. The alarm journal says "out of service not
carried", the alarms show, and the new leader ends the window in the log
(no later takeover carries it back, even before that commits, unless
that controller restarts first: its log then brings the window back,
as it would on the other controller). The leader
also ends in the log a window that expired on its clock, so a controller
whose clock is behind finds none to restore; one that takes over while a
window runs restores no more than was granted. Take the equipment out again
if the work goes on.

**Escalation goes on at its level.** For the alarms an escalation policy
covers, the leader records each occurrence (its activation, and when it
began waiting on a person) in the log at every log tick, and its end. A
new leader continues an occurrence the field still asserts: it keeps its
ID (so the links already sent still name it), its activation time and
its escalation clock, and resends the current level once (`resend after
takeover`). One that began within a tick of the leader's death is not in
the log yet: the new leader starts it afresh at level 1. While a new leader's takeover is still
waiting (for its inputs to come live, or for the log to catch up), an
acknowledgement or a shelve sent to it is refused with "this controller
is taking over: its alarm state is not restored yet" (503); retry it a
moment later.

**When the takeover could not catch the log up** (the new leader's own
copy is unsynced and only one other copy answers, or neither does), no
acknowledgement is carried at all: acknowledged alarms annunciate again
on the new leader (the safe direction), and the alarm journal says
"acks not carried: takeover unsynced" when it held any. Shelves still
carry. Once its log syncs, an acknowledgement the log committed still
applies to an alarm that has stood since the takeover (its condition
read true at every scan): the alarm takes the acknowledged occurrence. An
alarm whose condition dropped in between is a new occurrence, and the
journal says the old acknowledgement was not carried. Binding an
acknowledgement to its occurrence needs all three copies of the log on a
build that has it (both controllers and the witness): an older copy
stores entries without their occurrences, and an acknowledgement that
passed through it applies to whatever occurrence is on, as before. Substitutes it held stay, marked "catch-up failed" until the log
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

The same goes for the **alarm contacts** when a channel pages people:
each controller keeps its own `alarm-contacts.json` and
`alarm-secrets.json` in its state directory, and a standby that has
other ones (or none) would page the wrong people, or nobody, after a
takeover. The controllers compare a digest of their content, keyed with
the peer token and never sent, at every handshake. When they differ,
`GET /api/cluster` and the banner say "alarm contacts differ from the
peer": copy both files from the controller that has the right ones into
the other's state directory (`scp -p`, keeping them mode 0600 and owned
by the service user), or set the same entries on both with
`naut alarms contacts set` and `naut alarms secrets set`. Like the other
credential checks, it never stops either from leading.

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
