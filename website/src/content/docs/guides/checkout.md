---
title: I/O checkout and commissioning sign-off
description: An optional sheet of a project's I/O points, a run that records each point's pass, fail or note with who and when and what the controller read, kept apart from the project and never rewritten.
---

Commissioning a site is checking every wire: this input changes when the
float is lifted, this output moves the valve. A checkout is the record of
doing that point by point. It is **optional**: a project with no `checkout:`
section has no checkout page, no routes and no prompts, and nothing about
forces or alarms changes.

This page describes what is built so far: the sheet, recording results,
forcing an output, and the permissions. The HMI page, the sealed and exported
sheet and a redundant pair come in later steps, and say so below.

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

## Forcing an output

An output with `method: force` can be driven from a step. A force bypasses
the program's permissives, so this one is held to more than a plain force.
Nothing is forced unless all of these hold:

- the run is open and was begun against this sheet;
- the caller holds `checkout` **and** `force` in the point's area (a fresh
  password, for a browser sign-in);
- the caller typed the point's id as `confirm`;
- the point's **equipment is out of service** (the alarms' window: `naut alarms
  out-of-service <equipment>`), so its alarms are quiet and the plant can see
  it is being worked on. A point with no `equipment:`, or equipment the alarms
  do not know, cannot be forced from a checkout;
- nothing is forced at that address, in it or around it (a member of a struct
  tag, or the tag a member is in): a force someone else holds is never taken
  over; and no other checkout force stands (one at a time).

```
POST /api/checkout/runs/{id}/force          {point, value, seconds?, confirm}
POST /api/checkout/runs/{id}/force/release  {}
```

The force stands for `seconds` (30 by default, at most 300), counted by the
controller's own monotonic clock, and then **the controller lets it go
itself**: closing the browser, signing out or losing the link cannot leave an
output forced. It also ends when released, when its run is abandoned, **when
the equipment is back in service or its window runs out** (the interlock is
checked every second while a force stands; the record says `equipment back in
service`), when someone clears or replaces the force (`replaced`), and when
the controller restarts (a force does not survive one; the record says
`interrupted`). The force is the one the controller put there, told apart by
a number the force table gives it, never by its value: a force someone else set
or changed on the output meanwhile is not removed. While a checkout force
stands, every frame carries `checkoutForce` (the point, who, the milliseconds
left) so any screen can say an output is being driven.

A result recorded on a forced point carries what was forced, for how long it
was allowed, how it ended (`released`, `expired`, `run abandoned`,
`interrupted`, `replaced`) and the sequence number of the entry that began it,
so the sheet can be checked against the audit journal. The force, its release
and the controller's own expiry are audit entries.

## Who

| Permission | Held by | For |
|---|---|---|
| `checkout` | `engineer` | recording results, per area; beginning and abandoning a run needs it for the whole controller. Forcing an output in a checkout needs `force` too. |
| `signoff` | `supervisor` | sealing a run: a fresh password, and by default someone who recorded none of its results (`signoff: two-person`). `signoff: single` lets the recorder sign; `naut check` warns and the sealed record says so. |

Every begin, result and abandon is an audit entry, accepted or refused.

## The Checkout page

