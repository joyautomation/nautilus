# Design brief: compacting the operator-intent log (#1455)

Status: **approved, being built.** PR 2a-1 (the local side) and PR 2a-2 (the
wire) build the log's part and change nothing in production: no build
proposes a compaction until PR 2b (the decision, in `server`) lands and
switches it on. §8 lists what each PR holds.

The log (`oplog`) grows by about 2,400 entries a day at a busy plant's rate
and is never compacted today. A year's log is about 876k entries. Reading it
at start takes 466 MB of memory and catching a new copy up takes 8.8 s, and
both keep growing. On a Verdin-class box (870 MB) a busy plant's log can't
be opened after a few years. The measurements are on #1455.

## 0. Recommendation

Compact the log by **dropping dead entries, not by snapshotting consumer state.**

- **Who decides:** the leader decides which committed entries no longer
  affect any state. It proposes that decision as an ordinary log entry,
  `KindCompact` ("K").
- **Every copy agrees:** every copy, the witness included, carries out the
  same drop once K commits, checked against a digest. All three copies keep
  the same entries.
- **Kept entries stay verbatim,** with their original Seq, Term and TS. The
  base is a sparse run of kept entries up to `Through`, followed by the
  dense tail.
- **Readers keep working unchanged:** `Apply`, `ReadCommitted`,
  `naut notes export`, the `.superseded` reader and the checkout chain still
  read entries.
- **When it starts:** compaction runs only while all three copies are heard,
  synced, at or past Through, and on keep rules no newer than the leader's.
- **Old builds:** a build from before compaction refuses to open a compacted
  file, with a message that names the fix.

## 1. Why not a state snapshot

- **The witness applies nothing,** so it can neither build a consumer-state
  snapshot nor check one. Yet a replacement may be filled from the witness's
  copy.
- **Independent snapshots differ in bytes:** sessions are judged against the
  clock, and the alarm log applies to the live engine on a leader but to a
  view on a standby.
- **Restore paths:** a snapshot needs a Restore path beside Apply in every
  consumer. The checkout chain is the hardest.
- **Unknown kinds:** an older build keeps an entry kind it doesn't know and
  applies it after an upgrade. A snapshot can't carry a kind's state for a
  build that doesn't know the kind.

The dead-entry design has a second path per consumer too: the
`Rests()`/`Digest()` of §3. It is acceptable because its failure mode is
"compaction refused", never "state lost".

## 2. What is kept

The default is keep. An entry ≤ Through is dropped only when the consumer
that owns its kind says no state rests on it, as of T (§3). The table below
applies under the **closure rule:** if any part of an entry is kept, every
later entry that undoes any part of it is kept too, repeated until nothing
changes. This covers multi-target acks (`IDs`/`Occs`), alarm-detect bodies,
`suback`/lapse `Refs`, and occurrence entries.

