---
title: Alarm management
description: Set up alarm management on a nautilus controller, step by step — rationalization, on-call rosters, escalation, email/SMS/voice/push channels, acknowledging from a page, out of service, and the ISA-18.2 reports — and what each step's tests prove.
---

[Alarms](/guides/alarms/) gives a controller an alarm list, acknowledge,
shelve and a journal. Alarm management is what a site builds on top of
that: someone has decided what each alarm means, the right person is
paged when nobody answers, a pump being rebuilt does not page anyone, and
the numbers say whether the alarm system helps the operators or buries
them.

This page walks one site through all of it, in the order you would do it
on a real project. Every step runs on a laptop; the commands and their
output are real. The [Alarms](/guides/alarms/) page is the reference for
each setting; this one is the procedure. At the end, [What is tested,
and what is not](#what-is-tested-and-what-is-not) says how far the tests
go.

## Before you start

You need `naut` and a project with alarms. This page uses
`examples/lift-station` from the [nautilus
repository](https://github.com/joyautomation/nautilus): a duplex sewage
lift station with fourteen alarms and a simulated plant, so alarms really
happen.

```sh
cp -r nautilus/examples/lift-station my-station
cd my-station
naut check .
```

`naut check` ends with `naut check: 9 file(s), 0 with errors`. Every
change below goes in `nautilus.yaml`, under its `alarms:` section, unless
it says otherwise.

## Rationalize first

ISA-18.2 asks for every alarm to be rationalized before anyone is paged
for it: what happens if nobody responds (the consequence), what the
operator does (the response), and whether the priority is right. An
alarm nobody can answer should not wake anyone at 2 a.m.

The answers live on the alarm itself, so they are reviewed in Git with
the rest of the project. Replace the `LSHH101` line in `defs:` with:

```yaml
    - id: LSHH101
      tag: LSHH101
      name: Wet-well high-high (LSHH-101)
      priority: critical
      on-delay: 2s
      class: process
      consequence: Wet well overflows to the street within about 20 minutes
      response: Confirm both pumps are running; call out the on-call technician
      rationalization: { state: approved, by: alice, at: 2026-11-20 }
```

A `reviewed` or `approved` alarm needs `by:` and `at:`; `naut check`
refuses one without them. A rule takes the same keys and copies them to
every alarm it generates.

The team reviews a flat table, the master alarm database, exported from
the manifest and checked back against it:

```sh
naut alarms master export . -f master.csv
```

```
wrote 14 alarm(s) to master.csv
rationalization: 14 alarms, 1 approved, 0 reviewed, 13 pending; 13 with no operator response
high and critical still pending: BothUnavail, HighLevel, P101_FailToRun, P101_LockedOut, P101_OverTemp, P101_VfdFault, P102_FailToRun, P102_LockedOut, P102_OverTemp, P102_VfdFault
```

That second line is the to-do list. Once the team has signed a master
off, `check` tells you whenever the manifest and the master disagree:

```sh
naut alarms master check . -f master.csv     # exit 0: they match
```

```
LSHH101: consequence: master "Wet well overflows within about 20 minutes", manifest "Wet well overflows to the street within about 20 minutes" (manifest)
1 difference(s) between master.csv and the manifest
```

Exit 1 means they differ, and every difference is named by alarm id and
says where the alarm is declared. A spreadsheet that reorders columns or
adds blank rows is not a difference, but keep the `at` column as text:
a spreadsheet that rewrites `2026-11-20` as its own date format is.

## Who is on call

A roster is a list of people, by user name (or `oidc:<name>`), never by
address. Add to `alarms:`:

```yaml
  oncall:
    timezone: America/Chicago
    rosters:
      - { id: operators, members: [alice] }     # everyone on it, always
      - id: standby                             # one at a time
        members: [bob, erin]
        rotate: weekly                          # or daily
        start: 2026-11-02                       # bob's first week
        handover: "07:00"
```

A roster can also follow shifts (`shifts:` with `days`, `from`, `to`);
see [the reference](/guides/alarms/#escalation-and-on-call). Ask who is
on call at any moment, offline:

```sh
naut alarms oncall -at "2026-11-23 06:55" .
```

```
on call at Mon 2026-11-23 06:55 (America/Chicago)

ROSTER     KIND      ON CALL
operators  everyone  alice
standby    weekly    bob
```

At `07:05` the same command says `erin`: the handover is at 07:00.
Handovers on the nights clocks change are handled as instants, so a
handover in the repeated hour happens once and one in the skipped hour
happens just after the gap.

## An escalation policy, tried on the log

A policy says which alarms it covers, who is paged at each level, after
how long, and when it stops. Start with the `log` channel, which writes
each page to the controller's log instead of sending it anywhere: a
policy is tried this way before anyone's phone is involved.

```yaml
  escalation:
    - id: wet-well
      match: { class: process, priority: [high, critical] }
      levels:
        - { after: 0s,  notify: [roster:operators], via: [log] }
        - { after: 10m, notify: [roster:standby],   via: [log] }
        - { after: 30m, notify: [carol],            via: [log] }
      repeat: { every: 15m, times: 2 }   # the last level again
      stop: ack                          # or ack-or-rtn
```

- **Policies are tried in order; the first that covers the alarm runs
  it.** `match:` fields (`class`, `priority`, `id`, `site`, `area`) must
  all match; an optional `when:` limits a policy to days and hours. An
  alarm no policy covers pages nobody.
- **The clock starts when the alarm begins waiting on a person.** A
  latched alarm that flaps between active and returned-to-normal keeps
  climbing; it does not start again at level 1.
- **An acknowledgement or a shelf stops it.** With `stop: ack-or-rtn`, so
  does a return to normal.
- **A roster is read when its level falls due,** not when the alarm
  began: a handover between levels pages the new person, and never
  re-sends the levels already sent.
- **An alarm with `ack-required: false` pages while it is active** and
  stops when it returns to normal.

See what the policy covers, and what one alarm would page:

```sh
naut alarms escalation .
naut alarms escalation -id LSHH101 -at "2026-11-23 06:55" .
```

```
POLICY                ALARMS  LEVELS
wet-well              2       3
(none: pages nobody)  12

LSHH101: policy wet-well (stop: ack), if it begins waiting at Mon 2026-11-23 06:55 (America/Chicago) and nobody acknowledges it

AFTER   AT     LEVEL         WHO                       VIA
0s      06:55  1             roster:operators (alice)  log
10m0s   07:05  2             roster:standby (erin)     log
30m0s   07:25  3             carol                     log
45m0s   07:40  3 (repeat 1)  carol                     log
1h0m0s  07:55  3 (repeat 2)  carol                     log
```

Level 2 pages erin, not bob: it falls due after the 07:00 handover.

Now see a page leave. Run the station, trip the high-high float (the
example has a bench-only tag for it), and read the journal:

```sh
naut run .
# in a second terminal:
curl -s -X POST localhost:8080/api/tags -H 'Content-Type: application/json' \
  -d '{"name":"ForceLSHH","value":true}'
curl -s 'localhost:8080/api/alarms/journal?kind=notify'
```

The log shows `alarm escalation id=LSHH101 … level=1 … to=alice`, and the
journal has a `notify` row for it:

```json
{"id":"LSHH101","kind":"notify","priority":"critical","state":"unack-active","occurrence":"cd2904fdde3aea8a","level":1,"to":"alice","via":"log","result":"sent", …}
```

Every page is a `notify` row naming the level, the person, the channel
and the result (`sent`, `failed` with a reason class, or
`nobody-on-call`), never an address. The journal leaves these rows out
unless you ask for `kind=notify`. Acknowledge, and level 2 never comes:

```sh
curl -s -X POST localhost:8080/api/alarms/ack -H 'Content-Type: application/json' \
  -d '{"ids":["LSHH101"],"by":"alice"}'
```

`naut test` and acceptance tests never page anyone; only `naut run` does.
Stop the station (Ctrl-C) before the next step.

## Channels: email, SMS, voice, webhook, push

Swap `log` for real channels. The channel settings go in the manifest;
nothing in them is a secret or anyone's address:

```yaml
  channels:
    email:
      smtp: mail.example.com:587
      tls: starttls                 # or implicit (port 465)
      from: alarms@example.com
      username: alarms              # its password is a secret, below
    sms:                            # a Twilio-compatible REST API
      provider: https://api.twilio.com
      account: AC0001               # its token is a secret, below
      from: "+15550100"
```

and in the policy, `via: [email]` at level 1, `via: [sms]` at level 2 and
`via: [sms, email]` at level 3. `voice:` takes the same keys as `sms:`
(a call that reads the alarm out), `webhook:` posts JSON to a paging
service, and `push:` pages the phones people subscribed through the
[installable HMI](/guides/installable-hmi/). The
[reference](/guides/alarms/#channels) has every key.

What the channels do for you:

- **Transport security is on.** Email goes over TLS with the server's
  certificate verified, and a server that offers no STARTTLS is refused.
  The SMS, voice and webhook endpoints are https, verified, and a
  redirect is never followed. `insecure: true` on a channel turns
  verification off (and allows plaintext only where no password or token
  is sent); `naut check` warns about it and `naut run` records it in the
  audit trail at every start.
- **A failed send is tried again only when it could pass** (a timeout, the
  provider's own error), never for a refused password, and the alarm and
  the controller's leadership are asked again before each try: a retry
  never leaves for an alarm someone acknowledged meanwhile.
- **Each channel kind has its own sender,** so a mail server that hangs
  delays only email.
- **A channel that keeps failing raises a system alarm,**
  `nautilus.alarm-notify`, on the third failure in a row, until a send on
  it succeeds.

## Addresses and secrets, on the box

Addresses and passwords never go in the manifest or Git. They live in two
files in the controller's state directory, mode 0600. On a box `naut
deploy` set up that is `/var/lib/nautilus`, and you run these with
`sudo`; on a laptop the directory is under `$XDG_STATE_HOME` (usually
`~/.local/state/nautilus/<project>`), and no `sudo` is needed.

Ask first what is missing:

```sh
naut alarms contacts check
```

```
error: alarms.escalation[0] (wet-well) level 1 pages alice by email, but alice has no email address in …/alarm-contacts.json: naut alarms contacts set alice --email <address>
error: alarms.escalation[0] (wet-well) level 2 pages bob by sms, but bob has no sms address in …/alarm-contacts.json: naut alarms contacts set bob --sms <address>
…
error: alarms.channels.sms needs the secret sms.token, which …/alarm-secrets.json does not hold: naut alarms secrets set sms.token (reads it from stdin)
```

Every roster member is checked, and each error names the command that
fixes it. Run them:

```sh
naut alarms contacts set alice --email alice@example.com
naut alarms contacts set bob   --sms +15550101
naut alarms contacts set erin  --sms +15550103
naut alarms contacts set carol --sms +15550102 --email carol@example.com
naut alarms secrets set email.password     # type it; it is read from stdin
naut alarms secrets set sms.token
naut alarms contacts check
```

```
ok: every person the escalation levels page has an address for the channel, and every channel its secret (…)
```

`naut alarms contacts list` shows addresses masked (`a…@example.com`);
`naut alarms secrets list` says which secrets are set, never their
values. A secret is never taken as an argument, where it would sit in
the shell history.

`naut check` makes the same findings on a machine that holds the files,
and warns once where it cannot see them (a laptop, CI). `naut run`
refuses to start with channels and no state directory, and at every
start warns about a missing address; a page to someone with no address
fails as `no-contact`, is journalled, and counts toward the
`nautilus.alarm-notify` alarm.

## Acknowledging from a page

A page is more useful if the person can acknowledge it where they read
it. Give the controller the HMI's address as a paged person reaches it:

```yaml
  oncall:
    ack-url: https://hmi.plant.example:8443
```

Every email, SMS and push page then carries a link:

- **Opening the link changes nothing.** It shows the alarm and an
  **Acknowledge** button, so a mail scanner that opens every link
  acknowledges nothing. The button acknowledges, as the person the page
  was sent to, and the journal and audit trail say so (`via: link`).
- **A link is for one occurrence, once, for 24 hours at most.** Pressed
  again, or after the alarm returned to normal and came back, it
  acknowledges nothing and says why.
- **The person must still be allowed to.** Under an
  [`auth:`](/guides/auth/) block the link works only while the person is
  an enabled user who holds `ack` for that alarm. A site that wants a
  sign-in as well sets `ack-from-notification: sign-in`.
- **https only,** unless `ack-url-insecure: true` (warned and journalled).
- **The host must be one the controller answers by.** An IP address or
  `localhost` always is; a name must be in `NAUTILUS_ALLOWED_HOSTS` in the
  controller's environment (`/etc/nautilus/env` on a box `naut deploy`
  set up). `naut check` warns otherwise:

  ```
  warning: alarms.oncall.ack-url: the controller answers hmi.plant.example only if it is an allowed host: add it to NAUTILUS_ALLOWED_HOSTS in the controller's environment (/etc/nautilus/env on a box naut deploy set up), or a link opens a refusal
  ```

  `naut check` reads the variable where it runs, so on the box, or with
  `NAUTILUS_ALLOWED_HOSTS=hmi.plant.example naut check .`, it is quiet.

A link is a credential: anyone who holds the message can use it, as with
any paging service. A voice call and a webhook carry no link.

### By SMS reply or a keypress

The SMS and voice provider can bring an acknowledgement back. It is off
until you turn it on:

```yaml
  oncall:
    inbound:
      url: https://hmi.plant.example:8443   # the controller as the provider reaches it
      channels: [sms]                       # and/or voice
```

Set the number's messaging webhook at the provider to
`<url>/alarm-inbound/sms`. Each SMS page then ends "Reply ACK 12345678 to
acknowledge." A reply with that code, from the number in that person's
contacts, acknowledges as them; the provider texts back "Acknowledged …".
A call says "Press 1 to acknowledge." and needs no setup at the provider.

- **Only the provider is believed.** Every request must carry the
  provider's signature, under the channel's token, over the configured
  `url`. A forged or unsigned request is refused and journalled.
- **Guessing is limited.** Five wrong codes from one person in an hour
  and their codes are refused for the hour; ten wrong codes in a day and
  the controller suspends SMS acknowledgements (system alarm
  `nautilus.sms-ack-suspended`) until the oldest is a day old. Links and
  the HMI still work.
- Neither the number nor the message text is ever journalled.

The [reference](/guides/alarms/#replying-to-an-sms-pressing-1-on-a-call)
has the rest: shared numbers, a takeover mid-reply, what the provider
hears.

### By push

`via: [push]` pages each phone the person subscribed through the
installable HMI. The page carries its link inside the encrypted push, and
**a tap never acknowledges**: it opens the confirm page, where the
person presses Acknowledge. A subscription ends when its person does (a
changed password, a removed or disabled user), not when they sign out,
so on a shared device turn notifications off or use
`ack-from-notification: sign-in`. Setup (the `push:` channel's
`subject:`, the VAPID key, a pair) is in [the
reference](/guides/alarms/#channels) and
[Installable HMI](/guides/installable-hmi/).

## Hiding alarms that mean nothing now

An alarm that means nothing in the plant's present state should not page
anyone. Two tools, and both fail toward showing the alarm.

**Designed suppression** (`suppress-when:`) hides an alarm while a BOOL
reads true, with the reason on the row. Here, the low-low float during a
drain-down:

```yaml
    - { id: LSLL101, tag: LSLL101, suppress-when: DrainDown,
        suppress-reason: "wet well being drained down",
        name: "Wet-well low-low (LSLL-101)", priority: medium, on-delay: 5s, class: process }
```

(`DrainDown` is a BOOL your drain-down sequence sets: declare it under
`tags:` as `{ name: DrainDown, role: state, init: false }`. Until a
program writes it, `naut check` warns that nothing binds it.) Only a readable BOOL that is true suppresses: a number, a tag
that is missing, or a substituted signal never hides the alarm, and
`naut check` refuses a `suppress-when` that is not a BOOL. A suppression
ends the alarm's occurrence, so an acknowledgement from before does not
carry past it.

**Out of service** is an operator's action on a piece of equipment: every
alarm of it is hidden until a deadline. An alarm's equipment defaults to
the tag its condition is in; in the lift station each alarm is a flat
BOOL, so group a pump's alarms by adding `equipment: P101` to each of
P-101's five definitions (and `P102` to P-102's). Then, on the running
controller:

```sh
naut alarms out-of-service P101 --host localhost --for 8h --reason "VFD swap, WO 4471"
naut alarms oos-list --host localhost
naut alarms in-service P101 --host localhost
```

```
P101 out of service until 2026-11-22T14:20:12-08:00: 5 alarm(s) hidden
EQUIPMENT  UNTIL                      BY  ALARMS  REASON
P101       2026-11-22T14:20:12-08:00      5       VFD swap, WO 4471
P101 back in service: 5 alarm(s) evaluated again
```

- **It always ends:** a week at most, and a reason and a duration are
  required. When it ends, a condition still present annunciates at once,
  unacknowledged.
- **Who:** taking equipment out needs the `out-of-service` permission over
  every alarm it hides and a fresh password; putting it back needs only
  `ack`. Both are in the audit trail. (With no `auth:` block, as here,
  `BY` is empty and writes need only the token, if one is set.)
- **A clock set back does not lengthen it,** and a restart restores no
  more than was left.
- **The controller's own alarms** (audit journal down, notifications
  failing) are never taken out of service.

A signal on a [substitute value](/guides/substitutes/) suppresses the
alarm on it too, as `signal substituted`.

## Measuring the alarm system

The ISA-18.2 performance numbers come from the alarm journal, either a
journal file or a running controller:

```sh
naut alarms report flood      -host localhost
naut alarms report chatter    -host localhost
naut alarms report bad-actors -host localhost -since 30d
naut alarms report stale      -host localhost
```

| report | measures |
| --- | --- |
| `flood` | alarms per 10-minute window; a flood is more than 10 in a window |
| `chatter` | alarms that activate 3 or more times within 60 seconds |
| `bad-actors` | the 10 most frequent alarms and their share of all alarms |
| `stale` | alarms continuously active for more than 24 hours |

Each report first says how much journal it read:

```
journal http://localhost:8080: 2 events from 2026-11-15 06:14 to 2026-11-22 06:14 (earliest 2026-11-22 06:13, latest 2026-11-22 06:14)
note: nothing is journalled before 2026-11-22 06:13: a quiet system, or a journal that starts there
```

The in-memory journal holds only the latest rows; for reports over weeks,
give the controller a [durable journal](/guides/alarms/#the-journal)
(`sink: file` or `postgres`). A restart that continues an alarm does not
count as a new activation, and does not reset a stale alarm's age.

## On a redundant pair

- **Only the leader pages.** The write gate is asked before every send, so
  a standby, or a leader that has lost its lease, sends nothing.
- **A restart or a takeover does not start escalation again at level 1**
  for an alarm still active: the current level is sent once more,
  journalled `resend after restart` or `resend after takeover`, and the
  next level falls due on the original clock.
- **Each controller keeps its own contacts and secrets files.** Set them on
  both; a pair whose members' contacts differ says so on the cluster
  status ("alarm contacts differ from the peer") and in each one's start
  warnings.
- **An ack link sent before a takeover works after it,** and a link used on
  one controller is refused on the other. A link that lands on the
  standby is passed to the leader.

## What is tested, and what is not

Each statement on this page is a row in the [runtime
claims](/verified/runtime/), with the tests that prove it and, where the
tests cover only part, a note. In short:

- **The issue's own acceptance test** runs the whole chain in one
  process: an unacknowledged alarm escalates through two levels on two
  channels (email over STARTTLS with a password, then SMS through the
  provider), and an acknowledgement stops the third. Variants
  acknowledge by link and by SMS reply.
- **Against fakes, not real services.** Email is tested against an SMTP
  server in the test, SMS and voice against a fake Twilio-compatible
  provider (its signature check against the provider's published
  examples), push against the RFC 8291 and RFC 8292 test vectors and a
  test push service. No test sends through a real mail provider, a real
  SMS or voice account, or a real push service, and no push has been
  delivered to a phone by a test.
- **Pairs** are tested in-process (the leadership gate, a takeover's
  resend, links across a takeover); a takeover's resend is tested on the
  escalation engine, not across two running controllers.
- **The CLI.** Each `naut alarms` command on this page has a test. The
  walk-through's `naut run` steps (a page on `log`, the journal row) were
  run by hand for this page; the tests build the escalation engine and
  dispatcher in-process.

When a claim's tests stop passing, its row on the claims page says so.
