# Design: a redundant controller pair and a witness, without Kubernetes

Status: **designed, not built.** The maintainer answered the first round of questions on
2026-11-17, and no question is open (§9).
The test plan is [redundancy-test-plan.md](redundancy-test-plan.md).
Issues: #710 (the pair), #716 (clients that follow the active controller). Part of #725.
Context: **Runtime** (`leader`, `retain`, `runtime`), plus a new `quorum` package and
`naut witness`.

---

## 0. The shape, in one paragraph

Two controllers, A and B, run the same build. A third voter, the **witness**, holds no process
data: only its votes and a copy of the operator-intent log (`oplog`), and it can be rebuilt
from scratch. Leadership is a **lease granted by a majority** of the three, numbered by a **term**. The
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
- **`oplog`: the witness is its third peer** (decision 6). Operator intent (acks, shelves) is
  committed by a majority of A, B and the witness, as `oplog` does under Kubernetes, so an ack
  survives a failover. The leader sequences. The witness stores the entries (kilobytes a day)
  and follows the leader that its own vote state names.
  - **Where acks come from.** The state stream still carries alarm state for a hot takeover,
    but the log is where an ack comes from.
  - **A new leader catches up first.** It catches up the log from the other peers (`oplog`'s
    `leaderCatchUp`) before it applies alarm state or accepts operator writes.
  - **No quorum, no operator writes.** A lone survivor has no majority, so its operator writes
    are refused. This matches the read-only HMI in §7.
- **The witness can be rebuilt from scratch.** A witness that comes back with an empty disk
  loses no committed entry while both controllers are up:
  - **Its votes:** it grants nothing for one lease (§3.1).
  - **Its in-sync mark:** it counts as unknown until the leader's next renewal records it
    again (§3.3).
  - **Its `oplog` copy:** it re-copies the whole log from the leader, as any peer that was away
    does.

  While both controllers are up, every committed entry is on at least one of them.

  **Two faults stop the pair; only the override can lose an ack.** First, B is down while A and
  the witness commit an ack. Then the witness loses its disk. The rebuilt witness can't be
  adopted without B (§3.1), so A has no majority and fences: control stops at the second fault.
  If A then dies before anyone has copied the ack, B and the rebuilt witness can't adopt it
  without A either, so B stays fenced with no quorum, and the ack is still on A's disk: when A
  returns, nothing is lost. The ack can be lost only if an engineer runs B on the override (§7)
  instead of waiting for A; whether it is depends on PR 5's rule for whose `oplog` survives the
  heal (§7). The guide states that bound, and the override's record names it.
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
- **Peer calls carry a peer token** (`NAUTILUS_PEER_TOKEN`), compared in constant time as
  `oplog` does. It is a separate secret from the client token `NAUTILUS_TOKEN`, so a client
  that can write tags can't cast a vote. `naut run` refuses to start a pair member or a witness
  without one, and `tools/deploy` installs it on all three boxes. Mutual TLS can follow #708.
  How peer traffic meets the server's Host rule is §6.1.

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
- **Renewal** is the same request at the same term, every `lease/4`, marked as a renewal. A
  renewal that changes nothing the voter has persisted is answered without a new fsync.
- **Re-term on a refused renewal.** A voter can hold a promise that the leader's renewals
  can't satisfy: a standby that lost a contested campaign has promised itself the same term
  (or a higher one), and a voter that was in its restart wait when the leader won may have
  promised its own candidate since. A leader whose renewal is refused by a voter that has
  promised its term or a later one renews at `maxSeen + 1` from then on. A majority at the new
  term extends the lease, and the old lease keeps running until then. Once the standby's
  promise to itself runs out, its voter grants the leader again. Without this, the pair
  runs on {leader, witness} alone and a witness blip ends a healthy lease (found by the
  simulator, test plan F13 and F19).
- **Every request is named, and its grant answers only it.** A request carries a per-boot
  random nonce and an id; a grant echoes both and names the controller it was granted to. A
  grant counts only toward the request it answers: addressed to this controller, from this
  boot, with the same id and term. A lease is timed from the send instant of **one** request
  that a majority granted, so a late grant for an earlier renewal never stretches a later
  one, and a replayed or misdelivered grant (the other controller's, or an earlier boot's)
  counts for nothing.
