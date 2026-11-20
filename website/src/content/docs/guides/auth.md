---
title: Users, roles & sign-in
description: The manifest's auth block — roles, areas, users and service tokens — local users and personal access tokens on the box, browser sign-in, and the audit journal that records who did what.
---

An `auth:` block in `nautilus.yaml` says who may do what on a controller.

- **The manifest, reviewed in git**, holds the roles, the areas and which user has which role.
- **The box** holds the credentials: passwords and personal access tokens, in a users file that never enters the project.
- **The [audit journal](/guides/http-api/)** records every change, with the user who made it.

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
| `operator` | `write` (tag writes), `ack` (alarms), `sfc` (step and transition commands) |
| `supervisor` | `shelve`, `substitute`, `audit` (reading the journal) |
| `engineer` | `force`, `edit` (online edits, rollbacks), `activate`, `deploy` (a Logix or Siemens download), `config` (live I/O configuration, and [IO-Link](/guides/io-link/) parameter writes, journalled as `param.write` on `<port device>/<variable>`) |
| `administrator` | `users`, `override` |

A custom role `inherits` one role (built-in or custom), then can `allow` more permissions or `deny` some.

### Areas

An area is a set of tag patterns. A grant with `areas:` limits `write`, `ack`, `shelve`, `substitute` and `force` to those tags. Its other permissions still apply to the whole controller. A grant with no `areas:` covers the whole controller. An `areas:` that is empty is refused: it would cover nothing.

### Who a request is

| Principal | How |
|---|---|
| `anonymous` | no credential |
| `token` | `NAUTILUS_TOKEN`, with `token-role` |
| `token:<name>` | a service token from `service-tokens:`, its secret in `NAUTILUS_TOKEN_<NAME>` |
| `user:<name>` | a signed-in browser, or that user's personal access token |

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
sudo naut users list
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
| `POST /api/elevate` | `{"password"}`: the session's password again, for the [elevated permissions](#elevation) (`200` with `elevatedUntil`) |
| `GET /api/me` | who the request acts as, its roles and permissions, `secure`, the session's `expires` and `elevatedUntil`, the `elevated` permissions, `signIn` (this controller offers `/signin`) and `now` (the controller's clock, to judge `elevatedUntil` by) |

## Elevation

Some permissions change what the plant runs, so a signed-in browser holds them only while its password is fresh: **`force`, `edit`, `activate`, `deploy`, `config`, `users` and `override`**. Those are the elevated permissions. That includes clearing a force and rolling an edit back: clearing a force can start a motor.

- **The window.** The password counts for `auth.elevation` (15 minutes by default, 1m to 12h) after the user last entered it, at sign-in or at re-authentication. Use doesn't extend it. It never outlasts the session; an `elevation` as long as `session` means the sign-in counts for the whole session.
- **Past it**, an elevated action is refused with `401` and `{"error": …, "elevate": true}`, and nothing is done. The error tells the user to confirm their password. An HMI asks for it (`POST /api/elevate`) and sends the action again.
- **Signing in again** (`/signin`) confirms it too: a fresh sign-in counts as elevated. A user who doesn't hold the permission at all gets a `403` and is never asked for a password.
- **Which pages ask.** An HMI built with the kit asks for the password itself (see [In the HMI](#in-the-hmi)). The built-in dashboard writes only tags, which are never elevated. The VS Code extension uses a token. An HMI of your own handles the `401` as above.
- **One browser.** Re-authenticating elevates that session only. The same user's other sign-ins (another screen) are not elevated.
- **Tokens are always elevated.** `NAUTILUS_TOKEN`, service tokens and personal access tokens are sent with every request, and their role already bounds them. The journal says which kind of credential each elevated action came with (`detail.credential`: `session`, `pat`, `token` or `service`).
- **Re-authentication** (`POST /api/elevate`) is checked like a sign-in: from the controller's own page, over HTTPS or from loopback, with one message for every wrong password. A user who holds none of the elevated permissions has nothing to confirm and is refused (`403`) before any password check. It's throttled per session, whatever address the attempts come from. Each wrong password also counts against the user's sign-in throttle. **After five wrong passwords in a row the session ends** (a stolen cookie guessing): the browser signs in again, and the journal records `auth.session-ended`. Every attempt is journalled as `auth.elevate`.
- **The journal.** Elevation doesn't depend on it. Re-authentication works while the journal is down, and so does clearing a force or rolling back once the password is fresh. Setting a force or editing the program needs the journal too, so it is still refused then (`503`), after the password check. See [When the journal cannot be written](/guides/http-api/#when-the-journal-cannot-be-written).
- **On a redundant pair** elevation lives with the session, on the leader. A standby refers `POST /api/elevate` there, like sign-in.
- **OIDC** sign-in (to come) will elevate by signing in at the identity provider again.

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
- **A sign-in that ends mid-shift** (signed out elsewhere, revoked, expired, five wrong passwords) is noticed at the next refused request or dropped live stream. The kit checks `/api/me`, and if the session is gone it goes to the sign-in page, which comes back to the same screen.
- **On a standby**, the page goes to the leader the controller names (`redundancy.public-urls`). Only that URL is opened, only if it is `http(s)`, and never `http` from an `https` page. The refused action is not sent again there. A second referral within 30 seconds is not followed, so two controllers naming each other don't bounce the page; the HMI shows it instead (`auth.referral`).
- **Without `auth:`**, or against a controller without `/api/me`, nothing changes.

**The controller decides.** What the HMI shows or greys out for a role is a convenience; every request is still checked by the controller, and its refusal is the answer.

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
- **Sign-out needs the leader.** If no leader is reachable, signing out can't revoke the session. It ends at the next failover or when it expires.
- **A standby never reads for a browser.** A browser's read reaching a standby gets the same `503` with the leader's address, even for `/api/cluster` and `/api/history`, which the standby otherwise answers itself.
- **A view of both controllers** (each one's `/api/cluster`, read from one page) needs a token or `anonymous: viewer`: a browser's session cookie belongs to one controller and is never sent to the other.
- **One address for the pair** (a floating address, so a browser never has to be sent anywhere) is #716. `/readyz` tells it which controller leads.

## Limits

- **The OPC UA server and Sparkplug writes are not covered yet** (#708). A client that reaches them is not asked for a role, and `naut run` prints a warning saying so whenever `auth:` is set.
- **`naut historian`'s own port is not covered.** It answers its archive by `localhost`, an IP address or an allowed name, or with `NAUTILUS_TOKEN`, as before, with no roles. Reads through the controller's `/api/history` need `read`; keep the historian's port off networks that shouldn't read the archive.
- **A standby answers some reads from its own users file.** `/api/cluster` and `/api/history` are answered by the standby itself, so a token revoked or a user disabled only on the leader can still read them there until the users file is copied to the standby.
- **Sessions live on the leader.** On a redundant pair a failover signs browser users out (see [A redundant pair](#a-redundant-pair)).
- **Not behind a load balancer that spreads requests over replicas.** A replica that isn't leading refers a signed-in browser to the leader. Point browsers at the leader (`/readyz`), or wait for the pair's floating address (#716).
