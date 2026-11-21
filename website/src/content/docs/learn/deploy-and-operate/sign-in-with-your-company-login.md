---
title: Sign in with your company login
description: "Let people sign in to the controller with the company's own login (OpenID Connect), map their groups to roles, and see a forced re-login before a force. Tried here against Keycloak; Entra ID and Google are not tested."
sidebar:
  order: 4
---

In [Who may operate it](/learn/deploy-and-operate/who-may-operate-it/) each
person has a password that lives on the controller. Most companies already have a
login for everyone, in Microsoft Entra ID, Google, Okta or Keycloak, and do not
want a second set of passwords on every box. The controller can send people
there to sign in (**OpenID Connect**, OIDC), and take their roles from the
**groups** they are in at the company. About twenty minutes. You need `naut`
([install](/learn/getting-started/install/)), **Docker**, and a browser.

You will run a real login server of your own (Keycloak, a free one) so nothing
here touches a company account. At the end is what changes for Entra ID or Google.
That part is **not tested** here: only Keycloak is.

## 1. A login server to practise on

Save this as `realm.json` in a folder of its own, outside the project. It defines a
Keycloak *realm* called `plant`: two groups, `eng` and `ops`, three people (alice in
`eng`, bob in `ops`, carol in neither), and the controller as an application
(`naut`) with a secret. The passwords are for a lesson on your own computer:

```json
{
  "realm": "plant",
  "enabled": true,
  "sslRequired": "none",
  "groups": [{ "name": "eng" }, { "name": "ops" }],
  "users": [
    { "username": "alice", "enabled": true, "email": "alice@example.com", "emailVerified": true,
      "firstName": "Alice", "lastName": "Engineer",
      "credentials": [{ "type": "password", "value": "alice-password", "temporary": false }],
      "groups": ["/eng"] },
    { "username": "bob", "enabled": true, "email": "bob@example.com", "emailVerified": true,
      "firstName": "Bob", "lastName": "Operator",
      "credentials": [{ "type": "password", "value": "bob-password", "temporary": false }],
      "groups": ["/ops"] },
    { "username": "carol", "enabled": true, "email": "carol@example.com", "emailVerified": true,
      "firstName": "Carol", "lastName": "Visitor",
      "credentials": [{ "type": "password", "value": "carol-password", "temporary": false }] }
  ],
  "clients": [
    { "clientId": "naut", "enabled": true, "protocol": "openid-connect",
      "publicClient": false, "clientAuthenticatorType": "client-secret",
      "secret": "naut-lesson-secret",
      "standardFlowEnabled": true, "directAccessGrantsEnabled": false,
      "redirectUris": ["http://localhost:8080/*", "http://localhost:8081/*"],
      "attributes": { "pkce.code.challenge.method": "S256" },
      "protocolMappers": [
        { "name": "groups", "protocol": "openid-connect",
          "protocolMapper": "oidc-group-membership-mapper",
          "config": { "claim.name": "groups", "full.path": "false", "id.token.claim": "true",
                      "access.token.claim": "false", "userinfo.token.claim": "false" } }
      ] }
  ]
}
```

