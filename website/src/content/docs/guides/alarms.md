---
title: Alarms
description: ISA-18.2 alarm state from BOOL tags — active list, acknowledge, shelve, journal — declared in nautilus.yaml, served at /api/alarms, and asserted in virtual time.
---

An alarm engine is a small thing when alarms are just tags. The bits are
already there: your logic compares a measurement to a limit every scan,
or an edge node publishes a `.HH` member it computed itself. What is
missing is the part a *person* needs — an active list, who acknowledged
what, a shelf that expires, and a record of what happened.

That is what `alarms:` adds. Declare it in `nautilus.yaml`, and the
controller grows five `/api/alarms*` routes, a counts-only summary on
every stream frame, and an alarm journal — with no engine, no ticker, and
no opinion of its own about your control logic. A project with no
`alarms:` section behaves exactly as it did before this existed.

## The shape

```yaml
# nautilus.yaml
alarms:
  site-from: "^([A-Za-z]+[0-9]+)_"   # RTU9_ZONE15_FIT_001 → site RTU9
  area-from: "^[A-Za-z]+[0-9]+_([A-Za-z]+[0-9]+)_"   # → area ZONE15

  defaults: { ack-required: true, auto-clear: true, shelve: true }
  shelve-times: [5m, 15m, 30m, 1h, 2h, 4h, 8h]
  journal: { keep: 500 }             # + optional sink: file | postgres
  notify:
    - log: true                      # events also land in the log stream

  rules:                             # generate definitions in bulk
    - match: { type: AnalogInput, member: HH }
      name: "{desc} High High"
      priority: high
      on-delay: 5m
      enable: "{site}__Online"
      class: process

  defs: []                           # or explicit entries, one per condition

alarm-files:                         # generated sets, in their own artifact
  - alarms/rtu9.yaml
```

`examples/lift-station` is this, complete and runnable — fourteen explicit
`defs:`, no rule expansion needed at that size:

```sh
naut check examples/lift-station         # validate the rules, offline
naut alarms list examples/lift-station   # see what they expanded to
naut test examples/lift-station          # the acceptance suite
naut run examples/lift-station           # dashboard + /api/alarms
```

`examples/remote-fleet/scada` is the `rules:` half of the same feature:
**one** rule (`match: { type: Pump, member: Fault }`) expands across every
pump on all three sites' shared `Pump` Template, with `enable:
"{site}__Online"` suppressing a dark site's alarms instead of leaving them
lit on their last-known reading.

## Rules versus definitions

A **definition** is one alarm: an id, a BOOL condition path, a priority,
and the operator policy. A **rule** generates definitions in bulk by
matching a struct type and a member.

The reason rules exist is arithmetic. A fleet is the same handful of UDTs
repeated across a few hundred instances — an analog input, a motor, a
valve — so a dozen `(type, member)` pairs account for the overwhelming
majority of a site's alarms. Writing them out is a generated file nobody
reads; writing the dozen rules is a page you can review.

```yaml
rules:
  - match: { type: AnalogInput, member: HH }   # every AnalogInput tag's HH bit
    priority: high
  - match: { tag: "*_YA" }                     # every standalone alarm contact
    priority: critical
```

`match:` takes `type`, `member` and `tag`, all globs. A rule **with** a
`member` matches struct members; a rule **without** one matches flat BOOL
tags — a struct tag is not itself a BOOL, so "which member" is never a
detail a rule can leave out. Rules are tried in file order and the **first
match wins**, and the whole expansion happens once, at load, never per
scan.

`id`, `name`, `site`, `area`, `display` and `enable` are templates over
`{tag} {member} {site} {area} {desc} {type} {path}`. An unknown
placeholder is a load error — `{sight}` failing the build beats `{sight}`
reaching an operator screen as literal braces.

The rest are ordinary definitions, one per condition, and they belong in
`alarm-files:` when a generator emits them:

```yaml
# alarms/rtu9.yaml — a bare YAML list, exactly like a tag file
- id: RTU9_ZONE15_INT_001_YA
  tag: RTU9_ZONE15_INT_001_YA     # a flat tag, or "tag.member"; BOOL, true = in alarm
  name: RTU 9 Zone 15 Intrusion
  priority: critical             # diagnostic|low|medium|high|critical
  class: security
  site: RTU9
  area: ZONE15
  on-delay: 5m
  enable: RTU9__Online
