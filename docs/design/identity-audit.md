# Design: users, roles and sign-in, and the runtime audit journal

Status: **designed, not built.** The audit journal (§4) is being built first. The identity
half (§2, §3) waits on the open questions in §6.
Issues: #708 (users, roles, sign-in), #709 (the runtime audit journal). Part of #725.
Context: **Operator surface**, with two new packages, `audit` and `auth`.
Builds on the browser-security set (#812, #818, #826, #846, #849) and on the audit record that
[redundancy.md](redundancy.md) §7.1 shapes for #709 (#787).

---

## 0. The shape, in one paragraph

nautilus answers a request in three gates.
1. **The Host rule** (#819) stays: a request must be addressed to a host no page can rebind, or carry a bearer credential.
2. **The principal**: `anonymous`, `token`, `token:<name>` or `user:<name>`.
3. **The permission**: whether that principal may do this action to this target. It replaces today's yes/no `authorizeWrite`.

Roles, areas and the user-to-role mapping are manifest data, reviewed in git. Credentials stay on the box. OIDC (Entra ID, Google, Keycloak) supplies identity, and groups map to roles. Configuration actions need a recent re-authentication.

Every action that changes something, accepted or refused, writes exactly one entry to an append-only audit journal:
- the entry is a JSONL line, fsynced before the action is answered;
- each line is chained by SHA-256 to the line before;
- `naut audit verify` checks the chain;
- an optional Postgres sink catches up from the file;
- entries are replicated through `oplog` once the redundant pair (#710) runs.

With no `auth:` block, permissions don't change; `naut run` only starts journalling.

## 1. What exists, and what changes

**Exists:**
- `server.authorizeWrite`: one bearer token, or a same-origin check without one.
- The Host rule and CORS (#811, #815, #819; `server.GuardHost`, `server.HostGuard`).
- `alarm.Journal`: a ring, plus file and Postgres sinks for alarm events. Its `by` field is an unauthenticated operator string.
- `oplog`: the operator-intent log, not mounted yet.
- OPC UA server writes, gated by a trusted application certificate (`opcua-server-writes.md`; UserName tokens are slice 4).
- Sparkplug commands, gated by `sparkplug.writable`.

**Nothing records runtime actions.**

**Changes:**
- **New packages:** `audit` (standard library only, like `alarm`) and `auth`, plus `auth/oidc` for the OIDC libraries.
- **`server`** composes them.
- **Other surfaces** (the OPC UA server, the Sparkplug edge, the Logix and Siemens facades) are handed a principal and a record function. They don't import `server`.
- **[context-map.md](context-map.md)** lists both packages under Operator surface, and `internal/contextmap` gains their import rules with the packages.

## 2. Identity: how users and roles relate to the token model

There are three gates, applied in this order. The first stays exactly as it is.

1. **The Host rule (#819), unchanged in kind.** A request is answered only by a trusted Host (localhost, an IP, `NAUTILUS_ALLOWED_HOSTS`) or with a valid **bearer credential**.
   - Today that means `NAUTILUS_TOKEN`. It will be any credential the server recognises: the client token, a named service token, or a personal access token.
   - `GuardHost` and `HostGuard` take a credential-check function instead of one token string. Each such credential is a secret a rebound page can't have, so by name it is as good as today's token.
   - A session cookie does **not** bypass it in v1. The sign-in page itself must be reachable, so the name has to be allowed anyway.
   - A cookie is scoped to the name the user signed in at, so a rebound page never carries it. Letting a valid session through by any name is a possible later relaxation, not v1.
2. **Who is asking: a principal.**
   - `anonymous`: no credentials.
   - `token`: `NAUTILUS_TOKEN`, as today.
   - `token:<name>`: a named service token.
   - `user:<name>`: a signed-in session or a personal access token.
3. **What they may do: a permission check per action and target.** This replaces `authorizeWrite`'s yes/no.

**Backward compatibility is the default.** With no `auth:` block in the manifest, permissions don't change:
- `anonymous` may do what a same-origin request may do today;
- `NAUTILUS_TOKEN` is a principal with every permission.

What changes is that `naut run` journals every action (§3) with `user: anonymous` or `user: token`. That means a new `audit/` directory in the controller's working directory and an fsync per action. That cost is small for operator-rate actions, and the guide says so.

**Other embedders of `server.New` are unchanged.** There are nine non-test callers, including the wasm playground (no filesystem), SDK programs, both facades, the OPC UA server and virtual devices. They get an in-memory ring unless they pass a sink themselves; only `naut run` wires the file by default.

**With an `auth:` block:**
- **Roles and permissions live in the manifest** (in git, reviewed like code). **Credentials live on the box.**
  ```yaml
  auth:
    anonymous: viewer          # control-room wallboards; or "none" to require sign-in to view
    token-role: administrator  # what NAUTILUS_TOKEN may do (default administrator: back-compat)
    session: 12h               # one shift plus handover
    elevation: 15m             # how long a re-authentication lasts
    areas:
      receiving: {tags: ["RCV_*", "PIT_*"]}
      roasting:  {tags: ["RST_*", "BURNER_*"]}
    roles:                     # built-ins: viewer, operator, supervisor, engineer, administrator
      lead-operator: {inherits: operator, allow: [shelve]}
    users:                     # local users: name -> roles (per area); no secrets here
      jsmith: [{role: operator, areas: [receiving]}]
      akhan:  [engineer]
    oidc:                      # optional; see §2
      issuer: https://login.microsoftonline.com/<tenant>/v2.0
      client-id: <id>          # secret from NAUTILUS_OIDC_SECRET
      groups-claim: groups
      groups: {"<plant-ops group id>": operator, "<controls group id>": engineer}
    service-tokens:            # named tokens for scripts and services
      historian: viewer        # secret from NAUTILUS_TOKEN_HISTORIAN
  ```
- **Actions** (the permission names):
  - `read`;
  - `write` (tag writes);
  - `ack`, `shelve`, `suppress` (#707);
  - `force`;
  - `sfc` (step/transition);
  - `edit` (online edit, rollback);
  - `activate`;
  - `deploy` (Logix/Siemens download);
  - `config` (#706/#711 applies);
  - `audit` (read the journal);
  - `users` (manage users and tokens);
  - `override` (#787's engineer override).
- **Built-in roles:**
  - viewer: read.
  - operator: + write, ack, sfc.
  - supervisor: + shelve, suppress, audit.
  - engineer: + force, edit, activate, deploy, config.
  - administrator: + users, override.
  - Custom roles inherit and add or remove actions.
- **Areas** scope `write`, `ack`, `shelve`, `suppress` and `force` by tag patterns (tag classes later).
  - A grant without areas covers the whole controller.
  - The server checks the target tag against the grant, and refuses with 403 and a journal entry.
- **Screens.** The HMI kit hides or disables what the user can't do (from `GET /api/me`). Screens are enforced through the data: the API refuses the action, and a screen the user may not see shows nothing they may not read. That is said plainly in the guide; screen-level hiding is a convenience, not the control.
- **Elevation.** Actions marked elevated (default: force, edit, activate, deploy, config, users, override) need a re-authentication within the last `elevation`.
  - Without one, the API answers 401 with `{"elevate": true}`; the HMI and the extension prompt and retry.
  - Local users re-enter their password. OIDC users go through `prompt=login&max_age=0`, and the server checks `auth_time`.
- **Service tokens and the historian.** #849 says the historian's `NAUTILUS_TOKEN` is the same secret as the controller's.
  - With service tokens, the historian's `NAUTILUS_TOKEN` can be the `historian` service token instead of the administrator one.
  - The controller recognises it as `token:historian` (viewer), and the historian's own API accepts the same secret, so "one secret on both sides" still holds with less privilege.
  - #787's pair shares one credential set: `NAUTILUS_TOKEN`, the service tokens and the users file are the same on A and B, and `tools/deploy` installs them. The peer handshake's HMAC comparison extends to a digest of the whole set.
- **Credentials:**
  - **Local users:** an argon2id hash (`golang.org/x/crypto/argon2`) in a users file in the controller's state directory, outside the project, so hashes never reach git. Managed with `naut users add|passwd|disable|list` on the box.
  - **A break-glass local administrator** is documented for when the IdP is unreachable.
  - **Personal access tokens:** `naut users token <name>` issues one, stored hashed, revocable. The VS Code extension and the CLI send it as `Bearer`, so online edits carry the engineer's name, not `token`. An OIDC device flow for the CLI is a later option.
- **Sessions:**
  - **The cookie:** `HttpOnly`, `Secure` when served over TLS, `SameSite=Strict`, host-only. It carries a random session ID; no claims are kept client-side.
  - **Where they live:** session records stay in memory and are replicated as `oplog` entries once #710's pair runs, so **a takeover keeps sessions**. Until then, a restart signs everyone out, which the guide says.
  - **Revocation:** sign-out and disabling a user revoke at once.
  - **EventSource works** with a cookie on the same origin, so a signed-in browser HMI gets the live stream with no token (the gap #818 documented).
- **CSRF:** writes keep #812's same-origin check. A cookie-authenticated write must also be same-origin; a bearer one may come from anywhere (as today). `SameSite=Strict` is the second layer.
- **Transport (TLS):** today nautilus serves plain HTTP. A password must not cross a plant LAN in clear, and IdPs accept non-https redirect URIs only for `localhost`. So:
  - v1 adds `server.tls: {cert, key}`, using standard `crypto/tls` and files the operator supplies.
  - Password sign-in is refused over plain HTTP unless the request comes from loopback.
  - Behind a TLS-terminating proxy that keeps the Host (`tailscale serve`, nginx), `server.trusted-proxies` names the proxy addresses whose `X-Forwarded-Proto: https` is believed. Only then do cookies get `Secure` and sign-in work.
  - OIDC needs one of the two.
- **Dependencies and no home-grown crypto:**
  - `crypto/rand` for IDs and secrets;
  - argon2id for passwords (`golang.org/x/crypto`);
  - `crypto/subtle` for comparisons;
  - `github.com/coreos/go-oidc/v3` + `golang.org/x/oauth2` for OIDC;
  - SHA-256 for the journal chain.
  - The new modules are `golang.org/x/crypto`, `golang.org/x/oauth2`, `github.com/coreos/go-oidc/v3` and its `go-jose`. They're imported only by `auth` (argon2) and `auth/oidc`, which only `naut run` wires in. `lang/*` and the VM stay pure stdlib, and the wasm build doesn't pull OIDC. Question 4 (§6).

## 3. OIDC scope

**In:**
- the authorization-code flow with PKCE as a confidential client (secret from the environment);
- discovery from `issuer`;
- ID-token verification by go-oidc (signature, `iss`, `aud`, `exp`, `nonce`);
- roles from a groups claim through a manifest map;
- `preferred_username`/`email` as the display name, `sub` as the stable identity;
- elevation via `max_age=0` and `auth_time`;
- providers: Entra ID, Google, Keycloak. A Keycloak container is the test IdP in CI. Entra and Google are checked by hand once each.

**Out (later issues if wanted):**
- SAML;
- SCIM provisioning;
- refresh tokens beyond the session;
- front- and back-channel logout (signing out of the IdP is the user's business; nautilus ends its own session);
- the CLI device flow;
- calling provider APIs (Graph and so on).

Groups beyond the token's size limit (Entra's overage claim) are refused with a clear message in v1.

**Offline sites:** OIDC needs the IdP reachable at sign-in, not afterwards. A session outlives an IdP outage. Local users and the break-glass administrator cover isolated sites.

## 4. The audit journal (#709)

**The entry** is #787 §7.1's shape, unchanged, plus three fields:

```json
{"id":"01J…","ts":"2026-11-18T14:02:11.204Z","user":"user:jsmith","source":"hmi",
 "action":"tag.write","target":"RCV_Conveyor2.SpeedSP","before":42.0,"after":55.0,
 "result":"ok","detail":{"area":"receiving"},
 "from":"192.0.2.22","session":"s-3f9c…","prev":"sha256:…"}
```

- `source`: `hmi`, `api`, `opcua`, `sparkplug`, `cli`, `ide` or `system`.
- `result`: `ok`, `refused` (with the reason) or `error`.
- `from`: the remote address, or for OPC UA the client certificate thumbprint.
- `session`: the session ID's hash, never the ID itself.
- `prev`: the chain (below).
- `id`: a ULID, the replay key, as in #787.

**Where it lives:**
- **For `naut run`, always:** an append-only JSONL file in `audit/` under the controller's working directory (as `retain.json` defaults there), or at `audit.path`.
  - Each entry is fsynced before the action is answered.
  - Files rotate daily and by size.
  - A torn last line is dropped on open, as in `oplog`.
  - An in-memory ring serves the recent view, as the alarm journal's does.
- **Optional:** a Postgres sink (`audit.sink: postgres`, the historian's DSN by default) for queries, screens and export.
  - It **tails the files** with a cursor (the last `id` applied, kept in Postgres), so it catches up from the local file after any outage.
  - The file is the source of truth; Postgres is a copy.
- **With redundancy (#710):** entries go through `oplog`, so both controllers hold the same history. #787's override records are written locally first, then to `oplog` when quorum returns; that path is kept as is.
  - **One entry per action, on the leader.** A write the standby proxies is journalled once, by the leader that applies it. `from` is the client's address (the standby's proxy adds it, and the leader believes it only from an authenticated peer), and `detail.via` names the standby.
  - **The chain is per controller.** `prev` chains each controller's own file in its own write order. It isn't part of the replicated entry, whose identity is `id`. Two files that hold the same entries in a different order both verify.

**When the journal can't be written** (disk full, read-only filesystem), the action and the entry must not part ways silently.
- Proposed: elevated actions (force, edit, activate, deploy, config, users, override) are **refused** (503, "audit journal unwritable").
- Operator actions (write, ack, sfc) are **allowed, and an "audit journal unwritable" system alarm is raised**, so a full disk can't stop an operator running the plant. The entries are kept in the ring and flushed when the file can be written again.
- This is question 5 (§6).

**Flooding:** an unauthenticated client could force an fsynced entry per refused request. Refusals are coalesced per (source address, action): the first few in a minute are written in full, the rest as one `refused ×n` summary entry. Accepted actions are never coalesced.

**Retention:** `audit.keep` (default 365 days) and `audit.max-size`, for small flash. Pruning the oldest file writes an `audit.prune` entry carrying the pruned file's last hash, so `verify` knows where the chain legitimately begins.

**Tamper evidence**, always on since it is cheap:
- Each line carries `prev`, the SHA-256 of the previous line's bytes. A rotated file's first line chains to the last line of the file before it.
- `naut audit verify [dir]` walks the chain and reports the first break: an edited or deleted line, or a missing file.
- **What it doesn't stop:** someone with root who rewrites the whole tail and recomputes the chain. The answer is an **external anchor**: the head hash goes to the Postgres sink (when configured), and to the peer once #710 runs. `verify` compares against an anchor when it has one. The guide states this boundary plainly rather than claiming more.

**What is audited:** every action that changes something, exactly one entry each, accepted **and refused**.

| Area | Actions |
|---|---|
| Tags | `tag.write` from the HTTP API and HMI, OPC UA server writes, Sparkplug NCMD, the Logix and Siemens facades' writes |
| Forces | `force.set`, `force.clear`, `force.clear-all` |
| SFC | `sfc.step`, `sfc.transition` |
| Alarms | `alarm.ack`, `alarm.shelve`, `alarm.unshelve`, `alarm.suppress`, `alarm.unsuppress` (#707). The alarm journal's unauthenticated `by` becomes the principal |
| Programs | `program.edit`, `program.rollback`, `program.activate`, `logix.deploy`, `siemens.download` |
| Configuration | `config.apply` (#706, when it lands) |
| Identity | `auth.signin` (ok or failed, rate-limited per source), `auth.signout`, `auth.elevate`, `user.add`/`disable`/`passwd`, `token.issue`/`revoke` |
| Redundancy | #787 §7.1's actions, unchanged |
| System | `system.start` and `system.stop` (with the version and the program hash) |

**Not audited:** reads, and the program's own writes (the scan isn't a user action). A Host-rule 403 isn't an action either; it is counted, not journalled, so a rebinding probe can't flood the journal.

**Reading it:**
- `GET /api/audit?from&to&user&action&target&result&limit` needs the `audit` permission.
- `GET /api/audit.csv` exports.
- The HMI kit gets an `AuditTable` (filterable, CSV button).
- `naut audit tail|query|verify` on the box.

**The "exactly one entry per write path" test:** a table-driven test drives every row of the table above through its real entry point, and asserts exactly one entry with the right user, source, action, target and result, accepted and refused. A new write path without a row fails the table. As with the extension's `apiFetch`, a source scan fails on a write to the store that bypasses the recording seam.

## 5. Delivery: small PRs, each with mutation-checked tests

1. **This design doc** and the context-map rows.
2. **`audit` package:** entry, file sink (fsync, rotation, torn line), hash chain, `naut audit verify`, ring.
3. **Journal every HTTP write path** in `server/` with today's principals (`anonymous`, `token`); `/api/audit` and CSV. This alone gives #709 value on every deployment. The Logix facade follows in the same PR; the Siemens facade follows in a small one of its own.
4. **`auth` core:** principals, the manifest `auth:` block, roles, areas and permissions. `authorizeWrite` becomes `authorize(action, target)`; `/api/me`; refusals journalled.
5. **Local users and sessions:** `naut users`, argon2id, sign-in and sign-out pages, cookies, elevation, personal access tokens; the extension sends PATs.
6. **OIDC**, against Keycloak in CI.
7. **HMI kit:** sign-in, role-aware controls, the elevation prompt, `AuditTable`.
8. **Other surfaces**, each coordinated with the work already in that area:
   - **OPC UA server:** UserName tokens checked against local users. This is `opcua-server-writes.md` slice 4; the maintainer decided on 2026-10-31 that UserName tokens come "when a site needs per-person audit", which is now.
   - **Sparkplug commands:** run as principal `sparkplug:<host>`, with an allowed-actions list.
   - **#707 suppress:** journalled with whoever builds #707.
   - **`oplog` integration:** with #710's pair.
   - **Keycloak in CI** (PR 6): a CI job of its own.
9. **Postgres audit sink** with catch-up.
10. **Demo** for the #725 checkpoint. What it must prove is question 7, and it isn't scripted before that's answered.

The redundancy hooks (sessions and entries through `oplog`) land with #710's pair, not before.

## 6. Open questions (put to the maintainer 2026-11-18)

1. **Viewing without signing in.** With `auth:` on, may anonymous users view (`anonymous: viewer`, for control-room wallboards) by default, or must everyone sign in?
2. **Where the user-to-role mapping lives.** In the manifest (reviewed in git; credentials stay on the box), as proposed, or on the box with the credentials?
3. **`NAUTILUS_TOKEN` once `auth:` is on.** Keep it as an administrator service principal (back-compat), or default it to something narrower?
4. **Dependencies.** Is `golang.org/x/crypto`, `x/oauth2` and `coreos/go-oidc` (with `go-jose`) acceptable for the `naut` binary? They're kept out of `lang/*`, the VM and the wasm build. The alternative is no OIDC in-process: delegate to a fronting proxy such as oauth2-proxy and trust its headers from `trusted-proxies`.
5. **When the journal can't be written:** refuse elevated actions, and allow operator actions with an alarm, as proposed? Or refuse everything, or allow everything?
6. **TLS:** is `server.tls` with operator-supplied certificate files plus `trusted-proxies` the right v1? Is refusing password sign-in over plain HTTP from anywhere but loopback acceptable?
7. **The demo** for the #725 checkpoint: what must it prove, and to whom?