Two parts matter later. `redirectUris` is where Keycloak may send the browser back
to (the controller's two likely ports: it uses 8081 when 8080 is taken), and the
`groups` mapper puts the person's groups into the login token. Start it from that
folder (the first run downloads a 430 MB image; it takes about 15 seconds to
start):

```sh
docker run -d --rm --name lesson-keycloak -p 127.0.0.1:8180:8080 \
  -e KC_BOOTSTRAP_ADMIN_USERNAME=admin -e KC_BOOTSTRAP_ADMIN_PASSWORD=admin \
  -v "$PWD/realm.json:/opt/keycloak/data/import/realm.json:ro" \
  quay.io/keycloak/keycloak:26.0.7 start-dev --import-realm
```

When this works, this address answers with a page of JSON:
<http://localhost:8180/realms/plant/.well-known/openid-configuration>

## 2. Tell the controller about it

```sh
naut new signin --template minimal
cd signin
naut auth init --oidc http://localhost:8180/realms/plant --client-id naut --no-tls
```

```text
nautilus.yaml: wrote a starter auth: block
With it, every request needs a credential: reads too, unless you set anonymous: viewer.
No server.tls written: --no-tls. A password then signs in only from the controller's own loopback, or through a TLS proxy named in server.trusted-proxies.
Next:
  register the redirect URI https://<the controller's host>/auth/oidc/callback at the provider (naut run prints it)
  set NAUTILUS_OIDC_SECRET on the controller to the client secret the provider issued
  map a group id under auth.oidc.groups (the id, never the display name), or a name under auth.oidc.users
  restart naut run to apply (a built controller: build and deploy it again)
```

`--no-tls` is only because this lesson runs on your own computer (a browser treats
`localhost` as safe); a controller anyone else reaches needs HTTPS, as in the last
lesson. The issuer is the address of the realm, and the `client-id` is the
application the realm knows. Now say which group may do what. In `nautilus.yaml`,
in the `auth:` block the command added, replace the commented `groups:` lines with
the two real ones, and add `elevation: 1m` (it is commented out; the default is
fifteen minutes, a minute lets you see it work):

```yaml
auth:
  users: {}
  oidc:
    issuer: "http://localhost:8180/realms/plant"
    client-id: "naut"
    groups:
      eng: engineer
      ops: operator
  elevation: 1m
```

The keys are the group names exactly as they arrive in the login token. Keycloak,
as set up above, sends plain names (`eng`); Entra ID sends ids, below. The line
the command printed about "the id, never the display name" is about providers
where a name can be changed or repeated. People in `eng` act as an `engineer`, people in `ops` as an
`operator`. Give the project a screen so there is a page to sign in to: save this
as `signin.mimic.json` in the project folder (the [screen lesson](/learn/operator-screens/screen-in-five-minutes/)
explains it):

```json
{
  "name": "Tank",
  "canvas": {"width": 400, "height": 300},
  "equipment": [
    {"id": "T1", "component": "Tank", "x": 80, "y": 40, "width": 200, "label": "TK-1", "bind": {"levelPct": "Setpoint"}}
  ]
}
```

`naut check .` should say `0 with errors` (ignore a warning about `AGENTS.md` or about small text on the *Diagnostics* page: those are about `naut`'s own screens, not yours). The application's secret
never goes in the project. It goes in an environment variable of the controller:

```sh
NAUTILUS_OIDC_SECRET=naut-lesson-secret naut run
```

It prints the address to register at the provider (here the realm already allows
it), and then, if 8080 is taken, the port it moved to:

```text
auth: OIDC sign-in at http://localhost:8180/realms/plant: register the redirect URI http://<the address you open the controller at>/auth/oidc/callback with the provider (...)
nautilus · signin — hmi (the project's mimics, 3 pages, built in 3s) + tag API on http://localhost:8080 — Ctrl+C to stop
```

## 3. Sign in

Open the address `naut run` printed (<http://localhost:8080> unless it said it
moved). You are sent to
a sign-in page with one button, **Sign in with localhost** (the provider's name is
its host name). Press it. Keycloak's own page opens: sign in as `alice` with
`alice-password`. The browser comes back to the tank screen.

The password went to Keycloak, never to the controller. Ask the controller who it
thinks you are. Open the browser's developer console on the controller's page
(F12, *Console*) and run (the console shows these fields as an object you can
open; the lines below are its contents):

```js
fetch('/api/me').then(r => r.json()).then(console.log)
```

```text
principal: "oidc:alice"
roles: ["engineer"]
permissions: {"*": ["read","write","ack","shelve","substitute","force","sfc","edit","activate","deploy","config","audit"]}
session: {method: "oidc", expires: "…", elevatedUntil: "…"}
```

She is `oidc:alice`, an `engineer` because Keycloak said she is in `eng`. Nobody typed
her into the manifest: sign-in names come from the company. Open a private window
and do the same as `bob` (`bob-password`): he is an `operator`, with `read`, `write`,
`ack` and `sfc`, because he is in `ops`.

Carol is in neither group, so she is refused, and the page says what to do about it:

```text
signed in at localhost as carol, but the token carries no groups and the name has no grant: add a group id under auth.oidc.groups (and have the provider send the groups claim), or the name under auth.oidc.users
```

She proved who she is, and the controller still gave her nothing. Having a
company login is not the same as being allowed to run this plant.

## 4. Roles still decide

In bob's window, try to force a tag. A force replaces a tag's value whatever the
logic says, so only an engineer may:

```js
fetch('/api/forces', {method: 'POST', headers: {'Content-Type': 'application/json'},
  body: JSON.stringify({name: 'Setpoint', value: 20})}).then(r => r.json()).then(console.log)
```

```text
{error: "oidc:bob (operator) may not force"}
```

In alice's window the same call works and returns the force table.

## 5. Make them prove it again before a force

A person can leave a signed-in screen on a desk. So the actions that change what
the plant does (`force`, `edit`, `activate`, `deploy`, `config`, `users`,
`override`) need a login that is *fresh*: entered within `auth.elevation`, one
minute here. Alice has just signed in, so her force worked. Wait a minute, then in
her window clear it:

```js
fetch('/api/forces/Setpoint', {method: 'DELETE'}).then(r => r.json()).then(console.log)
```

```text
{elevate: "oidc", error: "confirm your sign-in at the provider to do this: force needs credentials entered at the provider within the last 1m (auth.elevation). The HMI sends you there (GET /auth/oidc/start?elevate=1). ...", url: "/auth/oidc/start?elevate=1"}
```

Nothing was changed. The controller does not know her password and never will, so it
sends her back to the provider to enter it again. A screen made with the HMI kit
does this by itself, in a dialog that asks whether to go to the provider. Here you
do it by hand. In the same console:

```js
location.href = '/auth/oidc/start?elevate=1&next=/'
```

Keycloak asks for the password again, even though it still remembers her (the
controller asks it to). Enter `alice-password`, and you come back to the tank
screen with a fresh login. Do the clear again: it works, and answers
`{forces: []}`. Using it does not extend the minute: a minute after that, she
confirms again.

## 6. Who did what

Every one of those is recorded under the company name, in the audit journal
(alice may read it, as an engineer):

```js
fetch('/api/audit').then(r => r.json()).then(j => j.entries.slice(0, 5).forEach(e => console.log(e.user, e.action, e.result)))
```

```text
oidc:alice force.clear ok
oidc:alice auth.elevate ok
oidc:alice force.clear refused
oidc:alice force.set ok
oidc:alice auth.signin ok
```

Newest first: she cleared the force, confirmed at the provider, was refused the
clear without that, forced the tag, and signed in. Bob's refused force is
further down: `oidc:bob force.set refused`. (Your list will also hold the lines
for any other times you signed in.)

## 7. When it goes wrong

Each refusal says what to do. Three you may meet:

- **"`oidc:bob` is pinned to another account at this provider"**. The first time a
  name signs in, the controller remembers which account at the provider it was, so
  that nobody can take the name later by making a look-alike account. If you
  delete and recreate the Keycloak container, the people are new accounts with
  the same names, and this is what you get. It is the safety working; tell the
  controller the change is real: `naut users unpin bob` (on a box,
  `sudo /opt/nautilus/current/controller users --state-dir /var/lib/nautilus unpin bob`).
- **Carol's refusal above.** Fix it at the provider, by putting her in a group, or
  here, by mapping her group.
- **A redirect error at Keycloak** ("Invalid parameter: redirect_uri"). The address
  the browser used is not in the application's `redirectUris`. Register the
  controller's address at the provider, ending in `/auth/oidc/callback`.

## 8. Clean up

Stop `naut run` with Ctrl-C and remove the login server:

```sh
docker rm -f lesson-keycloak
```

## Entra ID, Google, Okta (not tested here)

Only Keycloak is tested. The controller speaks standard OpenID Connect, so the
parts that change are at the provider. What you would do:

- **Microsoft Entra ID.** Register an application (a web application with a client
  secret), add `https://<controller>/auth/oidc/callback` as its redirect URI, and use
  `https://login.microsoftonline.com/<tenant>/v2.0` as the issuer and the
  application's id as `client-id`. Entra puts group **ids** (long GUIDs) in the
  token, not names, so the `groups:` keys are those ids; ask Entra to send only the
  groups assigned to the application, because a user in too many groups gets none
  in the token. And for the forced re-login to work, add the optional claim
  `auth_time` to the ID token.
- **Google.** A personal or workspace Google account has no groups in the token, so
  grant by name instead: `auth.oidc.users: {jsmith@corp.example: engineer}`, and
  check with `naut users list` that the first person to sign in under a name is the
  one you meant.
- **Okta and others** work the same way as Keycloak: a groups claim, mapped by what
  it carries.

For all of them the [auth guide](/guides/auth/#oidc-sign-in) has the details and
what is checked.

## Before a real plant

- **HTTPS.** Leave out `--no-tls`, and use the certificate steps from the last lesson;
  a sign-in must not cross a network in the clear.
- **The secret.** `NAUTILUS_OIDC_SECRET` goes in the controller's environment, set
  for the service (on a box, in `/etc/nautilus/env`), never in `nautilus.yaml`.
- **Keep a way in when the login server is down.** The provider must be reachable
  to sign in (not afterwards). Keep one local user with a password, as in the last
  lesson, for the day it is not.
- **Signing out is not signing out of the company login.** On a shared screen the
  next person who presses *Sign in* may come straight back in as the last one while
  the company session lasts. Sign out at the provider too, or keep its sessions short.
- **A redundant pair** cannot use this yet: the controller refuses `auth.oidc`
  together with `redundancy:`, and says so.

## Next

[Who may operate it](/learn/deploy-and-operate/who-may-operate-it/) is the
local-users version of this, if you want both on one controller: a person can
use either.