```

`alarm-files:` mirrors `tag-files:` in every respect, including the part
that matters: an id declared in two sources is an **error naming both**,
never last-wins. Last-wins reads fine on the day it is written and rots
silently the first time the generator that emitted one side changes.

`naut alarms list` dumps the whole expanded set (`-o yaml` gives YAML
a manifest could take back verbatim; `-count`, `-site` and `-priority`
narrow it). That is the deal: a dozen rules covering two thousand alarms
is a good trade only because you can see the two thousand.

## States

Six states, the ISA-18.2 ones. The two axes are *is the condition present*
and *has an operator seen it*, which is why return-to-normal is not the
end of an alarm's life.

| from | event | to |
| --- | --- | --- |
| Normal | condition true, held `on-delay` | **Unack-Active** |
| Unack-Active | ack | Ack-Active |
| Unack-Active | condition false, held `off-delay` | **Unack-RTN** |
| Ack-Active | condition false | Normal — or Unack-RTN with `auto-clear: false` |
| Unack-RTN | ack | Normal |
| Unack-RTN | condition true again | Unack-Active |
| any | shelve *until* | **Shelved** — prior state restored on expiry |
| any | `enable` false, or the tag is missing | **Suppressed** |

`ack-required: false` collapses Unack-Active → Ack-Active and Unack-RTN →
Normal on arrival: the annunciate-only contacts that have no operator
workflow at all. `auto-clear: false` latches — the alarm happened, and
clearing the field bit does not discharge the obligation to see it.

**A shelf always expires.** There is no permanent shelf, because a
permanently silenced alarm is indistinguishable from a broken one; and it
restores the state it interrupted rather than dropping to Normal, so
shelving is never a back-door acknowledgement.

**Suppressed is the answer to a dark site.** Host tags hold their last
values, so a site that dies with four alarms up keeps them up forever.
Point `enable:` at that node's online flag — `enable: "{site}__Online"` in
a rule — and the whole site moves to Suppressed instead. A definition
whose tag does not resolve at all (a site that has never birthed) is
Suppressed with a reason, never an error: one dark site must not fault the
engine.

## Ack is host state

Acknowledgement and shelf are **never written back to the edge**. An ack
is meaningless to a site that is offline, which is exactly when operators
ack most; writing it back would make it a control action subject to
store-and-forward ordering; and it would not survive the node's next
birth.

They persist through [retain](/guides/redundancy/) instead, beside the
operator setpoints, which buys redundancy correctness for free: a standby
re-reads the retain store on the leadership edge, so a failover cannot
resurrect four hundred acked alarms as unacked. Ack and shelf persist,
and so does the **occurrence** of an alarm whose condition is present:
its ID, when it activated, and when it began waiting on a person. The
state itself re-derives from the field, because a retained "active"
would be a claim about the plant made by a file. A kept occurrence
continues only if the field asserts the condition again; it then keeps
its activation time and its escalation level, and its ack applies to it
alone. A false reading ends it (a standalone controller allows a minute
for drivers that have not read yet, but no ack carries through it), as
does `enable` going false, and a condition that stays unreadable for two
minutes (a remote site not yet birthed). A pair carries occurrences
through its operator-intent log rather than retain; see
[Redundancy](/guides/redundancy/).

### Occurrences

An occurrence runs from an activation out of normal to the next return
to normal. A flap between unack-active and unack-rtn is the same
occurrence: it is one wait on a person, discharged by one ack. Every
alarm row (`occurrence` on `GET /api/alarms`) and every journal row names
its occurrence, a 16-character random hex ID that is never reused. An
ack may name the occurrence the person saw:

```
POST /api/alarms/ack  {"ids":["TT101_HI"],"by":"op","occurrences":{"TT101_HI":"5f2c09d1e7a4b366"}}
```

If the alarm returned to normal and activated again since, the ack is
refused (`409`, "a new occurrence since; open the HMI to see it") and
nothing is acknowledged. An id the map does not name is acked on
whatever occurrence it is on, as before.

An edge-side latch reset (`M.FAILRST`, a horn acknowledge) is a different
thing and stays an ordinary tag write. It is not the same button as
"Ack".

## The API

```
GET  /api/alarms                    {"ts":…,"summary":{…},"alarms":[…]}
GET  /api/alarms/journal?from&to&site&priority&id&kind&state&q&limit
POST /api/alarms/ack       {"ids":["RTU9_ZONE15_FIT_001.HH"],"by":"operator1"}
                           {"all":true,"by":"operator1"}