| kind | kept | dropped when |
|---|---|---|
| note | always, every revision (the witness keeps note text, as today) | never |
| checkout | always (the chain counts only checkout entries; seals bind their own PrevSeq/PrevHash; `verify --chain` and `ExportChain` read every line) | never |
| ack | while any acknowledged occurrence it names is still acknowledged; while its Link is inside the spent-link window (so a link stays spent) | every part cleared or superseded, and the link window passed |
| ack-clear, occurrence-start/-end, alarm-detect | while the occurrence or episode it starts, moves or ends is still open, or while a kept entry needs it (closure) | the occurrence or episode ended |
| shelve/unshelve, out-of-service/in-service | while in force | unshelved, returned, or Until < T |
| substitute, unsubstitute, suback | while active, and for the request-ID window after it ends (a retried start with the same Req must still be recognised) | ended and past that window |
| session open | while open and Expires > T | ended or expired |
| session end | until its Expires (it is a tombstone that beats a later open) | Expires < T |
| lapse | while any entry it names is kept | every named entry dropped |
| noop | never needed | always |
| compact | the latest K (the header names it) | superseded by a later K |
| a kind no consumer claims (a newer build's) | always | never |

**High-water marks** are not entries, so they get a hook. Before a copy
replays a compacted log, `Options.Base(through)` tells every consumer that
everything up to Through has been delivered. For substitutes, the applied
and substitute-sequence marks become at least Through, so the retain-store
gate cannot re-admit a stale retained substitute.

**Seq-keyed state:** substitute IDs, `Ref/Refs`, the alarm log's ack, shelf
and out-of-service seqs and its lapsed set keep working, because seqs never
change.

**Bound:** compaction removes the churn (about 96% of the bench mix), not
the notes or checkout runs, which grow forever by decision (an archive for
them only if a project asks).

| log age | memory today | after compaction (worst case) |
|---|---|---|
| 1 year | 466 MB | about 25 MB |
| 5 years | not measured | about 100 MB |

The worst case is a 7-day tail (about 17k entries, 8 MB) plus at most 4% of
everything ever written being permanent. These are estimates; the bench
reruns in PR 2b and the table goes on #1455.

## 3. Who compacts, and when

**Only the leader decides.** It does so on its own goroutine, one decision
at a time, never on the log tick's goroutine (which is serial with
Heartbeat). It compacts only when every one of these holds:

- its term is synced (its noop is committed);
- it is not superseding, forked, lost, diverged, or holding a vouch's
  one-shot;
- **all three copies were heard within the lease, are synced, and count.**
  A copy that is down blocks compaction (safety beats size), so a copy
  behind the base can only be a wiped, lost or restored one, never one that
  was simply away. Status says `compaction: waiting for <copy>`. Once
  compaction has been blocked for more than 7 days, the condition
  `oplog-compaction-blocked` names the copy it waits for, or the reason
  (clocks, rules, disk), so the growth is never silent;
- every copy advertises a compaction rules version ≥ 1, and **the leader's
  rules version is ≥ every copy's**, so the decider knows every field and
  kind any current copy writes. The version is the keep-rules version, not a
  capability bit; any later change to a keep rule or an `Entry` field bumps
  it. It comes from `Options` and is unset (never advertised) until PR 2b
  switches compaction on, so a build with only the log's part can't be taken
  for one that decides;
- the part older than the margin (7 days by TS before T) holds ≥ 20,000
  entries or ≥ 8 MB;
- the previous K's TS is ≥ 24 h old (a durable timer: K is in the log);
- the log holds no K with a higher rules version than the leader's.

**T, the compaction time.** Every TS is stamped by the sequencing leader's
clock, so "the newest committed TS" alone still follows a stepped leader
clock. Instead:

- every copy's answer carries its wall clock (`now`), and the gate already
  requires all three copies heard;
- T = the minimum of the three clocks and the newest committed TS;
- if the clocks spread by more than 5 minutes, nothing is compacted and
  status says `compaction: clocks disagree`.

A leader whose clock steps forward and then sequences entries can therefore
not drop shelves, out-of-service windows or sessions that are still in
force.

**Through** is the highest seq that meets all three:

- its TS is ≤ T − margin;
- it is ≤ the commit index every copy last reported;
- **no `KindLapse` above Through names a seq at or below it.** `rewrite()`
  keeps only one commit line, so commit points are not recorded; this rule
  is what keeps a lapse and its named entries from being split.

**Deciding dead:**

1. The leader replays its committed log up to Through, plus the tail to its
   commit, into **scratch consumers** in view mode: never the engine, clock
   pinned to T.
2. Each scratch consumer answers `Rests() []seq` and `Digest()`. `Digest()`
   is the consumer's **complete** state, normalised at T (lazy prunes such
   as spent links and request IDs are applied at T first), so the check is
   not refused forever.
3. The kept set is everything those consumers rest on, plus every unclaimed
   kind and every note and checkout entry, with the closure rule applied.
4. **Check before proposing:** a second scratch replay, of the kept entries
   plus the tail, must give the same `Digest()` for every consumer. If any
   differs, nothing is proposed: an ERROR plus the condition
   `oplog-compaction-refused`, naming the consumer (that is a bug, with the
   evidence kept).
