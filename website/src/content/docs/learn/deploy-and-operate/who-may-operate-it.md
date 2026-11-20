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

One command adds an `auth:` block to `nautilus.yaml`, naming the people and
their roles (run it in the project folder, on your own computer):

```sh
naut auth init --user akhan:operator --user jsmith:engineer
```

```text
nautilus.yaml: wrote a starter auth: block
With it, every request needs a credential: reads too, unless you set anonymous: viewer.
server.tls: self-signed: the controller serves HTTPS with a certificate it makes in its state directory, so passwords never cross the network in the clear.
…
Next:
  naut users add akhan       # akhan's password, kept on the controller (sudo on a box)
  naut users add jsmith      # jsmith's password, kept on the controller (sudo on a box)
  restart naut run to apply (a built controller: build and deploy it again)
```

It changed two things in the file. Under `server:` it added `tls: self-signed`
(the HTTPS part, below), and at the end it added the block:

```yaml
auth:
  users:                     # viewer < operator < supervisor < engineer < administrator
    akhan: [operator]
    jsmith: [engineer]
  # anonymous: viewer          # a request with no credential may read (a wall screen)
  # session: 12h               # a browser sign-in lasts this long (1m to 168h)
  …
```

The manifest says which user has which role, so it is reviewed in git; the
passwords are not in it. The other keys are left as commented examples to
turn on when you need them. The built-in roles each add to the one before:
`viewer` reads; `operator` also writes tags, acknowledges alarms and commands
sequences; `supervisor` shelves alarms and reads the audit journal; `engineer`
also forces, edits online and downloads; `administrator` manages users. Run
`naut check` (it refuses a misspelt role), then update the box:

```sh
naut deploy update deploy@plc1
```

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

## HTTPS, and the certificate's fingerprint

A browser sign-in sends a password, so the controller accepts one only over
HTTPS. `naut auth init` already turned that on: `tls: self-signed` makes the
controller create its own certificate the first time it starts, in its state
directory on the box, and renew it. The start-up log says so, and prints the
certificate's **fingerprint**:

```sh
ssh deploy@plc1 'sudo journalctl -u nautilus -n 20 --no-pager | grep -A1 self-signed'
```

```text
controller[1423]: naut run: HTTPS with a self-signed certificate it just made (/var/lib/nautilus/tls/self-signed.pem, until 2027-12-22).
controller[1423]:   SHA-256 fingerprint: C1:2C:37:75:EE:0C:7B:9A:F8:82:76:08:83:99:B7:CE:76:70:E0:08:AB:BA:F9:FD:37:24:D9:07:60:20:90:EC
```

A self-signed certificate is not vouched for by anyone, so a browser warns the
first time. The fingerprint is how you know it is *your* box you are talking
to: open `https://plc1:8080`, look at the certificate in the warning page, and
compare. (The same fingerprint from the outside, with
`openssl s_client -connect plc1:8080 | openssl x509 -noout -fingerprint -sha256`.)
The log also lists the names and addresses the certificate is valid for.

Plain `http://` to that port is refused (`400`: it speaks only HTTPS). After you
accept the certificate, `https://plc1:8080` goes to the sign-in page, and after
signing in the screen opens. A wrong password and an unknown user both answer
the same way, so the page does not say which names exist:

```text
{"error":"sign-in refused: the user name or password is wrong"}
```

The browser's session lasts 12 hours by default (`auth.session`), and a
restart of the controller signs everyone out.

**For a plant, use a certificate from your own certificate authority**, so no
browser warns: set `server.tls: {cert: /abs/cert.pem, key: /abs/key.pem}`. On a
box the service's user cannot read a root-owned key, so the files are handed
over as systemd credentials: [HTTPS in Continuous
deployment](/guides/deployment/#https) has the steps (and notes that they need a
real machine or virtual machine, not every container).

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

and the action then works. A screen built with the HMI kit does this itself:
when the controller refuses an elevated action, it asks for the password in a
dialog, sends it, and repeats the action once (the [auth
guide](/guides/auth/#in-the-hmi) shows the one call that turns it on). An HMI
of your own handles the `401` as above. A user whose roles hold none of these permissions,
a viewer or an operator, has nothing to confirm: they get a `403`, never a
password prompt. Using an action does not extend the window.
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
