---
title: Users, roles & sign-in
description: The manifest's auth block — roles, areas, users and service tokens — local users and personal access tokens on the box, browser sign-in, and the audit journal that records who did what.
---

:::caution[PREVIEW: not finished yet]
`auth:` works for writes, but it is not yet a complete access control. Until the read gate lands (#708 PR 5b-2):

- **Reads are not gated.** Anyone who can reach the controller can read its tags, alarms and program. `naut run` prints a warning saying so. Writes, the audit journal and sign-in follow the `auth:` block.
- **The OPC UA server and Sparkplug writes are not covered.** A client that reaches them is not asked for a role.
- **Browser sessions live on the leader.** On a redundant pair a failover signs browser users out (see [A redundant pair](#a-redundant-pair)).
- **Not behind a load balancer that spreads requests over replicas.** A replica that isn't leading refers a signed-in browser to the leader's peer address, which the browser may not reach. Point browsers at the leader, or wait for the pair's floating address (#716).

Don't rely on `auth:` to keep data private yet. Use it to control who may change the process, and to journal who did.
:::

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

A token is sent as `Authorization: Bearer <token>`. With no credential, a write gets `401`. A principal without the permission gets `403`, and the message names its roles and the permission it lacks.

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

- **HTTPS only.** Password sign-in needs the request to reach the controller over TLS: `server.tls`, or a TLS proxy named in `server.trusted-proxies` (see [HTTPS](/guides/deployment/#https)). Plain HTTP is accepted only from the controller's own loopback, with no proxy in between.
- **One message for every refusal.** A wrong password, an unknown or disabled user, and a user the manifest doesn't name all get the same answer and take as long, so the page doesn't say which names exist. The journal records the real reason (`auth.signin`, refused).
- **Throttled, never locked.** Repeated attempts from one address are slowed (`429`, with `Retry-After`), and so, more loosely, are attempts on one name. A flood of guesses on one name from many addresses slows that user's sign-in too, but never disables the account: it works again as soon as the flood stops.
- **The cookie.** `nautilus_session` is `HttpOnly`, `SameSite=Strict`, host-only, and `Secure` over HTTPS. It holds a random id and nothing else.
- **Writes.** A signed-in browser writes only from the controller's own page: the request's `Origin` must be the controller's address. The journal names the user and a hash of the session id, never the id itself.
- **The Host rule still applies.** A session cookie doesn't count as a credential, so the browser must reach the controller by `localhost`, an IP address or a name in `NAUTILUS_ALLOWED_HOSTS` (see [HTTP API](/guides/http-api/#authentication)).
- **Signing out** (`POST /api/signout`) ends the session on the controller, not just in the browser, and is journalled (`auth.signout`).
- **Changes on the box end sessions.** Disabling a user, changing their password or removing them ends their sessions at once.
- **A restart signs everyone out.** Sessions are kept in memory.
- **`naut run` only.** `naut logix serve` and `naut siemens serve` take tokens but not browser sign-in.

The endpoints, for an HMI of your own:

| | |
|---|---|
| `GET /signin` | the sign-in page (`?next=/path` returns there afterwards, on this controller only) |
| `POST /api/signin` | `{"name", "password"}` as JSON (`200` with `user` and `expires`) or a form (`303` to `next`) |
| `POST /api/signout` | ends the session and clears the cookie |
| `GET /api/me` | who the request acts as, its roles and permissions, `secure`, and the session's `expires` |

## A redundant pair

- **Copy the users file.** Both controllers need the same `users.json`. Copy it from one box to the other after a change, rather than adding users on each box (each box would salt the passwords differently). The pair's [hello check](/guides/redundancy/) reports a difference.
- **Sessions live on the leader.** A standby refers a browser to the leader. It answers sign-in, sign-out and any request carrying the session cookie with `503`, the leader's address in `Location` and in the JSON field `leader`. Requests with a bearer token are forwarded as before.
- **A failover signs browser users out.** The new leader doesn't know the old one's sessions, so its users sign in again there. The old leader ends its sessions as soon as it is a standby, so they don't come back if it leads again (journalled as `auth.sessions-ended`).
- **During an election** a controller that knows no leader refuses a new sign-in (`503`, try again shortly). Sessions it issued still work there and can be signed out.
- **Sign-out needs the leader.** If no leader is reachable, signing out can't revoke the session. It ends at the next failover or when it expires.
- **The leader's address is the one in `peers:`.** A browser may not be able to reach that address (a floating address for the pair is #716).