5. **New code:** substitutes have no view-mode consumer today (their state
   lives in `runtime`), so PR 2b builds one. A test checks that `Digest()`
   covers every field: a mutation of each field must change it.

**Sequencing K:** the decision runs on its own goroutine, and leadership may
change and come back meanwhile. So when K is sequenced (under the
sequencing lock), the leader re-checks:

- the term is the same;
- the gate still holds;
- the decision's `prevThrough` equals the current base;
- **no entry is uncommitted.** An earlier K may be among them, and the
  current base moves only when a K commits: a new K would follow the base
  from before it, and every copy that commits both would diverge;
- the lapse rule holds against the log as it stands, and no kept entry
  loses a lapse that names it.

If any of these fails, the decision is discarded. Tail growth alone is fine.

**K's body** goes in `Value`, the field every build keeps:

- `{v:1, rules, through, throughTerm, prevThrough, keep, kept, digest}`;
- `keep` is incremental and run-length encoded: the new kept seqs in
  (prevThrough, Through], plus the previously kept seqs now dropped, so its
  size follows the window;
- `digest` is the SHA-256 over the kept entries' **(seq, term) pairs** in
  order. Log matching makes the pairs enough, and bytes differ between
  builds, since a follower re-marshals entries and drops fields its build
  doesn't know;
- it stays under the 512 KiB entry cap: an over-size decision compacts a
  smaller range;
- it is never lapsed (`carrierLocked` skips it). Dead stays dead, so a late
  commit is still correct.

## 4. Carrying it out

**Logically at commit, physically in the background:**

- **At commit**, under the node's lock, a copy only records K as committed
  and checks its digest against its own log (a hash over (seq, term) pairs,
  cheap). Nothing heavy runs under the lock or the sequencing lock.
- **A background goroutine** on each copy then does the work, starting
  after a random delay of 0–60 s so the three copies don't fsync at once:
  1. builds the new slice and the temp file, outside the lock, from the
     entries up to Through and the tail as of that moment (all committed,
     never changed);
  2. syncs the temp file.
- **A rewrite during the build:** an uncommitted suffix can be replaced (and
  the file rewritten) while the temp file is being built. So the build
  copies only the **committed** tail outside the lock, and the file keeps a
  generation counter that every rewrite bumps. If the generation moved
  during the build, the build is thrown away and redone.
- **Then, under the lock,** it appends only the tail written since that
  moment, syncs that small delta, renames, syncs the directory, swaps
  memory, and writes `Mark.Base{Through, Digest}`.
- **Target:** the lock held for ≤ 10 ms. The bench measures it on a
  Verdin-class box, and a test with the build's sync blocked asserts that
  `Synced()`, `Status()` and replication are still answered.
- **Several Ks:** committed Ks are carried out in order; a start that finds
  committed Ks past the file's base carries each out.

**A crash anywhere is safe:**

- before the rename, the old file remains; it is correct, logically
  compacted but not physically, and the next start carries out any
  **committed** K past the file's base. An uncommitted K is only a commit
  floor (below), never an order to drop, since a newer term may replace it;
- between the rename and the mark, the file is ahead of its mark, which is
  fine.

**Between commit and rewrite:** a copy that is logically compacted but still
physically dense answers exactly as a compacted one does (`base`, 409 below
Through, install), so all three copies look alike during the 0–60 s window.

**Undo:** controllers keep a copy of the file from before the compaction as
`.precompact-<time>` until the next compaction; the witness keeps none. It
is a copy (or a hard link), never a rename of the live file, which would
leave a held log missing. Before writing it the copy checks free disk: under
twice the file's size, the copy refuses the physical compaction (it stays
logically compacted and dense on disk) and raises a condition naming the
copy and the space, since eMMC fills quietly.

