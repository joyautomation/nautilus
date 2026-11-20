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
    but the log is where an ack comes from (#710 PR 3b-2c, `server/alarms_oplog.go`).
    - **The write path.** Ack, shelve and unshelve are validated against the leader's engine,
      then proposed. "Ack all" becomes the leader's unacknowledged ids.
    - **Applying.** The leader applies each committed entry to its live engine, which journals
      it; a standby records it in a replicated view.
    - **At a takeover,** after the catch-up and before the first scan, the new leader restores
      the view: acks held for each alarm's first activation, and shelves until their own ends.
    - **Unsynced takeovers carry no ack** (naut-lead): those alarms re-annunciate and the alarm
      journal says "acks not carried: takeover unsynced". Shelves carry. With the witness a
      replica (3b-4b) that takes a new leader whose own copy is unsynced and only one other
      copy answering, or none.
    - **Occurrences: the invariant** (#1033 review). The view holds an ack for an alarm only
      while the leader's engine has that alarm's current occurrence acknowledged. Four rules
      keep it:
      - an ack that would change nothing is refused (409) and never recorded;
      - every transition out of ack-active on the leader proposes an `ack-clear`: a return to
        normal, a latched RTN to unack-rtn, a new activation, a suppress. A shelve keeps the
        ack. Clears are retried until committed and batched;
      - the leader reconciles the view against its engine at every log tick;
      - an unsynced takeover clears the view's acks.
    - **The residual window** is the leader dying between such a transition and the clear's
      commit: one log round trip plus a scan with the peer up (measured at 20-60 ms on
      loopback, `TestPairAckDoesNotCarryToANewOccurrence`), up to the peer's outage plus a
      retry with it down. Acks bound to an activation proper come with #715.
    - **Every takeover resets the engine.** A replica that led before keeps an engine frozen at
      its last leadership. Every takeover calls `alarm.Engine.ResetOperatorState` before
      restoring the view, so an ack of an occurrence that ended, or a shelf undone, while it
      stood by doesn't survive in it. Entries are applied live while the election's gate is
      open (from the takeover's start), so one committed during the takeover lands in the
      engine or in the restored view.
    - **Late entries lapse.** An ack or a shelve committed more than 60 s after it was proposed
      lapses (`oplog.KindLapse`, by age, as a late substitute start does): it would act on
      whatever occurrence is live by then. Unshelves and ack-clears never lapse.
  - **A new leader catches up first.** It catches up the log from the other peers (`oplog`'s
    `leaderCatchUp`) before it applies alarm state or accepts operator writes.
  - **No quorum, no operator writes.** A lone survivor has no majority, so its operator writes
    are refused. This matches the read-only HMI in §7.
  - **Three copies, wired (#710 PR 3b-4a, 3b-4b).** A, B and the witness each keep a copy;
    a write needs two that count.
    - **Only a synced copy counts.** A copy is synced once a synced leader's replication has
      proved it equal up to the commit index, or once it caught up as leader from a majority
      that counts. Each copy's mark (`oplog.mark`, `<cluster>.oplog.mark` on the witness)
      holds held/synced, its identity (a controller: cluster/node/box; the witness: its
      incarnation for the cluster), the highest term it has seen (so a lost copy still
      refuses a deposed leader) and the last vouch. A copy from another identity, or lost
      under the held mark, is moved aside and starts unsynced (`oplog-lost`,
      `oplog-unsynced` on `/api/cluster` and the banner). The leader counts the witness's
      copy only at the incarnation its election counts (`Elector.CountedIncarnation`).
    - **The witness's copy** is a passive `oplog.Node` per bound cluster at
      `node.Prefix + cluster + "/log"`, behind the peer token (`cmd/naut/witnesslog.go`). It
      never leads or dials; it takes replication only from the controller its vote names, at
      a term no higher than it promised (a renewal can re-term the vote above the leader's
      epoch, the log's term).
    - **A vouch starts a pair.** Three empty copies are all unsynced, so the commissioning
      re-commission vouches for that controller's copy (`Elector.SetVouch`, `oplog.Node.
      Vouch`): one synced copy, whose first catch-up as leader may succeed with no other
      counting copy, once, and only when EVERY peer answered (a silent one may hold a write
      committed without it: #1053 review); it takes the most up-to-date counting log, and the
      one-shot is spent in the mark before it is used. It is refused for a lost copy, when any
      peer doesn't answer, or when a peer that does NOT count holds a later log (a counting
      one's log is taken: A and B lost, the witness synced, re-commission A and the witness's
      entries come back). A refusal never refuses the re-commission: control never waits on
      operator data; the log stays read-only (`oplog-vouch-refused`). A copy already synced
      needs none. Copies from before marks count as synced.
    - **An unsynced controller leads for control, read-only for operator intent.** It
      doesn't campaign while the other controller is reachable, synced and may campaign (for
      two leases with no leader at most); a leader whose catch-up failed hands over to such a
      controller only if it can also sync the others (a pending vouch, or another counting
      copy heard), so the lead isn't handed over for nothing (it stops renewing until the
      lease is seen over; at most once per 10 minutes). The hello carries each controller's
      log report for this (synced, can sync, may campaign), bound in its MAC
      (`node/logpref.go`).
    - **A synced leader alone among the copies is read-only too** (`oplog-read-only`:
      "operator log read-only: no second counting copy — recommission (3c)"): entries
      committed while it lagged may be on the lost copies, so only a person can decide (3c's
      vouch or `--accept-loss` for a synced controller).
    - **The leader retries its catch-up** every half lease while its term is unsynced, once
      the runtime's takeover is done: the witness never pulls, and nothing leaves an unsynced
      leader.
  - **The 2-of-2 interim (3b-1 to 3b-4a)** ran the log on A and B only: either controller
    down refused every operator write, and every takeover after the leader died ran unsynced.
  - **A late start lapses (#1030 review).** A start answered "outcome unknown" sits on its
    proposer alone. If that proposer dies and returns much later, the new leader's catch-up
    adopts the start, and committing it would start the substitute then.
    - **Instead, the leader names it in a lapse entry.** The entry that commits an earlier
      term's leftovers is `oplog.KindLapse`. It names the substitute starts older than
      `SubstituteStartCommitBound` (60 s) on that leader's clock, decided **by age alone**: how
      many replicas hold an earlier term's entry proves nothing about its commit (Raft §5.4.2),
      and copies an earlier catch-up adopted would count (#1032 review).
    - **Every replica decides by that entry,** never by its own clock, so they agree. A named
      start found in the same committed range is applied as lapsed: listed and journalled, and
      it raises no alarm. Stops and acks never lapse.
    - **The accepted cost.** A start that was committed and answered 200, but whose commit
      push was lost and which a new leader catches up more than 60 s later, lapses too. A
      replica that had started it ends it loudly: the safe direction.
- **Three replicas, and only a synced one counts** (#710 PR 3b-4a, `oplog`).
  - **Who counts.** A replica's store counts toward a commit, and its answer toward a new
    leader's catch-up, only while it is **synced**: its log is known to hold every committed
    entry. Without the rule, B and a rebuilt empty witness could make a majority that holds an
    entry nowhere (A and the old witness committed it while B was down, and A then died).
  - **How a replica becomes synced.** A synced leader's replication proves its log equal up
    to that leader's commit index, or, as a leader, it catches up from a majority that counts.
    A witness counts only at the incarnation the election counts (`Options.Counts`).
  - **The mark.** Each replica keeps a small mark beside its log (`oplog.held`), written and
    fsynced once per identity: `held` before its first entry is stored, `synced` as above.
    No write pays a per-entry fsync for it.
  - **A lost log is seen.** A log missing, emptied, or damaged before its last line under the
    held mark is moved aside (`oplog.jsonl.lost-<time>`), never truncated. The replica starts
    empty, counts for nothing, and its controller may not lead (3b-4b) until a synced leader
    has given it the log again. A log written for another identity (a re-adopted controller, a
    rebuilt witness) is moved aside as `.previous-<time>`. What the mark can't see is a log
    rolled back to an older copy that still holds entries (a restored backup): never restore
    one.
  - **The witness follows its vote.** It takes replication only from the member its vote
    names, at that term (`Options.Admit`), never by address. It never dials a controller: the
    leader's heartbeat fills it, a chunk at a time.
  - **Nothing leaves an unsynced leader** (#1050 review). Until a leader has caught up for its
    term, it sends no heartbeat or resend, and a follower takes nothing from it by pulling: what
    it holds past its commit index may be an earlier term's leftover, and a follower would
    replace with it an entry a later term committed whose commit push it missed. A peer still
    short of the leader's log after a resend doesn't count as holding the entry. One entry is at
    most 512 KiB (larger is refused at its proposal, 413), so a stored entry can always be read
    back, sent and fetched.
  - **Every peer at once.** The leader replicates to both peers at once and commits on the
    first majority that counts, after a short wait for the other, so a silent peer never holds
    up a write by its call timeout. A takeover's catch-up asks both peers at once too.
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
  experimental: true     # required until the state stream (§4) lands
  peers:                 # the same file on both controllers
    a: ctl-a:9450
    b: ctl-b:9450
  witness: witness-host:9460
  lease: 2s              # default; the takeover time it gives is published
  state-dir: /srv/naut   # optional: default $STATE_DIRECTORY (systemd)
  stream: every-scan     # or {every: 5}: one image per 5 scans
  stream-link: ctl-a-link:9451   # optional dedicated A–B cable (§6)
```

- **An explicit opt-in until §4.** Until the state stream (step 4), a pair needs
  `experimental: true`, and Load refuses one without it, because a takeover starts from the
  new leader's own retain file and loses a setpoint the old leader took. A running pair says
  so at start, in `/api/cluster`'s `warnings` and on the dashboard (#1028, naut-lead's call).
  The key goes when step 4 lands.
- **Two modes, one section.** `peers:` selects the pair. Without it, `redundancy:` is the
  Kubernetes Lease as before, and `lease:` is the Lease's name. With it, `lease:` is a time
  (200 ms to 1 min), and a name there is refused. `witness:` or `state-dir:` without `peers:`
  is refused rather than ignored.
- **Self.** Both controllers run one build, so the manifest can't say which controller it is.
  `NAUTILUS_NODE=a|b` says so, and `tools/deploy` sets it per box.
- **The cluster ID** (§6.2) is `NAUTILUS_CLUSTER`, beside `NAUTILUS_PEER_TOKEN` in
  `/etc/nautilus/env`, outside the state directory. A controller whose state directory is wiped
  therefore still starts, and re-adopts the pair's witness incarnation from its peer (#976).
  Commissioning (3c) draws it and installs it on A, B and the witness. A member started
  without it is refused, with a message that says how to commission.
- **The tag API addresses** that `/api/cluster` and the standby's proxy use are each
  controller's `peers:` host with this controller's API port: both run one manifest.
- **Misconfiguration is refused at start, never resolved quietly.** A peer list that differs
  between A and B is refused at the peer handshake (#710 PR 3b-2b). Each hello carries a hash of
  the pair's members (peers and witness) both ways.
  - **On a mismatch** both controllers raise `redundancy.config-differs`.
  - **The hold.** While the peer keeps saying so (heard within three hello intervals), a
    controller that does not lead does not campaign. A leader keeps renewing, so an edited
    manifest rolled to one box doesn't stop a running plant, and no second leader rises on
    another view of the members.
  - **The cost.** If the leader's lease lapses while the mismatched peer is up, nobody leads
    until the manifests match. A peer that goes quiet holds nothing.
  - **The lease is not hashed.** The voters already refuse a longer lease (`lease-mismatch`).
  - **Timing.** The hello runs at start and every 16 leases (at least 5 s).

  Two processes that both claim to be `a` are refused in two layers (#1028 review), and the
  hello adds a third:
  - **The state file names its owner.** `quorum.state` records the member and the cluster ID
    that wrote it, and a controller refuses to start over a file another member or another
    pair wrote. That catches an env file copied to the wrong box with its own state kept.
  - **The voters bind a name to a boot nonce.** A cloned disk carries a matching state file
    and env. So a voter whose running promise to `a` was granted under one boot nonce refuses
    `a` under any other until that promise has run out, and raises
    `redundancy.duplicate-member`. A real restart of `a` waits out its restart wait first, by
    when the promise is over, so it costs a restart nothing.
  - **The hello's boot nonce.** Each hello carries the sender's boot nonce. A peer name seen
    under a nonce it had given up for a newer one (N1, N2, N1) is two processes, and raises
    `redundancy.duplicate-member`. It needs both processes to reach the controller that
    checks, so it detects; it doesn't refuse.
  - **The gap the voters alone leave (#1029).** The rule acts only at a voter that holds a
    running promise to the original. A clone brings its own copy of the original's voter, so
    it needs just one more grant. When the original is partitioned from one voter (the A–B
    link down, or A cut from the witness), that voter's promise to it lapses, and the clone
    wins it after a lease. It then leads alongside the original, and `Duplicates` stays 0.
  - **The fix: the state is bound to its box (#1029, layer 1).** `quorum.state` records the
    fingerprint of the box that wrote it (`node.BoxFingerprint`):
    - **The fingerprint** is a hash of `/etc/machine-id` and the MACs of the interfaces the
      peer and witness links leave by. Other NICs don't count, so a USB NIC plugged in doesn't
      change a box. Either component changed is a changed box: a cloned VM usually keeps its
      machine-id and gets a new MAC.
    - **A file from another box is treated as lost:** moved aside, and the controller starts
      uncommissioned, raising `redundancy.box-changed` with which part changed.
    - **A real change** (a NIC swap, a migration) re-adopts from the healthy peer within
      seconds (#976). **A clone stays uncommissioned for good:** the peer's `Adopt` goes to
      the original's address. That difference is how an operator tells the two apart, and the
      event says so.
    - **When the fingerprint can't be read yet** (a peer or witness address that doesn't
      resolve), the check is never skipped (#1034 review). The controller neither votes nor
      campaigns and leaves its file untouched, re-reading every quarter lease, and starts once
      it can read it. That costs no liveness, since it can't reach its peers without resolving
      them either, and it raises no false "changed". With no machine-id at all, a pair member
      refuses to start.
    - **Never change both boxes at once:** both uncommissioned is the operator's case (B2).
      3c's `recommission` refuses a box-changed controller without `--box-replaced`.
    - **The residual:** a clone that keeps both the machine-id and the peer-link MAC (a
      MAC-spoofed clone).
  - **Layer 2: the voters relay their running promises (#1029).** A MAC-spoofed clone loads
    the original's voter state as its own, so the pair has four voters under three names.
    Layer 2 makes the voter the clone needs hear what another voter promised:
    - **The relay.** Every vote frame (a request or a reply) carries the sender voter's
      running promise **to another node**: the grantee, the boot nonce it was granted under,
      and the time left, stamped as the frame leaves. A promise to its own candidate is never
      relayed (the witness-down election, a clone's self-grant), and a voter relays only its
      own promise, never one it heard.
    - **Two refusals.** A voter refuses a request from X while a heard promise runs that is
      (a) to X under another nonce, or (b) made by X's own voter to someone else. A real X
      never campaigns while its own voter is promised elsewhere (`Candidate.Tick` waits out
      `LeaderSeen`), so (b) refuses only a copy of X. Rule (b) closes the clone of the
      **standby** with the leader cut from the witness, which (a) alone misses.
    - **Timing.** A heard promise is held until `receipt + left·(1 + MaxRho)`: the refusal
      outlasts the original in real time whatever the clocks' rates, and no relay runs past a
      lease. A refusal past `left`, in that margin, isn't counted as a duplicate, so a fast
      real restart raises no false `redundancy.duplicate-member`.
    - **A restarted voter's wait stays one lease** (the #1079 review asked whether it should
      be `lease·(1+MaxRho)`, the longest a heard promise is held):
      - **What it forgets:** only the relays heard before its restart. They come back within its
        wait, since every running relay rides on each probe, every look (`lease/4`), and on
        every campaign and renewal.
      - **Why the margin is smaller than it looks:** the margin a forgotten relay needs is
        the clock-rate gap `ρ·lease` (0.01 by default), not `MaxRho`. `MaxRho` in the hold is
        a conservative cap.
      - **What it would take to lose it:** a forgotten relay that is never heard again needs
        the relaying link to fail after the relay was heard and before the restart. That is a
        third fault, beyond the clone and the voter's restart.
      - **What stretching would cost:** every takeover's `W_r` would grow by a tenth of a
        lease, for that case alone.
    - **Conflicts.** A relay naming a name under another boot than the voter's own running
      promise, or than another voter's relay, is a duplicate's sign and counted. Both boots
      are then live, so step 1 below refuses new grants to both. A voter's newer relay about
      a name replaces its own earlier one.
    - **The standby probes the witness every look** (a term-0 request with id `ProbeID`,
      always refused), in its restart hold too, so the two hear each other's promises while
      no campaign runs. The probe's reply goes to the voter only, never to the candidate or the
      handshake. A controller's voter that has just promised the other controller pushes a
      probe at once (`Voter.RelayChanged`), so the witness hears a new promise within a one-way
      delay rather than a look.
    - **Wire:** vote protocol version 2 (`quorum.WireVersion`; 3 since step 1 below). A frame of another version
      is refused whole. A pair or witness that mixes releases across it can't vote, and says
      so (`redundancy.version-mismatch`, either side, and on the witness's log) rather than
      failing to elect in silence. A release before version 2 answers a new frame with 400
      "bad frame", which the sender reports the same way.
    - **What it costs:** only refusals. A promise to another node never ends early, and the
      restart wait covers a voter that forgot what it heard. A takeover can lose one refused
      campaign at the witness (§4.5, `R`).
  - **Step 1: a duplicated name gets no new grant (#1029).** Layer 2 alone left a race. When
    the lease changes hands while a clone runs (the clone booted with its original, either
    controller restarting, the leader going unhealthy), both copies campaign while neither
    holds a promise to relay. The duplicated voter (a copy of the original's) can then back
    the clone while the original's own voter backs the other controller: {B, old A voter}
    against {clone, W}. The #1079 measure: two leaders in 172, 184, 17 and 227 of 10,000
    runs. Step 1 closes it:
    - **Every voter tracks the boot nonces it hears per name:**
      - from any request (probe, campaign, renewal): live for a lease;
      - from a relayed promise: live until its hold ends.
    - **Two live nonces for a name: no new grant to that name**, either boot, and
      `redundancy.duplicate-member`. A running promise to one of those very boots still
      renews: the incumbent keeps the promises that protect it. Refusing the incumbent too
      was tried, and lets the clone in (87 two-leader runs in 2,000): it drops those
      promises.
    - **A candidate counts no grant from a voter its own voter knows is duplicated**, its
      own included (`NewAdoption` wires `Voter.Duplicated`). Two processes cast that vote.
    - **The relay carries a duplicated name** (`Dup`, `DupLeft`), first-hand only. Hearers
      refuse new grants to it for `DupLeft·(1+MaxRho)`.
    - **The standby probes its peer as well as the witness**, every look. A standby cut
      from the witness would otherwise be heard by nobody, and its clone would be the only
      boot of its name any voter sees.
    - **A term goes to one boot of a name.** A voter never grants a term it granted to one
      boot of a name to another boot of it, even once that promise has run out: the other
      boot asks above it. Otherwise a clone and its original could each hold the same term
      (I4, found in the rich clone schedules).
    - **A voter yields its own round** (`YieldTo`) only to a request it would grant.
    - **Wire: vote protocol version 3.**
    - **Each piece is pinned** by a schedule where it alone stands between the pair and two
      leaders (`quorum/sim/dupname_test.go`).
    - **Dropped as unpinnable:**
      - **A candidate check on its own name relayed under another boot.** The same reply
        always reaches its own voter first, so the discount covers it.
      - **First-heard precedence among relays.** A second boot already refuses new grants
        to both.
      - **#1028's rule** (refuse a name under another nonce while a running promise holds
        it). A promise and the nonce that asked for it are live for the same window, so the
        name refusal covers it whole. Its test now pins `dup_name_ignored`.
      - **The relay rules (a) and (b) are kept,** pinned at the voter, where each still
        refuses a bad grant alone. No clone schedule inside the claim needs them since step
        1.
    - **The measure.** `TestCloneRaceSeeds` covers the four cases, plus rich schedules (a
      clone with one to three of a cut, restart, unhealthy controller, delay or loss). Every
      oracle but I7 is on. 0 two-leader runs in 20,000 per case, and in 200,000 clone
      schedules (`TestCloneSeeds`).
    - **The claim, exactly:** never two leaders while the clone and its original can both
      reach the voters.
    - **What it doesn't close:**
      - **A network split that separates a running clone from its original**, with the
        original beside the witness and the clone beside the peer, or the mirror. Once the
        other boot ages out (a lease), no voter on either side sees two, and each side holds
        two of the three names, since the duplicated voter counts on both sides.
        `TestMeasureCloneSplit` gives two leaders in 2,000 of 2,000 runs of each shape. An
        original cut from both other voters, with the clone leading meanwhile, can also
        come back at a lower term than the clone's (I2): a device that fences by term
        refuses it until its next re-term, a look or so later.
      - **The 3c fix: a sticky confirmed-duplicate mark.** A voter that has seen two boots
        of a name interleaved keeps refusing that name and discounting its vote until an
        operator clears it, so a split after the clone was seen leaves no leader on either
        side. It needs a guard against a false mark from reordered frames (two alternations,
        or N1 after N2 by more than the maximum delay) and a reorder test. It comes first in
        3c, with the operator clear and the spare flow, and lifting `experimental` needs it.
      - **Irreducible: a clone booted on the far side of a split that already exists** is
        never seen beside the original. Only the box check (layer 1) stops it.
      - **One more 3c item:** a restart faster than the vote link's one-way delay can be
        counted as a duplicate once, for a promise granted to its old boot after it died.
        It needs the same evidence rule as the sticky mark.
    - **What it costs:**
      - **No other fault:** a running clone costs redundancy, not the leader. A clone of a
        healthy settled leader costs no takeover: the renewals of its running promises
        continue. Whatever restarts or hangs meanwhile, the pair has a leader within a
        takeover's bound.
      - **A clone plus a network fault** can leave the pair without a leader until the
        clone is stopped or the fault heals: the duplicated controller's vote counts for
        nobody. This happened in 61 of 5,000 clone-and-cut schedules while both lasted.
        Never two leaders.
      - **A plain restart or power cycle** raises no false `duplicate-member` and elects
        within the bound. Two rules keep it quiet:
        - a relayed promise counts a conflict only while it certainly runs
          (`receipt + left − lease/4`);
        - rule (b) counts only a relay from another boot than the request it refuses, since
          one boot's own frames can arrive out of order.
      - **Stopping the clone** brings a real controller back within `cloneStopBound`
        (5.6 leases; worst 2.4 s at a 2 s lease in the simulator).
    - **Step 2 (3c): the clone as a spare.** The incumbent keeps the name, and the newcomer
      demotes itself, durably and silently, on a positive "held by" reply; `recommission`
      turns a spare into a replacement.
- **`NAUTILUS_NODE` set with no pair in the manifest** is refused: each box would run as its
  own leader against the same I/O.
- **A pair member's tag API must be reachable by its peer.** The manifest's default
  `server.addr` is `localhost:8080`, and `naut deploy` sets no address. A member whose tag API
  listens on loopback only, while its `peers:` address is on the network, is refused at start
  with what to set (`server.addr` or `NAUTILUS_ADDR`).
- **With no leader known, a standby refuses writes** (503 "no leader") rather than taking them
  into a store that doesn't scan, and still answers reads (#621's rule for a gate-closed
  lease holder).
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
  its timer. That lease is the longer of its own and the one its state says its last promises
  were made under (§6.2). Its controller **doesn't campaign during that wait either**, because
  without its own vote the campaign can't win. The campaign could still take the witness's
  first grant from a leader that rests on the two controllers (the witness still in its own
  restart wait), and this voter would then refuse that leader's renewals too, so the leader's
  lease would lapse with nobody able to take over (#1021 review). The cost is at most one
  lease of takeover time after a restart.
- **Granting a term.** A candidate asks for term T+1. A voter grants it when T+1 is greater
  than every term it has promised, and its current promise to anyone else has run out. A grant
  promises not to grant to anyone else for `lease`, timed from when the voter **received** the
  request.
- **Renewal** is the same request at the same term, every `lease/4`, marked as a renewal. A
  renewal that changes nothing the voter has persisted is answered without a new fsync.
  A lease just won renews **at once**, not at the next look. The campaign's grants can arrive
  late (a voter's slow fsync), leaving less of the lease than a look. Answering that renewal
  needs no write, and that is safe: the request is for the same term from the same grantee,
  so nothing durable changes, only the in-memory promise end moves later. A voter that
  restarts before persisting anything newer waits a full lease before granting anyone, which
  outlasts any promise extension it gave and didn't persist. This keeps a lease alive while
  fsync stays under about `lease(1−ρ) − 2·RTT` (found by the nightly seeds, #998).
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
  fast brings it back. `TestTargetedOneWayLeaderSkewed` pinned that schedule until the restart
  hold moved its first election (#1021 review); the back-off is now pinned by
  `TestTargetedOneWayLeaderBacksOff` and the T1 tests.
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
  - **The handshake** (`quorum.Adoption`, #976). The two controllers exchange `Adopt` messages
    over the A–B link: a proposal, an ack, and a state message ("this is what I count").
    - **Generations.** Each adoption is an `(generation, incarnation)` pair, and the generation
      only rises. A controller adopts a proposal only above its own generation. Two proposals
      at one generation are concurrent, and the higher incarnation id wins on both sides. A
      late, duplicated or earlier-boot proposal therefore can't move a controller that has moved
      on. Without generations, A could end up counting one copy while B counts another.
    - **Order.** The acceptor adopts and persists, then acks. The proposer adopts only on the
      ack that matches its own proposal, never on a state message, which may be stale. A
      proposer whose proposal is refused learns the other's generation and proposes the other's
      incarnation one generation up.
    - **Persist before send.** Every `Adopt` leaves only once the state it vouches for is
      fsynced, like a grant.
    - **Boots.** Every `Adopt` carries its sender's boot nonce and echoes the nonce of the
      peer's boot it answers. A controller that lost its file takes only messages sent to its
      current boot. A proposal or an ack sent before the loss carries a floor that misses
      terms led since, and would let it lead one of them again (#1008 review). The peer
      counts as heard, and its boot as learnt, only from an `Adopt` that echoes this boot: a
      late one sent before this controller restarted would say the peer is up when it may be
      down. A peer's first message after its own restart echoes no boot of ours; the reply
      echoes its nonce, and its next message echoes ours.
  - **Agreement isn't enough at a switch.** Suppose A leads on grants from itself and the old
    witness, and B's promise to A has lapsed. If the pair switched to a second copy that has
    been up for hours (its restart wait long over), B and that copy would make a majority while
    A's last lease still runs: the voter set changed, so the two majorities need not
    intersect. Three rules close it:
    - **A quiet period.** A controller counts no grant from a newly adopted incarnation for
      one full lease after it has persisted the adoption. Each side stopped counting the old
      incarnation before the other persisted: the proposer when it proposed, the acceptor when
      it adopted, before its ack. So by then every lease granted under the old voter set has
      ended. **A restart owes it again**: a restarted controller counts its adopted
      incarnation only one lease after it starts. It may have adopted just before it went down,
      and the peer may hold a lease that rests on this node's own **pre-restart** grant (with the
      old witness), which its restart wait doesn't cover (#1008 review).
      - **One rule decides it,** at the write: an adoption counts its incarnation at once only
        if the controller was already counting that incarnation (a generation bump that kept
        it), and then a quiet period still owed (a restart's) is not cut short. Every other
        adoption counts it one lease after the write. A proposer counts nothing while its
        proposal is in flight, so re-adopting the **old** incarnation over a pending proposal
        owes the quiet period too: the peer may have adopted the proposed one with its ack
        lost, and may lead on it still. So does a bump that lands on the same write as the
        adoption of a new incarnation.
      - **Nothing counts while a proposal is in flight,** whatever adoption a write makes
        durable: a proposal drops an adoption still waiting for its write (a failed or slow
        one), and a write that lands while a proposal is pending counts nothing. Otherwise a
        failed write of (2, J), then a proposal of (3, I) whose write succeeds, would count J
        while B moves to I with its acks lost: each leads on its own witness (#1014 review).
        The hold-off runs from the last adoption, durable or not.
      - **Heard, not counted.** In its quiet period the counted incarnation's replies show the
        witness hears this controller, so a campaign it refuses is "refused by a majority" and
        retries on a later look. Its grants never count. Otherwise every restart would back off
        two leases as if unheard.
    - **Forward only.** A controller stops counting the old incarnation, durably, before it
      asks the other to switch. It counts again only on the matching ack. Generations only
      rise. An old incarnation comes back only through a full handshake at a higher
      generation, with its quiet period: the same argument as any adoption. A strict
      never-again list was rejected, because a stray copy adopted once in a double fault would
      retire the real witness for good.
    - **A term floor.** A rebuilt witness has promised term 0. The handshake carries each
      controller's floor: the highest term its voter promised, its candidate has heard of, and
      any witness incarnation said it promised in this boot (a stray copy's only raises it). Both raise their voters (which then
      grant no term at or below it) and their candidates to the higher floor before counting
      the new witness, so no term is granted twice (I4). The rebuilt witness itself needs no
      floor: every majority of the new set includes a controller voter that holds it.
  - **When a switch is proposed.** A commissioned controller proposes a new incarnation only
    while it has heard from the peer within a lease, when that incarnation has answered, and
    when the counted one has been silent for a **hold-off**:
    one lease, doubling with each switch since the controller started, up to 32 leases. A stray
    copy that answers on and off therefore can't make the pair switch every lease, each switch
    costing a lease with no witness. The peer must be heard because a proposer counts no
    witness until the peer acks, and can't safely fall back: the peer may have adopted with
    the ack lost. So a peer that fails after the proposal leaves it with none until the peer
    answers. That is the one-controller-down cost below. Every adoption is an event
    (`redundancy.witness.incarnation`). So is any time two incarnations answer within a lease of
    each other: two witness copies are running, a misconfiguration the operator must fix.
  - **A controller that lost its state file** (wiped, or torn by a crash during an in-place
    write) is **uncommissioned**: it has no adopted incarnation and no floor. Its voter grants
    nothing, and its candidate counts no witness, until it has re-commissioned (#976). Without
    this, two such controllers make a majority on their own and grant terms below one already
    led, so the fencing token goes back.
    - **What it asks.** Its peer is authoritative. It asks the peer what it counts, and re-adopts
      that incarnation one generation up.
    - **When.** Only after it has heard the witness answer at that incarnation, so its floor
      includes the witness's promised term. Every term led before the loss was granted by a
      majority that included the peer or the witness.
    - **Probes.** A standby doesn't campaign under a live leader, so it would never hear the
      witness. Until it is commissioned, it sends the witness a term-0 probe each look, which
      is always refused. The refusal carries the incarnation and the promised term.
    - **Strays.** It never proposes an incarnation of its own, so a stray copy can't move the
      pair. Only the operator's command names one (below).
    - **Both controllers uncommissioned** (both files lost, or a new pair) **wait for the
      operator** (#710 B2). The witness may be intact and still miss terms: B led term 11 on
      {A, B} while the witness, promised 9, was cut off, and both files were lost before a
      renewal reached it. No surviving voter holds 11, so re-commissioning on the witness at
      floor 9 could lead 10 and 11 again. Neither controller proposes, neither takes the other's
      proposal unless an operator made it, and both raise **re-commission by hand**
      (`EventRecommission` naming no incarnation). **This trades away #976's automatic
      recovery** for a two-disk loss (H2 re-commissioned the pair on the witness by itself) for
      a pair that never reuses a term. It also means a **new pair is commissioned by the
      operator**: a fresh install can't be told from two wiped disks.
    - **It gives way.** If its proposal meets one of the peer's at the same or a higher
      generation (a switch the lost controller can't hear), it re-proposes the peer's current
      incarnation above both, and the peer takes that over its own switch. Neither waits on the
      other for ever. The peer may switch later, after its hold-off.
    - **Only the peer's committed incarnation.** Every Adopt carries the sender's committed
      (durably adopted) generation and incarnation. An uncommissioned controller adopts only a
      re-adoption of the incarnation its peer has committed, C: never the peer's switch, and
      never a pending one. **The argument.** Say the lost controller A led term t before the
      loss, on a majority of A and one other voter. If that voter was the peer B, B's voter
      promised t, and B's floor carries it. If it was the incarnation C, only C knows t, and
      A learns it by hearing C answer, which it must before it adopts. If it was another
      incarnation X: A led on X only before B committed C (A counts what B committed, or what
      B pends), and B committed C through A, whose proposal or ack carried A's voter's
      promised term, at least t; so B's floor carries t again. The one exception is X = a
      switch B still pends, which A adopted with its ack lost (below). Otherwise re-adopting
      C, heard, is above every term A led. A switch to a rebuilt witness's J is not: in the
      case where A led on C and C is dead, neither J nor B's floor knows t, and A would grant
      it again (and `oplog` names entries by sequence and term).
    - **The residual** (accepted, naut-lead 2026-11-19). A adopted B's switch to X, the ack was lost, A led on X (B, pending,
      counts nothing), then A's file was lost. If X still answers, A's floor includes X's
      promised term (it counts every incarnation it hears), and refusing X costs liveness only.
      If X died too while C still answers, X was a second copy, and if B has not heard X since
      A led on it (B's floor would carry t), A re-adopts C at a floor that misses t. That is
      two of the voters of term t lost (A's file and X), behind an ack lost and a
      misconfiguration (two witness copies). The rule doesn't catch it, and the simulator's
      two-of-three case doesn't name it (X was never committed by B); `TestKnownGapPendingStray`
      pins it. Closing it would make a lost controller wait whenever its peer pends an
      incarnation it can't hear, at the cost of the give-way path above. Instead it is made
      visible: when B takes an uncommissioned peer's re-adoption over its own pending switch it
      raises `EventReadoptOverSwitch` (`redundancy.witness.readopt-over-switch`), naming both.
    - **If C never answers** it stays uncommissioned, its voter grants nothing, and once it has
      been up a lease it raises **re-commission by hand** (`EventRecommission`, naming C) for
      the operator (§7). The witness may only be unreachable from it, so the alarm says to check
      that first. An incarnation that has answered it never raises the alarm: it can re-adopt
      it.
  - **The costs.**
    - **One controller down:** a rebuilt witness can't be adopted. That is already a two-fault
      case (witness disk and a controller), so the survivor behaves as in §7.
    - **A lost controller file with the witness unreachable:** that controller grants nothing
      until the witness answers. Two faults; the pair behaves as in §7.
    - **Two of the three voters losing their state before either has re-commissioned** (a
      controller's file and the witness's, or both controllers' files, witness intact or
      not): no surviving majority is sure to remember the terms, so a floor taken from the
      survivors can miss terms only the lost voters knew. **It reliably waits for an
      operator** (the committed-incarnation rule, and both-uncommissioned waits, above): the
      lost controllers stay uncommissioned and alarm, and the pair elects nothing until the
      operator re-commissions it by hand (§7). The simulator names the case (a controller's
      file lost, and its peer's committed incarnation dying before it re-commissions; or both
      controllers uncommissioned at once) and excuses it against liveness (I6, I7, steady
      state) only, never I1, I2, I4, the lease or the ledger. Its named cases run the
      operator's command (a `Recommission` fault) and assert the pair recovers after it.
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
   expires, not at expiry: `IsLeader` is false once `remaining() < m_fence`. A driver reads
   the gate, the send budget and the lease's term in one call (`io.SendGate`), so nothing it
   holds from an earlier term of the lease leaves under a later one.
   - `m_fence = d_net + m_send`. `d_net` is an assumed one-way delivery bound for the plant
     network, 50 ms by default. `m_send` bounds the gap between a send path's gate check and
     its send system call under normal scheduling, 20 ms by default. Both are set in the
     manifest (`redundancy: net-delay:` and `send-margin:`). Unset, they scale down with a
     short lease (at most lease/8 and lease/16), so the margin is under a quarter of any lease;
     set, their sum must be under a quarter of the lease, or the manifest is refused: the gate
     would close before each renewal.
   - Each send also runs with a deadline of `remaining() − d_net`, and a send whose deadline
     has passed is not started.
   - **Why the margin is needed.** Any sender that checks and then sends can let one frame
     through after the check (the PROFINET review of #790 confirmed it: at most one frame per
     sending goroutine). With the margin, that frame still leaves while the lease is valid,
     and it arrives before any voter's promise expires.
   - The margin comes out of the time the leader may write. It is not added to takeover time.
3. **Fencing resets sockets.** A write still in flight at its send deadline (unanswered, or
   with bytes still in the kernel's send buffer) has its field socket closed with
   `SO_LINGER` 0. The close sends a reset and discards whatever the kernel still held. The
   instant is that send's deadline (`remaining() − d_net` from its own gate check), not the
   gate's close `m_send` earlier: a byte that left before the deadline arrives before the
   lease ends, so the deadline is the last moment a byte may leave. The reset is a timer armed
   at that deadline, not something done after a read's own timeout returns, so a stalled or
   blocked sender does not delay it. Writes queued in the driver never start after the gate
   closes (rule 1), and never under a later term of the lease than the one they were computed
   in.

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
| Modbus TCP | before each write (`modbus/driver.go`) | send deadline; RST on fence. **Done (3b-3a)**: before every request, including a bit's write after its read-modify-write read, which went out on the read's look; the RST is a timer at the deadline; a reopen, or a look under another term of the lease, forgets what is queued, and a flush that took its queue before stops (#1040 review) |
| EtherNet/IP | before each write (`eip/driver.go`) | send deadline; RST on fence. **Done (3b-3a)**: before every request (a scalar, each struct member), RST through `eip/cip` |
| OPC UA client | in `WriteOutputs` and its flush | check before each write; deadline. **Done (3b-3b-1)**: before every chunk (the first too), each to its send deadline; on a pair the session socket is dialled with SO_LINGER 0 and a fenced write that fails is aborted in place (Linux: connect AF_UNSPEC, since gopcua's Close sends a CloseSecureChannel with no deadline and can block in a partition); a term move is a reopen, which clears pending |
| OptoMMP | looked at per scan, datagram and stream check | check before each datagram; deadline. **Done (3b-3b-1)**: every output write (TCP) to its send deadline, reset by a timer at it and on failure; a term move is a reopen. Session-setup writes (watchdog, stream config) are not outputs and keep the standby rules (#618, #626) |
| Redfish, SNMP, Prometheus (`hw/base.go`) | **once per batch** (`flushWrites`) | check before each command; deadline. **Done (3b-3a)**: a close mid-batch drops the rest, logged once, never put back; each queued command carries the lease's term it was computed in, and one from an earlier term (the lease lost and won again) is dropped, never sent; Redfish's socket is reset at the deadline too (linger 0 set as the request takes its connection, since net/http may close it itself), and a command unanswered by then is "outcome unknown", never sent again (#1040 review) |
| READ/WRITE channels (#699) | **none**: fixed in #797 (#796) | deadline. **Done (3b-3b-2)**: on a pair a write is judged on one read of gate, budget and term, runs on a fenced context to its deadline, and carries the gate, so a send split into requests (Write Tag Fragmented) reads it before each one (`fence.Check` in `cip.SendUnitData`) |
| Sparkplug host NCMD/DCMD | gated (#325) | check before each publish. **Done (3b-3b-1)**: a held or parked command is published only under the lease's term it was computed in (a term move drops them, and what the program last handed over). **Residual:** an NCMD published inside the lease reaches the broker in time, but the broker forwards it to the edge on its own timing, which nothing on this controller can fence; the edge's own comm watchdog covers that hop |
| Sparkplug edge | **none** (#798): every replica publishes | only the leader holds the session; NDEATH on step-down. **Until #798, a pair with `sparkplug:` is refused at load** (3b-3a) |
| PROFINET device (#790) | gate look every 200 ms | check before each RT, DCP and LLDP frame (test plan F33). **Already so** (`Config.MaySend` gates the port and every RPC socket; #790); 3b-3b-2 adds the F33 test, which closes the gate in a running AR with LLDP announcing and finds no frame of any kind after it, and the gate now carries the fence margin (3b-3a) |
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

> `T ≈ L_rem + ρ·lease + C + (RTT + f) + W_r + R + L_op + I + S + O`

| Term | Meaning |
|---|---|
| `L_rem` | time left on the old lease at the fault, from 0 to `lease`; 0 after an orderly `Release` |
| `ρ·lease` | the clock-rate margin |
| `C` | the standby's campaign delay: up to `lease/4` (§3.1) |
| `RTT + f` | one vote round, including each voter's fsync |
| `W_r` | up to one lease more, if a voter has just restarted and is waiting out its timer (§3.1) |
| `R` | up to one campaign refused by a stale relayed promise (§2, #1029 layer 2): the witness may hold the standby's own voter's promise to the dead leader for `MaxRho·lease` past its end, and the retry comes half a look plus the jitter later, after one more vote round trip: at most `MaxRho·lease + 3·lease/8 + RTT` (0.95 s plus a round trip at a 2 s lease). Measured over 300 leader deaths in the simulator, worst takeover with the relay against without: 2.80 s / 2.53 s on 1–5 ms links (`TestStaleRelayEndsAndTheStandbyLeads`), 3.32 s / 2.73 s at 100 ms one way, 5.14 s / 3.90 s at 450 ms (the lease/4 rule's edge) |
| `I` | the inputs wait, up to #786's cap: about 0 when the standby polls, a session connect and first read when it does not. When a frozen old leader still holds a device's **only** session, it lasts until that device's own session timeout, and may hit the cap. |
| `L_op` | a pair's operator-intent log catch-up before the first scan (`SyncLeader`, §1): one round trip to the copies that answer (the old leader's and the witness's, asked at once), plus up to 50 ms waiting for a straggler once a majority answered; up to the log's call timeout (`lease/2`, at least 500 ms) when only a silent copy could make the majority. The runtime gives up at 2 s and runs not synced (it keeps what it applied, `catch-up-failed` on what came from retain). Since the witness is a copy (#710 3b-4b), a takeover after the old leader died syncs from the witness |
| `S` | one scan |
| `O` | the full output set going out |

**Operator writes, not takeover time.** The leader pushes each operator-intent write's commit to
the replicas that stored it before answering, under the log's sequencing lock, so a holder that
stalls delays every write by up to the log's call timeout (`lease/2`). It is not in the takeover
path, and no fix is needed now (#1030 review).

**A named exception: the live I/O re-apply** (#706, #1010, #1013). When the old leader made a
live I/O apply in its last ~2 s, or at a standalone start, the new leader's first scan waits
for the kept I/O configuration to be re-applied: up to `DefaultTakeoverIOWait` (10 s) plus
`ioGrace` (2 s), so 12 s at worst. A standby that followed the store re-applies nothing, so the
wait is about 0 otherwise. A pair refuses live I/O applies until the state stream (its retain is
a file), and also while the peer's build doesn't keep them (the hello's `keeps-live-io`,
#1013). The term matters once step 4 lets a pair keep them.

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
| Both controllers' state files (or a new pair) | Neither re-commissions on its own, even with the witness intact: nobody leads, and both raise "re-commission by hand" until the engineer's command (§3.1, §7). |
| One controller's file plus the witness incarnation its peer committed | The same: it waits for the engineer's command (§7). |
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
  client token itself. The same handshake compares the pair's **credential set** (#708), read
  anew at every hello: the users file's digest (`auth/users.Digest`) and the tag API
  certificate's leaf (`server.CertSource.Pin`), each as its own HMAC under the peer token, so a
  digest never leaves either. Each part is same, differs, unreadable or off, or **unverified** when
  the answer does not prove itself under the peer token (different peer tokens, a reflected or
  replayed answer): then the files are not blamed. A mismatch is a condition on `/api/cluster`
  and the banner, cleared when the check cannot run (the peer silent), and it never carries a
  path or an OS error, since `/api/cluster` answers without a token: those stay in the log. It is **report only**: it never holds a campaign
  or closes the gate, since a users change or a certificate renewal reaches A and B at slightly
  different times.
- **Clients that follow the leader (#716).** `/api/cluster` advertises each controller's API
  address, and a browser HMI that follows it can't send a token.
  - **What has to be trusted.** Each advertised name must be in `NAUTILUS_ALLOWED_HOSTS` on
    both controllers, or advertised as an IP or the floating address.
  - **The warning.** At start, `naut run` warns about any advertised peer name that its own
    `NAUTILUS_ALLOWED_HOSTS` doesn't list, and names the variable.
  - **Other clients.** OPC UA clients aren't affected: the rule is HTTP-only. Scripts and
    `naut` subcommands send `NAUTILUS_TOKEN`, as #846 already makes them.

### 6.2 The vote transport and the state files (`quorum/node`, #710 PR 3a)

`quorum` is sans-IO; `quorum/node` drives it on real sockets, files and clocks, under the
same driver contract as the simulator.

- **Votes are HTTP.** A vote is `POST /quorum/v1/<cluster>/vote` with the binary `Request`
  frame, answered with the `Reply` frame. A handshake message is `POST …/adopt`, answered
  204. The client-token check is `GET …/hello`. Connections are kept alive, and each call has
  a deadline of lease/8. A grant waiting for its voter's fsync is answered when the write
  completes, or not at all.
- **Three headers on every call.**
  - `X-Nautilus-Cluster` carries the peer token, the header `oplog` already uses, so one check
    guards the whole peer port.
  - `X-Nautilus-Members` names the pair's two controllers (below).
  - `X-Nautilus-Lease` carries the sender's lease. A voter refuses a request whose lease is
    longer than its own, because the lease would outlive the promise it rests on. That covers
    two controllers with different `lease:` values, and a witness started with a shorter
    `--lease` than the pair's. The refusal is logged and raised on both sides
    (`redundancy.lease-mismatch`), never silent. A longer witness lease is safe; it only slows
    a takeover.
- **A change of lease across a restart.** A promise can outlive its process. So the state
  file records the longest lease a promise may still run under (`VoterState.Lease`). A voter
  restarted with a shorter lease waits that longer one out before it grants. Until then it
  still accepts requests stating it, so a live leader's renewals are seen. Its first grant
  after the wait records its own lease. A voter restarted with a longer lease than its file
  says writes it before its first grant, even an unchanged renewal. Without these, a witness
  rolled from 4 s to 2 s granted B inside A's 4 s lease, and so did one rolled 2 → 4 → 2 s
  (#1021 review and re-review). The state file's format is version 2 since this field; a
  file of another version reads as unreadable (`ErrVersion`), not as damaged.
- **Pairs never share a voter.** The witness keys each pair by a **cluster ID**: 128 random
  bits drawn when the pair is commissioned (3c), not the project name, so two sites that run
  one project never meet. At its first vote, a cluster is bound to its two controllers, in a
  `<cluster>.members` file made durable before its voter exists. A vote from anyone else, or
  naming other members, is refused.
- **No term jumps.** A request for a term more than 2^40 above what a voter has promised (or
  its floor) is refused, logged, and raised on both sides as `redundancy.term-jump-refused`.
  A token holder or a bug then needs about 16 million forced jumps to push a pair's terms to
  the top of the range. The cap is wide enough that an isolated controller, campaigning every
  few seconds, still rejoins after decades; the first cap, 2^20, was crossed in about 50 days.
  A fresh voter, with nothing promised, takes any first term. That gap, like the cap itself,
  is liveness only. An operator's re-commission floor must stay within 2^39 of the known
  terms.
- **The client-token check** sends HMAC-SHA256, keyed with the peer token, over the direction,
  the asker's and answerer's names, a fresh challenge and the client token.
  - The client token never leaves the process.
  - The challenge keeps an old answer from being replayed.
  - The names and the direction mean an asker's own question, reflected back at it, doesn't
    pass as its peer's answer. A controller answers only its peer.
- **The witness's port** cuts off slow clients (read and header timeouts) and serves at most
  256 connections at once.
- **`Term`** is the last fencing token the controller led under. It only rises, as the
  Kubernetes elector's does; the runtime's channels rely on that. The gate (`IsLeader`) is
  what says whether it leads now.
- **State files.** Each is written whole to a temporary file, fsynced, renamed over the old
  one, and the directory fsynced. A crash at any instant leaves the old state or the new
  one. An exclusive lock keeps a second process off the same file.
  - **On a controller,** `quorum.state` lives in its **state directory**, systemd's
    `StateDirectory` that `naut deploy` uses, beside the retain file. It never lives in the
    project tree: a deploy replaces the project, and every redeploy would then need a
    re-commission. `redundancy: state-dir:` overrides it; without either, a member refuses to
    start, and so does one whose state directory is inside the project's checkout. Every
    `naut deploy` step (install, update, a failed update's rollback, rollback, restore,
    uninstall without `-purge`) leaves the file untouched, and a backup never carries it: an
    old voter state put back would unsay a promise (`TestDeployBoxKeepsTheVoterState`).
  - **On the witness,** `naut witness --state <dir>` (default `$STATE_DIRECTORY`) holds one
    `<cluster>.quorum` and one `<cluster>.members` per pair. A pair with no file gets a new
    incarnation at its first vote.
  - **An unreadable file.** A controller with an unreadable file starts uncommissioned and
    keeps the file aside. The witness refuses to start over an unreadable file unless
    `--rebuild` is given: the file is then kept aside and the pair gets a new incarnation.
- **The boot nonce** is drawn from `crypto/rand` at every start and written nowhere.
- **The state file's format is version 4** since `Box` (version 3 added `Owner` and `Cluster`,
  §2). The driver stamps them on every write: the member, the cluster ID and the box
  fingerprint on a controller; `w`, the pair's cluster ID and no box on the witness. A file another member or pair wrote stops the start
  (`ErrNotOwner`) and is left where it is.
- **Controllers** refuse to start without `CLOCK_BOOTTIME` (Linux). The witness runs anywhere.
- **In `naut run`** (#710 PR 3b-1), a member's peer listener binds its own `peers:` port on
  every address, behind the peer token, with the witness's timeouts and at most 64
  connections. The state file's lock is taken first, so a second process of the same
  controller stops before it binds or votes. The listener closes only after the election's
  last `Release`. A member is a voter even with no leader to elect, so it sends systemd
  `READY=1` after the first-decision timeout (5 s) either way: otherwise `TimeoutStartSec`
  would restart it in a loop, and every restart is a new restart wait.
- **Retained values in a pair** are each controller's own `retain.json` until the state
  stream (§4): a cold takeover loads the new leader's file, which holds what it last saved
  as a leader. Live I/O applies stay refused for a pair (a file store reaches one replica).

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

**Re-commissioning by hand.** When two of the three voters have lost their state before
either re-commissioned (§3.1, the witness incarnation), no majority is sure to remember which
terms were led. Two shapes **wait**, the controllers uncommissioned, their voters granting
nothing, each raising **re-commission by hand**:
- a controller lost its file while the incarnation its peer committed died (the alarm names
  that incarnation; the witness may only be unreachable from it, so the engineer checks that
  first);
- both controllers lost their files, even with the witness intact (the alarm names no
  incarnation). **This gives up #976's automatic recovery from a two-disk loss**: the pair
  stays down until the engineer acts. A **new pair** is in the same state, so commissioning a
  new install is the same command.

The pair elects nobody, and the lone-survivor rules above apply. The engineer re-commissions
it with an audited command, on **either** controller (once per pair), that names the
incarnation to adopt (the witness's) and an **explicit term floor**: one above every term the
pair could have led. No surviving voter is sure to know the last terms, so the floor comes
from the survivors (the peer's term in `/api/cluster`, the last term in any `oplog`, the
witness's promised term) plus a margin far beyond what the plant could have elected while
the state was being lost, e.g. a million: a term costs nothing. A floor at or below what the
controller already knows is refused, so the margin is never forgotten. The floor is written to
the controller's file before anything else happens, then the ordinary handshake adopts the
incarnation, and the peer takes the floor with it (an uncommissioned peer takes only a
proposal the operator made). The permission is held in memory: after a restart the engineer
issues the command again, with a floor above the one the controller now knows (the first
floor stays, and its campaigns may have raised the witness's promise since); the command shows
that known floor. The core call is `quorum.Adoption.Recommission`;
the command, its `redundancy.recommission` record (§7.1) and its role (high-privilege,
safety-critical) come with the runtime wiring (#710 PR 3) and #708/#709. The guide says:
the engineer checks that the devices accept the new leader before trusting the fencing token
again.

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
| Witness | `redundancy.witness.incarnation`, when one is adopted; `redundancy.witness.copies`, when two incarnations answer within a lease; `redundancy.witness.lost`, when a controller must be re-commissioned by hand (the controller, the dead committed generation and incarnation or none, the incarnations heard, the time since boot); `redundancy.recommission`, the engineer's command (the incarnation, the supplied and the effective floor, the dead committed generation and incarnation, the floor's evidence); `redundancy.witness.readopt-over-switch`, a re-adoption taken over a pending switch (the floor may miss terms, §3.1) |
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