POST /api/alarms/shelve    {"id":"…","seconds":1800,"by":"operator1"}
POST /api/alarms/unshelve  {"id":"…","by":"operator1"}
```

Responses and status codes are in the
[HTTP API](/guides/http-api/#alarms) reference. Reads follow the same
Host rule as every other read. Writes go through the same
authorization as a tag write — `Authorization: Bearer` or
`X-Nautilus-Token`, same-origin by default — and a standby forwards them
to the leader exactly the way it forwards a tag write.

`by` is an **audit string the server does not authenticate**. Nautilus has
one token, not user accounts: the HMI supplies the operator's name and the
journal records what it was told. That is worth saying plainly rather than
implying an identity the system does not have.

Every stream frame gains one optional field:

```json
"alarms": { "active": 3, "unacked": 1, "shelved": 0, "suppressed": 12,
            "byPriority": {"high": 2, "medium": 1}, "worst": "high",
            "newest": {"id":"…","name":"…","priority":"high","ms":…},
            "rev": 41 }
```

Counts only — never two thousand rows on a 250 ms tick. `rev` bumps on any
state change, so an HMI refetches the full list exactly when something
moved and never otherwise. `GET /api/meta` reports `alarms: true|false`
and the offered `shelveTimes` (in seconds), so a screen knows whether to
render a banner at all instead of finding out by taking a 404.

With no `alarms:` section, all five routes answer **404** and the frame
carries no `alarms` field. That is deliberate: an empty 200 would look
like a quiet plant.

## The journal

Every state change produces exactly one event: `active`, `rtn`, `ack`,
`shelve`, `unshelve`, `suppress`, `unsuppress`. An escalation's sends are
journalled too, as `notify` rows (see [Escalation and on-call](#escalation-and-on-call)).

An in-memory ring is **always on**, in front of any sink, so the journal
page works on a box with no database at all — and it is bounded by
construction, which is the answer to a flapping storm across a couple of
thousand alarms. `journal: {keep: 5000}` sets its depth.

Two durable sinks are available:

```yaml
journal: { sink: file, path: alarms.jsonl }        # rotated JSONL
journal: { sink: postgres, dsn-env: NAUTILUS_ALARM_DB_URL }
```

`dsn-env` names the **environment variable** holding the connection
string, never the string itself: a manifest is committed, and a database
password in git is a password you have to rotate. It defaults to
`NAUTILUS_ALARM_DB_URL`, falling back to the historian's `DATABASE_URL`,
since `alarm_events` and `samples` belong in the same place. The table is
created idempotently at startup, the same one-DDL-string idiom the
historian uses.

`notify:` is the ship-it-somewhere seam — `- log: true` and
`- webhook: https://…` (with `header-env:` for its API key). Notifiers run
off the scan goroutine on a bounded queue: a slow endpoint drops events
with a counter rather than stalling the scan, because a controller that
waits on an HTTP call is a controller that stops controlling.

## Escalation and on-call

`notify:` ships every event. Escalation is different: it pages *people*
about an alarm nobody has acknowledged, level by level, until someone
does.

```yaml
alarms:
  oncall:
    timezone: America/Chicago        # rosters and windows read in this zone
    rosters:
      - { id: operators, members: [alice, dave] }          # everyone, always
      - id: standby                                        # one at a time
        members: [bob, erin]
        rotate: weekly                                     # or daily
        start: 2026-11-02                                  # bob's first week
        handover: "07:00"
      - id: crew                                           # by shift
        shifts:
          - { days: [mon-fri], from: "06:00", to: "18:00", members: [alice] }
          - { days: [mon-fri], from: "18:00", to: "06:00", members: [dave] }
  escalation:
    - id: process
      match: { class: process, priority: [high, critical] }  # also id, site, area
      # when: { days: [mon-fri], from: "06:00", to: "18:00" }  # optional
      levels:
        - { after: 0s,  notify: [roster:operators], via: [email] }
        - { after: 10m, notify: [roster:standby],   via: [sms] }
        - { after: 30m, notify: [carol],            via: [voice, email] }
      repeat: { every: 15m, times: 2 }   # the last level again
      stop: ack                          # or ack-or-rtn
```

- **People, not addresses.** A level names a roster (`roster:<id>`, read
  when the level is sent, so a handover between levels pages the new
  person) or a person by principal: a user name, or `oidc:<name>`. Email
  addresses and phone numbers never go in the manifest.
- **The first policy that covers the alarm runs it.** `match:` fields AND
  together; `when:` is read once, at the moment the alarm began waiting, and
  the policy then runs to the end even past the window.
- **The clock starts when the alarm begins waiting on a person,** not at
  its latest activation: a latched alarm that flaps between unack-active
  and unack-rtn keeps climbing. An acknowledgement, a shelf or a
  suppression ends the escalation; with `stop: ack-or-rtn`, so does a
  return to normal.