- **Only the three voters count.** A controller counts replies from itself, its peer, and the
  witness at the accepted incarnation, and ignores any other sender: a second witness copy
  under another name included. A configuration with ρ outside (0, 10%], a zero nonce, or
  names that aren't three distinct voters is refused at start.
- **Campaigning.** A standby looks every `lease/4`. It campaigns on its first look after the
  lease it last saw renewed has expired by its own clock, plus ρ. "Saw renewed" counts every
  renewal its own voter received from the other controller, **granted or refused**, timed
  one lease from receipt. Without that, a standby whose voter refused the leader (in its
  restart wait, or promised to itself) campaigns under a live leader at every look, and its
  voter keeps promising itself. That only delays a campaign: a leader that can send but not
  hear stops sending when it backs off (below), so the standby's wait ends one lease after its
  last renewal. One campaign is in flight at a time. A refused campaign is retried half a
  look plus a seeded per-node jitter in `[0, lease/4)` later, and the retry moves the look
  itself: on the `lease/4` grid a jitter below `lease/4` would change nothing, and two
  controllers started together would retry in lockstep. A campaign asks for one more than the
  highest term this controller has heard of, **including the terms its own voter was asked
  for by the other controller**, so two campaigners don't keep landing on equal terms.
- **Yield to a higher term.** A failed campaign leaves the candidate's own voter promised to
  it for a lease, and that refuses the other controller. With only the two controllers
  counted (the witness down, or at an incarnation not yet adopted, or a controller that lost
  the incarnation it accepts), each controller's campaigns would keep its own voter promised
  to itself, and the two would refuse each other forever (found by the simulator). So a voter
  whose running promise is to its **own** candidate yields it to the other controller's
  request for a **strictly higher** term, but only if that candidate isn't leading, and only
  after the candidate has given up every open round. Both happen in one call (`YieldTo`), so
  nothing can open a round in between. The yielded promise can then never back
  a lease, and the voter grants the request as usual. Equal terms never yield, so one term
  still has one grantee. A controller that can send but not hear can make a campaigning
  standby yield once per back-off. That delays the standby by about a lease at most; it never
  blocks it, because a leading candidate never yields.
- **The leader's own clock.** The leader times its lease from when it **sent** the request,
  shortened by a clock-rate margin ρ (1%). Its lease therefore ends before any voter's promise
  does.
- **Clocks.** Every timing uses local durations. Wall-clock times are never compared across
  hosts. Leases are timed on `CLOCK_BOOTTIME`, which keeps counting through a host suspend,
  unlike `CLOCK_MONOTONIC`.
- **The clock assumption.** ρ bounds the **relative** rate of any two voters' clocks, as
  F22 sweeps it, not each clock against real time: two clocks each off by ρ the other way
  are 2ρ apart, and a lease shortened by ρ would then outlive a promise. The argument holds
  only while that bound holds, **including while a process isn't running**.
  - **Where it breaks:** the leader's clock stops or slows without bound when a VM is paused,
    snapshotted or live-migrated, when a hypervisor deschedules the guest for long, or when the
    host suspends and the hypervisor stops the guest's boot clock.
    - The leader then resumes believing its lease is still valid, and it writes.
    - A stopped clock on the witness or on the standby is safe: they grant or campaign later,
      not earlier.
  - **What follows:** controllers in VMs that can be paused or migrated are **not supported for
    the pair**, and the guide says so. Bare metal and containers are supported. The witness may
    run in any VM.
  - **The tests:** they include a stopped leader clock (test plan F22b). It is an
    expected-failure case that measures the breach, not one that hides it.