**On a digest mismatch, the copy has diverged.** With digests over (seq,
term), a mismatch is a real divergence, not a build difference. The copy is
not emptied in-process: its consumers already hold applied state, and
`OnEmptied` resets only notes and checkout. It follows the supersede
precedent instead:

- the mark records `Diverged`, with an ERROR and the condition
  `oplog-diverged`;
- the copy counts for nothing, doesn't lead and doesn't compact;
- at its next start the file moves aside as `.diverged-<unix>`, the copy
  starts empty and unsynced, and it is refilled (§5). It restarts under
  supersede's rule;
- only the witness, which applies nothing, resets in place.

The leader can't mismatch itself, since its digest comes from its own log.

**A held K is a commit floor:**

- **Why:** a copy that stores K does not necessarily hold a commit index ≥
  Through: a catch-up commits only after its last page, and a failed
  commit-line write only warns.
- **The rule:** at open and in `carrierLocked`, any held K, committed or
  not, means everything up to its Through is committed, since Through was at
  or below a leader's commit.
- **Why it is safe:** the lapse rule in §3 ensures no lapse above Through
  names anything at or below it, and a later leader never names one,
  because it treats them as committed.
- **Tests:** a copy that restarts holding an uncommitted K and then leads; a
  lapse on each side of Through.

**File format:** a header line first:
`{"b":{"why":"compacted log: update naut, or export notes and start a new log","through":…,"throughTerm":…,"compact":<K's seq>,"kept":…,"digest":…,"rules":…,"lineage":…}}`.
The rules version is K's, so a build on older rules still sees, after a
restart, that the log was compacted under newer ones.
Then the kept entries, a commit line, and the tail.

- **An old build fails closed.** Both v0.23.0's reader and the streaming
  reader after it take the header for a line of neither kind and refuse the
  file with `unrecognised line "<first 80 bytes>"`, with or without a mark,
  so `New` fails. The `why` text comes first and is short enough, with no
  character JSON escapes, that its fix is inside those 80 bytes.
- An old `ReadCommitted` skips the header, so an old `naut notes export`
  still exports every note.
- A file without a header counts as Through 0.

**Sites that assume a dense log, and their change:**

