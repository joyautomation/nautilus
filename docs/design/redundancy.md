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
      "operator log read-only: no second counting copy"): entries committed while it lagged
      may be on the lost copies, so only a person can decide (`naut redundancy vouch`, below).
    - **A person accepts a loss (#1029 3c-3c-2a).** `naut redundancy vouch` (log only, on a
      commissioned controller; `POST /api/cluster/vouch`, journalled `redundancy.vouch`)
      prints the picture of the copies (`oplog.Picture`: each copy's answer, whether it
      counts, its last entry, its history) and a 10-hex digest (sha256, error texts left
      out), and refuses what would lose entries unless the run names that digest:
      `--accept-loss` (a silent copy, a later one that does not count, a forked one, or a
      synced copy with no other copy counting: that grants the one-shot), `--accept-lost-log`
      (this copy lost). A different digest refuses and reprints. What was accepted is kept per
      peer (`Mark.Accept`: silent, forked, or later up to (term, seq)); the one-shot catch-up
      overrides a copy only within it ("not what was accepted: naut redundancy vouch
      again"), always takes a counting answer, and is spent with `Alone`; a normal sync drops
      it. An override with EVERY other copy silent is refused (naut-lead (i),
      `override_all_silent`): the new history would sit on one copy, and the other two,
      back at the old one, could commit there and lose it at first contact; alone it commits
      nothing anyway. Stated window: the override answered by the witness, then dead before
      its first heartbeat moved the witness (half a lease).
    - **The log history.** A catch-up that actually overrides a copy starts history N+1
      under a random id (`Mark.Lineage`: number, id, who accepted), in the same mark write
      that spends the vouch, durable before the copy is synced for its term or takes
      anything. Every authenticated answer (catch-up reads, replication) and the hello
      (`<synced><can-sync><may><N>.<id hex>`, only above 0) carry it; `/api/quorum` only
      notes it (no token there). A copy at a lower history does not count, refuses
      replication from it (409 "history") and does not defer or yield to it. One that hears
      a higher history is superseded: a controller's records `SupersedeTo` durably, counts
      for nothing, takes and applies nothing, and at its next start moves the log aside
      (`.superseded-<unix>`, one kept, newest) BEFORE replay and drops the retain store's
      substitutes, acks and shelves (`Mark.DropRetained`, owed until done;
      `Runtime.DropRetainedIntent`). The superseded verdict the runtime and the alarm log ask
      under their own locks is an atomic kept with the mark: a commit's Apply takes those locks
      under the node's (#1177 review). "Vouch again" judges the picture anew (a pending one-shot
      is no reason for a no-op), and a vouch that grants the one-shot makes a leader catch up
      again for its term, so a fork chosen there is resolved at once. A vouch's one-shot and
      acceptance hold for the boot they were given in: a start drops them (the copy stays
      synced), so a spend a crash lost never re-arms an acceptance of a picture that may no
      longer be true; a spend that can't be written is spent in memory and retried. At the learn, leading or not
      (`Options.OnSuperseded`, and every half lease after), the runtime ends every substitute
      it holds (`log-superseded`, `Runtime.EndSupersededSubstitutes`) and the alarm view's
      acks and shelves go, a live engine's too (`AlarmLog.EndSuperseded`): a leader at N that
      learns N+1 (the window above) leads on read-only for control, running none of N, and
      hands over by the yield rule (a synced peer at a higher history counts). A takeover
      before the restart ends them too, restores none from the retain store
      (`runtime.StaleRetained`) and carries no acks or shelves. It restarts itself (exit 75 for `Restart=on-failure`) only
      when it is standby, the other leads synced at that history (its hello), a supervisor
      runs it (`INVOCATION_ID` or `NOTIFY_SOCKET`), and its start did not already move a
      superseded log aside (no loop); else the condition `oplog-superseded` says why. The
      witness's copy supersedes in place (`Options.ResetInPlace`). The same number under
      another id is a **fork** (two overrides, each with the other controller silent, the first never reaching the witness: the window above): neither
      counts the other or supersedes, both raise `oplog-forked`, and a vouch on the chosen
      side treats the forked copies as needing `--accept-loss`, even on a synced copy a
      majority counts (the only such case); its catch-up then starts N+1. Mixed versions: a
      build from before reads as history 0, so at N >= 1 it never counts (fail closed); at 0
      nothing changes. Stated: a replaced or wiped controller joining a pair at N >= 1
      restarts once; after an override with the witness silent, a new leadership before
      the witness synced is alone again (vouch again; no bump).
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
  experimental: true     # required until hot takeover (§4.2) lands
  peers:                 # the same file on both controllers
    a: ctl-a:9450
    b: ctl-b:9450
  witness: witness-host:9460
  lease: 1s              # default; the takeover time it gives is published
  state-dir: /srv/naut   # optional: default $STATE_DIRECTORY (systemd)
  stream-budget: 1MB/s   # the state stream's bytes a second (§4.1)
  stream-link: ctl-a-link:9451   # optional dedicated A–B cable (§6)