- **One-way partitions and the back-off.** A leader that can send but not receive keeps the
  voters' promises alive with renewals it never hears answered, so nobody else can be elected.
  A leader whose lease has lapsed without a confirmed majority therefore **sends nothing to the
  voters for two leases** after its lease ended before it campaigns again. A campaign that no
  majority even answered within a lease backs off for two leases from its send. The voters'
  promises run out and a reachable controller wins first. Two, not one: the standby campaigns
  up to `lease + ρ·lease + lease/4` after the last renewal it saw. A leader back after one
  lease would campaign at about the same moment, win the standby's voter with a campaign it
  can't hear answered, and hold the takeover off for another lease. At a 2 s lease, over 160
  F9 schedules per link delay with clocks skewed within ρ, the worst takeover at 1–100 ms
  delays is 6.4–6.9 s with one lease and 3.9–4.3 s with two. With perfect clocks, the yield and
  the moved retry of #979 hide the difference (3.5–3.7 s either way); a standby only 0.25%
  fast brings it back. `TestTargetedOneWayLeaderSkewed` pins that schedule.
  **The trade, accepted:** a lapsed leader that is the only viable candidate also waits two
  leases. With the standby already down and the witness link out for a full lease, the pair
  is leaderless for 4.0 s instead of 2.0 s (a half-lease blip costs nothing either way). That
  is a double fault, and the one-way leader is a single fault whose recovery the plant sees,
  so the faster F9 takeover wins. The back-off silences only the candidate: the same node's
  voter keeps answering.
