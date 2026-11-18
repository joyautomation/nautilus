# Design: a redundant controller pair and a witness, without Kubernetes

Status: **designed, not built.** The maintainer answered the first round of questions on
2026-11-17. One question is still open (§9).
The test plan is [redundancy-test-plan.md](redundancy-test-plan.md).
Issues: #710 (the pair), #716 (clients that follow the active controller). Part of #725.
Context: **Runtime** (`leader`, `retain`, `runtime`), plus a new `quorum` package and
`naut witness`.

---

## 0. The shape, in one paragraph

Two controllers, A and B, run the same build. A third voter, the **witness**, holds no process
data. Leadership is a **lease granted by a majority** of the three, numbered by a **term**. The
leader scans and writes. It also streams its state image (program frames, timers, SFC steps,
retained values, alarm acknowledgements) to the standby after every scan. When the leader can't
renew with a majority before its lease runs out, it **fences itself**: its write gate closes and
its drivers go silent. The standby takes over only after the granted lease has expired. With a
current image it continues the sequence (**hot takeover**). Without one it starts over from the
field, as today (**cold takeover**). This is a hot standby, not lockstep. It is never called
bumpless, and the measured takeover time is published instead.

## 1. What exists, and what changes

- **`leader.Elector`** elects through a Kubernetes Lease, by compare-and-swap. It stays exactly
  as it is: `redundancy: {lease: …}` selects it.
