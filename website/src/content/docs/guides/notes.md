---
title: Operator notes and the shift log
description: Notes about an asset, a tag, an alarm, an area or the plant, written by a signed-in person, never overwritten or deleted, replicated on a redundant pair, exported with their history.
---

An operator writes down what the plant did not: *pump 2 is vibrating, I called
maintenance*, *the sensor on tank 3 is reading high, checked against the
sight glass*, *handover: burner 2 is on manual until the part arrives*. A note
is text, who wrote it, when, and what it is about, and it stays.

A note is **never overwritten and never deleted**. An edit is a new revision
and the first wording is still there; a retraction is one more revision and the
text is still there. The audit journal records every write with its text as
well (`note.add`, `note.revise`, `note.retract`), so the audit export holds
them.

> The notes API and `naut notes export` are described here. The panel in the
> operator screens and the shift-log page come with the next part of
> [#720](https://github.com/joyautomation/nautilus/issues/720).

## Turn it on

Notes belong to people, so a controller needs an `auth:` block with users (or
OIDC), and a state directory to keep them in (`auth.state-dir`, or `/var/lib/nautilus`
under systemd). Without them `/api/notes` answers 404 and says so. A controller
on its own keeps them in `notes.jsonl` in the state directory. A
[redundant pair](/guides/redundancy/) keeps them in its replicated operator
log, so they are on both controllers and on the witness.

## What a note is about

| Target | Meaning |
|---|---|
| `tag` + id | a tag; the tag must exist |
| `alarm` + id | an alarm; it must exist. Add `occurrence` (the id the alarm gave that occurrence) and `activeMs` to tie the note to one occurrence of it |
| `asset` + id | equipment: the id of a mimic or scene equipment. Not checked: the controller does not read those at run time |
| `area` + name | an area of the plant. Not checked |
| `log` | the plant-wide shift log: no id |

A note is `kind: note` (the default) or `handover`, which the shift-log view
puts first for the next shift.

## Who may write

The permission is `note` (the `operator` role and everything above it hold it;
`viewer` does not). Reading notes is `read`. Like `write` and `ack`, `note` is
limited by **areas**: a note about a tag, or about an alarm, needs the permission
for that tag (an alarm's tag, for an alarm). A note on the shift log, an asset or an
area needs it anywhere. An alarm that has no tag, or that was removed since, needs a
grant that covers the whole controller (as acknowledging it does), and an addendum needs
the permission for the note's target as well as for the one it names.

The author is the person signed in, never a field of the request. A
**service token may add** a note (an integration posting *batch 7 started*)
and may not revise or retract one. A personal access token acts as its person.

## Changing a note

- **The author has an hour** from writing it to **revise** it (`POST
  /api/notes/{id}/revise`) or **retract** it (`…/retract`, with a reason). Every
  revision is kept: the note shows its latest text, "edited", and the history is
  one request away.
- **After the hour, correct it with an addendum**: a new note with `re: <the
  note's id>`, by anyone who may write notes there. The first note is untouched.
- **A supervisor may retract** anyone's note at any time (the permission is
  `audit`, which `supervisor` and above hold). The retraction names the reason
  and that it was not the author's.
- A retracted note is hidden from the list unless you ask (`?retracted=1`), and
  cannot be changed again. Its text is in the history.
- **Two changes at once**: if two people change one note at the same moment, the
  first is kept and the second is answered **409** ("the note changed while you
  were saving"), so nobody is told a change was made that was not. On a pair
  the log commits both and applies only the first; `naut notes export` counts the
  other as an entry that could not be applied.

A note of at most 4096 characters, plain text (a newline and a tab are the only
control characters): nothing renders it as markup.

## The API

| Route | Does |
|---|---|
| `POST /api/notes` | `{target:{kind,id,occurrence?,activeMs?}, text, kind?, re?}` → 201 with the note |
| `POST /api/notes/{id}/revise` | `{text}` |
| `POST /api/notes/{id}/retract` | `{reason}` |
| `GET /api/notes` | `?target=&id=&occurrence=&kind=&author=&since=&until=&limit=&retracted=1`, newest first |
| `GET /api/notes/{id}` | the note with every revision (`history`) |

On a pair a write is answered once a majority of the log's three copies hold it,
exactly as an alarm acknowledgement is. **A copy that cannot count a majority
refuses the note**, with the same reason an acknowledgement gets, and keeps
nothing: a note is never queued in memory to be lost. A write whose outcome is not
confirmed answers **503 with `X-Nautilus-Outcome: unknown`** and the note's
`note` id: it may still be kept, so read `GET /api/notes/{id}` before writing it
again.

## Export

```sh
naut notes export --format csv --since 2026-11-01 > notes.csv
naut notes export --format json                       # notes with every revision
controller notes export --state-dir /var/lib/nautilus # a built controller, on the box
```

It reads the state directory, so it works with the controller stopped. CSV has
one row per revision (a retracted note's rows are all there). A text that
starts with `=`, `+`, `-` or `@` is quoted so a spreadsheet does not run it.

## What to know on a pair

- **The witness holds the note text too**: it stores every entry of the log. The
  log is replicated from the first entry and never compacted, so a controller that
  was replaced or wiped is brought every note back by catch-up, not just recent
  ones. Years of 4 KiB notes lengthen the log's replay at start; that is the
  cost of keeping them where a failover cannot lose them.
- **A superseded history takes its notes with it.** When a person accepts a
  copy's loss (`naut redundancy vouch`), that copy's log history is replaced and
  the notes only it held leave the live view with the rest of that history: the
  notes on a pair are exactly its log's. `naut redundancy vouch` prints what
  would be lost before it does.
- **Upgrading a pair**: a controller or witness from before notes stores and
  replicates a note entry whole (the note travels in a field it keeps), so a
  rolling upgrade loses no note text, and the notes appear on a copy once it is
  upgraded and replays. It shows none until then, and cannot be asked to
  revise one.
- **From a controller on its own to a pair**: the notes file is not carried over
  by the pair's log. Export it first (`naut notes export`).
