---
title: Who may operate it
description: "Give each person their own sign-in and a role, give scripts their own tokens, and serve the screen over HTTPS so passwords are safe."
sidebar:
  order: 3
---

A shared token cannot say who changed a value or be taken back from one
person. In this lesson each person gets a name and a **role**, and every
change is recorded against them. About twenty minutes, on the box from the
[last lessons](/learn/deploy-and-operate/put-it-on-a-box/).

## Say who may do what

Add an `auth:` block to `nautilus.yaml`. The manifest says which user has
which role, so it is reviewed in git; the passwords are not in it:

```yaml
auth:
  users:
    akhan: [operator]
    jsmith: [engineer]
```

The built-in roles each add to the one before: `viewer` reads; `operator`
also writes tags, acknowledges alarms and commands sequences; `supervisor`
shelves alarms and reads the audit journal; `engineer` also forces, edits
online and downloads; `administrator` manages users. Run `naut check` (it
refuses a misspelt role) and update the box.

From now on every `GET` under `/api/` needs the `read` permission, too. With
no credential, a request gets `401`, and a screen opened in a browser
goes to a sign-in page. Only `/healthz` and `/readyz`, which say one word
each, answer without one.

## Give people passwords

Passwords live on the box, in a file the project never contains, managed
with the controller itself (the box has no `naut`):

```sh
ssh deploy@plc1
sudo /opt/nautilus/current/controller users --state-dir /var/lib/nautilus add akhan
```

It asks for the password twice. The controller picks the change up within a
second, so do not test it in the same instant. `users list` shows who
exists, `users passwd`, `users disable` and `users token` change them, and
every change is journalled with the system user who made it. A user the
manifest does not name cannot sign in, whatever the file says.

## Tokens for programs

A script, a historian or the VS Code extension signs in with a **personal
access token** that acts as its user:

```sh
sudo /opt/nautilus/current/controller users --state-dir /var/lib/nautilus token akhan
```

```text
token 001bbaf2fb94084b for akhan (shown once; naut users revoke akhan 001bbaf2fb94084b ends it)
```

(The token itself, `naut_pat_…`, is printed on the line before; copy it now,
it is not shown again.) Use it as a bearer token, and see exactly what it
may do:

```sh
curl -s https://plc1:8080/api/me -H "Authorization: Bearer $TOKEN"
```

```text
{"auth":true,"secure":true,"principal":"user:akhan","roles":["operator"],"permissions":{"*":["read","write","ack","sfc"]}}
```

akhan can write a setpoint, and cannot force a tag; the refusal names the
role and the missing permission:

```text
{"error":"user:akhan (operator) may not force"}
```

## Serve it over HTTPS

A browser sign-in sends a password, so the controller accepts one only over
HTTPS. Over plain HTTP from another machine the sign-in page says so, and
has no password box:

```text
Password sign-in needs HTTPS. Reach this controller over https:// (server.tls, or a TLS proxy named in server.trusted-proxies).
```

You need a certificate on the box. For a lab, make one yourself:

```sh
sudo install -d -m 755 /etc/nautilus/tls
sudo openssl req -x509 -newkey rsa:2048 -nodes -days 365 -subj "/CN=plc1" \
  -addext "subjectAltName=DNS:plc1,IP:10.0.0.5" \
  -keyout /etc/nautilus/tls/plc.key -out /etc/nautilus/tls/plc.crt
sudo chmod 600 /etc/nautilus/tls/plc.key
```

(For a plant, use a certificate from your own certificate authority; a
browser will warn about a self-signed one.) The service runs as a throwaway
user that cannot read a root-owned key, so hand the files over as systemd
credentials, and point the controller at the copies:

```sh
sudo mkdir -p /etc/systemd/system/nautilus.service.d
printf '[Service]\nLoadCredential=tls.crt:/etc/nautilus/tls/plc.crt\nLoadCredential=tls.key:/etc/nautilus/tls/plc.key\n' | sudo tee /etc/systemd/system/nautilus.service.d/tls.conf
printf 'NAUTILUS_TLS_CERT=/run/credentials/nautilus.service/tls.crt\nNAUTILUS_TLS_KEY=/run/credentials/nautilus.service/tls.key\n' | sudo tee -a /etc/nautilus/env
sudo systemctl daemon-reload && sudo systemctl restart nautilus
```

The start-up line now says `tag API on https://0.0.0.0:8080`. Systemd
credentials need a real machine or virtual machine; some containers cannot
provide them and the controller then fails to start with `permission denied`
on that path. Open `https://plc1:8080` in a browser: it goes to the sign-in
page, and after signing in the screen opens. A wrong password and an unknown user both answer the
same way, so the page does not say which names exist:

```text
{"error":"sign-in refused: the user name or password is wrong"}
```

The browser's session lasts 12 hours by default (`auth.session`), and a
restart of the controller signs everyone out.

## Some actions ask for your password again

A person who can force a tag or edit the program can change what the plant
does, so those actions need a **fresh password**: a signed-in browser holds
`force`, `edit`, `activate`, `deploy`, `config`, `users` and `override` only
for a while after its password was last typed (clearing a force counts: it
can start a motor). The window is `auth.elevation`, 15 minutes by default:

```yaml
auth:
  elevation: 15m
```

Right after signing in, an engineer can force a tag:

```text
{"forces":[{"name":"Sensor","value":20,"sinceMs":1795165440819}]}
```

Once the window has passed, the same kind of request is refused, and nothing
is done. With a one-minute window, clearing the force a minute later gives:

```text
{"elevate":true,"error":"confirm your password to do this: sign in again (/signin), or enter your password where the HMI asks for it (POST /api/elevate). force needs a password entered within the last 1m (auth.elevation)"}
```

The person confirms their password, by signing in again or, for an HMI of
your own, with `POST /api/elevate`:

```text
{"elevatedUntil":"2026-11-20T09:06:05.890423355Z","user":"jsmith"}
```

and the action then works. Using an action does not extend the window.
Tokens, for scripts and the VS Code extension, always count as elevated,
since they are sent with every request and their role already limits them.
Five wrong passwords in a row end the session. The [auth
guide](/guides/auth/#elevation) has the rest: how the journal records it, and
what a redundant pair does.

## Next

[Users, roles & sign-in](/guides/auth/) covers areas (limit a person to part
of the plant), service tokens for monitoring, the audit journal and a
redundant pair. [Continuous deployment](/guides/deployment/#https) covers
certificates, renewal and running behind a proxy.