- **The new quorum elector** implements the same `runtime.Coordinator` surface: `IsLeader`,
  `Term`, `Decided`, `SetHealth`, `Status` and `Release`. As a result the drivers' write gates,
  the OPC UA server's write refusal, the standby's API proxy, the sd_notify watchdog (#733) and
  the force drop on takeover all keep working without change.
- **The write gate already needs** a live lease and live scans (#621). A new leader already
  sends every output once (#600/#606).
- **I/O loss does not move leadership today.** A leader that scans but can't reach a device
  keeps the lease. With a unit watchdog set, an OptoMMP standby holds no session, so the
  device's own watchdog trips (#626). The I/O-health handover in §3.2 is **new**.
- **Takeover today is always cold.** It reloads retain, resets frames and zeroes the scan
  clocks (`runtime/retain.go`). The pair adds a hot path (§4). Kubernetes mode keeps cold.
- **`retain` has no shared store off-cluster.** Each controller keeps its own file, and the
  state stream keeps the two equal.
- **`oplog` (open question, §9).** Under Kubernetes, `oplog` replicates operator intent (acks,
  shelves) by majority, so that a failover never loses an acknowledgement. The proposal is to
  keep that guarantee: the witness joins `oplog` as its third peer. It stores operator intent
  (kilobytes a day), never process data. Without that, intent rides the stream, and an action
  the stream had not yet carried is lost on failover.
- **`naut run` under systemd** comes from #733. Install, update and rollback come from
  `tools/deploy` (#741). The pair uses both and duplicates neither.
- **Waiting for live inputs** before the first scan after a start or a takeover is #786. That
  issue builds it once for both: this design's takeover step and a plain start.

## 2. Members and the manifest

```yaml
redundancy:
  peers:                 # the same file on both controllers
    a: ctl-a:9450
    b: ctl-b:9450
  witness: witness-host:9460
  lease: 2s              # default; the takeover time it gives is published
  stream: every-scan     # or {every: 5}: one image per 5 scans
  stream-link: ctl-a-link:9451   # optional dedicated A–B cable (§6)
```

- **Self.** Both controllers run one build, so the manifest can't say which controller it is.
  `NAUTILUS_NODE=a|b` says so, and `tools/deploy` sets it per box.
- **Misconfiguration is refused at start, never resolved quietly.** A peer list that differs
  between A and B is refused. So are two processes that both claim to be `a`: each process
  carries a random instance id, and the voters see two different ids under one name.
- **Peer calls carry a shared token**, compared in constant time as `oplog` does. Mutual TLS
  can follow #708.

## 3. Election and self-fencing

### 3.1 Votes

- **Voter state** is `(promisedTerm, grantedTo, promiseUntil, inSyncMark)`. Both controllers
  and the witness keep it. The in-sync mark is §3.3. A voter **fsyncs its state before it answers**: this replaces the Lease's
  compare-and-swap, which was the whole correctness argument in Kubernetes mode. A voter that
  restarts, or lost its state file, **grants nothing for one full lease**, because it has lost
  its timer.
- **Granting a term.** A candidate asks for term T+1. A voter grants it when T+1 is greater
  than every term it has promised, and its current promise to anyone else has run out. A grant
  promises not to grant to anyone else for `lease`, timed from when the voter **received** the
  request.
- **Renewal** is the same request at the same term, every `lease/4`.
- **The leader's own clock.** The leader times its lease from when it **sent** the request,
  shortened by a clock-rate margin ρ (1%). Its lease therefore ends before any voter's promise
  does.
- **Clocks.** Every timing uses local monotonic durations. Wall-clock times are never compared
  across hosts.
- **The term is the fencing token.** It goes on every stream frame and every proxied API call,
  and receivers refuse an older term. Field devices can't check a token: a Modbus relay board
  knows no terms. **At the field, safety rests on timing**: the old leader's gate has closed
  before a new lease can be granted, and the devices' own comm watchdogs act on silence.

### 3.2 The write gate

The gate is open only while all of the following hold:

1. a majority lease is unexpired, judged when the gate is read rather than on the elector's
   next tick (as `IsLeader` does today);
2. scans are live (#621);
3. the leader hasn't decided to hand over for I/O health (below).

When the gate closes, a driver stops sending on its own: keep-alives and everything else. The
device's comm-watchdog setting then decides what happens.

**I/O-health handover (new).** Losing I/O never closes the gate by itself: on a single
controller, closing the gate would help nothing.

- **When it happens.** The leader hands over voluntarily, using `Release`, only when the
  standby is **strictly better**. That means the set of driver connections live on the standby
  contains every connection live on the leader, plus at least one the leader has lost.
- **The hold-off.** The condition must hold for a hold-off (default 3 × lease), so a flapping
  link doesn't ping-pong.
- **What counts.** Only connections the standby polls are compared. A no-session standby
  (§4.4: EtherNet/IP, IO-Link, OptoMMP with a unit watchdog) can't see the device, so a
  device the leader loses there triggers no handover. Its loss shows as a driver fault, and
  the device's watchdog acts, as #626 does today.
- **Large projects.** With many devices, the rule needs no per-device configuration: a
  handover happens only when the standby sees everything the leader sees, and more.

### 3.3 Vote freshness

Each renewal tells the voters how far the standby is in sync: the `(term, scan)` of the last
image it acknowledged, and whether that is current. The voters record it in their fsynced
state (§3.1). **An unknown mark counts as out of sync.** The mark is unknown when a witness
comes back without its state file, or when both controllers restart.

A candidate that was behind the recorded mark when the old leader last renewed still wins the
election. It takes over **cold** and raises an alarm naming how stale its state was (§9, answer
4).

## 4. The state stream and hot takeover

### 4.1 Contents and transport

After every scan, or every N scans, the leader streams:

- retained tags and `VAR RETAIN`;
- every program frame: FB internals, timers, SFC steps and their elapsed times;
- alarm ack and shelve state.

Frames are numbered `(term, scan)`, delta-encoded against the last acknowledged frame, and
checksummed, and each one carries the **build hash**. A full frame is sent on connect and after
any gap. The standby applies each frame to a shadow copy and persists retained values to its
own file, on the 2 s cadence the leader uses today.

### 4.2 Hot takeover

A hot takeover happens when the image is current (within a bound set as a multiple of the
stream interval) and the build hash matches. The new leader then:

- **continues from the image** instead of calling `ResetFrame`;
- advances timers by the time measured locally since the image arrived;
- still drops forces, as today: the standby never saw them;
- still sends the full output set once (#600/#606);
- waits for live inputs as #786 defines, then scans.

**What can repeat.** The image can trail the leader, so the new leader re-runs at most
`N scans + link delay` of logic, against fresh inputs. An edge-triggered action inside that
window can happen once more. The guide says so.

### 4.3 Cold takeover

Today's sequence applies (reload retain, reset frames, zero scan clocks, re-derive from the
field) when:

- there is no image;
- the image is older than the bound;
- the standby was behind the in-sync mark (§3.3);
- the **build hash differs**. A rolling update through `tools/deploy` is therefore a cold
  handover, and the guide says so.

This reverses the guide's "process state is deliberately not replicated", **for the pair
only**. The guide gains a section for the pair, and the Kubernetes text stays.

### 4.4 Standby field I/O, per driver

| Driver | Standby default | Why |
|---|---|---|
| Modbus TCP, OPC UA client | polls read-only | A second session costs nothing. Inputs are live at takeover, and the I/O-health handover (§3.2) can compare both sides. |
| OptoMMP | polls, or no session with a unit watchdog | Already the rule (#618, #626). |
| EtherNet/IP, IO-Link masters | no session | Connection budgets are tight, and some masters take only one client. |

A manifest can override any default. The time spent waiting for inputs counts in the takeover
time.

### 4.5 Takeover time

Takeover time is published as a formula and measured, never promised as a single number:

> `T ≈ time left on the old lease (≤ lease) + one vote round + inputs-ready wait
> (≈0 when the standby polls; session connect + first read when it does not) + one scan`

The test plan defines exactly how it is measured (§4 there).

## 5. Failure cases

| Down | Result |
|---|---|
| Witness only | A+B are still a majority, so the plant runs. Alarm: "witness down, one more failure stops control". |
| A (leader) | B+W elect B once A's lease has expired. The takeover is hot if B is in sync, cold with an alarm if not. |
| The A–B link only | W keeps renewing A, so nothing changes. The stream stops, and B is marked out of sync. |
| A cut from both B and W | A fences when its lease ends. B+W elect B after that. |
| Witness plus one controller | The lone survivor (§7). |
| Everything starts at once | A+B, or either controller with W, forms a majority and elects. One controller alone at power-up **does not start writing** without the override. |

## 6. Links

- **Votes** go between every pair of voters.
- **The stream** goes A→B by default over the plant network, with votes on a separate
  connection so that a busy stream can't delay a renewal.
- **An optional dedicated A–B cable** (`stream-link:`) carries the stream. It also carries A↔B
  votes as a second path beside the plant network.
- **The witness's state** is a small file, fsynced per grant.
- **One witness can serve several pairs.** It keeps state per pair, keyed by the project name.

## 7. Lone survivor and the engineer override

A controller alone can't tell "both others are dead" from "I'm cut off and the other side has
elected a leader". **Anything it writes may contend with a real leader.**

- **If it was leader**, its gate closes when its lease ends. It stops scanning and writing, and
  keep-alives stop. Each device holds its last value or goes to its fail-safe, by **its own**
  comm-watchdog setting. That makes those settings part of commissioning: checked in #719, and
  set as defaults in the device profiles of #717.
- **If it was standby**, it stays standby.
- **Either way**, it raises **no quorum**: in `/api/cluster`, as an HMI banner, as the systemd
  `STATUS=` line, and in the log. The HMI goes read-only.

**The engineer override**, "run without quorum", works like this:

- it is authenticated: a token now, a #708 role later;
- the engineer asserts that the other controller is **powered off or isolated**, and the
  record says so in those words;
- it lasts a limited time (default 8 h) and can be renewed;
- it **ends the moment any peer or the witness is heard**. The controller then goes through a
  normal election, and a controller whose state is current wins it;
- it does not survive a restart;
- it opens the gate with a hot or cold takeover, by §4's rule.

**The override is the only path to two writers.** It is taken when an engineer asserts
something that is not true. The record exists for that case.

### 7.1 Audit record, shaped to move into #709

The record is fsynced to a local append-only JSONL journal **before the gate opens**. It is
copied to the peers once quorum returns.

```json
{"id":"<ulid>","ts":"<rfc3339>","kind":"redundancy.override.start",
 "actor":{"name":"<user>","via":"cli|api","auth":"token"},
 "pair":"<project>","node":"a","reason":"<free text>",
 "attest":"peer b confirmed powered off",
 "context":{"term":12,"lastHeard":{"b":"41m","witness":"39m"},"inSync":false},
 "expires":"<rfc3339>"}
```

The `kind` values are `redundancy.override.start`, `.renew`, `.end` (a peer was heard),
`.expire` and `.revoke`.

- A crash after the fsync and before the gate opens leaves a record of an override that never
  ran. That is the safe direction.
- A torn final line is dropped, as `oplog` does.
- When #709 lands, the journal becomes one of its sources, and `actor` becomes a #708 identity.

## 8. Clients (#716, planned on that issue)

- **HMI.** `/api/cluster` lists every peer, and the HMI kit follows the leader. The standby
  serves a read-only view.
- **OPC UA server.** Non-transparent redundancy: ServerUriArray, RedundancySupport and
  ServiceLevel. The leader reports 200 or more. The standby reports less when it is in sync,
  and 1 when it is not.
- **Optional floating IP.** A new leader adds the address only after its gate opens, and sends
  gratuitous ARP.
  - **Orderly handover.** A leader that steps down in an orderly way (SIGTERM, I/O handover,
    a lost lease noticed on its own tick) drops the address before it releases.
  - **Kill, hang or pause.** The kernel still holds the address after a SIGKILL, a hang or a
    SIGSTOP. Both boxes can then answer ARP for a while: until systemd's watchdog (#733)
    restarts the process, `ExecStopPost` removes the address, or the paused process resumes
    and drops it.
  - **Why that is tolerable.** This is a client-routing overlap, not a second writer. Field
    writes are fenced by the gate. The fenced box's API refuses writes, and the new leader's
    gratuitous ARP moves the clients over.
  - **keepalived** is documented as the alternative.

## 9. Decisions

Settled with the maintainer, 2026-11-17:

1. **Witness: a third box, at whatever layer the site has.**
   - When the controllers serve the HMI themselves, it runs on a small third box.
   - When a dedicated SCADA/historian box exists, it runs there.
   - That box may sit on an HA cluster. There the witness runs as a pod, and a reschedule that
     loses its state file costs one lease of waiting (§3.1). A persistent volume avoids the
     wait.
   - In every case, the witness must not share a single point of failure with one controller,
     such as a switch or a power feed.
2. **Lone survivor: as §7.**
3. **Stream link: the plant network by default**, with an optional dedicated A–B cable that
   also carries votes.
4. **Stale winner: a cold takeover plus an alarm.**
5. **Demo** (the #725 checkpoint):
   - pull power on the active controller partway through a step: the standby carries on at the
     same step and resends every output, the HMI follows it, and the measured takeover time is
     shown;
   - then cut the witness and one controller: the survivor fences itself with a no-quorum
     alarm, and an engineer override is recorded and audited;
   - it runs in containers first, then on two ARM64 controllers once they deploy through
     `tools/deploy`.

### Open

6. **`oplog` and the witness (§1).** Should the witness join `oplog` as its third peer, so that
   operator intent survives a failover by majority, as it does under Kubernetes? The
   alternative is that intent rides the stream, and an action not yet streamed is lost.

## 10. Order of work

Each step is its own PR, "Part of #725":

1. This design and the test plan.
2. The `quorum` package (voter, candidate, lease), a simulated network, and the deterministic
   partition matrix. No runtime wiring yet.
3. `naut witness`, the manifest section and the `naut run` wiring, with **cold** takeover only.
   This is today's behaviour, without Kubernetes. The container harness arrives here.
4. The state stream and hot takeover, with stale or mismatched images falling back to cold.
5. Lone-survivor mode, the override and its audit journal.
6. #716: the clients and the floating IP.
7. The demo in containers. Then the bench run on two ARM64 controllers and a witness, and the
   published takeover time.
