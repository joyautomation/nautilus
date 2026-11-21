---
title: Audit trail
description: Who did what on a controller, and proof that nobody changed it afterwards. A one-minute demo that signs two people in, works a lift station, then tampers with the journal and shows naut audit verify catching it.
---

`naut run` journals the changes made through its API under the person
who made them: a tag write, a force, an alarm acknowledgement, an online
edit. Refused attempts are journalled too (see [Limits](#limits) for
coalescing). So is what the controller does by itself, as `system`: its
start and stop, a substitute running out, a prune of old files. The
journal is hash-chained on the box. A copy in Postgres, which the
controller can add to but not rewrite, lets `naut audit verify` show
that nothing was changed afterwards, within the [limits](#limits) below.

This page runs all of that end to end. It also shows how to set it up on a
controller of your own.

- Users and roles are set up in [Users, roles & sign-in](/guides/auth/).
- The journal, the copy and `verify` are documented in full in the
  [HTTP API guide](/guides/http-api/#audit-journal).

## Run the demo

From a copy of the repository:

```sh
tools/audit-demo/run.sh              # pauses between steps: press Enter
tools/audit-demo/run.sh --no-pause   # straight through, about a minute
```

**It needs** Go, `curl`, `jq`, and Docker (for a throwaway Postgres on
port 55440). With a Postgres of your own, set `DEMO_ADMIN_DSN` to a
superuser connection string instead; the demo then needs `psql`. It creates
the database `naut_audit_demo` and a role of the same name there.

**It builds** a lift station from `examples/lift-station` in a temporary
directory, with three additions:
- **Two people:** alice, an `operator`, and bob, an `engineer`, made with
  `naut auth init` and `naut users add`.
- **One substitute rule,** on a discharge-pressure input.
- **A Postgres copy of the journal**, under a role that may only add rows.
  `naut audit pg-schema --role` prints its grants.

**Its history is staged.** The journal starts with six weeks of past days
at a 30-day retention. `tools/audit-demo/seed` writes them with the real
journal writer, its clock set to each past day. Each day it does what
`naut run` does at every start: it records the retention, then prunes
what is older. Everything after that is live. The past days reach
Postgres only when the demo's controller first starts, so they are
copied and anchored then, not on the days they are dated. `verify` says
so on every run (the `anchored since` note below).

The demo checks each step and stops with `DEMO FAILED` if one does not hold.
CI runs it in its Postgres job, so it stays true.

## Act 1: who did what

alice signs in. She raises the inflow setpoint, tries to force the
wet-well level (refused: an operator can't force), trips the high-high
float and acknowledges its alarm, then signs out.

bob signs in and does the following:
- **A force:** he forces the level to 85% for a check, then clears it.
- **An online edit:** he changes the totalizer so it never counts negative
  flow.
- **A substitute:** he substitutes 45 psi for the discharge pressure, for
  two seconds. When it runs out, the controller puts the sensor back by
  itself.

Then the demo reads the journal as bob. An engineer holds the `audit`
permission (from `supervisor` up); an operator does not.

```text
07:49:54  user:alice  auth.signin                                                ok
07:49:54  user:alice  tag.write          InflowGpm        190 → 240              ok
07:49:54  user:alice  force.set          LIT101_Level      → 85                  refused  user:alice (operator) may not force
07:49:54  user:alice  tag.write          ForceLSHH        false → true           ok
07:49:56  user:alice  alarm.ack          LSHH101                                 ok
07:49:56  user:alice  tag.write          ForceLSHH        true → false           ok
07:49:56  user:alice  auth.signout                                               ok
07:49:56  user:bob    auth.signin                                                ok
07:49:56  user:bob    force.set          LIT101_Level      → 85                  ok
07:49:56  user:bob    force.clear        LIT101_Level     85 →                   ok
07:49:56  user:bob    program.edit       stats            d7445247f59d → 30c27ce1c6a4  ok
07:49:56  user:bob    substitute.start   PIT101_Pressure  42 → 45                ok
07:49:58  system      substitute.expire  PIT101_Pressure  45 → 42                ok
```

Each line names the person, not a shared station login, and says whether
the action was allowed. An online edit is journalled as the program's hash
before and after, never its source. The last line is the controller
undoing a substitute on its own: no one asked, so it is `system`. (The
full output also starts with two `users.change` lines: `naut users add`,
journalled under the operating-system user who ran it.) An HMI built with the kit shows the same journal on a page, with
the `AuditTable` component (the lift station has it on its diagnostics
page).

## Act 2: was anything changed?

`naut audit verify` checks the journal's hash chain on the box. It then
checks every line against the Postgres copy, and the chain's head against
the anchors the copy writes. The controller's role can't change a row once
it is written.

**1. As it stands:**

```text
verified: controller "lift-station", 268 journal entries in 31 files, head seq 364; 276 copied rows (268 compared line for line), 4 anchors (newest seq 364)
note: the copy holds 8 entries from before the journal's files (seq 89 to 96): pruned from the journal, kept in the copy
note: anchored since 2026-11-21T07:56:59Z; 263 entries are dated (by their own times, which the journal's writer sets) before that, so they rest on the journal as it stood then
starts after a pruned file: sha256:f1d83f54…
```

The run pruned the day that had just turned 31 days old. A prune is
journalled with the retention it pruned by, so `verify` accepts it. Here
the copy had taken that day before it went, so it keeps those 8 entries.
When the prune wins the race with a first copy, the copy shows a gap
instead, which the journalled prune explains, and the verdict is the same.

The `anchored since` note is the staged history: its lines were first
copied and anchored when the demo started. On a real controller with the
copy turned on from the start, the note covers only what came before the
copy did.

**2. Someone with root on the box rewrites one line,** changing an old
alarm acknowledgement from alice to bob:

```text
BROKEN: controller "lift-station", 268 journal entries in 31 files, head seq 364; 276 copied rows (268 compared line for line), 4 anchors (newest seq 364)
BROKEN: the journal's chain breaks at audit-20261031.jsonl:5: prev does not match the line before: a line was edited, removed, inserted or moved, or a file is missing
BROKEN: seq 172 differs between the journal (audit-20261031.jsonl:sha256:7f4d7753…) and the copy (sha256:b40e8e88…): the line was rewritten after it was copied
```

`verify` exits 1, and the copy still holds the original line:
`seq 172: user:alice alarm.ack HighLevel`.

**3. Or deletes a whole day.** `verify` says `BROKEN` again and lists
every line the copy holds that the files no longer do.

**4. Put back as it was,** it says `verified` again.

**5. A lowered retention.** Someone sets `audit.keep-days` from 30 to 7
and restarts. `naut run` holds the prune until Postgres has anchored the
new retention, then prunes. The verdict is still `verified`, because the
prune was honest, but every `verify` from then on says what happened:

```text
verified: controller "lift-station", 104 journal entries in 8 files, head seq 388; 300 copied rows (104 compared line for line), 7 anchors (newest seq 388)
WARNING: retention LOWERED from 30d to 7d at seq 365 (2026-11-21T07:57:05Z, by ops): look at what was pruned after it
```

A lowered retention is how someone would make a prune look honest, so it
is the line to look at.

### A role that could rewrite the copy

The copy proves something only if the controller can't rewrite it. Point
the controller at Postgres as a superuser, or as any role that can update,
delete or own the tables, and `verify` won't call the journal verified,
even when every line matches. It answers `UNPROTECTED` (exit 3) and names
the role:

```text
UNPROTECTED: the controller's role "postgres" could rewrite the copy or the anchors, their writer column included (it is a superuser): nothing here proves anything against it; run naut audit pg-schema --role postgres …
```

The copy still works as a copy, and the drivers block shows `anchor not
protected`. Give the controller its own role with
`naut audit pg-schema --role <role>`, which the demo does.

## On a controller of your own

```sh
naut auth init --user alice:operator --user bob:engineer
sudo naut users add alice
sudo naut users add bob
naut audit pg-schema --role naut_site | psql "$ADMIN_URL"   # as the tables' owner, never as naut_site
```

```yaml
audit:
  sink: postgres            # its connection string in NAUTILUS_AUDIT_DB_URL (or DATABASE_URL), as naut_site
```

Then, from a machine the controller's service user can't change:

```sh
naut audit verify --anchor postgres
```

Exit 0 is `verified`, 1 is `BROKEN` and 3 is `UNPROTECTED`. See
[Checking the journal against the copy](/guides/http-api/#checking-the-journal-against-the-copy)
for every check it makes.

## Limits

The full list is under [Audit journal](/guides/http-api/#audit-journal)
in the HTTP API guide. The ones that matter most for an audit:

- **On a redundant pair, `GET /api/audit` on the standby reads the
  leader's journal.** The standby journals some actions itself: its own
  refusals, and the duplicate-mark clear when that command runs on it.
  Those are in the standby's own files, where `naut audit verify` checks
  them, but its API doesn't show them yet.
- **Verify proves the journal against someone holding the controller's
  role, not against the database's owner.** Whoever owns the tables can
  rewrite the copy and the anchors. Keep that role away from the box, and
  run `verify` from a machine the controller's service user can't change.
- **Refusals are coalesced.** One address gets five refusals a minute in
  full per action and person, then one entry that counts the rest.
- **While the journal can't be written** (a full disk), operator actions
  (tag writes, alarm acknowledgements, SFC commands) still go through, so
  a full disk can't stop the plant, and so do the ways back to normal:
  clearing a force, ending a substitute, rolling an edit back. Their
  entries are held in memory and written when the journal can be again (a
  restart meanwhile loses them). Actions that need the journal are
  refused meanwhile: setting a force, program edits, live I/O changes,
  starting a substitute. The `audit` card on the drivers block says so,
  and so does a system alarm when the project has alarms.
- **Not every route is journalled yet.** The program routes that
  `naut logix serve` answers itself are not.
- **A line is protected once it is in the copy,** normally within a
  second of being written. Until then, root on the box could change it
  unseen. While Postgres is down that window is the outage; the controller
  keeps running and the copy catches up.
- **The demo's past six weeks are staged** (see above). The prune,
  the tampering and every verdict are real.