- **Only the controller holding the write gate pages.** The gate is asked
  immediately before every send, so a standby sends nothing, and a leader
  that loses its lease sends nothing more. A controller that wins the
  lease again with an escalation already under way sends the level now
  due, once, never a burst of the earlier ones; the journal says
  `joined late`. The escalation clock survives a restart and a takeover
  for an occurrence the field still asserts: the new leader (or the
  restarted controller) sends the current level once more, journalled
  `resend after restart` or `resend after takeover`, and the next level
  falls due on the original clock. An occurrence that began within one
  log tick (half a lease) of a leader's death is not carried: the new
  leader starts it afresh at level 1, which re-pages rather than missing
  a page.
- **A roster is read once per level,** when that level falls due. A
  handover after it pages the new person at the next level, never with
  the levels already sent.
- **A failed send is tried at most twice more,** and only for a reason
  that can pass (a timeout, no connection, the provider's own error),
  asking the write gate and the alarm again before each try: a retry
  never leaves for an alarm acknowledged meanwhile, nor from a controller
  that lost its lease. Escalation never waits on delivery: the next level,
  or a repeat, comes on its own timer. A timeout after the provider took
  the message can mean a page arrives twice; a duplicate beats a missed
  one.
- **An alarm with `ack-required: false` pages while it is active.** It
  never waits on an acknowledgement, so its levels run from its
  activation, and a return to normal (or a shelf, or a suppression) stops
  them.
- **Only `naut run` pages.** `naut test` and the acceptance harness build
  no escalation at all.
- **The browser playground has no time-zone database,** so there a
  project loads only with `timezone: UTC` (or none); `naut` itself has
  every IANA zone built in.

Every send is journalled as a `notify` row (level, the person, the
channel, and `sent`, `failed` or `nobody-on-call`), never with an
address. `GET /api/alarms/journal` leaves these rows out unless asked:
`?kind=notify`. They count against the in-memory ring's `keep` like any
other row. A channel whose last three sends all failed raises the system
alarm `nautilus.alarm-notify`, until a send on it succeeds (or this
controller stops leading).

A failed send's row says why by class only (`no-contact`, `config`,
`timeout`, `unreachable`, `tls`, `auth`, `rejected`, `provider-error`),
with the server's numeric code where there is one (`rejected (smtp
550)`): never the server's own text, which can echo an address.

### Channels

`log` is always there: it writes each page to the log stream, which is
how a policy is tried before people are paged. `alarms.channels:` adds
the ones that leave the box:

```yaml
alarms:
  channels:
    email:
      smtp: mail.example.com:587     # host:port
      tls: starttls                  # or implicit (465)
      from: alarms@example.com
      username: alarms               # password: the secret email.password
    sms:                             # a Twilio-compatible REST API
      provider: https://api.twilio.com
      account: AC0001                # token: the secret sms.token
      from: "+15550100"
    voice:                           # the same API, a call that reads the alarm out
      provider: https://api.twilio.com
      account: AC0001                # token: the secret voice.token
      from: "+15550100"
    webhook:                         # one POST per page, JSON, to a paging service
      url: https://hooks.example.com/page
      header: Authorization          # value: the secret webhook.header
    push:                            # a phone's own subscription is its address
      subject: mailto:ops@example.com
