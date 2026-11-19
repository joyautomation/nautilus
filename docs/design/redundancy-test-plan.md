# Test plan: the redundant controller pair and its witness

Status: **plan, nothing built yet.** It covers the design in [redundancy.md](redundancy.md)
(#710) and the clients that follow the active controller (#716). Part of #725.
Scope: every redundancy behaviour, tested thoroughly once it is implemented. The maintainer
asked for exactly that on 2026-11-17.

---

## 0. Principles

- **Invariants, not examples.** Every test checks the same short list of oracles (§2), all the
  time, not only at the end of a case.
- **The tests must have teeth.** For every safety rule in the design, a deliberately broken
  build has to make the suite fail (§3.3). A broken build that still passes means the suite has
  a hole, and that hole blocks the release.
- **Every random run is replayable.** A seed and the fault schedule go into every report, and
  one command reruns that case.
- **Numbers come with their environment.** Every published time names the lease, the scan
  period, the standby I/O mode, hot or cold, the hardware, the network and N.
- **Each fault is injected at every layer it can be.** The simulation proves the protocol. The
  containers prove the real binary on a real network stack. The bench proves real power, real
  NICs, real clocks and real flash.

## 1. Tiers and where they run

| Tier | What runs | Where | When |
|---|---|---|---|
| **T1 Unit** | `quorum` voter, candidate and lease. Stream frame encoding and apply. The journal. Manifest validation. | `go test`, GitHub-hosted | every PR |
| **T2 Simulation** | The real `quorum` and stream code on a deterministic discrete-event simulator: virtual clock per node (with rate skew), simulated network (cut, one-way, delay, loss, reorder), simulated disk (fsync latency, EIO, ENOSPC, torn writes), simulated I/O. The exhaustive partition matrix plus seeded random schedules. | `go test`, GitHub-hosted | every PR: the matrix plus 2,000 seeds. Nightly: 200,000 seeds and the mutation checks. |
| **T3 Containers** | Real `naut run` ×2 and `naut witness`, a field-device recorder (§2.1), an HMI client and an OPC UA client, each in its own network namespace or incus container on one host. Faults come from nftables, tc netem, SIGKILL, SIGSTOP, cgroup `cpu.max`, dm-delay and size-limited tmpfs. | self-hosted rig runner (privileged); a smoke subset in network namespaces on GitHub-hosted (sudo) | PRs that touch `quorum`, `runtime` takeover or `cmd/naut` redundancy: the smoke subset. Nightly: the full catalogue. Weekly: the percentile run. Weekly: the 24 h soak. |
| **T4 Bench** | Two ARM64 controllers and a witness box, deployed with `tools/deploy`, a real field device and a recorder. Real power pulls, cable pulls, the optional dedicated A–B cable, real clocks. | the bench, **only with the maintainer's OK** | before each milestone is called done; numbers for the guide; the 72 h soak before the redundancy guide ships |

**CI wiring.** The workflow files are owned by separate CI work, so this plan requests the
jobs rather than writing them. They are:

- a T3 smoke job on PRs, with a path filter;
- the nightly and weekly jobs on the rig runner.

**Kubernetes mode** keeps its existing `leader` tests, which must stay green and unchanged. The
pair adds no behaviour to it.

## 2. The oracles (checked continuously in T2, T3 and T4)

| # | Invariant | How T2 checks it | How T3 checks it | How T4 checks it |
|---|---|---|---|---|
| **I1** | **At most one writer, ever.** No two gates are open at the same instant. | global virtual time | Gate transitions are logged with `CLOCK_MONOTONIC`. Containers on one host share the kernel clock, so the logs merge onto one timeline. | the field recorder (I2), the authority here |
| **I2** | **The device sees one writer at a time.** After a write from B, no write from A arrives until A leads again in a later term. | simulated device | recorder | recorder |
| **I2-PN** | **One PROFINET station on the wire.** At no instant do two MACs answer DCP for the station name or send RT frames for it. The PLC logs no duplicate name or duplicate address. | simulated segment | the segment tap | the tap, plus the PLC's diagnostic buffer |
| **I3** | **No write leaves any output path through a closed gate, or after its send deadline.** That covers every row of design §3.4: drivers, READ/WRITE channels, Sparkplug host and edge, PROFINET, the floating IP. Keep-alives count. | a hook on each path | recorder + gate log for field paths; an MQTT observer for Sparkplug; the segment tap for PROFINET | same |
| **I4** | Terms only rise at every voter. No two controllers ever lead in the same term. | voter state | voter files, `/api/cluster` | same |
| **I5** | **A sequence never continues from a stale image.** A hot takeover happens only on a current image with a matching build hash. Everything else is cold, with the alarm. | stream state | takeover log + alarm | same |
| **I6** | **Liveness.** With a healthy majority, connected I/O and live scans, a leader exists within the formula bound (design §4.5). | after each fault heals, a leader within the bound | `/api/cluster` polled on all three, plus the recorder's first write | recorder's first write |
| **I7** | No flapping. One fault causes at most one takeover. A healed fault causes none, unless the I/O-health rule calls for one. | term changes per fault | term changes in the gate logs | same |
| **I8** | **Audit before effect.** No override opens a gate without a journal record that was fsynced first. | ordering of the simulated disk ops: fsync completes before the gate opens | the journal record carries its fsync-complete monotonic time; compared with the gate log; crash injection between the two | journal against the recorder's first write |
| **I9** | A standby holds no field session where its driver's rule says none (design §4.4), and writes nothing anywhere. | simulated device sessions | recorder's session log, by peer address | same |
| **I10** | Across a hot takeover, retained values go back by at most the **image age**: one stream interval on a crash, up to `hot-max-age` on an isolation (design §4.2). A cold takeover reports how far back it went. | stream state | the harness writes setpoints through the API with timestamps, then reads them on the new leader | same |

### 2.1 The field recorder

The recorder is the arbiter for I2, I3 and I9 in T3 and T4.

- **What it is.** A Modbus TCP slave and an OPC UA server, built on the core `modbus/slave`
  and `opcua/server` packages. Every write and every session open and close is logged with the
  peer's address, the register or node and the value, timestamped on the recorder's clock.
- **What it is not.** It is test-harness code. It does not change `virtdev`.
- **Plant behaviour.** Where a test needs plant behaviour behind the recorder (a tank that
  fills, a counter that counts), it uses `virtdev`'s plant model through its public API. A
  write-log hook inside `virtdev` itself would be requested from its owners, not added here.
- **PROFINET.** For a PROFINET device under redundancy (#790), the "device" being recorded is
  the IO controller.
  - **The controller.** T3 runs the PROFINET package's controller side (#723), or the simulated
    segment of #790's tests until that exists, on a Linux bridge between the containers. T4 uses
    the bench PLC.
  - **The tap.** A tap on the segment logs every DCP answer, every LLDP frame and every RT frame
    for the station, with its source MAC.
  - **ARP.** The tap logs ARP replies for the station's address.
- **Device watchdogs.** The recorder also models a comm watchdog: no write for X ms makes its
  outputs go to hold-last or fail-safe, per configuration. That lets the lone-survivor
  behaviour be asserted at the device.

## 3. Split-brain proofs (I1–I4)

### 3.1 The exhaustive partition matrix (T2, every PR)

**Links.** There are five links: {A–B, A–W, B–W, A–I/O, B–I/O}. The stream link counts as
its own link when it is configured.

**What is enumerated.** For every ordered pair of links (X, Y), and for each heal order:

- cut X, then Y, then heal in each of the two orders;
- each cut as both-ways and as each one-way direction;
- each event placed at eight phases of the lease renewal cycle (k·lease/8), so that a cut
  lands before, during and after a renewal.

**What each case checks:** I1–I7, plus the expected end state (which controller leads, hot or
cold, which alarms).

**What each case reports:** a timeline of detect, fence, elect, inputs ready and first output.

### 3.2 Seeded random schedules (T2)

These are random sequences of every fault in §5, and they may overlap: up to three at once,
with random durations from 0.1·lease to 10·lease. That is 2,000 seeds on every PR and 200,000
nightly. A failing seed gets minimised to the shortest schedule that still fails, then added
to the matrix as a named regression case.

**As built (PR 2b, `quorum/sim`).**
- **The simulator.** Virtual time is in integer nanoseconds, and clock rates are integer ppm, so a seed replays identically on amd64 and arm64. Every message goes through the real codec.
- **The oracles** are judged on exact intervals after each run:
  - **I1:** gate intervals, mapped through each node's clock history.
  - **The lease oracle:** while a gate is open, two counted voters hold running promises to that controller. It is judged from a ledger of every grant a real `Voter` emitted.
  - **The ledger rule:** no voter grants inside a running promise to another.
  - **I2:** a simulated device.
  - **I4:** per voter, and per term led.
  - **I6:** the bound is 3.5 leases after the last heal. On top of the §4.5 takeover, a leader that lapses just after the heal backs off two leases, and a late duplicate of a renewal can hold a standby off one lease past its arrival.
  - **I7:** changes of gate holder.
  - **Steady state:** once settled, one unbroken holder, every counted voter backing it, and no vote term moving. Every safety oracle passed while a stuck standby churned the terms, which is why this oracle exists.
- **The matrix** runs 1,728 cases, with expectations from a connectivity rule that is independent of the protocol: with two of three vote links cut, the fully-up link decides the leader.
- **The seeds.** `go test ./quorum/sim` runs seeds 1–2000. `QUORUM_SEEDS=N` runs 1–N, and `QUORUM_SEEDS=a-b` runs a range. `QUORUM_SEED=k ... -run '^TestSeeds$' -v` runs one seed with its trace. Every failure prints that line and a shrunk schedule as a Go literal.
- **The runtime:** about 2 s, or 21 s under `-race`.
- **Excluded by name:**
  - F22b, a frozen controller clock, which must breach I1;
  - skew beyond ρ, which must breach I1 at some phase;
  - #976, two lost controller files, a named expected failure until the handshake PR. The generator draws at most one lost controller file per schedule.

### 3.3 Mutation checks (T2, nightly; required to pass before each milestone)

Each mutation below is a build tag that breaks one rule of the design. Each one **must** make
this plan's §3.1 or §3.2 (or the §6/§8 cases for its milestone) fail, and the test names which
case caught it. A mutation joins the nightly run in the PR that builds the rule it breaks.

| Mutation | Rule it breaks | From PR |
|---|---|---|
| no clock-rate margin ρ on the leader's lease | design §3.1, the leader's own clock | 2 |
| a restarted voter grants at once | design §3.1, the restart wait | 2 |
| a voter answers before its fsync | design §3.1, fsync before answer | 2 |
| the leader times its lease from the reply, not the request | design §3.1 | 2 |
| the gate is judged on the elector's tick, not when it is read | design §3.2 | 3 |
| a new leader applies alarm state before its `oplog` catch-up | design §1, `oplog` | 3 |
| `Liveness` ignores a loop between scans (the behaviour before #858) | design §3.2, starvation | 3 |
| a standby's gated ticks don't count as on schedule | design §3.2, starvation | 3 |
| the in-sync mark is not persisted | design §3.3 | 4 |
| a stream frame of an older term is applied | I5 | 4 |
| hot takeover with no image-age check | I5 | 4 |
| hot takeover on an image with a different build hash | I5 | 4 |
| the gate is checked once per batch, not before each send (as `hw/base.go` does today) | design §3.4 | 3 |
| a send with no deadline | design §3.4 | 3 |
| no fence margin (the gate closes at lease expiry) | design §3.4 | 3 |
| sockets closed with FIN, not RST, on fence | design §3.4, RST on fence | 3 |
| the peer listener trusts a Host instead of requiring the peer token | design §6.1 | 3 |
| the standby's proxy drops or rewrites the client's Host or token | design §6.1 | 3 |
| no back-off after a lease lapses unconfirmed | design §3.1, one-way partitions | 2 |
| grants accepted from any witness incarnation | design §3.1, the witness incarnation | 2 |
| a grant counted toward a request it doesn't answer (matched by term, not id) | design §3.1, request ids | 2 |
| a new incarnation's grants counted with no quiet period | design §3.1, the witness incarnation | 2 (handshake) |
| an incarnation adopted without the term floor | I4 | 2 (handshake) |
| leases timed on `CLOCK_MONOTONIC`, which stops across a suspend | design §3.1, clocks | 2 |
| the override opens the gate before its journal fsync | I8 | 5 |
| the override survives hearing a peer | design §7 | 5 |

**The runner (2c).** Every PR-2 rule has a switch in package `quorum`. It exists only in a
build with the `quorum_mut` tag, and `QUORUM_MUT=<name>` turns one on; a normal build compiles a
constant false, so the checks cost nothing. `go run ./tools/quorum-mutants` does the following:
- it runs `go test ./quorum/...` once per mutation;
- it requires the named tests to go red;
- it counts a build or setup failure as a failure, and says so when a test panicked (which
  hides the tests after it);
- it checks the baseline (the tag on, no mutation) is green first;
- it prints a Markdown table and exits non-zero on any survivor.

Its own table test checks those verdicts, and that every mutation in `quorum/mut_on.go` has a
row. All 17 are caught, in about 50 s.

**The first 200,000-seed nightly (#998)** failed 20 seeds on main (78d234e8), all liveness or
steady state, none safety. They are named cases in `quorum/sim/nightly_test.go`, in four classes:
1. **A liveness bug, 8 seeds.** A voter the majority needs fsyncs in 1.6–1.9 s, so a won lease
   had less left than a look and lapsed before its first renewal. Fixed by renewing at once on
   winning (design §3.1; switch `renew_on_look`).
2. **Beyond the documented link rule, 7 seeds.** The witness is wiped and the A–B link has a
   0.86–0.96 s one-way delay, so takeovers may flap (F21). Every other oracle holds. The
   generator now caps a delay fault at base + jitter + fault ≤ lease/4.
3. **A lost controller file, 3 seeds.** The controller lost its accepted incarnation, so the
   witness can't back it (#976).
4. **A witness file torn by a crash mid-write, 2 seeds.** That is a rebuilt witness, not yet
   counted.

For classes 3 and 4, steady state is judged on A and B alone when the record shows a node
restarting from an unreadable file. The oracle used to look only for explicit wipes in the
schedule. The handshake PR flips this back.

**Where each is caught.**
- **Targeted sim cases (`quorum/sim/targeted_test.go`)** catch three rules the matrix and the
  seeds miss:
  - a voter answering before its fsync: a crash inside a slow fsync lets one term be led twice
    (I4, I2);
  - a grant matched by term, not request id: late grants stretch a lease past the witness's
    promise (the lease oracle);
  - the lapse back-off, removed or cut to one lease: a one-way leader must send nothing for two
    leases after its lapse.
- **Three liveness rules are caught only in T1** (`quorum/standby_test.go`): renewals seen when
  refused (#973), `Candidate.Observe`, and a retry that moves the look (#979). With the re-term,
  the yield and timers armed at `NextLook`, the simulator elects without them in every schedule
  tried (200 two-voter cold starts included). They stay as defence in depth, and the lockstep
  T1 harness, with instant delivery and 10 ms steps, pins each.
- **The two-lease lapse back-off still pays (design §3.1).** With perfect clocks, #979 makes F9
  take 3.5–3.7 s with one lease or two. With clocks skewed within ρ it does not: over 160 F9
  schedules per delay, the worst takeover at 1–100 ms is 6.4–6.9 s with one lease and 3.9–4.3 s
  with two. `TestTargetedOneWayLeaderSkewed` (A 0.5% slow, B 0.25% fast) is red with one lease
  at 6.43 s. Its cost stays the double fault.
- **Long links flap (F21, measured, not failed).** The same F9 sweep at a 2 s lease, with the
  worst one-way delay counted as delay plus jitter:

  | worst one-way delay | flapping runs (of 160) | worst takeover |
  |---|---|---|
  | 450 ms (lease/delay 4.4) | 0 | 4.8 s |
  | 525 ms (3.8) | 0 | 4.9 s |
  | 600 ms (3.3) | 1 | 6.5 s |
  | 750 ms (2.7) | 9 | 7.1 s |
  | 825 ms (2.4) | 18 | 7.4 s |

  At 900 ms (about lease/2.2), 54 runs flap, taking over up to three times. **The rule: lease ≥ 4
  × the worst one-way vote-link delay.** The guide states it, and `naut check` warns when a
  configured lease is below it for a declared link latency (the guide and config PR, on the
  #710 plan). The seeds keep delays within lease/4.
- **Loss on the sole vote path flaps (F20, measured above 3%).** Seed 260567 (an extra sample
  on #999) wipes the witness, so A–B carries every majority, and drops 27% of A–B both ways: a
  renewal round fails about half the time, a lease spans only three or four rounds, and
  takeovers flap. No lease protocol keeps I7 there. `TestMeasureSolePathLoss` measures it: the
  witness cut off from both controllers, p% loss on A–B both ways for 10 leases (the generator's
  longest fault), on 20,000 seeded networks, disks and clocks per rate, at a 2 s lease:

  | loss each way | windows with a takeover | with a second takeover (an I7 breach) |
  |---|---|---|
  | 1% | 5 | 0 |
  | 2% | 38 | 0 |
  | 3% | 114 | 0 |
  | 4% | 252 | 1 |
  | 5% | 484 | 4 |
  | 10% | 2,969 | 156 |
  | 27% | 14,074 | 5,357 |

  **The rule: on a sole vote path, I7 is claimed up to 3% loss each way**: in the simulator,
  no second takeover in 20,000 ten-lease windows at 1–3% (first at 4%); one takeover is the
  loss fault's own. That is a measurement with a thin margin, not a proof, and T3/T4 are to
  confirm it. If a 3% nightly seed ever breaches I7, the cap drops to 2%. Above the bound,
  takeovers are measured, not guaranteed. The guide states it. The
  generator caps loss at 3% on a link whose third voter the schedule also faults (down, paused,
  its disk, or a link to it), and still draws 1–30% elsewhere. Seed 260567 stays a named case
  (`TestNightlyBeyondRuleLoss`): I7 is excluded, and every other oracle, safety included, is
  asserted.

**What the sim catches on its own** (with the cases that go red):
- no ρ: 32 seeds and the skew test;
- the lease timed from the reply: 2 seeds and the skew test;
- a restarted voter granting at once: 23 seeds and named cases;
- grants from any incarnation: 16 seeds and named cases;
- a promise of half a lease: 256 matrix cases and 137 seeds;
- no re-term: 704 matrix cases and 144 seeds;
- no yield: the livelock regressions and one seed.

A formal model (TLA+ or PlusCal of the vote protocol, checked by TLC) is **optional**. It gets
decided after PR 2, from what the simulation finds.

### 3.4 Between the check and the send

A `quorumtest` build tag puts a controllable pause hook on every output path in design §3.4,
at three points:

1. after the gate check and before the deadline is computed;
2. after the deadline is set and before the send;
3. right before the send system call.

Each case sweeps pauses of 0.1, 0.5, 1 and 2 × lease, in T2 and T3:

- **At points 1 and 2,** nothing may leave late (I3).
- **At point 3, with pauses shorter than `m_fence`,** nothing may leave late either: the fence
  margin covers them (design §3.4). This is the case that proves the margin.
- **At point 3, with pauses longer than `m_fence`,** the stated residual: at most **one frame
  per sending goroutine** can reach the device late, by no more than the pause minus the
  margin. The case is an expected one: it measures both the count and the lateness. A late
  frame breaks I1, I2 and I3 alike, so it is excluded from those three by name, and only for the
  measured count and lateness.

### 3.5 The same proofs on real processes (T3, T4)

I1–I3 are asserted from gate logs and from the recorder during every T3 and T4 case. For T3
that means the full fault catalogue (§5) nightly. For T4 it means each bench session.

## 4. Takeover time: definition and measurement

### 4.1 Events

Every event is timestamped on one clock: the host's `CLOCK_MONOTONIC` in T3, the recorder's
clock in T4.

| Event | Meaning |
|---|---|
| `t0` | the fault was injected (by the harness) |
| `t_fence` | the old leader's gate closed (its log; not observable at power loss) |
| `t_elect` | the new leader's lease was granted |
| `t_inputs` | the new leader's inputs are live (#786) |
| `t_out` | **the first write from the new leader reaches the recorder** |
| `t_gap_start` | the last write from the old leader reaches the recorder |
| `t_hmi`, `t_ua` | the first fresh value at the HMI client and at the OPC UA client after the takeover |
| `t_pn_dcp`, `t_pn_ar`, `t_pn_valid` | PROFINET: the new leader's first DCP answer, its AR established, and the first cycle with valid data both ways (taken from the tap) |

### 4.2 What gets published

- **Takeover time** `= t_out − t0`.
- **Output gap** `= t_out − t_gap_start`. This is what a device's watchdog sees, so it is what
  sets a safe watchdog time.
- **Client recovery**: `t_hmi − t0` and `t_ua − t0`.
- **PROFINET re-establishment**: `t_pn_valid − t0`, reported as a separate figure with its
  breakdown: lease, the PLC's retry phase, DCP, connect, first valid cycle. It is part of the
  takeover time for any project with a PROFINET device, and the guide publishes it on its own
  line with the PLC's AR watchdog and update-time settings.
- The breakdown for each case, with every term of the design's formula (§4.5): `L_rem`, the ρ
  margin, the campaign delay `C`, the vote round with fsync, any restarted-voter wait `W_r`,
  the inputs wait `I` (and whether it hit the cap), the first scan `S` and the output set `O`.
  An I/O-health handover also reports its hold-off.

### 4.3 Method

- **The fault time is random**, uniform over the renewal cycle and the scan period, so that
  the injection does not alias with the renew ticker.
- **Sample sizes.** T3: N ≥ 200 per cell. T4: N ≥ 30 per cell for power pulls and N ≥ 100 for
  process kills and cable pulls.
- **Cells.**
  - fault type: power loss or SIGKILL, hang, partition from both, I/O loss;
  - lease: 1 s, 2 s, 4 s;
  - standby I/O: polling, or no session;
  - takeover: hot or cold;
  - stream link: plant network or dedicated cable (T4).
- **Statistics:** p50, p90, p99 and max, plus the formula's bound for the cell. With N = 200,
  p99 is the second-largest sample; the report says so rather than overstate it.
- **Budget.** A T3 cell whose p99 exceeds the formula bound by more than one scan plus 10% fails
  the weekly run.
- **Where the numbers go.** The published table in the redundancy guide uses T4 numbers only.
  T3 numbers are labelled containers.

## 5. Failure-injection catalogue

Each row is run in T2 where it can be simulated, in T3 where a container can inject it, and in
T4 where only hardware can.

| # | Fault | Injection | Expected | T2 | T3 | T4 |
|---|---|---|---|---|---|---|
| F1 | **Power loss, leader** | T3: `incus stop --force` (no orderly stop). T4: cut its supply. | The standby takes over after the lease, hot if in sync. Outputs are resent. | ✓ | ✓ | ✓ |
| F2 | Power loss, standby | as F1 | Nothing changes for the plant. "Standby lost" alarm. | ✓ | ✓ | ✓ |
| F3 | Process kill (SIGKILL), leader or standby | `kill -9` | as F1 / F2 | ✓ | ✓ | ✓ |
| F4 | Orderly stop (SIGTERM, `systemctl stop`) | signal | The leader releases its lease, and the standby takes over before a lease would expire. | ✓ | ✓ | ✓ |
| F5 | **Process pause** (SIGSTOP) for 0.25, 0.5, 1, 1.5 and 3 × lease | `kill -STOP/-CONT` | Below the fence: it resumes the same term, with no takeover. Above it: it fences, and on CONT it writes nothing (apart from the residual of design §3.4, which F35 measures) and comes back as standby. Sockets are closed with RST. | ✓ | ✓ | ✓ |
| F6 | Hung scan (logic that never returns; a blocking driver call) | the existing nested-loop trigger | #621: the gate closes at lease/2, then takeover. A hung standby does not campaign. | ✓ | ✓ | |
| F6b | **Leader starved between scans** (#858): its scan loop gets no CPU after a scan ends, while the elector's goroutine still runs | T2: the loop's ticks stop. T3: a `quorumtest` hook that blocks the loop between `end()` and the next `begin()`, plus a real case: the task pinned with `cpu:` and a SCHED_FIFO hog on the same core | `Liveness` flags it within `B = StallBound(period, lease/2)`. The elector stops renewing, and the gate closes within `B + lease/4 + lease/2` of the **first missed slot** (a slow task isn't late before its slot comes due). B takes over after the lease, with no overlap (I1). Rerun with the standby starved, which doesn't campaign, and with a healthy standby ticking but not scanning, which still acquires. | ✓ | ✓ | ✓ (FIFO hog: only with the #836 safeguards in place and the maintainer's explicit OK, never pinned to a vendor runtime's core; no RT load runs on a bench controller otherwise) |
| F7 | Starved elector (CPU throttled to 1–5%) | cgroup `cpu.max` | The gate is judged when read, so it closes on time. I1 holds. | ✓ | ✓ | |
| F8 | **Partition A–B**, both directions | nftables | W renews the leader. The stream stops, and the standby is marked out of sync. | ✓ | ✓ | ✓ (cable pull) |
| F9 | One-way cuts: A→B, B→A, **leader→voters only** (it can send but not receive) and voters→leader only | nftables, one direction | A–B one way: as F8, no flapping. A leader that can send but not receive fences at the end of its lease, backs off for two leases (design §3.1), and B is elected before it is back: within 4.6 s of the cut at a 2 s lease, with clocks skewed within ρ (6.4–6.9 s at worst with a one-lease back-off; `TestTargetedOneWayLeaderSkewed`). The accepted cost, measured too: with the standby down, a full-lease witness blip leaves the pair leaderless 4.0 s (2.0 s with one lease). A leader that can receive but not send fences, and B is elected. | ✓ | ✓ | |
| F10 | **Leader cut from the witness** | nftables | A+B are a majority, so nothing changes. "Witness unreachable" alarm. | ✓ | ✓ | ✓ |
| F11 | Standby cut from the witness | nftables | No change. Alarm. | ✓ | ✓ | ✓ |
| F12 | **Leader isolated from B and W** | nftables | The leader fences at the end of its lease. B+W elect B, which takes over hot or cold. When it is healed, A returns as standby, writes nothing, and resyncs. | ✓ | ✓ | ✓ |
| F13 | Witness down | stop it | No change. Alarm: "one more failure stops control". | ✓ | ✓ | ✓ |
| F14 | Witness restart, state file kept | restart | No change. | ✓ | ✓ | ✓ |
| F15 | Witness restart **without** its state file (HA-cluster reschedule) | wipe the file and restart; T3 also runs it as a pod on a kind cluster | It comes back as a new incarnation and grants nothing for one lease, and no double grant happens. It counts only after A and B adopt it together over the A–B link, and only once the quiet period (one lease after adoption) has passed; the adoption raises both voters to the higher of their promised terms (design §3.1). It re-copies the whole `oplog` from the leader, and no committed ack is lost while both controllers are up (§8.1). Rerun with one controller down: the rebuilt witness is not adopted, and the survivor behaves as F16 or F17. | ✓ | ✓ | |
| F15b | **Two witness copies at once**: the old pod on a partitioned node and its rescheduled copy, or a force-deleted pod, with the A–B link cut | T2: two incarnations. T3: kind, a StatefulSet with the node's network cut, then a force-delete. | I1 holds. Neither controller accepts the new incarnation without the other's concurrence (design §3.1). Kubernetes starts no second StatefulSet pod until the node is confirmed gone. **The switch case:** A leads on the old copy, B's promise to A has lapsed, and the pair adopts a second copy that has been up for hours. I1 holds because of the quiet period; the same case with the quiet period removed must break I1 (the handshake PR's mutation). | ✓ | ✓ | |
| F16 | Witness + standby down | stop both | With one voter of three left, the leader has no majority, so it fences at the end of its lease. The no-quorum alarm is raised, and the device watchdogs act. | ✓ | ✓ | ✓ |
| F17 | Witness + leader down | stop both | The standby has no majority: it stays standby, raises no-quorum, and offers the override. | ✓ | ✓ | ✓ |
| F18 | Both controllers down, witness up; restart in each order, at each delay | stop and start | The first controller back forms a majority with the witness and leads. It takes over hot only on a current image. If it was the out-of-sync standby, or the mark is unknown, it takes over cold with the alarm (decision 4). The second joins as standby and resyncs. | ✓ | ✓ | ✓ |
| F19 | **Cold start of all three**, in all six orders, with start offsets from 0 to 2 × lease | start | Exactly one leader. No write before the election (I1, I3). | ✓ | ✓ | ✓ |
| F20 | **Flapping links**: each link toggled with periods of lease/4, lease/2, lease and 2 × lease, at 50% duty, for 5 min | nftables loop; netem loss 1–30% | I1 holds. Takeovers stay within the I7 bound, and the hold-off stops ping-pong. **Loss on the sole vote path** (the third voter out, so one link carries every majority): I7 is claimed up to **3% loss each way**: in the simulator, no second takeover in 20,000 ten-lease windows at 1–3% (first at 4%); T3/T4 to confirm. Above it, takeovers are measured, not guaranteed; every other oracle still holds (§3.3, the loss rule). Where the third voter is counted, loss up to 30% is covered. | ✓ | ✓ | ✓ (scripted switch port, if the switch allows) |
| F21 | Latency and jitter: 1 ms to lease/2 delay, with ±50% jitter and reordering | tc netem | I1 holds. Delay near lease/2 may cost renewals: that is measured and the guide gets a minimum lease per link latency. | ✓ | ✓ | |
| F22 | **Clock-rate skew**: ±0.1%, ±0.5%, ±1% (= ρ) between any two voters | T2: per-node clock rate. T3: a `quorumtest` build tag that scales the elector's monotonic clock (containers share one kernel clock). | I1 holds up to ρ. At 2× ρ, T2 shows where the margin breaks, and the guide states the limit. | ✓ | ✓ | |
| F22b | **Stopped leader clock**: a VM paused or live-migrated, a host suspend | T2: the leader's clock frozen while others run. T3: `incus pause` of a VM leader for 0.5–3 × lease, and a guest suspend. | **Host suspend:** safe, because leases run on `CLOCK_BOOTTIME`. **VM pause:** an expected I1 breach, measured. The guide lists pausable or migratable VMs as unsupported for controllers (design §3.1). The resumed leader writes, so the case is excluded from I1, I2, I2-PN and I3 by name. | ✓ | ✓ | |
| F23 | Wall-clock steps: ±1 h jumps, NTP slewing | T3: the pair in two incus VMs, `date -s` in one. T4: `date -s` on one box. | No effect: only monotonic durations are used. The VMs share no monotonic clock, so the recorder (I2) arbitrates I1 here. | ✓ | ✓ | ✓ |
| F24 | **Slow disk**: fsync at 10 ms, 100 ms, lease/2 and over lease, on the voter state, retain and the journal | T2: simulated disk. T3: a dm-delay device in the rig VM. | A voter whose fsync has not finished has not answered, so the grant is late but never early. A slow retain save does not stall the scan. **Liveness bound:** a lease won renews at once with no write (design §3.1), so the pair keeps a leader while fsync stays under about `lease(1−ρ) − 2·RTT` (1.9 s at a 2 s lease). Slower, a voter the majority needs can't grant in time, and no lease is won while it is needed (measured, not failed). | ✓ | ✓ | |
| F25 | Disk full, EIO | T3: a size-limited tmpfs; T2: injected | A voter that cannot persist does not grant. Alarm. The leader's fate follows from the votes. | ✓ | ✓ | |
| F26 | Torn state files: the voter file, retain and the journal truncated at every byte offset | T2, and T1 for the decoders | The torn tail is dropped, and a voter that cannot read its state waits one lease. | ✓ | | |
| F27 | **I/O lost on the leader only** (#626) | nftables leader↔recorder | On a polled device, the leader hands over after the hold-off, because the standby is strictly better (design §3.2), and the standby's inputs are already live. On a no-session device (EtherNet/IP, IO-Link, OptoMMP with a watchdog), nothing hands over: a driver fault is raised, and the recorder's watchdog trips. | ✓ | ✓ | ✓ |
| F28 | I/O lost on both, and I/O flapping | as F27 | No handover ping-pong. Alarm. | ✓ | ✓ | |
| F29 | Stream link only cut (dedicated cable) | nftables / cable pull | Votes continue over the plant network, and the standby is marked out of sync. | ✓ | ✓ | ✓ |
| F30 | Misconfiguration: peer lists differ, two processes claim `a`, wrong token, one witness with two pairs of one name | start | Refused at start with a message that names the conflict. | ✓ | ✓ | |
| F31 | Hostile or broken messages: a replayed old grant, an old-term stream frame, a corrupted frame, a wrong build hash | T2/T1 injection | Refused. A frame that fails its checksum triggers a full resync. | ✓ | | |
| F32 | **PROFINET device handover** (#790): F1, F3, F4, F6 and F12 on a project with `profinet-device` and the floating IP | as those rows, plus the segment tap | One station at a time (I2-PN). The address moves before the new leader's first DCP answer. `t_pn_*` is measured. | ✓ | ✓ | ✓ (bench PLC, two hosts on the bench LAN) |
| F33 | **PROFINET with the old leader frozen**: SIGSTOP for longer than the lease, then SIGCONT | `kill -STOP/-CONT` | While frozen, the old box still holds the address, so the address-conflict effect on `T_pn` is measured. **After SIGCONT, the old leader sends no RT, DCP or LLDP frame at all**: its sender must read the gate before sending, not only on its next 200 ms look. | ✓ | ✓ | ✓ |
| F34 | PROFINET under redundancy **without** a floating IP | `naut check` / load | Refused, with a message that names the floating IP. | | ✓ | |
| F34b | PLCnext under `redundancy:` | T1 unit test: load / `naut check` | Refused for now, with a message saying why (design §3.4); `TestPlcnextRefusedUnderRedundancy` (#832) covers it until the driver is gated. A T1 test, so no tier is ticked. | | | |
| F35 | **A pause between the check and the send** on every output path | the §3.4 hook | As §3.4: no late send at points 1 and 2, and the residual measured at point 3. | ✓ | ✓ | |
| F36 | A single-client device still held by a frozen old leader | SIGSTOP the leader while it holds the only session | The new leader's inputs wait lasts until the device's session timeout, or the #786 cap. If the wait gives up, the takeover stays hot, with Bad-quality inputs and the alarm (design §4.2). | ✓ | ✓ | |

## 6. The state stream (I5, I10)

### 6.1 Fidelity

On a hot takeover, the new leader picks up exactly where the image left off. The ground truth
comes from the recorder and the plant model. Every check below runs under two faults:

- **Crash.** The **old** leader is killed at a random point, so the image age is about one
  stream interval.
- **Isolation (F12).** The old leader keeps scanning and writing, unseen, until its lease ends,
  so the image age is about `lease + campaign delay`. The test checks three things:
  - the standby measures the image age and reports it;
  - the takeover is hot when the age is within `hot-max-age` and cold when the bound is set
    lower;
  - the effects of the window are bounded. Actions the old leader took inside it can repeat,
    and input pulses that came and went inside it are missed. The test asserts the counts of
    both against the plant's ground truth.

**What carries over:**

- **SFC:** the active step from the image, and the step's elapsed time within the image age.
- **TON / TOF / TP:** elapsed time within the image age. No timer restarts from zero.
- **CTU / CTD** against a device-side count of real edges: the controller's count never ends
  ahead of the device's. It lags by no more than the pulses inside the re-run window
  (design §4.2).
- **Retained setpoints:** one written N ms before the fault is present on the new leader for
  every N above the image age. Below that, it is lost silently, as the design states, and the
  test counts it.
- **Alarm acks and shelves**, under the same rule.
- **`VAR RETAIN`:** carried.
- **PID integrator continuity:** measured, not claimed.
- **Forces are dropped.** Every output is sent once (#600/#606), as seen by the recorder.
- **Order:** inputs wait, then the first scan, then the full output set. The recorder sees no
  output before the first scan.
- **#786 gives up** (F36): the takeover stays hot, inputs carry Bad quality, and the alarm is
  raised.

### 6.2 Staleness

- **Stream lag**, from the leader's scan end to the standby's apply: a histogram, p50/p99/max,
  for small, medium and large frames (the example projects, plus a synthetic project with 10k
  tags and 500 FB instances).
- **Bandwidth** for each frame-size class and stream rate, sharing a loaded plant link. An
  iperf flood competes with it, to show that votes on their own connection still renew on time.
- **The image-age bound.** An image just inside the bound gives a hot takeover. One just
  outside gives cold plus the alarm, and the alarm names the staleness.
- **Stream cut, then leader death:** cold takeover plus the alarm (decision 4).

### 6.3 Resync

Each of these leads to a full frame and then a return to deltas:

- the standby restarts;
- the base of a delta has been lost;
- a frame fails its checksum;
- the leader changes (new term).

## 7. Rolling update and cold handover

Each case is run with `tools/deploy` (#741). The build hash changes on a new binary or a
changed manifest.

1. **Update the standby.** It returns with a mismatched hash. Expect the "standby on a
   different build" alarm, and no stream applied. The leader keeps running.
2. **Hand over** (SIGTERM on the old leader). Expect a cold takeover on the new build, with
   outputs resent, and the measured time recorded as the update's cost.
3. **Update the other controller.** Both are back in sync and the stream resumes.
4. **Roll back** at each step. Expect no double writer at any point, and the same cold
   handover rule.
5. **Update the witness while the pair runs.** Nothing changes, as F14.
6. **A config-only change** (manifest edit, same binary). The hash differs, so the handover is
   cold. That is documented, and the test proves it.

## 8. The engineer override and its audit trail (I8)

**Authentication**

1. No token: refused. Wrong token: refused. Both refusals are logged and counted.
2. Not offered while a quorum exists. If the node has quorum, the request is refused.

**Lifecycle**

3. **Valid override on a lone survivor:**
   - the journal record is fsynced before the gate opens, checked from the order of the
     recorder's writes against the journal's fsync time;
   - the record contains the attestation text, the context and the expiry;
   - the takeover is hot or cold by the usual rule.
4. **Expiry:** the gate closes at `expires`. The `.expire` record is written. Renewal extends
   it, with a `.renew` record.
5. **A peer is heard** (the other controller, or the adopted witness, comes back; a rebuilt
   witness not yet adopted doesn't count, design §7): the override ends at once with an `.end` record, a normal election follows, and there is one writer afterwards.
6. A restart ends the override. It is not resumed.

**Crash ordering**

7. A crash between the fsync and the gate opening leaves a record and no writes. The record
   says "never effective": `.start` with no `.end` and no gate-open in the log.
8. A crash before the fsync leaves no record and no writes.

**Every redundancy event is recorded** (design §7.1)

8a. Each takeover (hot or cold, with the image age), handover (orderly or I/O health), quorum
    lost and regained, adoption of a witness incarnation and stale cold takeover writes exactly
    one record in #709's shape. That shape is `ts`, `user`, `source`, `action`, `target`,
    `before`, `after`, `result` and `detail`.
8b. With quorum, records go through `oplog`. Without it, they go to the local journal and are
    appended to `oplog` when quorum returns, never duplicated (by `id`).

**Replication and format**

9. **The journal is copied** to the peers on quorum return. Records are never duplicated and
   never reordered. A torn final line is dropped.
10. **The record shape** is validated against a fixture of the #709-shaped record, so the
    later move into #709 is a check, not a guess.

**The dangerous case**

11. **The override is used while the other side is actually alive and partitioned.** This is
    the only path to two writers.
    - The test **expects** I1 to break for the time until the partition heals.
    - It measures that window and asserts that the override ends the moment the peer is heard.
    - The guide states the window. Two writers break I1, I2, I2-PN (on a PROFINET station)
      and I3, and the override's own term can equal the one the other side elected, which
      breaks I4 (design §7). The case is excluded from those five oracles by name, and from no
      other.

### 8.1 Operator intent through `oplog`, with the witness as third peer

1. **Ack while B is down.** A and the witness commit the ack, then A dies. When B is back,
   B and the witness elect B, which catches up from the witness. The ack is present and
   is not doubled.
2. **Ack while the witness is down.** A and B commit it. The witness, on its return, catches up.
3. **Witness rebuilt from scratch** (wiped disk, or a pod rescheduled without a volume):
   - it re-copies the whole log from the leader;
   - with both controllers up, nothing committed is lost;
   - its election state follows F15, adoption by both controllers included.
4. **Two faults stop the pair, then the override.** B is down while A and the witness commit,
   then the witness loses its disk.
   - The rebuilt witness can't be adopted without B (design §3.1): A has no majority and
     fences, with the no-quorum alarm. Nothing is lost.
   - A then dies before anyone has copied the entry. B and the rebuilt witness can't adopt
     its incarnation without A: B stays fenced with no quorum, and nothing is lost yet.
   - A returns: the ack is still there, and B copies it.
   - Rerun with the override on B instead of waiting for A. The ack **may be lost**, per PR 5's
     rule for whose `oplog` survives the heal (design §7). The test asserts that rule's
     outcome, that this is the only path that can lose an ack, and that the override's record
     and the guide both state it.
5. **Lone survivor.** An ack or shelve is refused with a "no quorum" error, and the HMI shows
   it read-only.
6. **The witness only follows the leader its votes name.** Replication from any other source
   is refused, a stale term included.

**Substitute values (#707)** go through `oplog` as operator intent: `substitute`, `unsubstitute`
and `suback` entries, the last two naming the substitute's entry by `seq`. Cases 7–10 are the
requirements agreed on #707. They join the run in #707's oplog PR, which builds the kinds.

7. **A new leader's first scan sees every committed substitute.**
   - Setup: partition A from B (F8); substitute a bad input on A, committed by A and the
     witness; kill A.
   - B's takeover catches up `oplog` and applies the substitute **before its first scan**,
     in the same step as the retain load. The `oplog` copy wins over B's retained one.
   - The first scan's input value is the substitute: checked on the scan, not after it.
   - If catch-up fails within its bounded wait, B keeps its retained substitutes and raises
     the alarm flag. A substitute is never dropped silently.
   - Mutation: catch-up deferred to the first `oplog` write (today's lazy `leaderCatchUp`).
     Case 7 must go red.
8. **Only the leader expires a substitute, and the expiry is an entry.**
   - At the end of the granted duration the leader appends `unsubstitute` (`by: system`,
     `reason: expired`). The standby never expires one on its own.
   - A takeover evaluates expiry once, as in case 9.
   - A substitute that expired on the old leader just before a takeover is not revived.
9. **How a leader times a substitute, and a wall-clock step** (F23-style).
   - **The leader that applies it** times the granted duration on `CLOCK_BOOTTIME`, from
     when it applied it.
   - **A new leader** computes the remainder **once, at takeover**, from the entry's
     wall-clock `until` on its own clock: `until − now`. Skew between the pair up to the
     skew bound, **30 s** (`runtime.SubstituteSkew`), is accepted. Then it times that
     remainder on `CLOCK_BOOTTIME`. A takeover never restarts the full duration.
   - **A remainder outside its bounds** means the new leader's wall clock was stepped: below
     zero, the substitute has expired. Above the granted duration it is capped there; above
     the granted duration plus the 30 s skew bound, the cap also raises the alarm
     (`__Substitute_Fault`, PR 3; until then a `fault` event, reason `clock-capped`).
     A leader that was already timing the entry (it led before, as in A→B→A) keeps the
     earlier of its own deadline and the one from `until`: a wall clock stepped back while
     it stood by never stretches the substitute.
   - **A single controller across a restart** works the same way: the remainder comes from
     `until`, so downtime counts toward the duration.
   - Steps: step the leader's clock ±1 h mid-substitute; it still ends at its granted time.
     Then step the standby's clock before a takeover. Set 1 h ahead, the remainder
     shrinks by 1 h: it ends early, at worst at once, with the usual expiry audit and
     `__Substitute_Expired` pulse. Set 1 h behind, the remainder grows past the granted
     duration: it is capped there, and `__Substitute_Fault` is raised. At the bound: 29 s
     behind is accepted as skew (no cap, no fault), and 31 s behind is capped and faults.
   - **Repeated takeovers inside one substitute:** take over A→B→A→B at intervals shorter than
     the granted duration. The substitute still ends at its granted time, ± the skew.
   - Mutations: expiry taken from the wall-clock `until` on a steady leader (a step then
     moves it), and a takeover restarting the full duration. Both turn case 9 red.
10. **Expiry doesn't wait for quorum.**
    - With no quorum, starting or stopping a substitute is refused like any operator write
      (case 5).
    - On the override path a substitute still ends on time: the expiry is journalled locally
      and reconciled at the heal (design §7, PR 5's open question).
    - A late or merged `unsubstitute` naming an older entry's `seq` does not end a newer
      substitute of the same tag.
    - Mutation: expiry gated on an `oplog` commit. Case 10 must go red.

11. **Every case runs** in T2 (the simulated disk and network). Cases 1–5 and 7–10 also run
    in T3.

### 8.2 Peer traffic and the Host rule (design §6.1)

Each case runs in T1 against the handlers and in T3 between containers, with every peer
addressed **by DNS name**: names that are not in any `NAUTILUS_ALLOWED_HOSTS`.

1. **The peer listener requires the peer token.**
   - A request without it gets 401/403, by name, by IP and on localhost alike, so a trusted
     Host never stands in for the token.
   - A request with the **client** token instead is refused.
   - With the peer token it is answered, by name or by IP.
2. **Peers by name, nothing allowed.** Votes, the stream, `oplog` replication and journal
   copies all work between peers addressed by name, with `NAUTILUS_ALLOWED_HOSTS` empty. This
   is F1 and F12, rerun this way.
3. **No peer route behind the tag API.** `server.Handler` serves no vote, stream or `oplog`
   path. A request for one gets 404, token or not.
4. **The standby's proxy** keeps the client's Host and token header:
   - by IP: answered by the leader;
   - by a name in both controllers' `NAUTILUS_ALLOWED_HOSTS`: answered by the leader;
   - by an untrusted name with the client token: answered;
   - by an untrusted name without it: refused, 403.

   The leader and the standby give the same answer in every case.
5. **Client token mismatch.** A and B with different `NAUTILUS_TOKEN` values raise "client
   token differs between A and B" at the handshake. The handshake's bytes never contain either
   token, which the test checks with a tap on the peer port.
6. **Advertised names.** `naut run` warns about an advertised peer name its
   `NAUTILUS_ALLOWED_HOSTS` doesn't list. A browser HMI following `/api/cluster` to that name
   gets 403. With the name listed, or with the controllers advertised by IP or the floating
   address, it follows (§9).
7. **Starting without a peer token.** A pair member or a witness without `NAUTILUS_PEER_TOKEN`
   is refused at start. This joins F30.

## 9. Clients follow the active controller (#716)

Each case runs during F1, F3, F4, F12 and F27 in T3. F1 and F12 also run in T4.

**HMI**

1. **HMI in a browser.** The kit, configured with both addresses, follows `/api/cluster` to the
   new leader. Data freshness recovers within `t_hmi`. The standby's view is read-only, and a
   write against it is refused. Driven by the rig's browser harness.
2. **The standby's API proxy** forwards to the leader, and refuses writes during the gap.

**OPC UA**

3. **OPC UA client** (a Go client in T3; a third-party client by hand on T4):
   - it reads ServerUriArray and ServiceLevel and moves to the new leader;
   - the leader's ServiceLevel is 200 or more, a standby in sync reports less, and one out of
     sync reports 1;
   - a write to the standby is refused (Bad_InvalidState).

**Floating IP**

4. **Floating IP** (design §8):
   - **Orderly handover (F4, I/O handover).** An observer runs `arping -D` throughout, and the
     address never answers from two MACs.
   - **Kill, hang or pause (F3, F5, F6).** Two MACs may answer until systemd or `ExecStopPost`
     acts. The test measures that window. It asserts that clients reach the new leader once
     its gratuitous ARP is seen, and that every write reaching the fenced box is refused.
   - Gratuitous ARP is seen on each change.
   - A client that knows only one address reconnects within `t_ua` / `t_hmi`.

**PROFINET**

5. **PROFINET IO controller.** The controller follows the station to the new leader:
   F32–F34, with `t_pn_*` reported beside `t_hmi` and `t_ua`.

**Sparkplug**

6. **Sparkplug edge** (when configured; #798): at most one birth holder on the broker at every
   instant, with the same assertion as `edge-collector.md` §5.4. NDEATH on step-down, and a
   new bdSeq on takeover.
7. **Sparkplug host:** NCMD and DCMD come only from the leader, and are checked before each
   publish.

## 10. Soak and chaos

**T3: containers**

- **Nightly 2 h and weekly 24 h runs:** seeded random faults from §5, one every 30–300 s,
  overlapping at most two at a time. All oracles are checked continuously.
- **Resource drift** is tracked, with a regression failing the run: RSS, goroutines, fds,
  journal and voter file size, stream bandwidth.

**T4: the bench, with the maintainer's OK**

- **A 72 h run** with scripted cable pulls and process kills.
- **30 power pulls** spread across it, by a switched PDU or relay if the maintainer provides
  one, and by hand otherwise.
- **Pass:** no oracle broken, no unexplained takeover, and no drift.

**The demo** (design §9.5) is the scripted subset of T3, then of T4: F1 mid-step, then F16/F17,
then the override. It runs as a test, so it can't silently regress.

## 11. Exit criteria by milestone (design §10)

| PR | Must pass before merge |
|---|---|
| 2 `quorum` + sim | T1. T2 matrix + 2,000 seeds. The PR-2 mutations caught (§3.3). F9, F15b, F22, F22b, F24–F26 and the vote half of F31 in T2. |
| 3 witness + wiring (cold) | The above, plus the T3 smoke subset: F1, F3–F5, F6b, F8–F10, F12, F13, F15–F20, F30, F35 and F36, and §8.2. Every output path in design §3.4 meets its rules (I3, §3.4). §8.1. The PR-3 mutations caught. Kubernetes `leader` tests unchanged. |
| 4 stream + hot takeover | §6 in T2 and T3. I5 and I10 in the nightly run. §7 rolling update in T3. The PR-4 mutations caught, and the stream half of F31. |
| 5 degraded modes | §8 in full. F16 and F17 with the device-watchdog assertion. F27 and F28 (the I/O-health handover). The PR-5 mutations caught. |
| 6 clients (#716) | §9 in T3, including F32–F34 for PROFINET. |
| 7 bench | §4 percentiles in T4, including `T_pn` against the bench PLC. The T4 rows of §5. The 72 h soak. Then the guide's numbers are published. |
