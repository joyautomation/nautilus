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

You need `naut` on your `PATH` and a project with alarms. This page uses
`examples/lift-station` from the [nautilus
repository](https://github.com/joyautomation/nautilus): a duplex sewage
lift station with fourteen alarms and a simulated plant, so alarms really
happen.

```sh
cp -r nautilus/examples/lift-station my-station
cd my-station
naut check .
```

`naut check` ends with `naut check: 9 file(s), 0 with errors` (it may
add warnings). Every change below goes in `nautilus.yaml`, under its
`alarms:` section, unless it says otherwise. `alarms:` has one `oncall:`,
one `channels:` and one `escalation:`: when a later step shows one of
them again, add its keys to the block you already have.

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
see [the reference](/guides/alarms/#escalation-and-on-call). Handovers
on the nights clocks change are handled as instants, so a handover in
the repeated hour happens once and one in the skipped hour happens just
after the gap. A roster pages nobody until a policy uses it: that is the
next step, and the step where you can ask who is on call.

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
  all match, ignoring case (`class: safety` covers an alarm declared
  `class: Safety`); an optional `when:` limits a policy to days and hours.
  An alarm no policy covers pages nobody, and `naut check` warns of each
  one.
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

Ask who is on call at a given moment, offline:

```sh
naut alarms oncall -at "2026-11-23 06:55" .
```

```
on call at Mon 2026-11-23 06:55 (America/Chicago)

ROSTER     KIND      ON CALL
operators  everyone  alice
standby    weekly    bob
```

At `07:05` the same command says `erin`: the handover is at 07:00. Then
see what the policy covers, and what one alarm would page:

```sh
naut alarms escalation .
naut alarms escalation -id LSHH101 -at "2026-11-23 06:55" .
```

```
for an alarm that begins waiting at …

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
naut run .            # writes retain.json and audit/ beside the project
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
unless you ask for `kind=notify`. Acknowledge, and level 2, due 10 minutes after level 1, never comes:

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

and in the policy's levels:

```yaml
      levels:
        - { after: 0s,  notify: [roster:operators], via: [email] }
        - { after: 10m, notify: [roster:standby],   via: [sms] }
        - { after: 30m, notify: [carol],            via: [sms, email] }
```
 `voice:` takes the same keys as `sms:`
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
  oncall:                                 # the same block as the rosters
    ack-url: https://hmi.plant.example:8443
```

Every email, SMS and push page then carries a link:

- **Opening the link changes nothing.** It shows the alarm and an
  **Acknowledge** button, so a mail scanner that opens every link
  acknowledges nothing. The button acknowledges, as the person the page
  was sent to, and the journal and audit trail say so (`via: link`). The
  page says so above the button, and names anyone else signed in on that
  browser, so a shared screen holds no surprise. It is laid out for a
  phone.
- **A link is for one occurrence, once, for 24 hours at most.** Pressed
  again, or after the alarm returned to normal and came back, it
  acknowledges nothing and says why.
- **The person must still be allowed to.** Under an
  [`auth:`](/guides/auth/) block the link works only while the person is
  an enabled user who holds `ack` for that alarm. A site that wants a
  sign-in as well sets `ack-from-notification: sign-in`.
- **https only,** unless `ack-url-insecure: true`. `naut check` warns about
  that, and so does `naut run` as it starts (in its output, so in the log
  of a box's service), which also records `alarm.ack-url-insecure` in the
  audit trail at every start.
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
  oncall:                                   # the same block again
    inbound:
      url: https://hmi.plant.example:8443   # the controller as the provider reaches it
      channels: [sms]                       # and/or voice
```

Set the number's messaging webhook at the provider to
`<url>/alarm-inbound/sms`. Each SMS page then ends "Reply ACK 12345678 to
acknowledge." A reply with that code, from the number in that person's
contacts, acknowledges as them; the provider texts back "Acknowledged …".
A call says "Press 1 to acknowledge."; the call itself tells the provider
where to post the key, so it needs no setup at the provider.

- **Only the provider is believed.** Every request must carry the
  provider's signature, under the channel's token, over the configured
  `url`. A forged or unsigned request is refused and journalled.
- **Guessing is limited.** Five wrong codes from one person in an hour
  and their codes are refused for the hour; ten wrong codes in a day and
  the controller suspends SMS acknowledgements (system alarm
  `nautilus.sms-ack-suspended`) until the oldest is a day old; SMS pages
  carry no code meanwhile.
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
P-101's five definitions (and `P102` to P-102's):

```yaml
    - { id: P101_FailToRun, tag: P101_FailToRun, equipment: P101, name: "P-101 failed to run", priority: high, class: equipment }
```

Then, with `naut run .` going again:

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
  `ack`. Both are in the audit trail.
- **A clock set back does not lengthen it,** and a restart restores no
  more than was left.
- **The controller's own alarms** (audit journal down, notifications
  failing) are never taken out of service.

These commands, and `naut alarms report -host` below, talk to the
controller's API. Under an [`auth:`](/guides/auth/) block they need a
credential: set `NAUTILUS_TOKEN` to your personal access token (`naut
users token <you>` on the box) or a service token. There is no `--token`
flag, so a token never sits in the shell history; a refusal (401 or
403) says whether a token was sent.

A signal on a [substitute value](/guides/substitutes/) suppresses the
alarm on it too, as `signal substituted`.

## Chattering and floods, live

The controller watches for the two problems that bury operators, with
nothing to configure: the ISA-18.2 numbers are the defaults.

- **An alarm chatters** when it activates 3 or more times within 60
  seconds. A flap from unack-rtn back to active counts. Bursts whose
  windows overlap are one episode, which ends 60 seconds after its last
  burst.
- **The plant is in a flood** when more than 10 alarms activate within
  10 minutes. The flood ends once the rate has stayed at or under 10 for
  a full 10 minutes, so it does not flicker at its edge.

Change either rule in `alarms:` (both are optional):

```yaml
alarms:
  chatter: { window: 60s, count: 3 }
  flood:   { window: 10m, threshold: 10 }
```

`naut check` refuses settings the detector cannot use: a window under a
second, a chatter count under 2, or a flood threshold under 1 or of 1024
and over.

What counts is a new activation. These do not count:

- an alarm whose condition stood through a restart or a takeover and
  then activates again;
- an alarm whose condition read true before a suppression (out of
  service, an enable, a dark site) and reads true again after it. One
  that read false before and true after is new, and counts;
- an activation inside a standalone controller's first minute, while its
  drivers may still read defaults.

Chatter detection measures an alarm's own transitions, not the
equipment around it. A low-flow alarm that stays true while its pump's
enable cycles off and on has not bounced, so it is not chattering; the
pump's start/stop cycling is a different problem, which this does not
detect.

A controller coming back with fifty alarms standing is not a flood. The
journal marks those `active` rows `standing`, and the reports leave them
out as well.

A site that comes back with a dozen alarms that were clear before it
went dark is a flood: those are new to the operator. Each of them began
sometime while the site was dark, not when the controller saw it, so its
record and its `active` row say what came back (`seenWhen`: "site RTU9
came back", "equipment P101 returned to service"), and the time reads as
when it was seen.

**What you see.** While an alarm chatters, its row in `/api/alarms`
carries `chatter` (since when, how many activations, how many episodes),
and the summary counts `chattering`. During a flood the summary carries
`flood` (activations in the window, since the flood began, and the peak).
The list also carries the flood's first three alarms in time order and
the equipment the activations came from. The first alarm of a flood is
the best free guess at its cause.

**The journal.** Each episode writes a start row and an end row: `chatter`
and `chatter-end` for an alarm, `flood` and `flood-end` (id
`__nautilus.flood`) for the plant. Each row carries its counts, its
window and when the episode began. The journal view leaves them out
unless you ask for them (`?kind=chatter,flood`), as it does notify rows.

Detection marks and journals. Nothing is shelved or suppressed because
an alarm chatters or the plant floods. What changes is how escalation
pages, next.

`naut alarms report flood -sliding` finds floods in the journal by the
same rule, so the report matches the controller. Without `-sliding`, the
report counts fixed 10-minute windows from the start of its range, and
its floods can differ at the edges. Give `-window` and `-threshold` (or
`-count` and `-window` for chatter) when `alarms:` changes the defaults.

**Across a restart or a takeover,** an episode in progress carries on.
A standalone controller keeps it in its retain store, and a redundant
pair keeps it in its operator-intent log (a takeover whose log caught
up; one that did not carries none). The controller that continues the
episode writes its end row, with the episode's start and, for a flood,
its id, and with the reason "carried over a restart or takeover". It
counts activations again from the restore, because the activations
before it are not carried. So a carried flood ends one window after the
restore (for a pair, after the last time the new leader heard from a live
leader) unless the plant floods again, and its `total` and `peak` are
at least what was carried. A `-sliding` report, which reads every
activation in the journal, can end it at a different time. An episode
whose window passed while the controller was stopped ends at its first
scan, with its end row at the time the episode ended and the reason
"ended while this controller was stopped or taking over". The same holds
when both controllers of a pair were down: the log dates the episode no
later than its last entry. That last entry is the episode's start, so a
pair that restarts with no leader in between (both controllers rebooted,
or a controller that comes back as leader while the other is down) ends
an episode older than its window at the first scan, even one that was
still going when the controllers stopped. It ends early, never late:
the alarms show and page as usual. A standalone controller saves every 2 seconds,
so one killed within 2 seconds of an episode's end can write that end
row again after the restart. The record
and the summary carry `carriedMs` for a carried episode. The new
leader's journal has the end row but not the start row, which is in the
old leader's journal.

A `chatter` or `flood` row with no end row means the controller stopped
during the episode and nothing carried it: a pair whose takeover did not
catch up its log, or a replica on an older version. If the controller's clock steps back
during an episode, detection reads the step as no time passing, and the
episode's end row gives its start in the new clock's time; a `-sliding`
report over a range that spans the step can differ from the controller
there.

### Paging while an alarm chatters or the plant floods

Both are on by default, with nothing to configure. The two settings
turn them off:

```yaml
alarms:
  chatter: { page: false }        # page every bounce, as before
  flood:   { group-pages: false } # page every alarm, as before
```

**A chattering alarm pages once per level.** Some escalations start
again at every activation: a policy with `stop: ack-or-rtn`, an alarm
with `ack-required: false`, and an alarm someone acknowledges between
bounces. Left alone, each bounce of a chattering alarm pages the first
level again. Once the alarm chatters, those bounces are held, and the
alarm escalates on one clock that starts when the episode began, under
the policy its latest bounce would page under (so a chatter across a
shift change, or into a policy's `when:` window, pages the people on now).
Each level goes once, as a chatter notice ("Seal leak is chattering: 7
times in 60 s"); a level whose repeats already fell due when the chatter
is seen goes once, not once per repeat. Later levels still climb on that clock. So a fast
chatter still pages a first level that waits (say `after: 30s`), which no
single bounce would reach. A latched alarm nobody acknowledged already
pages once per level, and is not changed. The bounces before the third
(the ones that made it chatter) paged as usual. When the chatter ends and
the alarm still waits, the levels its notices sent are not sent again; a
new activation after the end pages as usual.

A chatter notice carries no ack link and no reply code, because the
occurrence they would acknowledge changes at every bounce. It links to
the alarm on the alarms page, where you can shelve it with a reason.
The link uses `alarms.oncall.ack-url`, and you sign in as usual. A
shelved or suppressed alarm sends no notice. The journal gets one
`held` notify row when the first bounce is held and one at the episode's
end, with how many bounces were held. It never writes a row per bounce.

**A flood's pages are grouped.** During a flood each alarm keeps its own
escalation clock. When a level falls due, the alarm joins that level's
next grouped page instead of paging on its own. The first grouped page
for a policy level goes at once to each person and channel the level
names. After that, the level sends at most one grouped page every 5
minutes, listing the alarms that fell due since. So an alarm that
arrives late in a long flood is still told within 5 minutes.

A grouped page says how many alarms activated in the window and lists
the alarms it is about. A long list is cut to what the channel holds
("+150 more"). It also names the first out and the equipment, and links
to the alarms page's flood focus view. It never carries an ack link, a
reply code or a keypress: acknowledge the alarms in the HMI. Each alarm
the page listed gets a notify row with the result `grouped` and the
flood's id, so who was told about what stays in the journal. If a
grouped page fails for good, its alarms page on their own to that person
by that channel (the row says `failed`, then the page's own row), and so
do they for a grouped page the channel's queue could not take. When the
flood ends, a level a grouped page already covered does not page again;
the alarm's later levels page on their own as usual. Alarms that fell due
in the last 5 minutes and were waiting for the next grouped page page on
their own when the flood ends (at most about the flood threshold of
them, since a flood ends only after a full window under it).

**Critical alarms and the class `safety` (in any case: `Safety` too)
always page on their own**, and nothing in the configuration changes that. `individual` adds more:

```yaml
alarms:
  flood:
    individual: { priorities: [high], classes: [fire] }
```

Classes compare ignoring case. `naut check` refuses an unknown priority
there, and a class no alarm has (a typo would let the alarms it meant to
name be grouped). A
critical or safety alarm that chatters during a flood pages on its own,
once per level per episode.

**After a restart or a takeover** mid-flood, the flood carries over (see
above), so the first look groups its resends. Each policy level goes
again once, as a grouped page labelled "resend after restart" or "resend
after takeover". No covered alarm is paged on its own.

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

Each report first says how much journal it read: the range, the number
of events, the earliest and the latest. On the station you started a few
minutes ago, the default week gets a note:

```
note: the first event is at 2026-11-22 06:54, 6d 23h into the range: the system was quiet before it, or the journal starts there (a new or ring-only journal).
      Rates and window counts below are over the whole range; for the journal's span alone, add -from "2026-11-22 06:54".
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
  status ("alarm contacts differ from the peer").
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
