---
title: I/O checkout and commissioning sign-off
description: An optional sheet of a project's I/O points, a run that records each point's pass, fail or note with who and when and what the controller read, kept apart from the project and never rewritten.
---

Commissioning a site is checking every wire: this input changes when the
float is lifted, this output moves the valve. A checkout is the record of
doing that point by point. It is **optional**: a project with no `checkout:`
section has no checkout page, no routes and no prompts, and nothing about
forces or alarms changes.

This page describes what is built so far: the sheet, recording results, and
the permissions. Forcing an output from the checkout, the HMI page, the sealed
and exported sheet and a redundant pair come in later steps, and say so below.

## The sheet

`naut checkout init` writes `checkout.yaml` from the project: a row for each
scalar input and output tag, with what the project knows.

```yaml
title: plant I/O checkout
points:
  - id: XV101_Cmd
    tag: XV101_Cmd
    address: Local:1:O.Data.0   # the tag's alias, when it has one
    equipment: FT-201           # the equipment of an alarm on the tag
    area: rcv                   # the first auth area whose patterns match
    kind: DO                    # DI, DO, AI or AO
    method: force               # an input is observed, an output forced
    expect: valve opens within 5 s
```

Edit it by hand (the device and equipment the project cannot know, and what a
pass looks like) and commit it. Struct and array tags are not listed: add
their member rows (`P101.Speed`) yourself. Turn the checkout on in the
manifest:

```yaml
checkout: {}                  # or {file: checkout.yaml, signoff: two-person}
```

`naut check` holds the sheet to the project: a point names a tag the
project has, a point that is forced is a declared output, and
`signoff: single` is a warning.

## Runs and results

A **run** is one pass over the sheet. One is open at a time; a run that was
started on the wrong sheet is abandoned (with a reason), and its results stay.

A **result** is a person's: pass, fail or note, with a note (a fail and a note
say why). The controller records what it read at the point's tag and its
quality when the result was recorded, so a result says what was seen and the
client cannot. A later result for a point supersedes an earlier one in the
view and leaves it in the file.

A run marked **rehearsal** (against virtual devices) stays a rehearsal: it
cannot be changed into a live one, and a later step will refuse to seal it.

```
GET  /api/checkout                    the sheet, the open run, every run
GET  /api/checkout/runs/{id}          a run: the latest result for each point, progress by device and area
POST /api/checkout/runs               {rehearsal?}: begin a run
POST /api/checkout/runs/{id}/results  {point, outcome, note?}: record a result
POST /api/checkout/runs/{id}/abandon  {reason}: end a run without sealing it
```

The results are kept in `checkout.jsonl` in the controller's state directory,
one line per entry, fsynced, each chained by SHA-256 to the line before. A file
whose chain is broken (a line edited, or removed or moved from the middle) is
refused at start, never repaired. Lines removed from the end, or the whole file,
leave nothing to notice: the chain has no anchor outside the file yet, so
copy the file or export a run as part of signing it off (a later step). The checkout needs an `auth:` block with users (a result names
a person; a token names nobody) and a state directory, as notes do.

## Who

| Permission | Held by | For |
|---|---|---|
| `checkout` | `engineer` | recording results, per area; beginning and abandoning a run needs it for the whole controller. Forcing an output in a checkout will need `force` too. |
| `signoff` | `supervisor` | sealing a run (a later step): a fresh password, and by default someone who recorded none of its results (`signoff: two-person`). `signoff: single` lets the recorder sign; `naut check` warns and the sealed record says so. |

Every begin, result and abandon is an audit entry, accepted or refused.

## Not yet

- Forcing an output from a checkout step, with the equipment out of service, a
  typed confirmation and an expiry that the controller keeps.
- The HMI checkout page (progress by device and area).
- Sealing a run, the signed JSON and CSV export, the printable report and
  `naut checkout verify`.
- A redundant pair: its controllers answer that the checkout is not available
  yet, rather than keep results on one box.