- **The witness incarnation.** The witness draws a random incarnation id when it first starts
  and keeps it in its state file. A and B record, in their own fsynced state, the incarnation
  they accept.
  - **Why it matters.** It covers a witness that runs twice: an old pod on a partitioned node
    and its rescheduled copy, a force-deleted pod, or a copied VM. With the A–B link also cut,
    A and B could each win a majority with a different copy.
  - **The rule.** A controller ignores grants from any other incarnation. It adopts a new one
    only when the other controller concurs over the A–B link, which happens at commissioning or
    after a witness rebuild. Both controllers therefore always agree on which witness counts,
    and that witness's promise goes to one of them.
  - **Agreement isn't enough at a switch.** Suppose A leads on grants from itself and the old
    witness, and B's promise to A has lapsed. If the pair switched to a second copy that has
    been up for hours (its restart wait long over), B and that copy would make a majority while
    A's last lease still runs: the voter set changed, so the two majorities need not
    intersect. Three rules close it:
    - **A quiet period.** A controller counts no grant from a newly adopted incarnation for
      one full lease after it has persisted the adoption. By then every lease granted under
      the old voter set has ended.
    - **Forward only.** A controller stops counting the old incarnation before it asks the
      other to switch, and never goes back. Concurrent proposals break ties deterministically
      (the higher incarnation id wins).
    - **A term floor.** A rebuilt witness has promised term 0. The adoption handshake carries
      each controller's highest promised term, and both raise their voters to it before
      counting the new witness, so no term is granted twice (I4).
  - **When it ships.** The handshake is its own PR, after the simulator (PR 2's split), with a
    simulated case that fails without the quiet period. Until then a controller counts grants
    only from the incarnation it was commissioned with.
  - **The cost.** A rebuilt witness can't be adopted while one controller is down. That is
    already a two-fault case (witness disk and a controller), so the survivor behaves as in §7.
  - **On an HA cluster** the witness must be a StatefulSet with a persistent volume, never a
    Deployment: Kubernetes won't start a second copy of a StatefulSet pod while it can't
    confirm that the first is gone. The incarnation check covers a force-delete.
- **The term is the fencing token.** It goes on every stream frame and every proxied API call,
  and receivers refuse an older term. The token is the **epoch**: the term leadership was
  acquired in. A re-term on renewal (above) moves the vote term but not the epoch, so a write
  posted under the token is still valid while the lease runs on without a gap (#797 checks it
  right before the send). Any gap followed by a new acquisition changes it, because terms only
  rise and no term is led twice. The voters and the witness work with the vote term.
  **The rule for every other part (the gate, the stream, the proxy, oplog): stamp and compare
  the epoch (`Term()`) only, and never compare it with a vote term (`VoteTerm()`).** `Term()` is
  0 on a controller that isn't leading, so a receiver refuses a token older than the highest
  token it has received **from a leader**, never one compared against its own `Term()`. They are two
  number spaces. A test keeps `VoteTerm` from being called outside `quorum/`. Field devices can't check a token: a Modbus relay board
  knows no terms. **At the field, safety rests on timing**: the old leader's gate has closed
  before a new lease can be granted, and the devices' own comm watchdogs act on silence.

### 3.2 The write gate

The gate is open only while all of the following hold:

1. a majority lease is unexpired, judged when the gate is read rather than on the elector's
   next tick (as `IsLeader` does today);
2. scans are live (#621). That includes a loop **starved between scans** (#858): one whose
   next slot is overdue by more than `StallBound(period, lease/2)`, with its ticks counted
   whether or not the gate let logic run. Without #858, `Liveness` flagged only a scan that
   had stayed in progress too long, so a leader whose loop never ran again kept renewing;
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

### 3.4 Writes leave in time, on every output path

A gate that is open "when read" does not stop a write that leaves after the lease has expired.
Between the check and the send there can be a pause: a GC, a preemption, a SIGSTOP. Bytes may
also already be sitting in the kernel's send buffer. Every output path must therefore follow
three rules:

1. **Check before each send.** The gate is read immediately before every send: one request,
   one datagram or one frame. It is never read once per batch.
2. **A fence margin and a send deadline.** The gate closes a margin **before** the lease
   expires, not at expiry: `IsLeader` is false once `remaining() < m_fence`.
   - `m_fence = d_net + m_send`. `d_net` is an assumed one-way delivery bound for the plant
     network, 50 ms by default. `m_send` bounds the gap between a send path's gate check and
     its send system call under normal scheduling, 20 ms by default. Both are set in the
     manifest.
   - Each send also runs with a deadline of `remaining() − d_net`, and a send whose deadline
     has passed is not started.
   - **Why the margin is needed.** Any sender that checks and then sends can let one frame
     through after the check (the PROFINET review of #790 confirmed it: at most one frame per
     sending goroutine). With the margin, that frame still leaves while the lease is valid,
     and it arrives before any voter's promise expires.
   - The margin comes out of the time the leader may write. It is not added to takeover time.
3. **Fencing closes sockets.** When the gate closes, every field socket with a write in flight
   or queued is closed with `SO_LINGER` 0. The close sends a reset and discards whatever was
   still in the kernel's send buffer.

**The residual risk, stated.** The margin covers ordinary scheduling delay between check and
send. It does not cover a **freeze longer than `m_fence`** (a SIGSTOP, a long GC pause, a
stalled host) that lands between the last check and the send system call. Then at most one
frame per sending goroutine can reach a device late, by up to the freeze minus the margin.
Bytes delayed in a NIC queue or a switch beyond `d_net` are the same case. No user-space rule removes that window. The device's own comm
watchdog and the hardwired safety chain cover it (#725, "deliberately not built"). The test
plan measures it with a pause hook in exactly that spot.

**Every output path, and how it is gated:**

| Path | Gate today | Needed for the pair |
|---|---|---|
| Modbus TCP | before each write (`modbus/driver.go`) | send deadline; RST on fence |
| EtherNet/IP | before each write (`eip/driver.go`) | send deadline; RST on fence |
| OPC UA client | in `WriteOutputs` and its flush | check before each write; deadline |
| OptoMMP | looked at per scan, datagram and stream check | check before each datagram; deadline |
| Redfish, SNMP, Prometheus (`hw/base.go`) | **once per batch** (`flushWrites`) | check before each command; deadline |
| READ/WRITE channels (#699) | **none**: fixed in #797 (#796) | deadline |
| Sparkplug host NCMD/DCMD | gated (#325) | check before each publish |
| Sparkplug edge | **none** (#798): every replica publishes | only the leader holds the session; NDEATH on step-down |
| PROFINET device (#790) | gate look every 200 ms | check before each RT, DCP and LLDP frame (test plan F33) |
| OPC UA server Write service | refused on a standby | unchanged |
| HTTP API writes | proxied to the leader, or refused (503) | unchanged |
| Alarm notifiers (mail, chat) | run from scans, so leader only | unchanged |
| PLCnext (`plcnext`, `project_full.go`) | **none**: flushes its output image from its own loop | **refused under `redundancy:` for now**, by load and `naut check` (#832). Its `poll()` flushes every scan on the shm route, because that write is the shim's watchdog heartbeat, and on change over gRPC. Gating it is later work. |
| Floating IP | (new) | §8 |

The test plan's I3 oracle checks every row.

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

**Image age** is judged by the standby, on its own clock: the time from the moment the last
frame arrived to the moment it wins the election. It is not the in-sync mark, which says only
that the standby was keeping up when the old leader last renewed.

**The isolated leader.** A leader cut off from both peers (F12) goes on scanning and writing
until its lease ends, but none of its frames arrive. A standby that was in sync therefore wins
with an image about `lease + campaign delay` old.

**The bound.** A hot takeover happens when the image age is at most `hot-max-age` and the build
hash matches.

- **The worst image age at the election** is the sum of these terms (§4.5):
  - the old lease, `lease`;
  - the clock-rate margin, `ρ·lease`;
  - the campaign delay, `lease/4`;
  - one vote round with fsync, `RTT + f`;
  - a just-restarted voter's wait, `W_r ≤ lease`;
  - stream jitter.
- `hot-max-age` defaults to `3 × lease + 2 × stream interval`. While `RTT + f` stays under half
  a lease, that covers every one of those terms, the restarted voter included. A crash, a kill
  or an isolation of the leader therefore stays hot.
- **When the vote round runs slower,** an isolation can land past the default and go cold. It
  goes cold with the alarm, never hot on a stale image. The test plan's F21 and F24 measure
  that edge.
- **Setting it lower.** A project that would rather restart than re-run up to that much logic
  sets `hot-max-age` lower. The isolation case then becomes cold.

**The new leader, in order:**

1. keeps the image's frames instead of calling `ResetFrame`;
2. drops forces, as today: the standby never saw them;
3. waits for live inputs, as #786 defines;
4. runs its first scan. Its timers advance by the **time since the image arrived, up to this
   scan**: the image age plus the inputs wait, measured on the standby's clock;
5. sends the full output set once (#600/#606). Outputs are held until then (`io/io.go`).

**If the #786 wait gives up**, the takeover stays hot: frames are kept, inputs that are not
live carry Bad quality, and "took over without live inputs" is alarmed. Giving up never turns
a hot takeover into a cold one.

**The re-run window.** It is the image age: up to `hot-max-age`, not a few scans. Within it:

- **Actions can repeat.** The new leader may perform an action the old leader already
  performed, such as an SFC step's entry actions or an edge-triggered command.
- **Events can be lost.** An input pulse that came and went inside the window is never seen,
  so a counter can end short by the pulses in the window.
- **Operator writes can be lost silently.** A setpoint the old leader acknowledged through the
  API but had not yet streamed is gone on failover. On a crash that is up to one stream
  interval; on an isolation, up to a lease. The guide says so. Acks and shelves are not
  affected: they go through `oplog` (§1). Moving setpoint writes through `oplog` too is left to
  #709, whose entries already go that way.

### 4.3 Cold takeover

Today's sequence applies (reload retain, reset frames, zero scan clocks, re-derive from the
field) when:

- there is no image;
- the image is older than the bound;
- the standby was behind the in-sync mark (§3.3);
- a cold takeover after a long spell out of sync reloads the standby's own retain file, so
  setpoints go back to what it last received. The alarm names how far back that is;
- the **build hash differs**. A rolling update through `tools/deploy` is therefore a cold
  handover, and the guide says so.

This reverses the guide's "process state is deliberately not replicated", **for the pair
only**. The guide gains a section for the pair, and the Kubernetes text stays.

### 4.4 Standby field I/O, per driver

| Driver | Standby default | Why |
|---|---|---|
| Modbus TCP, OPC UA client | polls read-only | A second session is usually cheap. Inputs are live at takeover, and the I/O-health handover (§3.2) can compare both sides. A device that caps its connections (many Modbus gateways take only a few) is set to no session in its profile (#717). |
| OptoMMP | polls, or no session with a unit watchdog | Already the rule (#618, #626). |
| EtherNet/IP, IO-Link masters | no session | Connection budgets are tight, and some masters take only one client. |
| PROFINET device (`profinet-device`, #790) | silent: no socket, no DCP answers, no LLDP | Only the leader is the station on the wire. The PLC's outputs are stale on a standby until its AR is re-established with the new leader. |
| Redfish, SNMP, Prometheus (`hw/base.go`) | polls read-only; sends no command | They are IT devices that take many readers. Commands go only from the leader, checked before each one (§3.4). |
| Sparkplug host | asks for nothing: no STATE online, no NCMD or DCMD | Only the leader is the primary host (#325). Its sites stay offline on the standby. |
| Sparkplug edge | holds no session | One publisher per edge node ID (#798). The leader births on takeover, and publishes NDEATH on step-down. |
| READ/WRITE channels | no scans, so nothing is posted | A queue left from an earlier leadership is dropped when its turn comes (#796, #797). |
| PLCnext | — | Refused under `redundancy:` for now (§3.4, #832). |

A manifest can override the polling defaults for field drivers, except the PROFINET device,
which is always silent. The rows after it are fixed rules. The
time spent waiting for inputs counts in the takeover time.

### 4.5 Takeover time

Takeover time is published as a formula and measured, never promised as a single number:

> `T ≈ L_rem + ρ·lease + C + (RTT + f) + W_r + I + S + O`

| Term | Meaning |
|---|---|
| `L_rem` | time left on the old lease at the fault, from 0 to `lease`; 0 after an orderly `Release` |
| `ρ·lease` | the clock-rate margin |
| `C` | the standby's campaign delay: up to `lease/4` (§3.1) |
| `RTT + f` | one vote round, including each voter's fsync |
| `W_r` | up to one lease more, if a voter has just restarted and is waiting out its timer (§3.1) |
| `I` | the inputs wait, up to #786's cap: about 0 when the standby polls, a session connect and first read when it does not. When a frozen old leader still holds a device's **only** session, it lasts until that device's own session timeout, and may hit the cap. |
| `S` | one scan |
| `O` | the full output set going out |

**An I/O-health handover** (§3.2) waits for its hold-off (3 × lease) first, then hands over by
`Release`, so `L_rem` is 0.

**When nautilus is a PROFINET device**, the IO controller (the PLC) re-establishes its AR with
the new leader, and that sets the inputs-ready wait:

> `T_pn ≈ time left on the old lease + the PLC's next connect attempt after the new leader
> opens its sockets + DCP identify and set IP + connect, parameterise and the first valid
> cycle`

The PLC sees the old station go silent at once, through its AR watchdog. Its reconnect
attempts then run on its own retry interval, so the phase of that interval matters, and it is
measured rather than assumed.

The test plan defines exactly how it is measured (§4 there).

## 5. Failure cases

| Down | Result |
|---|---|
| Witness only | A+B are still a majority, so the plant runs. Alarm: "witness down, one more failure stops control". |
| A (leader) | B+W elect B once A's lease has expired. The takeover is hot if B is in sync, cold with an alarm if not. |
| The A–B link only | W keeps renewing A, so nothing changes. The stream stops, and B is marked out of sync. |
| A cut from both B and W | A fences when its lease ends. B+W elect B after that. |
| The leader starved between scans (#858) | Its elector still runs, but `Liveness` flags the overdue loop within `B ≤ lease/2`. The elector stops renewing at its next tick, and the gate closes half a lease after the last renew: within `B + lease/4 + lease/2` of the first missed slot. A slow task isn't late before its slot comes due, so a 10 s task is caught about 12 s after its last scan. B is elected once the lease lapses. The last renew is at most a tick before detection, so B can't win until `lease/4` after the gate has closed: the two never overlap. A starved standby doesn't campaign. |
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

### 6.1 Peer traffic and the Host rule (#826, #846)

The tag API now answers a request only when it is addressed to a trusted Host (`localhost`, an
IP, or a name in `NAUTILUS_ALLOWED_HOSTS`), or when it carries the token. The pair must work
when its members address each other by DNS name, so peer traffic is kept apart from that rule:

- **A peer listener of its own.** Votes, the state stream, `oplog` replication and audit-journal
  copies run on the peer port that `peers:` and `witness:` name (§2). They are **not** mounted
  behind `server.Handler`, and that includes `oplog`, which isn't mounted anywhere yet. The
  witness runs only this listener.
- **The token, every time.** Every peer request carries `NAUTILUS_PEER_TOKEN`, and the peer
  listener answers nothing without it, whatever the Host. Peers can therefore be addressed by
  name, by IP or through a floating address, with nothing to add to `NAUTILUS_ALLOWED_HOSTS`.
  A DNS-rebound browser page can't supply the token, so the listener needs no Host check of its
  own. It is never a browser surface: no CORS, and no static files.
- **The standby's proxy to the leader** (`/api/*` forwarded, as today) passes through the
  client's own Host and token header. The leader applies the same rule the standby would have.
  To a client the pair is **one controller**, so A and B share one `NAUTILUS_TOKEN` and one
  `NAUTILUS_ALLOWED_HOSTS`. `tools/deploy` installs both on both boxes. At the peer handshake
  each side compares an HMAC of its client token, keyed with the peer token, and raises
  "client token differs between A and B" when they don't match. The handshake never sends the
  client token itself.
- **Clients that follow the leader (#716).** `/api/cluster` advertises each controller's API
  address, and a browser HMI that follows it can't send a token.
  - **What has to be trusted.** Each advertised name must be in `NAUTILUS_ALLOWED_HOSTS` on
    both controllers, or advertised as an IP or the floating address.
  - **The warning.** At start, `naut run` warns about any advertised peer name that its own
    `NAUTILUS_ALLOWED_HOSTS` doesn't list, and names the variable.
  - **Other clients.** OPC UA clients aren't affected: the rule is HTTP-only. Scripts and
    `naut` subcommands send `NAUTILUS_TOKEN`, as #846 already makes them.

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
- it **ends the moment the other controller or the adopted witness is heard** (a rebuilt
  witness that isn't adopted yet doesn't count, §3.1). The controller then goes through a
  normal election (§3.1). The vote doesn't compare state: whoever wins takes over hot or cold
  by §4's rule, so a winner with a stale image takes over cold, with the alarm (§9, decision 4);
- it does not survive a restart;
- it opens the gate with a hot or cold takeover, by §4's rule.

**The override's term.** No majority granted it one, so the override claims the next term on
its own: its last-seen term + 1, marked as an override term in the journal (below), on stream
frames and on proxied calls. When the override ends, the controller reports that term to the
voters before it campaigns, so they promise only higher terms, and the next election lands above
it.

**The override is the only path to two writers.** It is taken when an engineer asserts
something that is not true. The record exists for that case. If the other side was in fact
alive with a majority, it may have elected the same term: that breaks I4 (test plan §2) as well
as I1, and only in this case.

**Open questions for PR 5**, recorded here so they're decided before the override ships:
- **Whose operator intent survives the heal.** Acks and shelves made under the override and on
  the other side's majority diverge. The candidate rule: the majority side's `oplog` wins, and
  the override side's entries are kept in the journal and listed for the engineer, not
  replayed.
- **Whether an override term needs a separate token space** (e.g. term plus an override bit)
  so receivers can tell it from a majority-granted term of the same number. The candidate
  answer is no: the override is the documented two-writer case, and receivers refuse it as soon
  as a majority term above it is heard.

### 7.1 Audit records, in #709's shape

Every redundancy event is written as a #709 entry. The fields are `ts`, `user`, `source`,
`action`, `target`, `before`, `after` and `result`. Redundancy-specific facts go in `detail`.

```json
{"id":"<ulid>","ts":"<rfc3339>","user":"<user>|system","source":"cli|api|system",
 "action":"redundancy.override.start","target":"<project>/a",
 "before":{"quorum":false,"gate":"closed","term":12},
 "after":{"gate":"open","term":13,"override":true,"takeover":"cold","expires":"<rfc3339>"},
 "result":"ok",
 "detail":{"reason":"<free text>","attest":"peer b confirmed powered off",
           "lastHeard":{"b":"41m","witness":"39m"},"imageAge":null}}
```

**The actions recorded:**

| Kind of event | Actions |
|---|---|
| Override | `redundancy.override.start`, `.renew`, `.end` (a peer was heard), `.expire`, `.revoke` |
| Takeover | `redundancy.takeover`, with hot or cold, the image age and both terms |
| Handover | `redundancy.handover`, with the reason: orderly or I/O health |
| Quorum | `redundancy.quorum.lost` and `.regained` |
| Witness | `redundancy.witness.incarnation`, when one is adopted |
| Stale takeover | `redundancy.stale`, a cold takeover with its staleness |

**Where records go.** With quorum, a record goes through `oplog`, as #709 specifies. Without
quorum, which is the override's whole case, it is fsynced to a local append-only JSONL journal
first: **before the gate opens**, for an override. It is appended to `oplog` once quorum
returns.

**Crash and replay rules:**

- A crash after the fsync and before the gate opens leaves a record of an override that never
  ran. That is the safe direction.
- A torn final line is dropped, as `oplog` does.
- Each record's `id` makes replay idempotent.

**Until #708 and #709 land**, `user` is the token's name and the journal stands alone. When
#709 lands, the journal becomes one of its sources unchanged.

## 8. Clients (#716, planned on that issue)

- **HMI.** `/api/cluster` lists every peer, and the HMI kit follows the leader. The standby
  serves a read-only view.
- **OPC UA server.** Non-transparent redundancy: ServerUriArray, RedundancySupport and
  ServiceLevel. The leader reports 200 or more. The standby reports less when it is in sync,
  and 1 when it is not.
- **Floating IP.** It is optional for clients. **It is required for a PROFINET device**
  (#790): on takeover the PLC re-assigns the station's IP, and the device accepts that only
  when the new leader's interface already holds the address. Because of that, `naut check`
  refuses `profinet-device` under `redundancy:` when no floating IP is set.
  - **Order on takeover:** the gate opens, then the address is added and gratuitous ARP is sent,
    then the PROFINET sockets open. The station never answers DCP without the address in place.
  - **Orderly handover.** A leader that steps down in an orderly way (SIGTERM, I/O handover,
    a lost lease noticed on its own tick) drops the address before it releases.
  - **How long the old box keeps the address**, by fault:
    - **SIGKILL or a crash:** systemd runs `ExecStopPost` as soon as the process exits, which
      takes milliseconds.
    - **A hung scan:** the elector still runs, so it drops the address as it steps down
      (#621).
    - **The whole process frozen** (SIGSTOP, a stalled kernel): the address stays until the
      process resumes and drops it, or systemd's watchdog (#733) kills it.

    Only that last case leaves the kernel answering ARP from two boxes for long.
  - **PROFINET can't shrug that overlap off.** A PLC that sees an address conflict may delay
    or refuse the AR. A frozen old leader therefore lengthens `T_pn`, and the test plan
    measures that case on its own.
  - **Why that is tolerable.** This is a client-routing overlap, not a second writer. Field
    writes are fenced by the gate. The fenced box's API refuses writes, and the new leader's
    gratuitous ARP moves the clients over.
  - **keepalived** is documented as the alternative.

## 9. Decisions

Settled with the maintainer, 2026-11-17:

1. **Witness: a third box, at whatever layer the site has.**
   - When the controllers serve the HMI themselves, it runs on a small third box.
   - When a dedicated SCADA/historian box exists, it runs there.
   - That box may sit on an HA cluster. There the witness runs as a pod. A reschedule that
     loses its state file comes back as a new incarnation: it grants nothing for one lease, and
     counts once both controllers have adopted it over the A–B link and a further lease of
     quiet period has passed (§3.1). Until then the pair
     runs on its two controllers alone, so a controller fault in that window stops control. A
     persistent volume avoids all of this, which is why the StatefulSet needs one.
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

6. **`oplog`: the witness joins as the third peer**, so that an operator ack survives a
   failover. The condition is that the witness can be rebuilt from scratch: a lost witness
   disk loses no committed entry while both controllers are up. With one controller already
   down it stops control until that controller returns, and only the override can then lose an
   ack (§1).

## 10. Order of work

Each step is its own PR, "Part of #725":

1. This design and the test plan.
2. The `quorum` package (voter, candidate, lease), a simulated network, and the deterministic
   partition matrix. No runtime wiring yet.
3. `naut witness`, the manifest section and the `naut run` wiring, with **cold** takeover only.
   This is today's behaviour, without Kubernetes. `oplog` runs on the three peers, with the
   witness as the third. Every output path meets §3.4: a check before each send, a deadline,
   RST on fence. That includes `hw/base.go`'s per-batch check. The container harness arrives
   here.
4. The state stream and hot takeover, with stale or mismatched images falling back to cold.
5. Degraded modes: the I/O-health handover (§3.2), lone-survivor mode, the override and its
   audit journal.
6. #716: the clients and the floating IP.
7. The demo in containers. Then the bench run on two ARM64 controllers and a witness, and the
   published takeover time.