| site | change |
|---|---|
| `lastLocked` | with no entries, return Through, never 0 |
| `termAtLocked` | return throughTerm at Through |
| replicate prev check | a PrevSeq below Through counts as matched up to Through |
| `acceptLocked` | skip entries at or below Through as committed (a sparse search would find another seq and refuse a conflict) |
| proved/have in the replicate handler | base-aware; dense above Through, as today |
| `replicateTo`'s previous entry | above Through only; below it, the install of §5 |
| `fetchAfter`'s "pages follow on" check | a base page is checked against the header's keep list instead |
| `pageLocked`/`afterLocked` with after below Through | only for `base=1` (§6), otherwise 409 |
| `open`, `ReadCommitted` | read the header; replay kept entries and the committed tail as one range, as today's open does |
| `rewrite` | writes the header |
| Vouch/Accept/Picture | compare (LastTerm, LastSeq): unaffected (Picture's digest covers no entry content) |

**The lost rule:** under the held mark, a log is lost if the file is
missing, has neither entries nor header, or its Through is below
`Mark.Base.Through` (an older file put back). `Mark.Base` is cleared on
supersede, lost, diverged, `Rebind` and `ResetInPlace`.

**Lineage:** the header carries the history. A superseded, lost or diverged
file moves aside whole, and the `.superseded` notes reader uses the new
`ReadCommitted`.

## 5. Refilling a copy behind the base

Under the gate in §3, only a copy with nothing applied is ever behind the
base: a wiped, new, lost, diverged or superseded one, with commit 0. No
consumer reset is needed.

**Both directions are covered:**

- **Push install, for a follower and the witness.** The witness never pulls;
  today it is filled only by the leader's resend.
  - When a peer's Have is below Through and it advertises a rules version,
    `replicateTo` sends **install requests** (`replicateReq.Install`): the
    header, then the kept entries in pages.
  - The copy stages them; it neither stores them in its log nor answers
    Have for them.
  - **Staging identity:** the staging is bound to (sender, term, lineage,
    K's seq, digest), and any change of these, a restart or a supersede
    discards it. There is one staging slot per copy: a wiped controller gets
    the leader's push and also runs its own pull every tick, and the newer
    term wins, the push on a tie. An install page from a stale term is
    refused by its term before it is staged, as replication is.
  - **Before the base is applied**, the copy checks the staged base against
    K's body: K comes with the install's last page (the witness never calls
    a peer, so it can't fetch K), at the seq the header names, and its
    Through, count and digest must be the header's and the staged
    entries'. When K then arrives again in the tail, it must be that same
    K, or the copy has diverged.
  - **Pages are taken by position:** a duplicate, late or out-of-turn page
    is ignored and the answer says where to go on; a page after the install
    changes nothing.
  - **Only onto nothing applied:** a copy with a commit index or a base of
    its own refuses an install.
  - After the last page, it checks the staged keep list and the (seq, term)
    digest, atomically writes the base file, takes Have = Through, and
    commits to Through, applying the base as one range. The witness applies
    nothing.
  - Normal replication then goes on from PrevSeq = Through.
- **Pull install, for a leader catching up** (`leaderCatchUp`, `CatchUp`):
  `fetchAfter` with `base=1`, with the same staging and checks. A compacted
  copy's 409 reaches the leader's choice as an answer (its base page), never
  as silence. A reader that holds the entry at Through, at its term, reads
  on from Through instead (its log is the same up to there).
- **Verification:** before anything is applied, an install's (seq, term)
  pairs, count and Through are checked against the serving copy's header
  and against K's own body, which is committed log content. The digest
  covers (seq, term) only, as K's does (§3): an entry's content rests on
  log matching, as replication's does.
- **An installed base before its tail:** the header keeps the K that came
  with the install, so the copy serves its base before the tail (which
  holds K) has come, and after a restart.
- **A copy that refuses** (it holds committed entries behind the base) is
  named in `oplog-peer-behind-base`, never resent to in a loop.
- **Have and Synced:** `Have` is only what was proved (#1472). Synced comes
  as today, from a synced leader's replication proving the copy equal up to
  its commit.
- **The takeover wait:** a base is small, so an install fits inside the
  intent-log wait.
- **Behind with applied state:** this can't arise under the gate, except
  from a restored old file, which the lost rule catches.
- **Same as today:** a wiped copy with no other synced copy except the
  witness still needs a vouch.

## 6. Wire and mixed versions

- **Vote protocol:** unchanged (`quorum.WireVersion` 9). Epoch records,
  marks, `HotCheck` and `LogState` carry no oplog seq; the scan image's
  seqs are its own.
- **The oplog HTTP JSON has no version, and none is added.** The `page=1`
  opt-in pattern is reused:
  - `afterResp`/`replicateResp` gain `compactRules: <n>`, `now`, and
    `base{through, throughTerm, compact, digest}` once a copy has one;
  - `replicateReq.Install` (install) is sent only to a copy that advertised a
    rules version;
  - `GET /api/oplog?…&base=1` answers base pages; without it, an `after`
    below Through gets 409 `compacted`, never a sparse page.
- **Rolling upgrade:** nothing compacts until all three copies run a build
  whose rules are ≤ the leader's. A copy replaced by an old build between
  the decision and the commit has a commit ≥ Through, as every copy did at
  the decision, and an old build at or past Through takes the tail normally.
  Only an old build that needs an install hits a gap repeatedly: the leader
  stops resending to it and raises `oplog-peer-behind-base` ("controller b
  runs a build from before log compaction and missed the compacted part:
  update it"). A copy's rules version is read from each answer, the 409s
  included (a wiped copy's first answer is a gap), never remembered.
- **An old leader** reading a compacted copy without `base=1` gets 409 and
  stays read-only under the existing condition, so it fails closed.
- **Downgrade after compaction is not supported,** and there is no back-out
  tool. An old build refuses the file, and its refusal names the fix.
  Going back below the first compacting build after it has compacted means
  every copy starts a new log; run `naut notes export` first (a compacting
  build reads moved-aside files). RELEASING.md, the release notes and the
  upgrade guide say so before anyone enables compaction. Before any
  compaction, a downgrade works as today. A rules downgrade with files kept
  (a copy moved to an older build that still compacts) is covered too: a
  leader does not compact if the log holds a K with a higher rules version
  than its own.

## 7. Context map

- `oplog` gains K, the base, the install and the floor. It stays
  kind-blind: it carries out a keep list and checks a digest.
- The keep decision lives in `server` (scratch view-mode consumers,
  `Rests`/`Digest`), plus a view-mode substitute consumer over `runtime`'s
  intent code. It is wired in `cmd/naut/pairrun.go` through
  `Options.Compact` and `Options.Base`.
- No new package edges are expected; `go test ./internal/contextmap/`
  confirms it, and the map changes first if not.

## 8. Slices and tests

Each PR is reviewed, mutation-checked (each guard gets a mutant row) and
tested against the full uncompacted log as the oracle, as the streaming PR
used the old reader.

**PR 2a-1 (oplog, local; dark).** The header and sparse base, the local
dense sites of §4, K carried out (logically at commit, physically in the
background), several Ks in order, the commit floor, `Mark.Base`, the lost
rule, `.precompact` and the free-disk check, and the rules version from
`Options` (unset: never advertised, nothing proposes K). Tests:

- what a compacted copy applies and serves equals the full log filtered to
  the kept entries ≤ Through plus the tail, entry for entry, for random logs
  and random Through; and a toy consumer with a toy dead rule ends in the
  same state as after a full replay;
- a lapse on each side of Through;
- an uncommitted held K, then the copy leads (the floor);
- a crash at every step: temp write, sync, delta, rename, directory sync,
  `.precompact`, mark;
- the lock stays free while the build's sync is blocked;
- a digest mismatch leads to diverged;
- v0.23.0's reader and the streaming reader (copied as test oracles) refuse
  the file and show the fix; their `ReadCommitted` still returns every note;
- an unwired node never advertises compaction and never proposes K.

**PR 2a-2 (oplog, wire; dark).** `compactRules`/`now`/`base` on the wire,
the 409, the push and pull installs, staging, the diverged refill,
`oplog-peer-behind-base`, the conditions. `oplog-compaction-blocked` is
raised from `Status` (why and since); PR 2b's decision sets them, and
derives "since" from the log's own timestamps, so a restart does not reset
the 7 days. Tests:

- the witness wiped after a compaction, refilled by push;
- a controller wiped and refilled by push while the other leads;
- a lost leader refilled by pull from the other controller and the witness;
- every note comes back to a wiped copy;
- an old copy behind the base gets the condition and no resend loop.

**PR 2b (server: the decision).** View-mode scratch consumers with T pinned
(alarms, substitutes (new), sessions, notes, checkout), `Rests()`/`Digest()`,
the closure rule, `Options.Base` high-waters, the check before proposing,
the trigger, and the rules version switched on. Tests, each consumer against
the full-replay oracle with the clock pinned:

- alarm entries: multi-target acks with partial clears, spent links,
  out-of-service, lapses keyed by seq;
- substitutes: the request-ID window, the retain gate after a compacted
  replay, and session-end tombstones;
- a test that `Digest()` covers every field;
- clock steps: a leader steps +30 days and then sequences entries, and
  nothing in force is dropped; a clock spread over 5 minutes refuses
  compaction;
- `naut checkout verify --chain` and seals across a compaction;
- `naut notes export`, including `.superseded`;
- a bench rerun with compaction: sizes, and how long the lock is held on a
  Verdin-class box, with the table on #1455;
- docs: the log stays bounded except notes and checkout runs; RELEASING.md,
  the release notes and the guide carry the "no downgrade past compaction"
  line.