```

Nothing in it is secret, and none of it is anyone's address. Those live
on the controller, in its state directory beside the users file
(`auth.state-dir`, else `/var/lib/nautilus` under systemd), mode 0600,
never in the project or Git:

```sh
sudo naut alarms contacts set alice --email alice@example.com --sms +15550101
sudo naut alarms contacts set oidc:carol --voice +15550102 --email carol@example.com
sudo naut alarms contacts list                 # masked; --show prints them
sudo naut alarms secrets set sms.token         # read from stdin, never an argument
sudo naut alarms secrets list                  # which are set, never their values
```

On a box with a built controller, the controller answers the same
commands without starting: `sudo /opt/nautilus/current/controller alarms
contacts --state-dir /var/lib/nautilus list`. A running controller reads
a change on its next page. The webhook names the person as a principal
(`"to": "alice"`) and needs no contact. A webhook URL that carries its own
token in the path or query puts that token in Git; prefer a service that
takes it in a header (`header:` and the secret `webhook.header`). The
browser playground has no state directory, so a project there with
`alarms.channels` and escalation is refused: try policies with `log`.

- **Transport security is on.** Email runs over TLS (STARTTLS, or
  implicit) with the server's certificate verified; a server that offers
  no STARTTLS is refused, and the password is sent only once TLS is up.
  The SMS and voice provider and the webhook are https, verified, and a
  redirect is never followed.
- **A site CA:** `ca-file: /etc/ssl/site-ca.pem` on a channel verifies
  against that bundle instead of the system roots.
- **`insecure: true`** on a channel skips certificate verification, and,
  for a channel that sends no credential (email with no `username`, a
  webhook with no `header`), allows plaintext. A password or a token never
  crosses plaintext either way. `naut check` warns about it, and `naut
  run` journals it in the audit trail (`alarm.channel-insecure`) at
  every start.
- **Check them on the box** with `sudo naut alarms contacts check` (the
  built controller: `controller alarms contacts --state-dir
  /var/lib/nautilus check`): it fails when a level pages someone by
  email, SMS or voice who has no address for it (roster members
  included), or a channel's secret is not set, each with the command that
  fixes it. `naut check` makes the same findings where it can see the
  files, and warns once where it cannot (a laptop, CI). `naut run` prints
  them as warnings at every start, but never refuses to start over a
  missing number: that send fails `no-contact`, is journalled, and counts
  toward the `nautilus.alarm-notify` system alarm.
- **On a redundant pair each controller keeps its own files:** set the
  contacts and secrets on both, or the standby pages nobody the day it
  leads. Each one's start warnings say what it lacks.
- **A proxy:** the SMS, voice and webhook channels honour `HTTPS_PROXY`
  and `NO_PROXY`. TLS stays end to end through it; `naut run` journals
  which channels go through which proxy host (`alarm.channel-proxy`) at
  start, never its credentials. Through an `https://` proxy, a channel's
  `ca-file:` must also hold the proxy's CA, and `insecure: true` skips
  verifying the proxy too.
- **Push pages a person's phones.** `via: [push]` sends a Web Push to each
  phone the person subscribed through the HMI (an installable HMI,
  [guides/installable-hmi](/guides/installable-hmi/)); there is nothing to
  put in the contacts file, the subscription is the address, and a person
  with none fails `no-contact` like a missing number. `subject:` is the
  `mailto:` or `https:` address you give the push services (Google, Apple,
  Mozilla), who may use it to reach you: it is required and never
  defaulted. The controller signs each push with a VAPID key, the secret
  `push.vapid-key`, made at the first start of a controller that is not
  half of a pair (`naut alarms secrets generate push.vapid-key` makes it
  by hand). **On a pair the key must be the same on both boxes**: a
  phone's subscription is bound to it, so make it on one and copy
  `alarm-secrets.json` to the other (`scp -p`); `naut alarms secrets list`
  shows whether it is set, and `GET /api/push/health` (the config
  permission) counts the subscriptions the key cannot open (sealed under
  another key: kept, never sent). A subscription ends when its person
  does: a changed password, a removed or disabled user or a revoked role
  stops the pages, with no session needed; signing out does not. (A
  users file that cannot be read ends nothing: nobody is paged, and
  nothing is dropped, until it can be read. One that reads fine but is
  wrong, such as a bad deploy of `users.json` or a pair member whose
  passwords were set separately, would fail many people at once, so the
  sweep **holds** a pass that would end more than half the
  subscriptions (across two or more people) instead of deleting them:
  the rows are kept, not sent, the system alarm `nautilus.push-held`
  rises and the journal says `push.hold`. Put the file right and they
  stand again by themselves. Nothing else ends them, no timer: if the
  removal is real (you removed those users on purpose), `naut alarms push
  confirm-revocations --host <controller>` ends the held subscriptions. It
  needs the config permission and a fresh password, and is journalled
  with the count. The alarm stays up until one or the other. A pair needs the same `users.json` on both boxes,
  as [auth](/guides/auth/) says; its hello check reports a difference.) An OIDC person's groups are
  those of their last sign-in on the controller: a sign-in refreshes
  them, and a subscription not refreshed within 14 days stops, so
  someone removed from a group at the provider is not paged for ever. A push
  is sent only by the leader, directly (no `HTTPS_PROXY`: the controller
  checks what the push service's name resolves to, and refuses a private
  address unless `allow-hosts:` names it, for a site's own relay).
  `allow-hosts:` also narrows where it may send at all.
  Subscriptions are kept in the retain store with their endpoint and keys
  sealed (AES-GCM under a key derived from the VAPID key): the file is not
  secret, and a ConfigMap is not a Secret, so what is in them is useless
  without the box's secrets file.
- **Each channel kind has its own sender,** so a mail server that hangs
  delays only email, never an SMS.

### Acknowledging from the page

Give the controller the address of the HMI as a person paged reaches it,
and every email and SMS page carries a link that acknowledges the alarm:

```yaml
alarms:
  oncall:
    ack-url: https://hmi.plant.example:8443
    # ack-from-notification: sign-in    # the default is link
```

- **Opening the link changes nothing.** It shows a page with the alarm
  and an **Acknowledge** button; the button acknowledges. A mail scanner
  that opens every link in a message acknowledges nothing.
- **The link acts as the person it was sent to,** for that one
  occurrence of that one alarm, once, for at most 24 hours. The alarm
  journal records them as the one who acknowledged it, and the audit
  trail records `alarm.ack` under them with `via: link`, the occurrence,
  the level and the channel. A link is a credential: anyone who holds the
  message can use it, as with any paging service.
- **The person must still be allowed to.** Under an `auth:` block, the
  link works only while the person is an enabled user who holds `ack` for
  that alarm (an OIDC person: has signed in on the controller, and holds
  it by name, since their groups are known only from a sign-in; a browser
  signed in as them acts with its session). Otherwise the page says why
  and acknowledges nothing. A browser signed in as them whose request
  did not come from the controller's own page (a TLS proxy not in
  `server.trusted-proxies`) has its sign-in ignored: the link alone
  acts, and the audit entry says so.