```

- **An explicit opt-in until §4.2.** A pair needs `experimental: true`, and Load refuses one
  without it (#1028, naut-lead's call). Since S1b the state stream keeps the leader's retained
  values across a takeover, but the takeover is cold (program state restarts), and `naut run`
  says so at every start. The standing `/api/cluster` warning of the interim is gone: the
  stream's own state raises one when it is not in sync. The key goes when hot takeover lands.
- **Two modes, one section.** `peers:` selects the pair. Without it, `redundancy:` is the
  Kubernetes Lease as before, and `lease:` is the Lease's name. With it, `lease:` is a time
  (200 ms to 1 min), and a name there is refused. `witness:` or `state-dir:` without `peers:`
  is refused rather than ignored.
- **Self.** Both controllers run one build, so the manifest can't say which controller it is.
  `NAUTILUS_NODE=a|b` says so, and `tools/deploy` sets it per box.
- **The cluster ID** (§6.2) is `NAUTILUS_CLUSTER`, beside `NAUTILUS_PEER_TOKEN` in
  `/etc/nautilus/env`, outside the state directory. A controller whose state directory is wiped
  therefore still starts, and re-adopts the pair's witness incarnation from its peer (#976).
  `naut redundancy new-pair` draws it and writes it into each controller's env file (3c-3c);
  the witness keys each pair by it as the controllers first vote, and needs only the peer
  token. A member started without it is refused, with a message that names new-pair.
- **The tag API addresses** that `/api/cluster` and the standby's proxy use are each
  controller's `peers:` host with its own API port. A controller knows its own; the peer's is
  the address the peer announces in its hello answer (`api`, bound by the answer's InfoMAC),
  never this controller's port on the peer's host, since the two ports may differ (#710 demo
  finding 1). Until a verified answer names it, or when it names this controller's own API (a
  standby would proxy to itself), or from a build that announces none, the standby knows no
  leader address: it serves reads and refuses writes, rather than guess. A hello with no
  verified answer is retried after `max(lease, 1 s)`, doubling up to the hello interval
  (`max(16·lease, 5 s)`), and at once when the peer's votes carry a new boot nonce or a vote
  call to it is answered after one that failed, at least one retry after the hello before
  (#1120: with the interval alone, a standby that started before its peer refused writes for up
  to 16 leases). A forward has
  deadlines: dial 2 s (a dial failure is replayed to the next leader), TLS handshake 2 s,
  response headers 2 min (a 502, never replayed). The header deadline stays above the leader's
  own waits (10-15 s per handler; an I/O apply that fails adds up its put-backs), so a write
  the leader commits late is never reported as failed.
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
    - **Wire:** vote protocol version 2 (`quorum.WireVersion`; 3 since step 1, 4 since 3c-1, 5
      since 3c-2, 6 since 3c-3b, 7 since 3c-3c-1b, 8 since #710 S2c-1, §4.2, 9 since #710 d1, §4.7). A frame of another version
      is refused whole. A pair or witness that mixes releases across it can't vote, and says
      so (`redundancy.version-mismatch`, either side, and on the witness's log) rather than
      failing to elect in silence. A release before version 2 answers a new frame with 400
      "bad frame", which the sender reports the same way. A rolling upgrade across a version
      stops control once whatever the order: the old leader's majority and the new one share a
      member, which must restart. The guide's order (the standby, the witness, the former
      leader) makes it the witness, the quickest to start (`TestRollingUpgradeAcrossAWireVersion`;
      every order measured by `TestMeasureRollingUpgrade`). No release has shipped the vote
      protocol yet; from the first that does, a version change must also read the previous
      version's frames, so a rolling upgrade keeps a leader (naut-lead's release rule).
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
      boot of a name to another boot of it, even once that promise has run out and across
      its own restart: the other boot asks above it. The grant's boot nonce is in the state
      file (`VoterState.GrantNonce`, format 5; a format-4 file reads with none, and the
      first renewal under its standing promise writes the nonce). A downgrade below format 5
      is loud: an older controller moves the state aside and starts uncommissioned, and an
      older witness refuses to start without `--rebuild`. Otherwise a
      clone and its original could each hold the same term (I4, found in the rich clone
      schedules; the restart case by the #1094 review).
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
    - **The claim, exactly:** never two leaders while every voter keeps hearing both copies
      of the name. Clone-link cuts are measured, not gated: no pass/fail schedule cuts a
      clone's own links (`richCloneRun` and `TestCloneSeeds` cut only A–B, A–W and B–W).
    - **What it didn't close (closed by 3c-1, below, except where marked irreducible):**
      - **A network split that separates a running clone from its original**, with the
        original beside the witness and the clone beside the peer, or the mirror. Once the
        other boot ages out (a lease), no voter on either side sees two, and each side holds
        two of the three names, since the duplicated voter counts on both sides.
        Before 3c-1, two leaders in 2,000 of 2,000 runs of each shape (`TestCloneSplitNoTwoLeaders` now).
      - **The overlap can also start when a cut heals** (#1094 review). A voter forgets a
        boot after a lease of silence (`noteNonce`/`dupLeft`), so the first copy back gets
        a grant. The review's shapes were measured for a clone of A with
        cuts from 8 s + 2–5 leases for 8 leases. Two leaders per 400 runs:
        - {A–B, clone–B, clone–W, B–W}: 29;
        - {A–W, clone–B, clone–W, B–W}: 63;
        - {A–B, A–W, clone–B}: 14;
        - {A–W, clone–B}: 3.

        The sticky mark is the fix for these too. An
        original cut from both other voters, with the clone leading meanwhile, can also
        come back at a lower term than the clone's (I2): a device that fences by term
        refuses it until its next re-term, a look or so later.
      - **Irreducible: a clone booted on the far side of a split that already exists** is
        never seen beside the original. Only the box check (layer 1) stops it.
      - **A restart faster than the vote link's one-way delay** can still be counted as a
        duplicate once, by the relay rule, for a promise granted to its old boot after it
        died (an alarm only; 3c-1 removed the other source, below). Tracked on #616.
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
    - **Step 3c-1: the confirmed-duplicate mark (#1029).** Step 1 forgets a boot after a lease
      of silence, which is what a split looks like, so the splits and heal shapes above gave
      two leaders. A voter now remembers, until an operator clears it, that a name ran twice.
      - **The evidence.** A voter keeps, per name, the boots it heard in that name's OWN
        requests (never a relay: after a real restart the voters relay their promise to the
        old boot for up to a lease), with each boot's first receipt and highest `Seq`. A
        request's `Seq` numbers every request one boot sends, probes included (wire version
        4); request ids can't serve, every probe carries `ProbeID`. A frame is evidence only
        when its `Seq` is new for that boot, so a duplicated or replayed frame (F31) proves
        nothing (while the voter remembers that boot: a voter that restarted, or forgot the
        boot at its cap, takes a late copy as new). Two boots ran at once when EACH sent a
        new frame that arrived more than D = lease/4·(1+ρ) after the other's first frame;
        the voter then **marks** the name. One frame each, not two: the #1113 re-check
        measured that two would leave a split soon after a clone's start with no voter that
        marked it far more often (633 against 45 two-leader runs in 3,600 with the split
        within half a second), and two leaders outrank a false mark (naut-lead, James: when
        in doubt, nobody leads).
        A real restart (a plain one, or `naut deploy`'s, which stops the unit before starting
        it) stops the old process before the new one starts, so every frame of the old boot
        left before the new boot's first did and arrives within the one-way delay of it: the
        old boot never qualifies. Two live copies both keep sending, and both qualify within D
        and a look. Counting alternations instead (k = 2, 3, 4) measured the same, so it
        isn't used.
      - **Why both sides (a false-mark route found before review).** The first version took
        the boot heard FIRST as the older one, and marked when it sent past D after the
        other's first frame. A voter that never heard the old boot (it has just restarted,
        the documented clear, or its link to the controller has just come up) can hear a
        restarted controller's new boot first and then one old frame still in flight, inside
        the delay rule; the new boot then sends on, and that version marked a controller that
        had only restarted. `TestAVoterThatMissedTheOldBootMarksNoRestart` pins it, and
        `TestRestartsBesideAVoterRestartMarkNothing` (a controller restarting beside a voter's
        restart, links at the delay limit) is red once in 300 runs under the one-sided rule
        (`dup_mark_one_sided`). A voter keeps eight boots per name and forgets the one heard from
        longest ago, without banning it: an original cut from the voter while a copy restarted
        over and over comes back as a new boot and is marked (`evicted_boot_banned`).
      - **What a mark does.** The marked name gets no new grant (a running promise to that
        very boot still renews, as in step 1), `Voter.Duplicated` stays true (the candidate
        discounts that voter's grants), and every reply to the name says `Marked`. A
        candidate told so by any of its voters stops campaigning; it keeps probing (how it
        hears the clear) and keeps renewing a lease it holds. The controller says so in its
        hello ("may not campaign"), and a leader never yields (`maybeYield`) to a peer its
        own voter marked.
      - **The two rules of the #1113 review.** Fable found that a single voter restart inside
        a split could still give two leaders when only that voter had marked the name (a clone
        that reaches only the witness, the original cut from it, the witness restarted with
        the copy still running: it forgot the mark, its first unmarked reply freed the clone,
        and it granted it; 115 of 300 on main, 164 of 300 on the PR as first pushed), and a
        second route through the clone's copy of the original's voter granting the peer. The
        simulator delivered the peer's requests to the original only, so it could see
        neither; it now delivers them to a running clone too (`sendPeerVote`).
        - **(i) A hold ends only on the word of the voter boot that set it.** Replies carry the
          voter's boot (`Reply.VoterBoot`); an unmarked reply from another boot of that voter
          (it restarted and forgot) leaves the hold. Then only the held controller's own
          restart ends it (`hold_released_by_any_boot`).
        - **(ii) A held controller's voter makes no new grant to the other controller**
          (renewals of a running promise go on): one of the two copies of that voter must not
          hand the other controller a lease beside the original's (`held_voter_grants`). In
          the simulator the original is held, or discounts its own vote, whenever this
          happens, so no schedule needs the rule alone; it is pinned at the voter.
      - **The alarm** counts on the mark only, once per mark, and follows the voter's mark
        count (`MarkCount`), not the last duplicate's cause: a relay rule can count again in
        the very `Handle` that marked and overwrite it (#1113 review). It is never paced away
        by an earlier alarm: it carries the only word on the clear (`mark_alarm_from_cause`). Two live
        boots are still refused but no longer counted: a fast real restart looks like that
        for a moment, and a copy running beside the original is confirmed within a quarter
        lease and a look anyway. The relay rules (a)/(b) count as before.
      - **A discounted grant never re-terms a leader.** The discount turned a grant into a
        refusal at the leader's own term, which re-termed it every renewal while a voter
        stayed duplicated (every clone race failed the steady-state oracle once the mark
        made that permanent).
      - **The clear (3c-3a).** `naut redundancy clear-duplicate <name> --copy-stopped` on
        either controller: `POST /api/cluster/clear-duplicate`, the `redundancy` permission
        (elevated, administrator; without an `auth:` block `NAUTILUS_TOKEN` only, never the
        same-origin rule), journalled as `redundancy.clear-duplicate` with the operator's
        attestation (`--copy-stopped`) and the principal, answered by the controller itself
        (never proxied to a leader, never the no-leader 503: it is needed exactly when
        nobody leads). `Elector.ClearDuplicateAll` (`quorum/node/clear.go`) asks every
        member over the peer port (`POST …/dup`, behind `Guard`; no `WireVersion` change, an
        older member answers 404) in three phases:
        1. **Check, all or nothing.** Every member must answer, and the boots of the name
           the members heard in the last lease and D, **taken together**
           (`Voter.LiveBoots`, `quorum.CopyLive`), must be at most one. One member alone is
           not enough: an early split can put the copy with one member and its original with
           another, so each hears one boot; clearing the member that hears only the copy
           frees it (the copy's hold rests on that member's mark alone), and it leads beside
           the original. Measured: asking each member alone, 7 of 300 runs gave two leaders
           with the copy running behind an early split (unguarded 7); together, every such
           clear was refused, 0.
        2. **Clear**, carrying the one boot the check heard (`quorum.KeptBoot`) and every
           other boot ANY member knew (`Voter.KnownBoots`, `quorum.UnionBoots`, gathered by the
           check; `checkPhase`). A check that heard no boot at all is refused ("nothing
           running under a to keep"): a clear that keeps nothing has no use, and would retire
           a real controller that is only paused (naut-lead's ruling; `clear_without_keep`).
           Each member refuses if it hears any other boot (so a clear a peer sends without a
           check is guarded too), clears its mark if it has one, and **retires** all those
           boots, marked or not (`Voter.ClearDuplicateKeeping`; `clear_retires_marked_only`,
           `clear_retires_own_only`): a new frame from a retired boot marks the
           name again at once, before any grant (`retiredFrame`). A copy that was alive but
           unheard at the clear (unplugged, paused: a wrong attestation) comes back as the
           same boot. #1135 review: such a copy returning into a split gave two leaders in
           26, 23, 14 of 500 (touch 0, <1 s, <2 s), more than the restart route's 25, 6, 2;
           with retired boots, 0 (`TestACopyUnheardAtTheClearIsMarkedWhenItReturns`,
           `clear_forgets_boots`). The union matters when only one member knew the copy and it
           returns beside the other: 26, 25, 23, 9, 14, 6 of 500 when each member retires only
           its own; 0 with the union (`TestACopyKnownToOneMemberIsRetiredAtAll`). A real
           restart is a new boot and is never retired. **Residual (Fable re-check):** a copy
           PAUSED or with hung scans at the clear, back straight into a split beside the peer:
           4 of 1,000 (pause) and up to 13 (unhealthy) gave two leaders for 36 ms to 0.8 s,
           against 56 and 57 for the restart route. `retiredFrame` fires on the copy's own
           request; until it arrives the peer counts renewal grants from the copy's voter.
           3c-3b closes it: a reply whose `VoterBoot` is a retired boot is not counted and
           marks (below). 3c-3b also makes retired boots durable, with the marks.

           A run after a clear that missed a member (it errored or refused, so it retired
           nothing) is not done (`ClearReport.Done` checks the clear phase), and the check
           lets it run again with no mark left while a boot is still to be retired somewhere
           (`DupAnswer.Retired`; `clear_done_ignores_clear`, `clear_rerun_refused`).
        3. **Re-check a lease later**, in the command's output: a mark back, or two boots
           heard across the members (`CopyLive` again), means a copy runs.

        A member whose voter started under a lease and D ago (`Voter.Settled`) answers
        neither phase, and a witness that has not heard the pair since it started answers
        503: either could only say "heard nothing" (#1135 review: about 10% of checks passed
        that way with a member just restarted; 0 two leaders, the old boot's hold held).

        The held controller is freed without a restart: rule (i) lets the same voter boot's
        unmarked reply release its hold. A member the clear phase can't reach keeps its mark
        (never worse than marks on: 0 two leaders in 300 runs per split shape for one or two
        members cleared, `TestAPartialClearAfterTheCopyStopsIsNeverWorse`), and the command
        says so. Measured on the sim (`TestMeasureClear`, a `Clear` fault per member):
        cleared after the heal, the pair was leaderless 0.0 s on average (0.7 s at most),
        against 3.2 to 4.5 s (5.3 s) for the restart route 3c-3a still had (3c-3b removes
        it: marks are durable).
      - **Durable marks (#1029 3c-3b).** A voter keeps one `DupRecord` per name it marked or
        an operator cleared, in `quorum.state` format 7 (written only while a record exists;
        5 or 6 otherwise, so a pair with no duplicate can still go back a release): marked,
        the boots it knew when it marked (incumbent first, up to 8), the retired boots (up
        to 32), and the name's clear generation.
        - **Told at once, written after.** `Reply.Marked` leaves at once; the record is
          written as a change of its own (`Voter.MarkWrite`, handed out by `Handle`). A
          grant waits only for the grant's state (`need`), never for a record write: the
          prototype that held renewals behind it lost the incumbent's lease in 300 of 300
          runs with the witness's disk failing across the mark (161 today, and with this
          rule). Telling only once durable was worse in the fsync window (115 against
          today's 22 of 300): a mark lost before its write then also set no hold.
        - **Restored boots are stale** until a new frame (a Seq above the stored one), so
          `HeldBy` names no dead incumbent after a restart, and names the live one once it
          sends. All the boots known at the mark are kept, not just the incumbent:
          `HeldBy` answers only an asker it knows, so without the copy's boot a copy is
          never told it is a spare (early single peer restart: copies made spare 6 of 3,600
          with the incumbent alone, 2,997 with every boot).
        - **The hold rule.** A hold ends on an unmarked word from the boot that marked
          (rule (i)), or from any boot of that voter whose `Reply.ClearGen` is above the
          generation the hold was set under. Only an operator's clear raises a name's
          generation, and the clear sets every member to one above every member's and
          every hold's (`DupAnswer.Gen`, `HoldGen`; `checkPhase`). So a voter that forgot
          the mark (crashed before its write, wiped, rebuilt, restored from a backup, or a
          copy booted from an older disk) can't end a hold, and every clear does, also
          with one member's generation behind (`TestAClearFreesAHoldWhenAMemberIsRestoredFromABackup`,
          `TestAHoldEndsOnlyOnALaterClear`, `TestAHoldAVoterForgotEndsOnTheClear`). The
          prototype's bare any-boot release for a durable mark (approved shape) measured
          the same in every family; the generation replaces it because it also frees a
          hold whose mark was lost before its write, which rule (i) alone left to the held
          controller's restart.
        - **Retired-boot replies.** A reply whose `VoterBoot` is a boot this controller's
          own voter retired for that voter's name counts for nothing and marks the name
          (`Voter.RetiredReply`). `VoterBoot` is the controller's boot nonce in the node;
          the sim now uses it too (it used a separate id). Copy paused, hung or with hung
          scans at the clear, back into a split (`TestMeasurePausedAtTheClear`, 1,000 per
          cell): 4 (pause) and 13 and 2 (hung scans) without the rule, 0 with it
          (`TestACopyPausedOrHungAtTheClearIsMarkedWhenItReturns`).
        - **Measured** (`TestMeasureDurable`, 300 seeds per shape): two-leader counts in
          the restart families equal today's, and are all the unmarked early-cut residual.
          Today's restart families make the original a spare in 307 to 348 of 3,600 early
          triples and 309 of 3,600 early peer and witness doubles (a forgotten mark,
          re-marked after the heal on whichever boot is heard first); kept marks: 0 or 1.
          The fsync window (disk slowed to 0.3 to 1.5 s, both voters crashing inside it):
          two leaders in 13, 20, 19, 24 of 300 against 22, 22, 29, 24 with marks not kept,
          originals made spare 15, 56, 15, 48 against 57, 57, 48, 48. A wiped member
          forgets, as before (309 of 3,600 originals made spare with the peer and witness
          wiped early, either way). The renewal cell (a failing disk across the mark):
          161 of 300 incumbent leases broken, as today (the pre-existing gap), not the 300
          a mark write on the grant path gave. With a clear after a voter restart and the
          other controller then dead (`clearlive`): never without a leader (rule (i) alone
          left the held controller out in 1,051 to 1,506 of 1,800 in the prototype).
        - **Costs.** A false mark is permanent until cleared (no restart clears); the
          steady hold (a voter still marking the leader at the end) in 64 to 181 of 3,600
          late double and triple restarts where the original restarts (today 0 to 85). A
          member that ever marked or cleared a name keeps its record (generation, retired
          boots) and writes format 7 from then on, also after the clear; an earlier release
          refuses that file (`newerStateMsg` names the way on: move it aside, re-adopt).
        - **Review (#1144, Fable).** A hold can outlive every member's mark (the marking voter
          crashed before its write, or was wiped): the check counts the held controller as
          marked (`DupAnswer.Held`, `clear_ignores_held`), and the clear's generation frees
          it; no member knows the copy's boot then, so none is retired, and the command
          says so. A retired boot's reply marks only when matched to a round of this boot
          (`retired_reply_unmatched`). A hold's generation is the highest this candidate saw
          from that voter, never lowered (`hold_gen_reset_on_boot`): a restored older disk
          can't end a hold a wiped boot set, and `HoldGen` covers every generation seen, so
          a hold set between the check and the clear phase still ends on the clear
          (`hold_gen_ignores_seen`); the re-check reports a controller still held as not
          done (`done_ignores_held`). A failed spare write hands out a later write
          without the flag (`spare_failure_unwritten`), so a record write in flight can't
          leave the disk on a spare the controller doesn't know it is. **Residual, stated:**
          a restored boot's stored Seq is the one at the mark, so after a restart a frame
          the dead incumbent sent between the mark and the restart, replayed or late, makes
          it fresh for half a lease; making the real box a spare that way needs such frames
          at both other voters for a lease.
        - **Rules (i) and (ii) since durable marks.** A restart no longer forgets a mark, so
          the old restart families no longer exercise them (the full runner found
          `hold_released_by_any_boot`, `held_voter_grants`, `marked_not_told` and
          `marked_still_campaigns` masked there). They still matter in the fsync window, the
          one place a mark is lost: `TestTheFsyncWindowKeepsRulesIAndII` (both voters crash
          inside a slowed write; 44 two-leader runs of 600, 93 without rule (i), 71 without
          rule (ii)) is their sim pin, beside the unit tests.
      - **The measure.** `TestCloneSplitNoTwoLeaders`: the six shapes above, 0 two-leader
        runs (200 per shape in CI, 2,000 measured; before, 2,000/2,000 for both splits and
        126, 306, 58, 13 of 2,000 for the heal shapes). The "mark" oracle (no mark in a run
        without a clone) runs in every seed suite: 0 in 200,000 default seeds. Without the
        `Seq` filter, 2 in 20,000 marked a restarted controller on a duplicated old frame.
      - **A restart inside a split** forgets that member's marks.
        `TestCloneSplitSingleRestarts`: every split shape (the six, the heal shapes mirrored
        to a clone of B, and the review's clone that reaches only one voter) × each member
        restarting once (the original, the clone, the peer, the witness), with the peer's
        requests reaching the clone: 0 two-leader runs in 48,000 (1,000 per cell; 100 in
        CI). Two members restarting (`TestMeasureCloneSplitDoubleRestarts`, every pair):
        0 in 21,600, measured, not claimed. The claim is **no single restart gives two
        leaders**; the clear's order (copy first) keeps the double out.
      - **What it costs, stated:**
        - **During a split with a clone, usually nobody leads**: 0% of the split led in both
          splits and the two heal shapes that cut B–W (99% and 94% in the other two); never
          two.
        - **Once a member that marked a controller restarts, for any reason** (`naut deploy`
          included), that controller stays held until it is itself restarted.
        - **After a mark, a witness outage or a cut leaves no leader until it is cleared**:
          the leader that remains needs both unmarked voters.
        - A real controller leads again once the cut has healed, the clone is gone and the
          marks are cleared (100% of the time after it, in every shape).
      - **Detection window (a stated residual).** A split that starts before any voter has
        heard both copies send past the delay leaves nothing marked, as a clone booted beyond
        an existing split does. `TestMeasureCloneEarlyCut` (every split shape, no restart, a
        2 s lease): two leaders in 52, 21 and 1 of 3,600 with the split 0–0.5 s, 0.5–1 s and
        1–1.5 s after the clone's start, 0 from 1.5 s on.
      - **False marks:** a new frame of a dead boot delayed past lease/4 (outside the delay rule;
        Seq stops a copy or replay of a frame the voter already had, not one it never got), or
        two processes of one member overlapping for longer than that. Either costs that
        controller's redundancy until cleared, never the leader. `TestDeployRestartsMarkNothing`
        restarts both controllers and the witness as `naut deploy` does.
    - **Step 3c-2: the clone as a spare (#1029).** A mark holds the copy but leaves it
      running beside the original, probing and held, until an operator acts. Now the copy
      that is not the incumbent demotes itself to a **spare**: durable, silent, and never
      automatic in the other direction (`recommission --from-spare` turns a spare into a
      replacement, 3c-3).
      - **The incumbent (voter).** Every reply to a marked name carries `HeldBy` (wire version
        5): of the boots of that name the voter heard before the mark, the one heard FIRST
        among those that sent a new frame (a `Seq` above its last) within lease/2; 0 when the
        name is unmarked, is the voter's own, or no boot is fresh. After the mark the voter
        keeps following the known boots' frames (no new boot is added, none is marked). A
        fresh frame and not a live nonce: a nonce stays live for a lease after its process
        stopped, so the original's restart after a spare was named would find its dead boot
        named over it (1 of 300 demoted both with "live", 0 with "fresh"; `held_by_stale`).
        Preferring the boot holding the voter's running promise first was measured too:
        identical counts in every family, so it is not used. Two rules from the #1133 review:
        a refusal names the incumbent as of the frame it answers (a marked standby gets only
        refusals, and a lossy original's frames can be half a lease apart: naming it before
        its own frame demoted the original in 2 to 121 of 300 runs at 30 to 80% loss, and
        both copies in 3; now 0; `held_by_before_frame`), and a voter names no incumbent to a
        boot it first heard after the mark (an original restarted 1.5 to 3 s after a copy
        started was told the copy holds the name and went spare in 300, 300, 288, 196 of 300;
        now 0, it stays held or the copy goes spare; `held_by_names_to_unknown`).
      - **The decision (copy).** A candidate counts only the two OTHER voters' words on replies
        to its own boot (its own voter may be the copy's). It demotes when BOTH name the same
        other boot, each for a full lease without a break, and never while it holds a lease
        (a guard: it never fires in the simulator; `spare_while_leading`). Both, never one:
        with one voter's word, late frames of a real restart's old boot (the 3c-1 false-mark
        residual) gave false spares in 0, 0, 0, 0, 73, 198, 300, 299 of 300 at delays of 0.25
        to 2.5 leases; with both, 0 at every delay (`spare_one_voter`). It also keeps a
        partitioned original from being demoted by the one voter it still reaches. A voter's
        word is its latest: one cut off since keeps its word, as a mark's hold does.
      - **Durable, then silent.** The flag (`VoterState.Spare`, state format 6, written only
        for a spare so a member that is not one still writes format 5 and can go back to an
        earlier release) is written first, and rides every later write. Only once the write
        is durable does the controller go silent: no look (no campaign, probe or renewal), no
        answer to votes, adopts or hellos (503, as a stopped controller), no hello of its own,
        no operator-intent log replication either way, no re-commission (`ErrSpare`), its gate
        closed and no leader named (so its tag API refuses writes). A crash before the write
        leaves the held copy, which decides again; a failed write the same
        (`spare_flag_after_silence`), and the flag is taken back in memory so no later write
        carries it unasked (`spare_flag_kept_after_failure`). A copy that restarts before its
        flag landed (or any time after the mark) is a boot the voters first heard after the
        mark: they name it nothing, so it stays the held copy (3c-1), never a spare. The flag is read at start before the box check (a
        copy's box differs, and the check would treat the file as lost:
        `spare_forgotten_on_box_change`), and `redundancy.spare` is raised at the decision
        and at every start. A state file of a format above the release's own is refused at
        start and left in place, with the fix named (`newer_state_moved_aside`). Releases
        before this one can't do that: they set a format-6 file aside and start
        uncommissioned, and only the vote protocol's version change keeps them from voting
        with a newer pair.
      - **The measure** (`TestMeasureSpare`, 2 s lease; the PR has the table). A clone beside
        a settled pair, with the original or its peer restarting or the leader's scans
        hanging: the clone went spare in 300 of 300 runs of each, 3.4 s after it started on
        average, 4.0 s at most, the original never (`TestACloneBesideASettledPairBecomesASpare`).
        Never both copies spare, never two leaders, in any family; the early-cut residual
        unchanged (20 and 7 of 1,200, as without the spare). Late frames of a restart, eight
        delays: 0 spares in 2,400 (`TestLateFramesOfARestartMakeNoSpare`). The incumbent
        restarting or dying while the spare runs: the spare stays, the original is never
        demoted. A voter restarting between the mark and the decision, under a cut: the
        decision may not come (608 of 1,200 went spare), never the original.
      - **What it costs, stated:**
        - **The incumbent dies while the spare runs**: the pair runs on the other controller
          and the witness alone until an operator acts (redundancy lost, not the leader).
        - **After a spare, one leader on two voters until the marks are cleared**: the copy
          goes quiet before a split can take the original's lease, so the original keeps
          leading, and the peer that marked it never re-grants it (76 to 112 of 200 runs per
          split shape end that way; the steady oracle accepts exactly that, `spareCost`: a voter
          that marked the leader's name and has not read the mark back since a restart). One
          more fault then leaves no leader. The clear-duplicate command brings every shape
          back to a full steady state with no mark left (`TestASplitWithASpareClearsByTheCommand`;
          before 3c-3b the restart clear did, leaderless 3.2 to 4.5 s on average and 5.3 s
          at most across it).
        - **Incumbent = who ran first, not which box is real.** A copy booted with its
          original, while the original was down, or heard before the original's restart within
          about a second of the copy's start (before the mark), may keep the name: the original
          went spare in 87 of 300 same-time boots, 42 of 300 rich races, and 300, 257, 3 of 300
          restarting 0.25, 0.5, 1 s after the copy (0 from 1.5 s).
        - **Late frames to both voters** (the restarted controller's uplink holding its old
          boot's frames more than about 1.25 leases, five times the delay rule): both voters
          can name the old boot, and the only copy goes spare: 0 of 300 up to a lease late,
          then 24, 150, 299 of 300 at 2.5, 3, 4 s (`TestLateFramesToBothVotersUpToALeaseMakeNoSpare`
          gates the first part). The other controller leads; that box is silent until an
          operator acts. The realistic source of a dead boot's frame that late is the old
          process's kernel send buffer, which a graceful close keeps retransmitting through a
          link outage; the elector dials its peer connections with SO_LINGER 0 (`resetOnClose`,
          `peer_dial_lingers`, `TestPeerConnectionsCloseWithAReset`), so an exiting process's
          unsent frames die with it, and the residual needs more than a lease of delay inside
          the network. The simulator has no kernel and was not re-measured for it. The way out: stop the copy, reset the
          real box (stop naut, delete `quorum.state`, start: it re-adopts from the peer), then
          the operator's clear (the rig's `TestTheRealBoxMadeASpareRecoversWithTheCommand`).
          Restarts no longer clear marks (#1029 3c-3b). Since 3c-3c,
          `recommission --from-spare` does it in one command (below).
    - **Step 3c-3c: commissioning, and the spare's way back (#1029).** The operator's
      commands that make a pair and remake a member (`naut redundancy`, `quorum/node/commission.go`,
      routes `POST /api/cluster/commission` and `/api/cluster/recommission`, the `redundancy`
      permission, journalled `redundancy.commission` and `redundancy.recommission` with the
      cluster ID and each flag named). Each asks the other members over the peer port first:
      the clear's `/dup` route takes a third op, `state` (no wire bump; an older release
      answers 400 and is named as one).
      - **`new-pair`** (offline) draws the cluster ID and a 32-byte peer token and writes
        `a.env`, `b.env`, `witness.env` at mode 0600, never over a file; it prints the paths
        and what is not secret, the token only with `--print-secrets` (naut-lead).
      - **`commission`** refuses unless every member answers and is settled and neither
        controller is commissioned or holds a floor (one under way). Terms don't tell a new
        pair: the witness promises terms to uncommissioned campaigns before any
        commissioning (9 in the loopback test). Nor can the witness tell a new pair from one
        whose two controllers both lost their state (B2, below): the floor is a margin (1000,
        far under `MaxTermJump/2`) above every term the three report, and a term the two
        controllers granted each other without the witness is lost with both files, which
        no command can know. `commission_unchecked`.
      - **`recommission`**: the floor's evidence is every answering member's highest term
        (promise, floor, known floor), not only this controller's, which knows little after
        a lost file (`recommission_local_floor_only`). Without `--floor` it prints the
        evidence and a floor above it and changes nothing; a member that did not answer is
        named. The witness must answer; `--incarnation` must be its current one. A
        box-changed controller needs `--box-replaced`, refused in the Elector, not only the
        CLI (`ErrBoxChanged`, `box_replaced_unchecked`).
      - **`recommission --from-spare --copy-stopped`**, on the spare: the operator states the
        boot the voters held under the name is stopped (journalled), and both other voters
        (the peer's and the witness's, not the spare's own silent one: `from_spare_own_view`,
        `from_spare_one_voter`) must answer, be settled, and hear **no** boot of the name
        within a lease and D, stricter than "the incumbent's boot unheard" and needing no
        incumbent nonce, which a spare's file doesn't keep; the voters' records name it, and
        the command prints and journals it. Then both clear the name **keeping no boot**:
        every boot either knew is retired and the generation goes above both (naut-lead's
        ruling lifts #1135's "nothing to keep" refusal on this path only; the
        clear-duplicate check still refuses it). Each re-checks its own voter at the clear as
        the clear-duplicate command's members do. Last, the spare's `quorum.state` is moved
        aside through its state file (`State.MoveAside`: renamed, directory fsynced, every
        later write refused, `from_spare_write_after_move`) and it stays silent until the
        operator restarts naut: a fresh, uncommissioned boot that re-adopts from the peer
        (#976), unknown to every voter, so unheld. An in-process re-initialisation was not
        built: it would be new code on the safety path where a restart reuses the start-up
        one. Every order fails closed: a clear that missed a voter moves nothing (run it
        again); a spare never restarted stays silent. The spare's only messages are these
        `/dup` requests, which carry no boot. It refuses unless the peer is a commissioned
        member (it re-adopts from it; with the peer uncommissioned too, the spare's file may
        hold the pair's only terms, `from_spare_peer_unchecked`), and it clears the peer
        first and the witness only if the peer cleared: a voter that refuses heard the name
        since the check, and a clear at the other would mark that process there for nothing
        (`from_spare_clears_past_refusal`); a clear that missed a voter moves nothing aside
        (`from_spare_moves_after_partial`).
      - **An uncommissioned controller looks at most every lease/8** (#1157 review). Its look
        always sends the peer a query `Adopt` and the witness a term-0 probe, and every answer
        poked the next look at once: two uncommissioned controllers (a new pair waiting for
        `commission`, or both files lost) ping-ponged some 30,000 requests a second each
        (40,000 sockets in TIME_WAIT per loopback test, a likely source of loopback-timing
        flakes in CI). It holds no gate and grants nothing, so pacing costs adoption latency
        only (`uncommissioned_looks_unpaced`, `TestANewPairWaitingForCommissionIsQuiet`). The marks its own voter held on other names go
        aside with its file (3c-3b's wiped-voter case). Its operator-intent log is left as
        it is: a spare is either the real box (its own log, silent since: it catches up as
        after an outage) or a MAC-spoofed copy (a copy of that log, from the clone's
        start); a copy on another box has another log identity and its log is kept aside.
      - **The operator's "stopped" wrong** (simulator `FromSpare` fault,
        `TestFromSpareNeverMakesTwoLeaders`, `TestMeasureFromSpare`): the incumbent cut off,
        paused or frozen at the command returns with a retired boot; switched off and back
        on, it returns as a fresh boot with its old state beside the replacement; hung, it is
        still heard and the command refuses (every run). The real box or the copy as the
        spare, returning with every link up or into #1135's split shape (the incumbent with
        one voter, the replacement with the other, the peer cut from the witness; at the
        heal, after a touch, or into a split two leases old): 0 two leaders (numbers in the
        PR), and with the incumbent really off the replacement takes over when its peer dies,
        in every run. The split cells reach the retired boots (`clear_forgets_boots` turns
        cut and pause cells red).
      - **The incumbent switched back on straight into a split: the adoption-generation
        fence (3c-3c-1b, a MUST before `experimental` lifted).** A fresh boot with its old
        disk, never heard beside the replacement, made two leaders in every such run before
        1b, often in the same term (Fable on #1157), so no term floor separates the two. The
        adoption generation does: `--from-spare`'s last phase, after both clears, **fences**
        the name at the peer, then at the witness at the peer's value (`Voter.Fence`,
        `DupRecord.Fence`, state format 8, written only with a fence; `fence_volatile`): the
        lowest generation of the name's state either takes, `max(carried, Gen + 2,
        PendingGen + 2, previous fence + 1)` at the peer. No state of the name from before the
        command reaches it: a commissioned one adopted only after the peer did, or as the
        receiver of the peer's proposal with its Ack lost (still pending at the peer:
        `fence_peer_own_gen_only`); an uncommissioned one (a lost file) may hold a durable
        re-adoption proposal of its own at most one above the peer's adoption when made
        (#1164 review B3, `fence_plus_one`), or, an earlier replacement dead re-adopting, at
        most the earlier fence (`fence_ignores_previous`). Every request carries the sender's
        committed generation and every reply the voter's (wire 7: `Request.Gen`, `Reply.Gen`,
        `Reply.Fence`, `Adopt.Fence`, and each frame's `Fences`; a mixed-version pair is
        refused by the existing version check). Below its name's fence a frame is refused and
        otherwise ignored, at any term: no grant, no boot heard from a commissioned old state
        (no duplicate mark of the replacement), no relayed promise, no yield, no word that the peer is up
        (`fence_requests_off`; the witness at the peer's value: `fence_witness_unset`); the
        peer's candidate counts no reply from it (`fence_replies_off`). The peer's handshake
        drops, **unanswered**, an Adopt from a commissioned state below the fence
        (`fence_adopt_off`) and a non-zero proposal below it from any state
        (`fenced_propose_answered`): answered, an old disk would re-propose above it. The
        replacement's query is answered with the fence (`Adopt.Fence`), and the witness's
        fenced refusal of its probes carries it too: it re-adopts at it or above
        (`Adoption.ProposeAtLeast`, `fence_unproposed`). A proposal is taken only by the boot
        it addresses, so the old box beside the peer can't adopt the peer's proposal meant
        for the replacement (`propose_any_echo`); the proposer learns the boot from the State
        that refuses it and re-sends at once, once per peer boot (`propose_resent_at_look`;
        the witness-switch families measured unchanged, `TestMeasureSwitch`). **The fence
        survives one member's file loss** (#1164 review B1, B2; naut-lead's ruling (ii)):
        frames carry the voter's fences, and **only a voter that lost its state learns them**
        (`Voter.LearnFences`, `fence_not_learned`; only on the pair's names, `fence_learns_any_name`;
        a controller sets its own fence from its own adoption, never a carried value,
        `fence_takes_carried`): a controller while uncommissioned, before
        the reply that makes the witness heard lets it take the peer's State, and the witness
        for a name it keeps no record of (a rebuilt witness, which waits out its quiet period
        before the pair counts it, re-teaches a peer wiped later). A witness the command
        reached keeps the name's clear record, so an incomplete fence does not spread
        (ruling C). A clear or a fence is answered only once it is on disk
        (`dup_answered_before_write`), and a member fences only a pair controller's name, a
        controller only its peer's (`fence_any_name`). The old box learns it is fenced from
        any refusal, a probe's too, raises `redundancy.superseded` once per boot ("this box's
        state is from before a was replaced (recommission --from-spare): it can no longer
        vote ... Wipe its state only with the network whole ... or keep it off";
        `Status.Superseded`), and campaigns no more while the witness's latest word says it
        is fenced, or no witness word came and another voter's did
        (`fenced_still_campaigns`: else it moves its own voter's terms each look).
        Commission and recommission adopt at or above every member's fence
        (`MemberState.Fence`, `Adoption.ProposeAtLeast`, `recommission_under_fence`): a
        both-lost recommission after a `--from-spare` would otherwise restart at generation 1
        and stay fenced at the witness. **Stated cost (naut-lead's ruling C):** a fence that
        reached the peer but not the witness ends the command INCOMPLETE ("the peer is
        fenced, the witness is not: run the command again", exit 1), nothing moved aside;
        until it runs again the incumbent is refused at the peer and leads only with the
        witness, which it does once the peer dies (`fence_hold_ignores_witness` leaves no
        leader then). A fence is never lifted, only passed by the pair's generation. **Stated
        boundary (naut-lead's ruling on #1164):** an old disk that is uncommissioned with no
        proposal in flight (wiped, on the alarm's advice too, or a lost file) is
        indistinguishable from the replacement: it is a copy of the name like any other. A
        fenced voter still hears an uncommissioned sender's boot (`Request.Uncommissioned`,
        `fence_hides_lookalike`; the #1164 re-check's N1: ignored whole, a wiped old box that
        reached the witness alone, then the peer alone, re-adopted at the fence beside the
        peer and led while the replacement led with the witness, 300/300 before, 0 now), so
        a voter that hears it beside the replacement marks the name (3c-1). Since 3c-3c-2b (D1) the old box's adoption probe carries its sequence (a voter notes
        a boot only from a frame with Seq > 0, so before it no voter ever counted the wiped box
        as a second boot: `adopt_probe_unseq`) and reaches the peer too, as a standby's probe
        does (`adopt_probe_witness_only`). A commissioned controller takes an uncommissioned
        peer boot's re-adoption only while its own voter hears no other live boot of that name
        (rule P, `peer_takes_beside_live`), and recommission refuses while the peer or the
        witness hears another live boot of its name (`recommission_beside_live`). The #1164
        final look's shape (the box at the witness alone, then the peer's side of a split: two
        leaders in every run from 1/2 to 3/4 lease, 489/1000 at one lease, and 1000/1000 down
        to 1/8 lease with the split starting as the box moves), with the replacement leading or
        not, and its mirror (the replacement cut from the witness: up to 1000/1000 at 1/4
        lease) are 0 in 1000 runs per cell (`TestAWipedOldBoxBesideItsReplacementIsMarked`, 96
        cells, the real box or the copy as the spare; the simulator's peer link reaches both
        boxes of the name, `PeerLinkReachesEveryBox`, since the old box may hold the address). P's cost: a controller that
        lost its file re-adopts only once its previous boot has aged out of the peer's live
        boots, p50 1.27 s and p99 2.51 s after it is back at a 2 s lease (0.26 s and 0.29 s
        before), and is never marked (500 runs, `TestALostStateRestartReadoptsWithinTwoLeasesUnmarked`).
        **Stated D1 boundary (naut-lead's ruling on 3c-3c-2b, in place of "a lease and a
        quarter"):** the replacement leads on the witness's promise, cut from the peer; the
        wiped old box reaches the witness and the peer for under half a lease, then a split
        leaves it with the peer, the peer cut from the witness. Within D a restart and a second
        boot look alike to a voter (the duplicate rule's delay, the window of a clone whose split
        follows its start by under D, #1113): the witness marks the name, but the replacement's
        running promise keeps renewing there, and the box's vote at the peer comes from a voter
        that heard the replacement too briefly to object. Two leaders in up to 147 and 79 of 1000
        runs at 1/8 and 1/4 lease (1000 and up to 1000 before), 0 from half a lease (2 s lease;
        `TestMeasureD1`, the `wLead` cells). The alarm and the guide still say wipe it only with
        the network whole, or keep it off. Stated too: a witness restored from an older file at the same
        incarnation, which keeps a record of the name but not its newest fence, does not learn
        it (the #1164 re-check's S7): it takes only an earlier `--from-spare`'s fence, and the
        old box of a later one can lead beside it in a split. Restoring a voter's state from an old
        file is unsupported (the guide: a backup never contains `quorum.state`); a witness
        whose state is lost is rebuilt (a new incarnation learns the fences from the
        controllers). Numbers: every
        `TestMeasureFromSpare` cell 0 two leaders (the eight power-into-split cells were
        200/200), also with both voters restarted before the old box returns, the peer wiped
        after the command, the witness rebuilt then the peer wiped, the incumbent dead
        re-adopting, and the old box wiped while off. The simulator gives the copy the original's
        address once `--from-spare` makes it the member (`tookAddress`: the peer's Adopts
        reach it, as its votes already reach a clone); the rig runs the real box as the spare
        end to end, the copy switched back on after (`TestTheRealBoxMadeASpareComesBackWithFromSpare`).
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
  without one, and `tools/deploy` installs it on all three boxes. Every peer call runs TLS
  keyed by it (§6.1, peer TLS). How peer traffic meets the server's Host rule is §6.1.

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

Each renewal tells the voters how far the standby is in sync: the `(term, seq)` of the last
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
| Alarm notifiers (mail, chat) | run from scans, so leader only | unchanged; not drained before a switchover's RESIGN (below): across a switchover a page may be sent twice, never lost |
| PLCnext (`plcnext`, `project_full.go`) | **none**: flushes its output image from its own loop | **refused under `redundancy:` for now**, by load and `naut check` (#832). Its `poll()` flushes every scan on the shm route, because that write is the shim's watchdog heartbeat, and on change over gRPC. Gating it is later work. |
| Floating IP | (new) | §8 |

**Writes cut at the deadline are counted (#710).** A write whose send deadline passed while it
was on the wire (its socket reset by the timer or by the failure, its outcome unknown) is
recorded once, by device, in `fence.Cut`: Modbus TCP and EtherNet/IP in the transport that
resets the socket, OptoMMP and OPC UA client where they reset it, the `hw/base.go` drivers
(Redfish, SNMP, Prometheus) where the failed send's deadline is seen, and READ/WRITE channels in
the CIP transport, under the channel's name (`fence.WithDevice`), only for a request on the wire.
A write refused because the gate was closed, whose deadline had passed before it started
(`fence.ErrNotStarted`), or that failed before anything was sent (a dial), is not a cut, nor is a
failure before the deadline. About the last five minutes (15 s buckets, fixed memory per device) are in
`leader.Status.WritesCut`, so `/api/cluster` and `naut redundancy status` name the devices too
slow for the budget. The Sparkplug host is not counted: it drops on a term change and has its
own `WriteDrops`. Device names are the manifest's source or driver id, `driver/id` where the
project has several drivers.

The test plan's I3 oracle checks every row.

**The drain before a planned switchover's RESIGN (#710 d1b).** RESIGN removes the
lease-and-fence gap that lets a send which passed the gate just before it closed finish in
time, so before RESIGN leaves, every such send must have ended (`fence.Drain`):

- **Counted from before the read.** A send is counted (`fence.Enter`, inside `fence.Begin`)
  *before* the gate read it is judged on, until it has ended (answered, failed or reset). After
  the gate closes, every send that can still leave read it open earlier, so it was counted
  earlier; the drain waits until nothing is counted. Counted after the read, a send that read
  the gate open just before it closed would be missed.
- **Cut short, not waited out.** While the drain runs, every armed send is reset at once (as at
  its deadline), a send armed later is reset as it is armed, every fenced context is cancelled,
  and a send not yet started does not start (`fence.Started`). A hung device cannot stretch the
  drain to a lease.
- **Settled.** The drain then waits the fence margin (`m_send + d_net`) after the last send that
  went out: its bytes have arrived.
- **Unproven is a failover.** A drain that has not finished within half a lease sends no
  RESIGN: the leader has stopped leading all the same, and its lease runs out at the voters as
  in a failover (`node.ErrDrainUnproven`). Its own voter keeps its promise until then too
  (§4.7), so no standby leads before that lease's end, and a send still to leave on its send
  deadline goes within it. A drain that ends after that lease (the resigner stalled) frees
  nothing and sends no RESIGN either: the promise may back a new campaign by then.
- **A drain's reset is no write cut.** A send the drain cuts short is not counted in
  `fence.Cut`: its deadline had not passed.

| Path | How it is drained |
|---|---|
| Modbus TCP and RTU, EtherNet/IP, OPC UA client, OptoMMP, Redfish, SNMP, Prometheus, READ/WRITE channels | counted by `fence.Begin` before each request's gate read; ended when the request returns (after its socket's reset, where it failed), on every path that drops it after `Begin` |
| The takeover hold's safe values (#710 (b2)) | queued as commands, so drained as the driver's writes |
| Sparkplug host NCMD/DCMD | counted before `commandsAllowed` reads the gate; a publish whose wait timed out stays counted until paho's token completes, since it may still leave from paho's queue |
| Sparkplug edge, PROFINET device, PLCnext | refused at load under `redundancy:` (#798, #787 step 6, #832): nothing to drain. A path that lifts its refusal must count its sends first |
| Alarm notifiers | not drained (naut-lead, 2026-11-24): a page is not a field output, and a send runs to a mail or chat server's timeout, which would break the switchover's bound. Across a switchover a page may be sent twice, never lost: escalation carries across a takeover (#715 PR 1, `TestTakeoverResendLabel`, `TestTermChangeNoDuplicate`; switchover-shaped, a page leaving as the gate shuts: `TestSwitchoverPageSentTwiceNeverLost`) |
| Sparkplug host NCMD Node Control/Rebirth | not counted: any host may ask for a rebirth (Sparkplug 3.0 §5), and it commands no field device |
| SNMP, and a send whose gate has no lease (`budget < 0`) | counted, but bounded only by their own timeouts (a UDP datagram has no socket to reset): a slow one makes the drain unproven, and the switchover a failover |
| OPC UA server writes, HTTP API writes | inbound commands, not scan outputs; refused on a standby as before. d1c's switching state refuses them on the leader too (`ErrSwitching`) |
| IO-Link ISDU parameter writes (an operator's, through the API) | a write to the device, so it goes through the master driver's write gate (d1c: `opcua.Driver.CallFenced`, counted by `fence.Begin` like a write): refused on a standby by the driver as well as the API, drained before a RESIGN, and refused while switching. Reads stay ungated |

## 4. The state stream and hot takeover

### 4.1 Contents and transport

The leader streams **images** of its state to the standby (#710 S1; rulings 2026-11-22):

- **What an image holds.**
  - **The retain section**: `retain.State` as JSON, the same state the leader's retain saver
    writes (`Runtime.RetainImage`).
  - **Every task's program frame**: every variable, function-block internals (timers,
    counters, edges), SFC steps and their times. An SFC lowers to ST, so its steps are frame
    slots. The frame is flattened by `ir.FlattenInto` into 32-byte `ir.Leaf` values.
  - **The carried tags** (S2a; naut-lead's ruling 2026-11-23): every tag the field does not
    provide, whoever writes it, a program or an operator: state, setpoint and memory tags,
    retained or not. Not the inputs (they come back from the field), not the outputs (the
    first scan recomputes them), not the substitute companions the runtime owns. So a hot
    takeover keeps a mode an operator switched on the HMI even when it is not retained; what
    must survive a **cold** takeover still belongs in retain.
    - The tags a task's programs write go **with its frame**, copied from the store right
      after that scan's commit, so a counter in the frame and the tag it set are of one scan.
    - The others are copied from the store when the image is taken. A retained one is left
      out there: the retain section, taken at the same moment, already carries it.
    - A forced tag goes as its **actual** value: forces are not carried (§4.2).
    - A tag that holds a function block (an FB bound as a global, or a STRUCT or ARRAY of
      one) is not carried, nor a dt-tag (the runtime writes it each scan).
    - An image waits for every task's scan, so a slow task paces the images: an operator's
      write to a tag that task also writes reaches the standby with that task's next copy.
    - The set is fixed when the runtime starts. Its **tags layout** (each tag's name and
      declared type, `ir.TagsLayout`) is in the image, and a standby applies the tags only
      when its own is the same, all or none.
  - Alarm ack and shelve state is **not** taken from an image: it comes from the pair's
    `oplog` (§1).
- **Where the copy happens.** Each task copies its frame at the **end of its own scan**,
  once the stream asks (`Runtime.RequestImage`). The copy is values only, into a buffer it
  reuses, with no allocation, hashing or encoding in the scan (guard (a)).
  - The stream's goroutine collects the copies (`TakeImage`), adds the retain section, and
    encodes them.
  - So the frames of two tasks are not of one instant, as their scans never are.
  - Measured (i9-14900K): about 4 ns a leaf, so a 32 000-leaf frame adds about 120 µs to the
    scan that copies. The shipped examples hold 0–210 leaves, and their images are 0.1–2 kB.
- **Header.** Each image carries:
  - its format version;
  - its place in the stream, `(term, seq)`: `seq` only grows within a leadership (a program's
    scan count does not: an online edit resets it);
  - the leader's build;
  - per task: the frame's **layout**, the hash of the **source** it ran (S2a: the layout
    leaves bodies out), the timers' time base `NowMs` at the copy, and the scan count. The layout (`ir.Program.Layout`) is a hash of every slot's name, kind, binding,
    RETAIN/CONSTANT/TEMP flags and declared type (`LREAL` is not `REAL`), walked into every
    STRUCT, ARRAY, enumeration and FB type, plus `ir.FrameFormat`. Initial values and bodies are
    not in it, so a hot takeover (S2) also requires the same build and running sources.
  - A CRC-32C over the whole image.

  A timer's internals are absolute on the leader's `NowMs` (its wall clock), so a hot takeover
  rebases them by the image age (§4.2). Each built-in FB lists the slots that hold such a time
  (`FBDef.TimeBase`: TON and TP's start, TOF's stop, PID's last call), and
  `ir.RebaseFrame` moves them, into every nested FB and array of FBs; an SFC's step times are
  hidden TONs. A PID's is cleared rather than moved, so its first scan on the new leader has
  dt 0 rather than integrating across the takeover (ruling Q4).
  `TestTimeBaseCoversEveryClockSlot` runs every registered FB on two clocks and fails on a
  slot that keeps the clock without being listed.
- **Format.** The standby's stream request names the formats it reads
  (`X-Nautilus-Image-Formats: 1,2`), and the leader sends the newest both read. Format 2 adds
  the sources and the carried tags; a standby of a build before S2 names none and gets format
  1, so a pair mid rolling update still carries its retained values. A format 1 image is never
  taken over hot, and the status says the leader sends no tags.
- **Pace.** Full images, at most one every 100 ms, are sent on connect and after any gap.
  - A bytes-per-second budget (default 1 MB/s, configurable) stretches the interval rather
    than dropping images. The status reports the effective interval and the image age.
  - `naut check` warns when a project's image size × 10/s is over the budget.
  - Deltas come only if the measured sizes call for them.
  - An image is at most `runtime.MaxImageBytes` (8 MiB), with no string over 1 MiB in it. The
    encoder refuses a bigger one (`ErrImageTooBig`, which the stream reports) rather than send
    what no standby would read, and the decoder reads nothing bigger.
- **What the standby applies.**
  - It takes an image only when its own voter vouches for the term (`quorum.ImageTermOK`,
    S1b): the voter sees the peer holding a live lease; the term is above the last term this
    controller led and not below the newest it already took (its shadow's, or the one its
    retain file records: a leader's own saves record its term); and the term is at most the
    highest term the peer asked this voter at. A bound, not an equality: the image carries
    the fencing term (epoch), and the vote term the voter sees moves above it on a re-term.
    It rests on every new epoch being above every earlier one (two majorities share a voter
    whose promises only rise; `TestImageTermBoundAcrossADeposedLeader`). A deposed leader's
    open stream gets images through only while the standby's voter still sees its lease, all
    of them of its own epoch and sent while its gate was open; the only later leader is the
    standby itself, which stops following before it takes over.
  - It applies images only in `(term, seq)` order (`Shadow.Apply` refuses an older one too),
    and its retain file never goes back to an earlier `(term, seq)` (`CarryRetained`).
  - It applies each task's frame to a **shadow** frame of its own program (never run), and
    only when the layout matches. A mismatch leaves that task's shadow as it was and is
    reported.
  - It persists the retain section **by name, whatever the layouts**, to its own retain file,
    on the leader's 2 s cadence, when it changed, through the runtime's retain saver (the
    file's one writer). So a rolling update, which is a takeover across builds,
    keeps the setpoints.
- **What the standby carries, and what it keeps.** `retain.Carry` decides; a test fails when
  `retain.State` gains a field that is neither:
  - **Carried** from the image: retained tags, the parameter bookkeeping, `VAR RETAIN` locals,
    online edits (each with the build it was made against), and Web Push subscriptions (sealed
    under the pair's shared key; #722).
  - **Kept** as its own: alarm acks, shelves and flood, and substitutes (the log owns them,
    and taking them from an image could undo a superseded log's drop), and live I/O (refused
    for a pair).
- **At a takeover.** The new leader:
  1. stops following;
  2. flushes the newest image's retain section to its file (one fsync, counted in the
     takeover time);
  3. empties its shadow (`Shadow.Reset`): an image from before its own leadership must never
     be applied over what it did since;
  4. loads, as §4.2 and §4.3 say.

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

**The voters' word (S2c-1, #710).** The image's own checks can't see a leadership the standby
never heard of: A leads, its stream to B stops, A's lease lapses and A leads again in a later
epoch, then dies, and B's image is of the first epoch (naut-lead's ruling B on S2b). So the
voters vouch too:

- **Epoch records.** Each voter records, per name, the highest epoch it granted in a campaign
  (never a renewal, a re-term included), in the write the grant already makes, so it costs no
  extra fsync and the grant leaves only once it is durable. `RecordsFrom` says from which term
  the records cover every grant: a fresh or rebuilt state covers nothing until its first
  campaign grant; a file with grants but no records covers what came after its `Promised`; a
  commissioned controller's voter covers what came after its adoption floor (an earlier life's
  leaderships are at or below it). A voter covers an image's epoch `e` when `RecordsFrom ≤ e+1`.
  The state file is format 9 only once records are written.
- **The in-sync mark.** The standby POSTs `(epoch, seq)` for each image it applied whole. The
  leader keeps the highest of its own epoch, never above the last image it sent, and resets at
  every takeover. Current is seq-based: at most one image behind the last sent, the oldest
  unacknowledged one out no longer than 3 measured send-to-send times. A time-only rule went
  out of sync whenever a slow task stretched the capture. Every renewal carries the leader's
  epoch and the mark (wire version 8); a voter keeps the mark of a renewal it granted, once
  the grant is durable, in memory only (unknown after a restart until the next granted renewal).
- **The rule** (`quorum.HotCheck.Verdict`), on the replies of the round that won, never an
  earlier round's: the replies that cover the image's epoch must be a majority, and at least
  two of them must hold a mark of the old leader (a restarted voter has none, and one voter's
  word may be stale: the old leader's last lease rested on a renewal a majority granted, so two
  always include one that heard it); this controller led in no epoch after the image's but the
  current one (`LedBefore`; a granting voter's record for this controller is always the winning
  term, so the per-reply own-record check only fires from a refusing voter); then, from every
  winning reply, covering or not: the highest epoch granted the old leader must be the image's,
  and the newest mark (by epoch, then seq; of two at one position, the one not current) must be
  of the image's epoch, current, and not ahead of the image. Each failure is a named cold
  reason. The leader judges Current on its own sends: in step with the last image only while
  that went out within 3 send-to-send times (a stream that stopped is not current), and the
  send-to-send time is never measured across a reconnect. A delayed older renewal never
  replaces a newer mark from the same boot.
- **Checked by** a simulator oracle (a hot verdict never skips an epoch someone led in between)
  and the pair test of the ruling-B sequence.
- **Cold cases left:** a takeover within about `lease/4` of a takeover, or within about a lease
  and a quarter of a voter's restart (no mark yet), a voter on an older release, a rebuilt
  witness until it grants a campaign, and an image whose leadership began before the witness's
  current incarnation was adopted (`witness-rebuilt`, below).
- **Residuals (Fable, #1345).** The floor-derived `RecordsFrom` inherits the adoption's own
  residuals: an operator's `Recommission` with a floor set too low, and
  `EventReadoptOverSwitch`. A rebuilt witness's first campaign grant may reach it before the
  adoption that raises the controllers' floors, setting its `RecordsFrom` at or below a
  leadership its old incarnation granted. **Closed (#710 S3):** the witness's records speak
  only for its own incarnation, and an earlier one may have granted any term up to the floor of
  the adoption that made the current one count. So the check (`HotCheck.recordsFrom`) never lets
  the witness's reply cover an epoch at or below the controller's own voter's term floor, which
  that adoption raised and which only rises; it is read when the check is made, so a later
  rise (a recommission) errs cold. A `RecordsFrom` of 0 stays 0. The hot verdict and the
  hand-back floor read coverage only through it. **Where it mattered:** on the hot verdict the
  S3 history was already cold through the marks rule, since a rebuilt witness's marks come from
  renewals it granted, all of the later leadership (`no-mark` or `mark-other-epoch`); there the
  floor only names the reason (`witness-rebuilt`). The hand-back floor has no marks rule, and
  took the witness's word for a floor below the later leadership: that is the path this
  closes (`TestHandBackFloorAfterAWitnessRebuild`). The cost: a leadership that began before
  the witness was re-adopted gets no coverage from it, so a failover before the next election
  is cold unless both controllers' voters elect the standby (`witness-rebuilt`, or
  `records-unknown` when the witness's records started above that leadership anyway). The
  simulator's oracle checks epochs only: it sets every mark current, and it does not run the
  hand-back, so the unit and candidate tests carry this.

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

**The bad-retain hand-back (S2c-3, #710).** A cold takeover whose own retain store cannot be
read asks the peer first, as the edge's gate after the flush and the store's first read
(before the inputs wait), so nothing has loaded, saved, scanned or written. The peer's file
is good when it reads, holds a stream position, and that position's term is at or past the
newest earlier leadership a covering majority of the winning round records. That is the
peer's epoch record, this controller's records of its EARLIER boots, and this boot's own
leaderships. A campaign's grant overwrites the asker's record, so each record also keeps
the boot of its last grant and the highest epoch granted to an earlier boot (state format
10). A reply reports that earlier-boot value as the asker's own, with no wire change: a
voter of the S2c-1 build reports the asker's current epoch, which refuses the hand-back.
A record is a grant, not a leadership (a split vote's lost campaign raises one voter's record),
so with all three voters answering and covering, the floor is each name's second-highest
record. The hot check (§4.2) does not read the earlier-boot record: above the image's epoch
it can only be a campaign that never led. A good peer gets the lead through a hold of 3 leases (no renewal, no campaign, gate shut).
A controller hands back at most once per max(20 leases, 1 min), on the wall clock, kept in
its voter state (a future time is ignored).

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

### 4.6 The takeover hold (#710 (b2))

`redundancy.takeover-hold` (opt-in) holds a new leader's outputs after a takeover until the
program sets its release tag. Rulings (naut-lead, 2026-11-23): at `max` the hold **stays held**,
with an alarm repeated every `max`; only the program's release tag or the audited
`naut redundancy release-hold` releases it. Releasing at `max` is the explicit per-hold opt-in
`on-max: release`. `max` defaults to 5 s, at most 1 min.

- **Where it sits.** In the runtime, not in the write gate. A closed gate is a standby: it stops
  keep-alives, and the `safe:` values must go out through the same drivers. The gate stays the
  leader's; the hold is a second condition on what the scan sends.
- **What it stops.** The main scan's output push (the only `WriteOutputs` call) is skipped and
  `outSent` stays false, so the release's push is the whole set (#600/#606). A channel `WRITE`
  posted while it holds, or due while it holds, is refused (`ERROR`). At its start the drivers
  that re-send on their own forget what they sent (`io.OutputsForgetter`: Modbus, EtherNet/IP),
  as they already do on a term move. PROFINET is refused under a pair (§3.4). Alarm notifiers
  are **not** stopped: alarms describe the process, and during a hold its inputs are live.
- **Safe values.** `io.SafeWriter` writes them on their own and reports per tag whether each
  landed (Modbus: a write the device confirmed). The runtime writes them on the hold's first scan
  and again whenever the driver asks for its outputs. One not delivered within 2 s raises an
  alarm. Load refuses `safe:` on a driver without it.
- **When it starts.** On a takeover (`TakeoverKnower`), never a start: cold, or every takeover
  with `on: always`, or a hot takeover from an image whose leader was holding (image format 3
  carries the hold's age; an inherited hold keeps it). The release tag is set FALSE at the start.
- **The release tag.** A BOOL state tag, not retained, bound to no driver (Load). The tag store
  refuses every write and force on it from outside the program.
- **Fable round 1** (folded): the pair's hold alarms read only the hold's own lock and the
  elector's gate, never `leadMu` (they run under the alarm engine's lock, which a takeover takes
  under `leadMu`); hold events reach the journal from a goroutine of their own, in order; a push
  that raced a hold's start is forgotten at once; a hot takeover from a holding leader holds
  whatever the coordinator calls it; `on-max: release` keeps its alarm raised a while after.

### 4.7 Planned switchover (#710 d1)

`naut redundancy switchover` hands the lead to a ready standby faster than a failover, hot and
bumpless. Without help a planned handover is no faster than a failover: the standby waits out
the voters' promises to the old leader. Four PRs: d1a the vote protocol's RESIGN (this
section), d1b the drain of every output path, d1c the runtime's switching state, the final
image, the command and the bench, d1d the hand-back's end of the resign hold and the simulator's
follow-ups. The plan and rulings are in #710 (naut-lead, 2026-11-23 and 2026-11-24).

- **RESIGN** (vote protocol 9: a request's `Resign` flag, with its epoch, boot nonce, a Seq
  above every renewal, and the standby's acknowledgement of the final image as its mark). The
  leader first stops leading (`quorum.ResignAt`) and its gate closes. Its own voter keeps its
  promise to it, and does not yield it, until the outputs are drained (d1b, below): sends the
  lease let start may still be on the wire, and a standby whose own voter heard none of the
  leader's renewals could otherwise lead beside them with that freed promise (Fable, d1b). Once
  the drain is proven, its own voter drops the promise (`quorum.ResignDrained`, in one call, as
  `YieldTo`), and only then does RESIGN leave. A drain that is never proven leaves the promise
  to run out where the lease would have ended: a failover. It goes
  to the **witness first**, and to the peer's voter on the witness's answer (or lease/8): the
  standby campaigns as soon as its own voter takes it, and a campaign that reached the witness
  before the witness's RESIGN would be refused, its retry a look away.
- **A voter takes it** only from the boot it promised, for the epoch it knows that boot leads
  in (its record of the boot's campaign, or the epoch of the boot's last renewal it granted: a
  standby that started after the leader's campaign holds no record of it), while the promise
  runs, and never after a campaign or renewal of that boot with a higher Seq (a later
  leadership whose first renewal here still waits on its write has not moved the epoch it knows)
  (`Voter.handleResign`). It drops the promise, the renewals it saw from that boot
  (`LeaderSeen`), and the promises it heard relayed to that boot; it ignores new relays to that
  boot for a lease and a quarter, or until it grants that boot again (one made before another
  voter took the RESIGN), and refuses
  renewals of that leadership for good (one in flight when the RESIGN left). It keeps the
  RESIGN's mark as the leader's, so the takeover is judged hot on the final image. A duplicate
  is answered as taken and changes nothing (nor does its controller act on it: an old copy must
  not make a controller that just resigned campaign); anything else is refused and changes
  nothing. The Seq gate carries the safety: a voter that heard a later campaign or renewal of
  the boot refuses an older RESIGN. One that heard nothing of a later leadership may take it,
  which is safe (no promise of its rests on that leadership) and costs a needless campaign.
  Nothing is written to disk: a restarted voter grants nothing until its promises ran out.
- **The resigner's own voter** does the same for relays to its own boot: the standby's voter
  granted its renewals and relayed that promise, which would refuse the standby's campaign there
  (`heardRefuses` rule (b)).
- **The standby** campaigns at once when its voter takes its peer's RESIGN (its next look moves
  to now), and its own resign hold, if any, ends: the peer is handing the lead back.
- **The old leader** holds off campaigning for two leases (`quorum.ResignHold`). If the standby
  never wins, it leads again after that, cold, in a new epoch. Its open rounds go with the
  resign, so a late grant of a renewal sent before it never makes it lead again, even once the
  hold has ended early. A lost RESIGN falls back to the
  lease running out, no worse than before. The hold is not "may not lead" in a bad-retain
  hand-back (S2c-3): a controller that says yes to its peer's hand-back ends its own hold
  (`Elector.EndResignHoldFor`, d1d), so it leads again without waiting the two leases out:
  only for an ask of a leadership after the one it resigned (a stale ask keeps the hold, with a
  log line), and never before its resign's drain is proven (`Candidate.EndResignHold` refuses
  then: `ResignDrained` relies on the hold to keep the resigner from campaigning in that window;
  Fable on d1d found two leaders behind a stale ask without it).
- **The drain (d1b).** RESIGN removes the lease-and-fence gap that lets a send which passed the
  gate just before it closed finish, so `Elector.Resign` runs the steps itself, in order: the
  gate shuts (`ResignAt`), the outputs are drained (§3.4, "The drain before a planned
  switchover's RESIGN"), and only then does RESIGN leave. `quorum/node` knows no driver, so the
  drain is handed in (`leader.Quorum.Resign` hands it `fence.Drain`); a drain that does not
  finish within half a lease sends no RESIGN, and the switchover becomes a failover. Alarm pages
  are not drained: across a switchover a page may be sent twice, never lost.
- **The command (d1c, `cmd/naut/pairswitch.go`).** `naut redundancy switchover` (POST
  `/api/cluster/switchover`, audited `redundancy.switchover`, the redundancy permission; a
  standby forwards it to the leader) runs, in order:
  1. *Refuse*, saying why, unless this controller leads; its standby is current by the
     acknowledgement rule (PEER_READY's predicate); no force is held; no takeover hold holds;
     `hot-max-age` is not 0; and the standby, asked (`<stream>/switch-ask`), says it may take
     over: it may lead (`MayLead`), is not in its hand-back guard, has applied this leadership's
     images, and its field I/O is healthy (no field driver in error, offline, connecting or
     degraded; its drivers' inputs live). Since d2 the leader also compares (§3.2's I/O-health
     rule, `ioNotReached`): every field driver connected on the leader must be connected on the
     standby, whose switch-ask answer carries its driver rows; a standby row `waiting` (a
     no-session driver the standby cannot see) is skipped. The refusal names the drivers. Vote protocol 9 needs no check: a frame
     of another version is refused outright, so a leader holding a lease has wire-9 voters.
  2. *Stop* (`runtime.BeginSwitch`). From here writes, forces, substitutes, SFC commands and
     online edits from outside the programs are refused with `ErrSwitching` (HTTP 503 with
     Retry-After, OPC UA `BadInvalidState`). Store changes are checked under the store's lock
     and frame changes under their program's lock, the locks the final image copies under, so a
     change either is in the final image or is refused. Each task finishes the scan it is in, its logic and
     its output push, then scans no more: its loop keeps ticking, so liveness sees it alive and
     its lease is renewed. The wait is bounded by lease/4. A gate that closes for a tick and
     reopens in the same term (a late renewal) does not end the switching state: the scans stay
     stopped, and the switchover goes on. It ends at an abort (`EndSwitch`) or at this
     controller's next leadership's takeover.
  3. *Final image* (`runtime.FinalImage`): one off-scan copy of every task's frame and the tags,
     as the last completed scans left them, numbered after every streamed image. The stream
     sends it at once (a wake cuts its interval short; the open round of end-of-scan copies is
     closed first, so no image is numbered after it) and the leader waits, at most lease/4, for
     the standby's acknowledgement of exactly that image. **S1b restated:** images come only
     from the end of a scan while the gate is open, except this one, which is still taken and
     sent before the gate shuts.
  4. *Resign* (`leader.Quorum.Resign`, above), with the final image's seq as the mark, on a
     context the client's going away cannot cut, and only if the switching state and the
     leadership still hold (else an abort).

  **The point of no return is the call to Resign**, not the first RESIGN: `ResignAt` shuts the
  gate before anything else. Before the call, a refusal or a timeout ends the switching state
  (`EndSwitch`) and this controller leads on in the same leadership, nothing lost (outcome
  `aborted`, HTTP 409). After it, this controller has stopped leading whatever comes back: a
  drain that does not finish sends no RESIGN and the lease runs out (outcome `failover`, HTTP
  502). The old leader stands down and follows its peer as any standby does. The report gives
  each step's time.
- **The standby's side (d1e).** Measured on the bench, most of the standby's time between its
  election and its first scan was tick latency: the takeover edge's image flush and
  operator-intent catch-up (each well under a millisecond once waited for in the same tick)
  were each started on one tick and seen done on the next tick of the fastest task. The edge's tick now waits for a step it
  started, up to 15 ms (`sameTickWait`), before leaving it to the next tick as before. The
  flush asks `LeadingTerm` and carries the image into the store while the tick waits under
  `leadMu`, so `LeadingTerm` reads a snapshot published under that lock instead of taking it,
  and the carry does the same while the edge waits for that flush (its store load has not begun
  then; during the catch-up's wait it locks as before). `BeginSwitch` checks the leadership and
  sets its flag under `leadMu`. The tick samples the lease and its term again, lock-free,
  before taking over. While it waits, the
  other loops' ticks answer "do not scan" at once (`edgeWait`) rather than queueing on the
  lock; a tick that took the lock just before still waits, and the waiting loop itself skips
  its slots for the wait (at most two waits: well under liveness's stall bound, at least a
  quarter lease). Nothing scans before the edge is taken either way. What remains is the tick
  on which the gate is first seen open: up to one period of the fastest task. The loops sleep in
  an uninterruptible `clock_nanosleep` on a locked real-time thread, and nothing signals into
  that path to wake them.
- **Tests.** Unit rules with one mutation each (`quorum/resign_test.go`); four switchovers back
  and forth on real elections over HTTP, each within lease/4 and with the order checked
  (`TestLoopbackAResignHandsTheLeadOverAtOnce`); in the simulator, the `Resign` fault under link
  loss, delay, duplication and replay, with the ledger ending a promise at a taken RESIGN, I3's
  resign case (no write under the resigned epoch after the resign), the "resign" oracle (no
  voter takes a RESIGN after receiving a later campaign or renewal of that boot) and the
  "rejoin" oracle (a resigner leads again only after its peer led, its hold ran out, it took
  a fresh RESIGN back, or its hold was ended early by a hand-back, the `EndHold` fault). The
  hold is judged on the resigner's own clock and in its own boot: one whose clock runs fast
  inside ρ, or that restarted, may lead again before two leases of the simulation's time (d1d).
  Fable reviewed it on #1355.
  d1c: the switching state's rules with one mutation each (`runtime/switch_test.go`), the
  final-image acknowledgement (`TestWaitAckTakesOnlyTheFinalImage`), the parameter write's
  gate (`TestCallFencedGoesOnlyThroughAnOpenGate`, `TestISDUWriteGoesThroughTheWriteGate`), and
  the command end to end on a real pair: hot and within a lease, refused, and aborted in the same
  leadership (`TestPairSwitchover*`).

### 4.8 Failback (#710 d2)

Opt-in: `redundancy.failback: {prefer, after, max}` (after default 5m, at least 10 leases, so
a flapping link or box is not failed back into; max default 3; pair only; needs `retain:`).

- **The loop** (`cmd/naut/pairfailback.go`) runs on every member and acts only on a leader
  that is not `prefer`, every lease/4. Its "ready" is the switchover's step-1 refusal set
  (`switchRefusal`, §4.7, including the I/O comparison), unchanged: one predicate for the
  manual and the automatic path, plus one check for a failback only: the standby's
  `failback.prefer` (in its switch-ask answer) must equal the leader's, so a manifest rolled out
  to one controller does not fail back every `after`. Once ready has held for `after` without
  a break (any refusal starts the clock again) the leader runs the d1 switchover itself (`by`
  "failback"), which looks once more, after its own checks, that failback is not suspended. The
  failback is journalled `redundancy.failback` with the readiness time and the switchover's
  outcome and report; an outcome other than done is journalled as an error (the field's
  evidence, should the lost-campaign bug bite unattended).
- **Suspension.** A switchover that starts on `prefer` (an operator's: the loop never runs
  there) suspends failback before the final image, so the image carries it to the controller
  that takes over; a switchover that does not hand over (refused, aborted) puts it back, unless
  an operator changed it meanwhile. `POST /api/cluster/failback {suspend, why}` (audited, the
  redundancy permission, on the leader; `suspend` is required, so a bare POST is refused, not a
  resume) suspends or resumes, and answers only once this leader's retain store holds it (a
  failover in the moment before the next state image still loses it: the standby gets it with
  that image); resume also ends the ping-pong stop and its count.
- **Ping-pong stop.** The non-preferred controller remembers, on its own clock (in memory: a
  restart forgets it), when it last failed back (done, or past the resign as a failover). If it takes the lead again within `after` of that, with failback not
  suspended (a manual switchover away would have suspended it), the preferred controller failed
  over back: the count goes up. At `max` failback suspends itself (`Stopped`), raising the
  system alarm `nautilus.failback-stopped` until resumed. The preferred controller holding the
  lead for `after` resets the count.
- **State** (`retain.FailbackState`: suspended, by, why, at, stopped, count) is a retain section
  the leader saves (`runtime.FailbackKeeper`), carried by `retain.Carry` in every image, so a
  hot or cold takeover and a restart keep it. A load that cannot read the store (and no image to
  carry it) hands the keeper an unknown state, which suspends failback with a warning rather
  than read it as never set (ruling Q2: an operator's suspend must not be lost to a bad disk).
  That includes a takeover whose store failed earlier on the same edge (a corrupt file the
  standby's carry or the edge's I/O read moved aside, which the load then reads as empty):
  `loadFailed` marks it unknown. This is conservative: a read that failed once on the edge and
  then succeeded also suspends failback, as that leadership treats its whole store. Journal entries are queued under the failback's lock and
  written after it (a journal write fsyncs; the lock is taken under the takeover's, the alarm
  engine's and the saver's), and never while a switchover holds the scans stopped. Fable
  reviewed it on #1364: one must-fix (that corrupt-file path), five should-fix, all taken; its
  re-check found the fixes sound and one more should-fix (a journal write while the scans were
  stopped), taken.
- **Status.** `/api/cluster`'s stream block carries `failback` on the leader (prefer, after,
  max, suspended/by/why/at, count, how long the preferred standby has been ready or why it is
  not), and a warning line while suspended.
- **Tests.** Loop rules on a fake pair and clock with one mutation each
  (`cmd/naut/pairfailback_test.go`: the clock's reset, suspension, the ping-pong stop and its
  reset, the manual switchover's suspend and undo, the unknown state); the runtime's keeper
  hand-over (`TestAnUnreadableStoreHandsAnUnknownFailbackState`); `TestCarry` for the image;
  and on the pair rig, the I/O comparison's refusal and a failback end to end (a manual
  switchover away suspends, carried to the other controller; resumed, it fails back hot after
  `after`, the counter unbroken). No bench: a failback is the d1c switchover, timed there.

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
- **Peer TLS (#710), on by default.** Every peer port (both controllers' and the witness's)
  serves TLS 1.3 only, and every peer call (votes, adopt, hello, the `oplog` copies) dials it.
  - **The key is the peer token's.** `internal/peertls` derives an Ed25519 key with
    `pairkey.Derive(token, "peer-tls")` on each box at start; nothing is written to disk,
    distributed or re-pinned when a box is replaced. A listener requires a client certificate,
    and each end accepts the other only if its leaf holds that key (`VerifyConnection`, which
    also runs on a resumed session; the listener issues no tickets anyway). There is no CA,
    no host name and no date check: peers are reached by name or IP (the witness also through
    a floating address), and
    §3's rule against comparing wall clocks across hosts holds (a test runs the two ends
    years apart).
  - **What it buys.** A party without the token can't read peer traffic, can't learn the
    token from it, and can't pass as a member on either end: a relay in the middle has
    another key, so the caller refuses it before a request (or the token) leaves. The token
    header still travels, inside TLS, so the listener's check is unchanged.
  - **What it doesn't.** Anyone with the token holds the key: one leaked env file is still
    every member. Per-box keys pinned at commissioning are the next step (#710 PR 2), and
    `experimental` waits for them. A witness serving several pairs holds one token, so those
    pairs share a key until then.
  - **A short token is refused.** The key's public half is visible to anyone who reaches a
    peer port, so a token could be guessed from it offline: `naut run` and `naut witness`
    refuse one under 32 characters, naming `naut redundancy new-pair` (64 hex digits), and
    `naut check` warns about one in its environment.
  - **Seams.** `quorum` and `oplog` are substrates, so `cmd/naut` builds the configurations
    and hands them down as `tls.Config` (`node.Options.TLS`, `oplog.Options.TLS`). `oplog`
    keeps naming its peers by their `http://` URLs, which its files record (an operator's
    acceptance names peers by them), and only sends them as https.
  - **Mismatches are named on the side that dials:** `redundancy.peer-tls-mismatch` (the
    other protocol at a voter: peer TLS off there, or an older release) and
    `redundancy.peer-identity` (a key that is not the pair's), each per voter on
    `/api/cluster` and the banner, cleared when a call gets through again; a state change is
    recorded only once its event is queued, so a dropped event is raised again at the next
    call. A listener sees a failed handshake only in its error log. The plain-HTTP text never
    suggests turning peer TLS off: what answers may be something else. A plain call reads a
    TLS listener's 400 as "speaks TLS"; a TLS call never does.
  - **Bounded handshakes.** Both transports bound the dial and the TLS handshake (2 s): a
    call's deadline ends the call, not the transport's own handshake, so without the bound a
    peer that accepts and never speaks would hold one connection per call.
  - **Off** (`redundancy.peer-tls: off`, `naut witness --peer-tls=off`) is for a lab bench:
    plain HTTP as before, a warning at every start and a standing one on `/api/cluster`, and
    the guide's trusted-link rule applies.
  - **No mixed mode.** A listener that took both would let anyone downgrade it. So a TLS box
    and a plain one don't vote together, and switching a running pair costs one control stop:
    witness first, then the standby (the leader fences when it stops; about the standby's
    restart plus a lease plus an election), then the old leader. The guide gives the order.
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
- **Peer TLS** (§6.1) wraps every call below; the frames and headers are the same either way.
- **Three headers on every call.**
  - `X-Nautilus-Cluster` carries the peer token, the header `oplog` already uses, so one check
    guards the whole peer port.
  - `X-Nautilus-Members` names the pair's two controllers (below).
  - `X-Nautilus-Lease` carries the sender's lease. A voter refuses a request whose lease is
    longer than its own, because the lease would outlive the promise it rests on. That covers
    two controllers with different `lease:` values, and a witness started with a shorter
    `--lease` than the pair's. The refusal is logged and raised on both sides
    (`redundancy.lease-mismatch`, with the voter it is about and its direction), never silent,
    and cleared once that voter takes the lease again: any answer past its lease check, a
    probe's included, so a standby clears it as well as the leader. The refusal given and the
    refusal received are kept apart, so two controllers with different leases each report once
    and never flap. Every vote answer states the voter's own lease (`X-Nautilus-Voter-Lease`),
    and a dialer clears only when its lease fits it: a voter in its restart wait still takes
    the longer lease it refuses after, which is no all-clear. A controller shows a lease refusal as a condition (a warning, which holds a leader's
    ServiceLevel at 200): of its own lease by the witness, `witness-lease`, worded as the loss of
    fault tolerance it is and naming the fix (`--lease` at least the pair's), plus the system
    alarm `nautilus.witness-lease` in a project with alarms; of its own lease by the peer,
    `lease-mismatch/<peer>`; and its own voter's refusal of the peer's, `lease-refused/<peer>`.
    The refuser shows it too because, while a pair is upgraded across a change of the default
    lease (standby, then leader, witness last), the refused side runs the old release, which
    shows nothing. A longer witness lease is safe; it only slows a takeover.
  - The default lease is 1 s for the pair and the witness alike (it was 2 s): measured on
    loopback and in a 30-minute soak on two ARM controllers (#710; table in the guide). At 1 s
    a stalled leader's write gate closes after about 0.67 s (up to 0.92 s by where the stall
    falls; 1.41 s at 2 s), its lease shortly after: the lease runs `lease·(1−ρ)` from the
    renewal's send, renewals go every `lease/4`, the gate closes the fence margin before the
    end. A link outage costs less: a lost renewal is retried only on the `lease/4` grid, so an
    outage to both voters of just over half the lease can cost it. `naut check` warns of a
    lease under 1 s.
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
- **The state file's format is version 5** since `GrantNonce` (version 3 added `Owner` and
  `Cluster`, 4 `Box`, §2); a spare's file is version 6 (`Spare`, #1029 3c-2), and only a
  spare's, so a member that is not one can go back to an earlier release. A file of a version
  above the release's own stops the start and is left where it is. The driver stamps them on every write: the member, the cluster ID and the box
  fingerprint on a controller; `w`, the pair's cluster ID and no box on the witness. A file another member or pair wrote stops the start
  (`ErrNotOwner`) and is left where it is.
- **Controllers** refuse to start without `CLOCK_BOOTTIME` (Linux). The witness runs anywhere.
- **In `naut run`** (#710 PR 3b-1), a member's peer listener binds the address its own `peers:`
  entry names, and no other (#710 peer TLS narrowed it from every address), or
  `NAUTILUS_PEER_LISTEN` on a box behind NAT or in a container. An address that is not the
  box's yet never stops the start (a refused start would spend systemd's start limit and leave
  the controller down for good, the #1230 review): the member starts, raises `peer-bind` on
  `/api/cluster`, and binds once it can. A host name that binds to loopback (a stock
  `/etc/hosts` maps the box's name to 127.0.1.1; systemd-resolved does while no network is
  up) is never served, and is the same standing condition, logged as an error, until it
  resolves to a network address. The listener is behind the peer token, with the witness's timeouts and at most 64
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

**The override and a hot takeover (S2c-1).** An override takes a term no voter granted, so
no voter's records can vouch for it: it always takes over cold. When it ends, its report to
the voters sets their epoch record for its name to the override's term, so a later takeover
from an image of before it goes cold too. (Not built yet: this is the rule it must follow.)

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
| Duplicate | `redundancy.clear-duplicate`, the operator's clear (target: the marked name; `detail.attest`, the operator's word that the copy is stopped; after: each member's check, clear and re-check; refused when a member is silent or two boots are heard; `error` when a member still marks it after) (#1029 3c-3a) |
| Commissioning | `redundancy.commission` (target `pair`; after: each member's state, the incarnation and floor taken, a refused vouch, the leader) and `redundancy.recommission` (target: this controller; `detail.floor`, `detail.incarnation`, `detail.boxReplaced` with `detail.attestBox`, `detail.fromSpare` with `detail.attest`, the operator's word that the held boot is stopped; after: the evidence, or for `--from-spare` each voter's check and clear, the boots retired and where the state file went) (#1029 3c-3c) |

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
