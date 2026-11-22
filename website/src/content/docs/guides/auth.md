---
title: Users, roles & sign-in
description: The manifest's auth block — roles, areas, users and service tokens — local users and personal access tokens on the box, browser sign-in, and the audit journal that records who did what.
---

An `auth:` block in `nautilus.yaml` says who may do what on a controller.

- **The manifest, reviewed in git**, holds the roles, the areas and which user has which role.
- **The box** holds the credentials: passwords and personal access tokens, in a users file that never enters the project.
- **The [audit journal](/guides/http-api/#audit-journal)** records every change, with the user who made it. [Audit trail](/guides/audit-trail/) shows it end to end, tampering included.

## Turn it on

From a project with no `auth:` block to an operator who signs in:

```sh
naut auth init --user alice:operator   # a starter auth: block, and HTTPS (server.tls: self-signed)
naut users add alice                   # alice's password, kept on the controller (sudo on a box)
naut run                               # restart it if it was running: auth: is read at start
```

- **What `naut auth init` writes.** It adds the block at the end of `nautilus.yaml`. The block names the users you give (`--user <name>:<role>`, repeatable) and lists the other keys as commented examples.
- **HTTPS.** It also adds `tls: self-signed` under `server:`, so passwords never cross the network in the clear, and prints the certificate's fingerprint to compare with the browser's (see [HTTPS](/guides/deployment/#https)).
- **When it skips `server.tls`.** With `--no-tls` (a TLS proxy in front, or a certificate of your own), when `server.tls` is set already, when `server.trusted-proxies` names a proxy, and on a redundant pair, where self-signed can't be used. Each time it says why.
- **What it leaves alone.** Nothing else in the file changes. It refuses a manifest that already has an `auth:` block, and it checks that the result loads before writing it.
- **Next steps.** Each command prints the next step.

- **A token for a script or VS Code:** `naut users token alice`.
- **An OPC UA client that writes as itself:** `naut opcua trust <thumbprint> --as scada:operator` (see [OPC UA](#opc-ua)).
- **Restart `naut run` after a change to `nautilus.yaml`.** The `auth:` block is read at start. Changes to users and tokens (`naut users`) take effect at once.

With `auth:`, every request needs a credential, reads included, unless you set `anonymous: viewer`.

## The `auth:` block

```yaml
auth:
  anonymous: none            # what a request with no credential may do: none or viewer
  token-role: administrator  # NAUTILUS_TOKEN's role (the default)
  session: 12h               # how long a browser sign-in lasts (1m to 168h)
  elevation: 15m             # how long its password counts for the elevated permissions (1m to 12h)
  areas:
    receiving: { tags: [RCV_*] }
    pumps:     { tags: [P1*, P2*] }
  roles:
    lead:                    # a custom role
      inherits: operator
      allow: [shelve]
  users:
    akhan:  [engineer]
    jsmith: [{ role: operator, areas: [receiving] }, viewer]
  service-tokens:
    historian: viewer        # its secret: NAUTILUS_TOKEN_HISTORIAN
```

`naut check` refuses a typo here: an unknown role, area or permission, a bad tag pattern, a misspelt key.

### Roles

Each built-in role adds to the one before:

| Role | Adds |
|---|---|
| `viewer` | `read` |
| `operator` | `write` (tag writes), `ack` (alarms), `sfc` (step and transition commands), `note` ([operator notes and the shift log](/guides/notes/)) |
| `supervisor` | `shelve`, `out-of-service` (taking equipment's alarms out of service), `substitute`, `audit` (reading the journal) |
| `engineer` | `force`, `edit` (online edits, rollbacks), `activate`, `deploy` (a Logix or Siemens download), `config` (live I/O configuration, and [IO-Link](/guides/io-link/) parameter writes, journalled as `param.write` on `<port device>/<variable>`) |
| `administrator` | `users`, `override`, `redundancy` (the redundant pair's operator commands: [clearing a duplicate mark](/guides/redundancy/)) |

A custom role `inherits` one role (built-in or custom), then can `allow` more permissions or `deny` some.

### Areas

An area is a set of tag patterns. A grant with `areas:` limits `write`, `ack`, `shelve`, `out-of-service`, `note`, `substitute` and `force` to those tags (a note about a tag or an alarm, to those tags; a note on the shift log, an asset or an area needs the permission anywhere). Its other permissions still apply to the whole controller. A grant with no `areas:` covers the whole controller. An `areas:` that is empty is refused: it would cover nothing.

### Who a request is

| Principal | How |
|---|---|
| `anonymous` | no credential |
| `token` | `NAUTILUS_TOKEN`, with `token-role` |
| `token:<name>` | a service token from `service-tokens:`, its secret in `NAUTILUS_TOKEN_<NAME>` |
| `user:<name>` | a signed-in browser, or that user's personal access token |
| `oidc:<name>` | a browser signed in at the [OIDC provider](#oidc-sign-in): the username claim, pinned to one account there |
| `opcua-cert:<name>` | an OPC UA client certificate `opcua-certificates:` maps (see [OPC UA](#opc-ua)) |
| `sparkplug:<host>` | a Sparkplug command, with `sparkplug:`'s grants (see [Sparkplug](#sparkplug)) |

A token is sent as `Authorization: Bearer <token>`. With no credential, a request that needs a permission gets `401`. A principal without the permission gets `403`, and the message names its roles and the permission it lacks.

## Reads

With an `auth:` block, reading needs the `read` permission (every built-in role has it):

- **Every `GET` under `/api/`**: tags, the live stream, alarms, forces, the program, I/O, `/api/cluster`, `/api/history` and the rest. No credential is a `401`, a role without `read` a `403`, an unknown token a `401` (never taken as anonymous).
- **A page.** A browser that opens the dashboard or an HMI screen without a session is sent to `/signin`, and comes back to the page afterwards. The page's own files (scripts, styles, fonts, images) are served to anyone: they hold no tag values, though an HMI build does name its tags.
- **Without a credential**, only `GET /api/me`, the sign-in page and routes, `/healthz` and `/readyz` answer.
- **`anonymous: viewer`** lets a request with no credential read, as before `auth:` existed (a wall screen nobody signs in to). `anonymous: none` is the default.
- **An open live stream ends** when its session does: at sign-out, when the user is disabled, removed or given a new password, and when the session expires. A stream opened with a personal access token ends within about two seconds of the token being revoked or its user disabled. The page shows the stream as lost; reloading it goes to the sign-in page.

**Monitoring, scripts and other services send a token.** A probe or monitor that reads `/api/cluster` or `/api/history`, the historian, or a script reading tags needs a credential that holds `read`: a service token with the `viewer` role (`service-tokens: {monitor: viewer}`), or a personal access token of a user with that role. To find the leader without a credential, use `/readyz`.

### Probes

`/healthz` and `/readyz` answer without a credential, by any `Host`, and say one word, nothing else: no role, version or tag.

| | `200 ok` | `503 not ready` |
|---|---|---|
| `GET /healthz` | the controller serves HTTP (every replica) | never |
| `GET /readyz` | a controller without redundancy, or the leader | a standby, no leader known, or a leader whose scans have stopped |

Point a liveness probe at `/healthz` (a standby answers it, so it is never restarted for standing by) and a load balancer or floating address that should reach only the leader at `/readyz`. The deployment `naut new --deploy` generates probes `/healthz`.

## Users on the box

A user named in `auth.users` signs in with a password kept on the controller. `naut users` manages passwords and tokens there:

```sh
sudo naut users add akhan          # asks for the password twice
sudo naut users passwd akhan
sudo naut users disable akhan      # no sign-in, and none of akhan's tokens works
sudo naut users token akhan        # a personal access token, shown once
sudo naut users revoke akhan <id>
sudo naut users signout akhan      # end every browser session of akhan (oidc:<name> for an OIDC name)
sudo naut users unpin jsmith@corp.example   # an OIDC name's account was re-created at the provider
sudo naut users list               # also the OIDC pins
```

- **Where the file lives.** The users file (`users.json`, argon2id password hashes and SHA-256 token hashes) is kept in the controller's state directory: `auth.state-dir`, else systemd's `$STATE_DIRECTORY` (`/var/lib/nautilus` on a box `naut deploy` set up). It is never inside the project. On the box, the built controller answers the same subcommands: `sudo /opt/nautilus/current/controller users --state-dir /var/lib/nautilus list`.
- **Changes take effect at once.** A running controller picks a change up within a second. It journals each one (`users.change`) under the operating-system user who made it.
- **Fails closed.** If the controller can't read the file, or others may read it, no local user can sign in or use a token until it is fixed. The system alarm `nautilus.users-file` is up meanwhile.
- **A box user the manifest doesn't name can't sign in.** The file holds credentials only; `auth.users` grants the roles.

A **personal access token** acts as its user, with the user's roles. Scripts and the VS Code extension (`nautilus.token`) use one.

## Browser sign-in

Open `/signin` on the controller, enter a user name and password, and the browser is signed in until the session ends: `auth.session` after sign-in (12 hours by default, one shift plus handover), or sooner if the user signs out.

- **HTTPS only.** Password sign-in needs the request to reach the controller over TLS: `server.tls`, or a TLS proxy named in `server.trusted-proxies` (see [HTTPS](/guides/deployment/#https)). Plain HTTP is accepted only from the controller's own loopback, with no proxy in between. Anywhere else the page shows no password box, only how to reach it over `https://`.
- **One message for every refusal.** A wrong password, an unknown or disabled user, and a user the manifest doesn't name all get the same answer and take as long, so the page doesn't say which names exist. The journal records the real reason (`auth.signin`, refused).
- **Throttled.** Repeated attempts from one address are slowed (`429`, with `Retry-After`); an IPv6 client counts by its `/64`. Attempts on one name are slowed too, more loosely. No account is ever disabled by failed attempts, but **a flood of guesses at one name from many addresses keeps that user from signing in for as long as it lasts**. Their personal access tokens keep working, and the flood shows in the journal.
- **The cookie.** Over HTTPS it is `__Host-nautilus_session`: `Secure`, `HttpOnly`, `SameSite=Strict`, `Path=/` and no domain, so no other page can set or replace it. Over plain HTTP on loopback it is `nautilus_session`. Browsers send a cookie to every port of a host, so any other web service on the same address receives that plain one; sign in over HTTPS wherever another service shares the box. Either holds a random id and nothing else.
- **Writes.** A signed-in browser writes only from the controller's own page: the request's `Origin` must be the controller's exact origin, scheme included (an `http://` page cannot write to the controller over `https://`). The journal names the user and a hash of the session id, never the id itself.
- **The Host rule still applies.** A session cookie doesn't count as a credential, so the browser must reach the controller by `localhost`, an IP address or a name in `NAUTILUS_ALLOWED_HOSTS` (see [HTTP API](/guides/http-api/#authentication)).
- **Signing out** (`POST /api/signout`) ends the session on the controller, not just in the browser, and is journalled (`auth.signout`).
- **Changes on the box end sessions.** Disabling a user, changing their password or removing them ends their sessions at once. A session also remembers the password it was proved with, so one that raced a change ends at its next request.
- **A restart signs everyone out.** Sessions are kept in memory.
- **`naut run` only.** `naut logix serve` and `naut siemens serve` take tokens but not browser sign-in. Under `anonymous: none` every request to them, reads included, needs a token; they say so when they start.

The endpoints, for an HMI of your own:

| | |
|---|---|
| `GET /signin` | the sign-in page (`?next=/path` returns there afterwards, on this controller only) |
| `POST /api/signin` | `{"name", "password"}` as JSON (`200` with `user` and `expires`) or a form (`303` to `next`) |
| `POST /api/signout` | ends the session and clears the cookie |
| `POST /api/elevate` | `{"password"}`: the session's password again, for the [elevated permissions](#elevation) (`200` with `elevatedUntil`). An OIDC session has no password here: `401` with `"elevate": "oidc"` and the `url` that confirms it [at the provider](#elevation) |
| `GET /api/me` | who the request acts as, its roles and permissions (and the tag patterns of its areas, `areas`), `secure`, the session's `expires` and `elevatedUntil`, the `elevated` permissions, `signIn` (this controller offers `/signin`), `signInMethods` (`password`, `oidc`), `provider` (the OIDC provider's name), the session's `method` (`password` or `oidc`: how it is confirmed again) and `now` (the controller's clock, to judge `elevatedUntil` by) |

## OIDC sign-in

A controller can sign browsers in at your identity provider: Microsoft Entra ID, Google, Keycloak, Okta, any OpenID Connect provider. Register the controller there as a web application (a confidential client), then:

```sh
naut auth init --oidc https://login.microsoftonline.com/<tenant>/v2.0 --client-id <application id>
```

or write the block yourself:

```yaml
auth:
  oidc:
    issuer: https://login.microsoftonline.com/<tenant>/v2.0
    client-id: 0a1b2c3d-...            # the secret goes in NAUTILUS_OIDC_SECRET on the controller
    groups:                            # by group id, never by display name
      "6f1e...-ops": operator
      "93ab...-eng": [{role: engineer}]
    users:                             # by name, for a provider without groups (a personal Google account)
      jsmith@corp.example: engineer
    # username-claim: preferred_username   # the name people are known by in the journal
    # groups-claim: groups
    # redirect-url: https://plc1.plant.lan:8443/auth/oidc/callback
```

- **The redirect URI.** Register `https://<the controller's host>/auth/oidc/callback` at the provider. `naut run` prints the one to register when it starts. Without `redirect-url` the controller builds it from the address the browser used, but only for a host it trusts (`localhost`, an IP address, or a name in `NAUTILUS_ALLOWED_HOSTS`); any other is refused.
- **The sign-in page** shows "Sign in with <provider>" beside the password form (only the button when no local user is granted anything). Same transport rule as passwords: HTTPS, or the controller's own loopback.
- **Who is signed in.** A person acts as `oidc:<name>`, the username claim lowercased (ASCII letters, digits and `- _ . @ + '`; any other character is refused, never folded into a look-alike). Their roles come from `auth.oidc.groups` (each group id the token carries) and `auth.oidc.users` (their name), never from `auth.users`: `oidc:akhan` and `user:akhan` are two people.
- **Every name is pinned to one account.** A name is a label the provider lets people change, so on the first sign-in of a name the controller records which account it came from (the issuer and the account's `sub`) in `oidc-pins.json` beside the users file. A later sign-in under that name from another account is refused, and journalled. If the account was really re-created at the provider, `naut users unpin <name>` on the controller lets the next sign-in pin it again. `naut users list` shows the pins.
- **A name in `auth.oidc.users` belongs to whoever signs in with it first.** Prefer groups. When you grant by name, use a username claim people cannot change themselves (`preferred_username` on Entra, Keycloak and Okta; never `email` on Entra, which a user can edit), and after the person's first sign-in check `naut users list` (or the journal's `auth.signin` with `pinned: true`) shows the account you expected.
- **Groups by id.** Entra puts group object ids in the token, Keycloak group paths. A key with a space looks like a display name; `naut check` and `naut run` warn about it. With too many groups Entra leaves them out ("overage"): the sign-in is refused and says to emit only the groups assigned to the application.
- **Every refusal says what to do:** no grant ("add one of your group ids under auth.oidc.groups", with the ids the token carried), a provider out of reach (try again; local users still sign in with a password), a pinned name, a username claim that is missing.
- **Signing out here leaves the provider's session.** On a shared screen the next person who clicks "Sign in with …" may come back as the previous user without being asked, while the provider still remembers them. Sign out at the provider too, or set its session short for those screens.
- **The session** is the same cookie as a password sign-in's, for `auth.session`. Each request checks it against the manifest and the pin as they are now: `naut users unpin` or `naut users signout` ends it within a second. A restart of the controller signs everyone out, as with passwords. Removing someone at the provider stops their next sign-in; a session they already have runs until it ends, or until `naut users signout`.
- **No tokens for OIDC names.** `naut users token` refuses an OIDC name: a personal access token would outlive the account's removal at the provider. Scripts and VS Code use a local user's token or a service token.
- **Offline sites.** The provider must be reachable to sign in, not afterwards. Keep a local user (or `NAUTILUS_TOKEN`) for when it isn't.
- **Upgrading.** A controller now takes the users lock (`.users.lock` in the state directory) at a name's first sign-in. One left owned by root by an older `sudo naut users` makes those sign-ins fail, with a message naming the `chown`; the next `sudo naut users` gives it back.
- **Not on a redundant pair yet.** The pins are written by the box that signs a person in, and a pair doesn't share them yet (#710), so `auth.oidc` is refused with `redundancy:`. Use local users on a pair.
- **Tested against Keycloak** (`tools/keycloak/run.sh` runs the sign-in and the confirmation at the provider against a real Keycloak), in Chrome and Firefox. Entra ID, Google and Safari are not tested yet.

The hops, for a proxy or firewall rule:

| | |
|---|---|
| `GET /auth/oidc/start?next=/path` | sends the browser to the provider (state, nonce and PKCE kept on the controller for ten minutes, bound to the browser by a short `SameSite=Lax` cookie) |
| `GET /auth/oidc/callback` | the provider's answer: a page that moves the browser on to `next` (a same-site step, so the `SameSite=Strict` session cookie is sent with it); journalled as `auth.signin` |
| `GET /auth/oidc/start?elevate=1&next=/path` | a signed-in OIDC session's [confirmation at the provider](#elevation): only from the controller's own pages, with the session's cookie |
| `GET /auth/oidc/elevated?t=…` | a confirmation's second, same-site step: the session's cookie must be the one that started it; journalled as `auth.elevate` |

## Elevation

Some permissions change what the plant runs, so a signed-in browser holds them only while its password is fresh: **`out-of-service`, `force`, `edit`, `activate`, `deploy`, `config`, `users` and `override`**. Those are the elevated permissions. That includes clearing a force and rolling an edit back: clearing a force can start a motor. Taking equipment out of service hides its alarms for up to a week; putting it back needs only `ack` (over any one of its alarms) and no fresh password.

- **The window.** The password counts for `auth.elevation` (15 minutes by default, 1m to 12h) after the user last entered it, at sign-in or at re-authentication. Use doesn't extend it. It never outlasts the session; an `elevation` as long as `session` means the sign-in counts for the whole session.
- **Past it**, an elevated action is refused with `401` and `{"error": …, "elevate": true}`, and nothing is done. The error tells the user to confirm their password. An HMI asks for it (`POST /api/elevate`) and sends the action again.
- **Signing in again** (`/signin`) confirms it too: a fresh sign-in counts as elevated. A user who doesn't hold the permission at all gets a `403` and is never asked for a password.
- **Which pages ask.** An HMI built with the kit asks for the password itself (see [In the HMI](#in-the-hmi)). The built-in dashboard writes only tags, which are never elevated. The VS Code extension uses a token. An HMI of your own handles the `401` as above.
- **One browser.** Re-authenticating elevates that session only. The same user's other sign-ins (another screen) are not elevated.
- **Tokens are always elevated.** `NAUTILUS_TOKEN`, service tokens and personal access tokens are sent with every request, and their role already bounds them. The journal says which kind of credential each elevated action came with (`detail.credential`: `session`, `pat`, `token` or `service`).
- **Re-authentication** (`POST /api/elevate`) is checked like a sign-in: from the controller's own page, over HTTPS or from loopback, with one message for every wrong password. A user who holds none of the elevated permissions has nothing to confirm and is refused (`403`) before any password check. It's throttled per session, whatever address the attempts come from. Each wrong password also counts against the user's sign-in throttle. **After five wrong passwords in a row the session ends** (a stolen cookie guessing): the browser signs in again, and the journal records `auth.session-ended`. Every attempt is journalled as `auth.elevate`.
- **The journal.** Elevation doesn't depend on it. Re-authentication works while the journal is down, and so does clearing a force or rolling back once the password is fresh. Setting a force or editing the program needs the journal too, so it is still refused then (`503`), after the password check. See [When the journal cannot be written](/guides/http-api/#when-the-journal-cannot-be-written).
- **On a redundant pair** elevation lives with the session, on the leader. A standby refers `POST /api/elevate` there, like sign-in.
- **An OIDC sign-in** has no password to confirm here. It counts from when the person last entered their credentials at the provider, the ID token's `auth_time` (up to 2 minutes ahead of the controller's clock counts as now), not from the moment the browser came back: a sign-in that rode on an old session at the provider is not fresh. Past `auth.elevation` (or from the start, when the sign-in rode on an old session there) an elevated action is refused with `401` and `{"error": …, "elevate": "oidc", "url": "/auth/oidc/start?elevate=1"}`. The person confirms at the provider: the page goes to that URL (with `&next=` the page to come back to), the controller asks the provider for the credentials again whatever session it holds (`prompt=login`, `max_age=0`), and the answer refreshes **that** session; no new one starts. Then they do the action again.
  - Signing in again here does not make a stale OIDC session fresh while the provider still holds the person's sign-in there (it answers with the old `auth_time`): confirm at the provider, or sign out there first.
  - The confirmation must come back as the same account (issuer and `sub`) with the same groups, and its `auth_time` must be from after it began. Another account's credentials, groups that changed since the sign-in ("sign in again"), and a provider that did not ask again (or whose clock is more than 2 minutes behind) are refused, journalled as `auth.elevate` with `method: oidc`.
  - **A provider that sends no `auth_time`** breaks OpenID Connect here (it is required when `max_age` is asked for): the confirmation is refused and says to have the provider put `auth_time` in the ID token (on Entra ID, the optional claim `auth_time`).
  - It starts only from the controller's own pages and only with the session's cookie: a link from another site, or one opened from mail or chat, is refused, never turned into a sign-in. The confirmation dies with the session: signing out, here or with `naut users signout`, mid-way refuses it. With `redirect-url` set, it works only on the HMI opened at that URL's address (where the provider sends the browser back).

## In the HMI

An HMI built with the HMI kit (`@joyautomation/nautilus-hmi`) handles sign-in with one call in its root layout, beside the `<ConfirmDialog/>` it already mounts:

```svelte
<script lang="ts">
  import { onMount } from 'svelte';
  import { createAuth, ConfirmDialog } from '@joyautomation/nautilus-hmi';
  const auth = createAuth();
  onMount(() => {
    void auth.start();
    return () => auth.stop();
  });
</script>

<ConfirmDialog />
```

From then on every kit client (tag writes, alarms, substitutes, IO-Link parameters, history) works like this:

- **Signing in** is the controller's own `/signin` page: opening the HMI without a session goes there and comes back. The kit has no password form of its own for signing in.
- **An elevated action past its window** asks for the password in the confirm dialog, sends it to `POST /api/elevate`, and repeats the action once, exactly as it was sent. Several actions refused at once share one prompt. Cancel, and the action reports the controller's refusal. A wrong password asks again and says so. Without a `<ConfirmDialog/>` mounted, nothing is asked: the action reports the refusal, which says to sign in again.
- **For an OIDC sign-in** the dialog asks instead whether to go to the provider to sign in again (anything unsaved on the page is lost). Yes, and the whole page goes there and comes back to the same screen; `auth.notice` (and the account chip) then says whether it was confirmed, and the person does the action again: the kit never repeats it across the trip. Only the controller's own `/auth/oidc/start` is ever opened.
- **A sign-in that ends mid-shift** (signed out elsewhere, revoked, expired, five wrong passwords) is noticed at the next refused request or dropped live stream. The kit checks `/api/me`, and if the session is gone it goes to the sign-in page, which comes back to the same screen.
- **On a standby**, the page goes to the leader the controller names (`redundancy.public-urls`). Only that URL is opened, only if it is `http(s)`, and never `http` from an `https` page. The refused action is not sent again there. A second referral within 30 seconds is not followed, so two controllers naming each other don't bounce the page, and none is followed in a browser that blocks `sessionStorage` (where that guard lives); the HMI shows the leader's link instead (`auth.referral`).
- **Controls follow the role.** A write, acknowledge, shelve, substitute or parameter control the user's role doesn't hold for that tag shows disabled, and its tooltip says who and what they don't hold. Areas count: `/api/me` lists the tag patterns of the user's own areas (`areas`), and the kit matches them exactly as the controller does. An elevated action past its password window shows a lock until the password is confirmed.
- **Acknowledgements are recorded as the user.** The controller records a signed-in user's (or a personal access token's) acknowledge, shelve and unshelve in the alarm journal under that user, whatever name the page sends; the HMI's dialogs no longer ask for one. A token keeps the name its client sends, as before.
- **`<AccountChip/>`** shows who is signed in (a local user or an OIDC name), whether the password (or the sign-in at the provider) currently counts for the elevated actions (and until when), and a sign-out button. Not signed in, it links to `/signin`, or straight to "Sign in with <provider>" when that is the only way in. **`<ReferralBanner/>`** shows a standby's referral the kit didn't follow, with the leader's link. **`<AuditTable/>`** reads the journal (`GET /api/audit`) for a user who holds `audit`.
- **Without `auth:`**, or against a controller without `/api/me`, nothing changes.

**The controller decides.** What the HMI shows or greys out for a role is a convenience; every request is still checked by the controller, and its refusal is the answer.

## OPC UA

With `auth:`, an OPC UA client writes only as someone the block names. Both ways need a SignAndEncrypt channel from a certificate the server trusts (`naut opcua trust`):

- **As a user.** The client signs in with a user name and password (a UserName token), checked like a browser's sign-in, throttle included. It then writes where the user's roles allow.
- **As its certificate.** For a SCADA or historian that connects as an application, not a person, map its certificate to grants, as a service token's:

  ```yaml
  auth:
    areas:
      receiving: {tags: ["RCV_*"]}
    opcua-certificates:
      scada:
        thumbprint: 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b
        grants: [{role: operator, areas: [receiving]}]
      historian:
        subject: "CN=Historian,O=Plant"
        grants: viewer
  ```

  Name each certificate by exactly one of:
  - `thumbprint`: the 40-digit SHA-1 that `naut opcua certs` prints;
  - `subject`: its subject, exactly as `naut opcua certs` prints it.

  **Prefer the thumbprint.** A thumbprint names one certificate. A subject is whatever the certificate says about itself, so a subject mapping also grants its roles to any other certificate with the same subject that is trusted later, a lookalike approved from `rejected/` for instance. If one certificate matches both a thumbprint mapping and a subject mapping, the thumbprint wins.

  The session then writes as `opcua-cert:<name>`, within those grants, and the journal names it so.

  **One command does both steps** for a client the server has refused: it approves the certificate and maps it by its full thumbprint.

  ```sh
  naut opcua certs                                         # the refused client's thumbprint
  naut opcua trust 9f86d081884c7d65 --as scada:operator@receiving
  ```

  `--as <name>:<role>[@<area>,...]` writes the `opcua-certificates` entry into `nautilus.yaml` and changes nothing else in the file. Before it moves the certificate, it checks the name, role and area. It refuses a project with no `auth:` block (run `naut auth init` first) and a name or certificate already mapped. The client can connect at once, read-only; restart `naut run` for the grant. On a box `naut deploy` set up there is no `nautilus.yaml` beside the store: `--as` prints the lines to add in the project's checkout, then deploy it.

A trusted certificate the block does not map is **read-only**: its client signs in with a user name to write. `opcua.anonymous-writes` can't be used with `auth:` (the load refuses it). Either way, the tag must also be open in `opcua.writable`. Without `auth:`, a trusted certificate writes what `opcua.writable` opens, as before.

## Sparkplug

With `auth:`, a Sparkplug command (NCMD or DCMD) writes a tag only when both say yes:

- `sparkplug.writable` opens the tag, as before;
- the block's `sparkplug:` grants `write` on the tag's area.

```yaml
auth:
  areas:
    receiving: {tags: ["RCV_*"]}
  sparkplug: [{role: operator, areas: [receiving]}]   # or simply: sparkplug: operator
```

**Without `sparkplug:`, or with only roles that can't write (`viewer`), every command write is refused.** `Node Control/Rebirth` is not a write: it always works. `naut check` and `naut run` warn when `sparkplug.writable` opens tags and `sparkplug:` grants no write. To allow writes, add the grant and restart `naut run`.

**Who sent it is not known.** MQTT does not say who published a command: anyone who can publish to the broker can send one. So `sparkplug:` grants every command the same roles, and the journal names each one `sparkplug:<host>`, where `<host>` is `sparkplug.primary-host` when it is set, else the broker's host. That name is a label, not a proof, and each entry says so. Keep the broker's publish rights to the hosts you trust (its ACLs), and keep the grant to the areas a host should write.

Every command write is journalled, written or refused: action `tag.write`, source `sparkplug`, one entry per metric (a template's members each by their path, `Motor1.Speed`), `from` the broker's host and port. A command's entries are written together, with one fsync. A flood of refused commands is coalesced like any other refusals (5 a minute in full, then a summary), with one difference: refusals by the `auth:` block are counted per tag, apart from the write gate's, so a host retrying one closed tag can't hide an `auth:` refusal of another in a summary. Without `auth:`, nothing changes: `sparkplug.writable` alone decides, and nothing is journalled.

## A redundant pair

- **Copy the users file.** Both controllers need the same `users.json`. Copy it from one box to the other after a change, rather than adding users on each box (each box would salt the passwords differently). The pair's [hello check](/guides/redundancy/) reports a difference.
- **Sessions live on the leader.** A standby refers a browser to the leader. It answers sign-in, sign-out and any request carrying the session cookie with `503` ("open the leader"). Requests with a bearer token are forwarded as before.
- **Where to send the browser.** Give each controller's address as a browser opens it, and the standby's `503` (JSON `"standby": true`) carries the leader's in `Location` and in the JSON field `leader` (its sign-in page links there too):

  ```yaml
  redundancy:
    public-urls:
      a: https://plc-a.plant.lan:8443
      b: https://plc-b.plant.lan:8443
  ```

  Without `public-urls` the `503` names no address: the `peers:` addresses are the pair's own and are never shown to a browser.
- **Behind a load balancer or a Kubernetes Service**, a browser lands on the standby on about half its connections and gets the `503`. Give the balancer a health check on [`/readyz`](#probes) so it sends browsers only to the leader. `public-urls` names the pair's controllers `a` and `b`. The two-replica Deployment `naut new --deploy` generates has no stable pod names for it, so there the `503` names no leader and the `/readyz` health check is the way in. Its Service still sends token clients to either pod: a standby forwards their requests to the leader.
- **A failover signs browser users out.** The new leader doesn't know the old one's sessions, so its users sign in again there. The old leader ends its sessions as soon as it is a standby, so they don't come back if it leads again (journalled as `auth.sessions-ended`).
- **During an election** a controller that knows no leader refuses a new sign-in (`503`, try again shortly). Sessions it issued still work there and can be signed out. If it then leads again without ever having seen another leader, those sessions carry on: no failover happened as far as it knows.
- **`naut users signout` reaches one box.** Sessions live on the leader, so run it there.
- **Sign-out needs the leader.** If no leader is reachable, signing out can't revoke the session. It ends at the next failover or when it expires.
- **A standby never reads for a browser.** A browser's read reaching a standby gets the same `503` with the leader's address, even for `/api/cluster` and `/api/history`, which the standby otherwise answers itself.
- **A view of both controllers** (each one's `/api/cluster`, read from one page) needs a token or `anonymous: viewer`: a browser's session cookie belongs to one controller and is never sent to the other.
- **One address for the pair** (a floating address, so a browser never has to be sent anywhere) is #716. `/readyz` tells it which controller leads.

## Limits

- **A pair's controllers trust each other's journal names.** A change passed between them through the operator-intent log carries who made it. A peer that holds the cluster token is trusted to say so truthfully, just as it is trusted with the change itself.
- **A Sparkplug command is not authenticated.** Its principal, `sparkplug:<host>`, is a label: anyone who can publish to the broker has `sparkplug:`'s grants (see [Sparkplug](#sparkplug)). The broker's own access control decides who may publish.
- **OPC UA reads are not gated by `auth:` yet.** Reading, browsing and subscribing stay open to every session, anonymous ones included. OPC UA writes are covered (see [OPC UA](#opc-ua)). `naut run` prints a warning saying so whenever `auth:` is set.
- **`naut historian`'s own port is not covered.** It answers its archive by `localhost`, an IP address or an allowed name, or with `NAUTILUS_TOKEN`, as before, with no roles. Reads through the controller's `/api/history` need `read`; keep the historian's port off networks that shouldn't read the archive.
- **A standby answers some reads from its own users file.** `/api/cluster` and `/api/history` are answered by the standby itself, so a token revoked or a user disabled only on the leader can still read them there until the users file is copied to the standby.
- **Sessions live on the leader.** On a redundant pair a failover signs browser users out (see [A redundant pair](#a-redundant-pair)).
- **Not behind a load balancer that spreads requests over replicas.** A replica that isn't leading refers a signed-in browser to the leader. Point browsers at the leader (`/readyz`), or wait for the pair's floating address (#716).
