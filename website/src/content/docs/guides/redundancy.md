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
  seconds of operator input.
- **Load order:** retained values are applied before the first scan and
  win over the manifest's `init:` seeds — what the operator set last
  outlives what the project file said first.
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
- **The lease needs live scans.** A leader renews only while its scans
  are completing, so a leader that is still running but whose scan is
  hung (logic that never returns, a driver call that blocks) hands over
  too. A scan counts as hung once one scan has been in progress for
  longer than twice its task's period, at least 1 s and at most 2 s,
  whatever the period: a task that runs every 10 s is fine between its
  scans, and a fast task that overruns now and then is not hung. A hung
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
- **The HTTP API follows the leader.** Every replica serves the
  dashboard, and a standby reverse-proxies API traffic to the leader
  (the leader's address rides inside the Lease), so a Service in front
  of the replicas just works. `GET /api/cluster` reports each replica's
  own view — mode, leader, transitions.
- The identity shown in `/api/cluster` comes from `POD_NAME`, and the
  proxy target from `POD_IP` — both from the downward API:

```yaml
env:
  - name: POD_NAME
    valueFrom: {fieldRef: {fieldPath: metadata.name}}
  - name: POD_IP
    valueFrom: {fieldRef: {fieldPath: status.podIP}}
```

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
nil while the runtime's scans are completing, else an error naming the
stuck task, so another election scheme can ask it too.

`rt.Run(ctx)` returns after the last scan and, with a retain store, the
last save. Wait for it to return before the process exits, or the writes
since the saver's last tick are lost.