- **`ack-from-notification: sign-in`** makes a site require a sign-in:
  the page asks the person to sign in as themselves, and only then
  acknowledges. Behind a TLS proxy, list it in
  `server.trusted-proxies`, or the press is refused (the refusal says
  so). It needs someone able to sign in (`auth:` users or OIDC).
- **The link stops working** once it has been used, once the alarm
  returned to normal and activated again (a new occurrence), or after 24
  hours. The page then says to open the HMI.
- **https only.** The link must not cross the network in plaintext:
  `ack-url` is `https://`, unless `ack-url-insecure: true` accepts that
  (warned by `naut check`, journalled as `alarm.ack-url-insecure` at
  every start). With no `ack-url`, pages say to acknowledge in the HMI.
  A host name in `ack-url` must be one the controller answers by
  (`server.allowed-hosts`, or `NAUTILUS_ALLOWED_HOSTS` on the box; an IP
  address always is): `naut check` warns when the manifest does not list
  it.
- **A voice call carries no link,** and neither does a webhook: it goes
  to a system, which must not act as the person. A call can take a
  keypress instead (below).
- **On a redundant pair** both controllers sign links with a key they
  derive from `NAUTILUS_PEER_TOKEN`, so a link sent before a takeover
  works after it, and a link used on one is refused on the other: the
  acknowledgement's entry in the operator-intent log names the link. A
  controller that is not half of a pair keeps its key in the state
  directory (`alarm-link.key`, 0600, made at the first start that needs
  it; deleting it ends every link already sent). `ack-url` may name
  either controller of a pair, or an address in front of both: a standby
  passes the link to the leader. Make `NAUTILUS_PEER_TOKEN` long and
  random (`naut redundancy new-pair` does): each link is signed under a
  key derived from it, and `naut run` warns about one shorter than 32
  characters.
- **The token is in the link's path,** so a reverse proxy in front of the
  controller writes it to its access log, where it stays usable until it
  is used or its 24 hours run out. Keep such logs as private as the
  pages.

### Replying to an SMS, pressing 1 on a call

The SMS and voice provider can also bring an acknowledgement back: a
reply to an SMS page, a keypress during the call. It is off until the
manifest turns it on:

```yaml
alarms:
  oncall:
    inbound:
      url: https://hmi.plant.example:8443   # the controller as the provider reaches it
      channels: [sms, voice]                # either, or both
```

- **SMS.** Each SMS page then ends "Reply ACK 12345678 to acknowledge."
  Set the number's messaging webhook at the provider to
  `<url>/alarm-inbound/sms` (POST). A reply `ACK 12345678` (or just the
  digits; case, spaces and dashes do not matter) acknowledges that
  occurrence as the person the sender's number belongs to, and the
  provider texts back "Acknowledged …". The code is the credential,
  since a sender's number can be forged: it names one occurrence of one
  alarm for one person, and lives for 24 hours after the page. Two pages
  of one occurrence to one person within the same hour carry the same
  code.
- **Voice.** The call says "Press 1 to acknowledge." and the provider
  posts the key to a URL the call names (no setup at the provider).
  1 from the number called acknowledges as the person called; another
  key does nothing.