A project with a `checkout:` section gets a **Checkout** screen in the built-in
HMI (a project with none gets no screen, no route and no banner). It shows the
progress by area and by device, the points with their latest result (the
reading and quality are the controller's, written into the result), and
Pass, Fail and Note buttons where you hold `checkout` for the point's area.
A fail or a note asks why. An output the sheet forces has a **Force…** button:
the value (on or off, or a number), how long, the point's id typed to confirm,
then your password.

While a checkout force stands, every screen shows a banner with the point, who
forced it and the time left. The time is the controller's figure from the last
frame, counted down in the browser. It never says the output was released on
its own: at zero it says "ending now", and only a frame without the force says
there is none. If the stream goes quiet, the banner says whether the force
still stands is not known.

A custom HMI app can mount `CheckoutPage` and `CheckoutBanner` from the kit
itself.

## Export

`GET /api/checkout/runs/{id}/export` gives a run as a file: `?format=json`
(the default) or `?format=csv`, for anyone who may read the run. It holds every
result, a fail before a pass included, by point in the sheet's order; the same
run gives the same bytes. The file says in its own content that it is
**unsealed** and, for a rehearsal run, that it can never be signed. A note that
a spreadsheet would read as a formula (it starts with `=`, `+`, `-` or `@`) gets
a leading apostrophe. The file is named `checkout-<run id>.json` or `.csv`.

```sh
curl -H "Authorization: Bearer $TOKEN" -o run.csv \
  "https://controller/api/checkout/runs/<run id>/export?format=csv"
```

The Checkout page has the same two links for the open run.

## Signing off

A person with the `signoff` permission (the `supervisor` role) seals a run:

```
POST /api/checkout/runs/{id}/seal     {note?}: sign the run off
GET  /api/checkout/runs/{id}/sealed   the sealed record, as a file
```

Sealing asks for a fresh password, as any elevated action does, and the
controller decides every condition itself, under the lock that takes results.
It refuses when

- the run is a **rehearsal**, or the controller cannot say its devices are
  real: it has no field driver configured (it runs the in-memory loopback),
  every endpoint it dials is a loopback or one of the project's own virtual
  devices, or a point is on a device at such an address. A run's rehearsal flag
  is the client's, so the controller derives this itself and records what it saw
  (the drivers and device endpoints) in the seal;
- the **sheet changed** since the run began (`checkout.yaml` was edited and
  deployed): a seal signs the results against one sheet, so abandon the run and
  begin again;
- a **checkout force** is standing or being started or ended;
- a point has **no result yet**, or its latest result is a **fail** (a later
  pass supersedes a fail; both stay in the file and in the record);
- under the default `signoff: two-person`, the signer **recorded results** in
  the run. `signoff: single` lets the recorder sign; `naut check` warns, and the
  sealed record says "single-person sign-off".

A sealed run takes no more results, forces or abandon; a new run may begin.

The sealed record is a file: the run's export (every result, with `sealed:
true`), a payload (who signed off and under which rule, who recorded, the
signer's note, the controller's evidence about its devices, where the seal sits
in the results file, and the hash of the export) and the controller's
signature over the payload, with the public key. The key is an Ed25519 key the
controller makes in its state directory (`checkout.key`, mode 0600) when the
checkout first opens; `GET /api/checkout` shows its fingerprint, so write it
down from the controller ahead of time.

```sh
naut checkout verify checkout-<run id>-sealed.json \
  --chain /var/lib/nautilus/checkout.jsonl \
  --fingerprint <the key's fingerprint>
```

`verify` checks the signature, that the payload names the key the record
carries, and that the export is the one the payload hashes. With `--chain` it
also checks the results file's own chain, that the seal is in it where the
record says, and that the export holds exactly the results the file holds for
the run. With `--fingerprint` it checks the key is the one you recorded. With
`--audit` (the controller's audit journal, a `.jsonl` file or its directory, or
a copy kept off the box) it finds the accepted `checkout.seal` entry that
carries this record's hash: the journal is a second, hash-chained place the
seal is written, and a site that copies it off the box (`audit.copy`) has an
anchor the state directory's owner cannot rewrite.

**What this is, and is not.** It is a tamper-evident record signed by the
controller: the signature proves the record came from that controller's key and
was not altered since. The people it names were identified by the controller's
sign-in (a user name and password, and a fresh password to sign off), not by a
personal certificate. It is **not** an electronic signature in a legal or
regulatory sense, and nothing here claims it satisfies one. Anyone who holds the
controller's state directory (the results file and the key) can forge both, so
keep a copy of the sealed record and the results file somewhere else when a run
is signed off: a copy made elsewhere is what makes an edit show. The seal's
hash is also written in the audit journal (the `checkout.seal` entry's `after`),
so a site that copies the audit journal off the box has an independent anchor
for `verify --audit`.

## Not yet

- The printable report and a Seal button on the Checkout page (sealing is the API and the command line for now).
- A redundant pair: its controllers answer that the checkout is not available
  yet, rather than keep results on one box.
