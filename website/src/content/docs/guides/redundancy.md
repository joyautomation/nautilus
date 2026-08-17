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

- **What is saved:** every `role: setpoint` tag, plus the source of any
  program that differs from what shipped (an online edit). Nothing else —
  inputs re-read from the field, state re-seeds, outputs re-derive from
  logic. The PID does not persist its integral; it warm-starts from live
  field values, the way a real changeover does.
- **When:** every 2 seconds, only when something changed, and only by the
  leader. A hard kill can lose at most 2 seconds of operator input.
- **Load order:** retained values are applied before the first scan and
  win over the manifest's `init:` seeds — what the operator set last
  outlives what the project file said first.
- A retained online edit still shows as *modified* in the dashboard and
  `nautilus pull`, so the git loop closes the same way it always did.

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
- **On takeover**, the new leader re-reads the retain store (the old
  leader may have accepted retunes or edits while this replica idled),
  discards program frames so warm-start logic runs against live field
  values, and zeroes its scan clock so the first `dt` is one scan, not
  the hours it spent standing by. Process state is deliberately **not**
  replicated — configuration travels through the retain store, process
  state re-derives from the field.
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