- **Only the provider is believed.** Every request must carry the
  provider's signature (`X-Twilio-Signature`), under the channel's own
  token (`sms.token`, `voice.token`), over the configured `url` plus the
  path: never over the address the request arrived at. So `url` must be
  exactly what the provider calls (scheme, host, port), https only, and
  its host must be one the controller answers by (`server.allowed-hosts`;
  `naut check` warns). A request that does not check is refused with
  403 and journalled.
- **Who replied.** The sender's number must be the SMS contact of exactly
  one person in the contacts file. A number nobody has gets no answer at
  all; a number two people share is told to use the link or the HMI.
  Five wrong codes from one person in an hour, and their codes are
  refused until the hour has passed.
- **Then it is a link.** The person must still stand here and hold `ack`
  for the alarm; the acknowledgement takes the same path as a link (the
  pair's operator-intent log, spent once, across a takeover), and the
  audit entry is `alarm.ack` with `via: sms` or `via: voice`. Neither the
  number nor the message's text is ever journalled or logged.
- **The provider always hears 200** with a TwiML answer, a refusal too
  (otherwise it sends no reply and plays the caller an error); the audit
  entry keeps the refusal's own status and reason.
- **Refusals are journalled** as any refused write is: the first five a
  minute from one address for one person in full, the rest folded into
  one summary entry (the provider calls from a few addresses, so a flood
  of forged requests costs the trail a summary, not a row each).
- **While no controller leads** (an election), a reply gets no answer
  (the standby has not checked who sent it) and a keypress hears "not
  acknowledged"; try again in a minute.
- **Not with `ack-from-notification: sign-in`:** a reply cannot sign in,
  so the manifest is refused with both.

Check a policy offline:

```sh
naut alarms oncall -at "2026-11-23 06:55" .         # who each roster has on call
naut alarms escalation .                            # alarms covered, per policy
naut alarms escalation -id LSHH101 -at "2026-11-23 06:55" .   # the steps one alarm would page
```

## Rationalization and performance

ISA-18.2 asks for every alarm to be **rationalized**: someone decides
what happens if nobody responds (the consequence), what the operator does
about it (the response), and whether its priority is right, and the
answers are kept in a *master alarm database*. In nautilus those columns
live on the definition itself, so they are reviewed in Git with the rest
of the project:

```yaml
defs:
  - tag: LSHH101
    name: Wet-well high-high (LSHH-101)
    priority: critical
    consequence: Wet well overflows to the street within about 20 minutes
    response: Confirm both pumps are running; call out the on-call technician
    rationalization: { state: approved, by: alice, at: 2026-11-20 }
```

`state:` is `pending` (the default, so an alarm that says nothing is
pending), `reviewed` or `approved`; a reviewed or approved alarm needs
`by:` and `at:` (the day, `YYYY-MM-DD`), and `naut check` refuses one
without them. A rule takes the same three keys and copies them to every
alarm it generates, so rationalizing a UDT's high-high once covers each
instance. `consequence:` and `response:` are plain text, not templates.

The master itself is a flat table a rationalization team can review in a
spreadsheet, exported from the manifest and checked back against it:

```sh
naut alarms master export . -f master.csv     # one row per alarm, sorted by id
naut alarms master check  . -f master.csv     # exit 1 if anything differs
```

Each row carries the alarm's id, name, condition, priority, class, site,
area, consequence, response and review state, and a `source` column that
says where the alarm is declared (the manifest, an alarm file, or the
rule that generated it), which is where a change has to go. `check` lists
every alarm only one side has and every column that differs, so a master
the team signed off cannot drift from what the controller runs without
someone seeing it. Both commands print how many alarms are pending,
reviewed and approved, and name the high and critical ones still
pending. A YAML master (`-f master.yaml`) holds the same columns. When a
spreadsheet opens the CSV, keep the `at` column as text: a spreadsheet
that turns `2026-11-20` into its own date format makes `check` report a
difference.

How the alarm system performs is measured from the [journal](#the-journal):

```sh
naut alarms report flood      -journal /var/lib/nautilus/alarms.jsonl
naut alarms report chatter    -host plc1
naut alarms report bad-actors -host plc1 -since 30d
naut alarms report stale      -host plc1
```

| report | what it measures (ISA-18.2) |
| --- | --- |
| `flood` | alarms per 10-minute window and per hour; every flood: more than 10 alarms in a window, with consecutive windows counted as one flood and the alarms that contributed most |
| `chatter` | alarms that activate 3 or more times within 60 seconds, and how many separate bursts |
| `bad-actors` | the 10 most frequent alarms (`-top N`) and their share of all alarms |
| `stale` | alarms continuously active for more than 24 hours |

`-journal` reads a file journal (and its rotated `.1`) without writing
anything; `-host` reads a running controller's journal API, whatever its
sink. The range is `-from` up to (not including) `-to`, or the last
`-since` (7 days by default, 90 for `stale`); `-site`
and `-priority` narrow it, and `-o json` prints the numbers for a
dashboard. Each report first says how many events it read and the
earliest and latest of them: a journal that holds only the last few
hours (the in-memory ring, or a file that rotated) says so instead of
reading as a quiet week.

Every journal row names its occurrence, and a restart that continues an
occurrence journals no new `active`, so the reports count each `active`
row as it comes (a flap through unack-rtn included: that is chattering).
Rows written before occurrences, by an engine that journalled `active`
again for a standing alarm at every start, are read the old way: an
`active` with no `rtn` before it is the same occurrence, so a restart
neither looks like a flood nor resets a stale alarm's age. To
recognise an alarm that was already standing when the range began, a
report also reads the 7 days before `-from`. Flood windows start at
`-from`. A report exits 0 whatever it finds; use `-o json` to act on
the numbers in a script.

## On the HMI

The kit ships three components and a client. Props in, callbacks out, no
fetching in the components:

| component | props |
| --- | --- |
| `AlarmBanner` | `summary`, `now`, `onclick?` — counts by priority, worst first, newest unacknowledged name |
| `AlarmTable` | `alarms`, `now`, `onack`, `onshelve`, `shelveTimes`, `sites?` |
| `AlarmJournal` | `events`, `from`, `to`, `onrange`, filters |

`createAlarmClient(realtime)` wires them up: it watches `frame.alarms.rev`
and refetches `/api/alarms` only when something moved, exposes
`active` / `shelved` / `summary` as runes, and flips `supported` to
`false` on that 404 so a controller without alarms renders nothing rather
than an error.

Priority never travels as colour alone — every one carries a label and a
glyph, and the palette reuses the theme's existing `--crit` / `--serious`
/ `--warn` / `--ink-2` / `--muted` roles.

## Testing

Alarm state is host state: it does not live in the tag store, and no ST
expression can see it. So acceptance tests get one key and three verbs of
their own, and virtual time makes an `on-delay` an exact assertion:

```yaml
- given: { RTU9_ZONE15_FIT_001.PV: 120.0 }
  advance: 4m                    # the bit is set; the ALARM is not, yet
  alarms: { active: [], unacked: 0 }
- advance: 1m1s                  # ... and now the on-delay has elapsed
  alarms:
    active: ["RTU9_ZONE15_FIT_001.HH"]
    unacked: 1
    state: { RTU9_ZONE15_FIT_001.HH: unack-active }
- ack: { ids: ["RTU9_ZONE15_FIT_001.HH"], by: test }
  scans: 1
  alarms: { state: { RTU9_ZONE15_FIT_001.HH: ack-active }, unacked: 0 }
- given: { RTU9_ZONE15_FIT_001.PV: 50.0 }
  scans: 1
  alarms: { active: [], journal: [active, ack, rtn] }
```

The matchers are `active`, `unacked`, `shelved`, `state`, `priority` and
`journal`; the verbs are `ack:`, `shelve:` and `unshelve:`. See
[Testing](/reference/testing/) for the full table.

## Checking it offline

`naut check` validates the whole section without running anything, and
it is where the mistakes that would otherwise wait for commissioning
surface:

- a condition path naming a member the type does not have, or one that is
  not a BOOL — an **error**, because the type is right there in the
  project and nothing at run time can make `.HHH` appear;
- a tag this manifest does not declare — a **warning**, since on a
  Sparkplug host most tags arrive from the field;
- a rule that matches nothing — a **warning**: dead, or a generator that
  moved out from under it.

It prints the definition count too, which is the number to diff against
whatever generated the tag set.

## Where the engine runs

**Bits at the edge, engine at the host.** Alarm *detection* is control
logic with a scan-rate guarantee; alarm *management* is an operator
concern that must survive the edge node going dark. On a Sparkplug fleet
that split is already true on the wire — a template lands as one struct
tag and the conditions are its members.

But the same engine runs on a single controller: point a definition at a
BOOL your own ladder computes and it works on a one-box `driver: memory`
project. That is the design's own smoke test, and it is what
`examples/lift-station` is.

Evaluation happens once per main scan, on the runtime's post-scan hook —
inside the scan lock, after outputs are written, when the tag store is
consistent and nothing else can be scanning. A standby skips `Scan()`
entirely, so a standby never evaluates: correct by construction.
