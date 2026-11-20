---
title: Put it on a Linux box, and let operators open the screen
description: "Install a controller on a Linux machine with one command, then open it to the network the way a plant needs: an address, the names people type, and a token."
sidebar:
  order: 1
---

So far the controller has run on your laptop. In this lesson it goes onto a
Linux machine, starts with the machine, and operators open its screen from
theirs. About fifteen minutes. You need a project with a screen (the
[screen lessons](/learn/operator-screens/screen-in-five-minutes/) make one)
and a Linux box with **systemd** and **SSH**, where your user can run `sudo`
without a password. Nothing else is needed on the box: not `naut`, not a
toolchain.

## Install it

From the project's folder, on your own computer:

```sh
naut deploy install deploy@plc1
```

(`deploy@plc1` is the SSH user and host. Anything `ssh` accepts works, and
`NAUT_SSH_OPTS` adds options such as a key.)

```text
built …/controller — a self-contained controller (…)
naut deploy: install plant-c970dca5 on deploy@plc1
installed plant-c970dca5; nautilus.service is running
```

`naut` built the controller on your machine, copied the one file to the box,
and installed a systemd service, `nautilus`, that starts at boot and restarts
the controller if it stops. The release is named for the project and a hash
of the build (`plant-c970dca5`), so you can always say which build is on a
box.

## It answers only on the box

Open the screen from your own computer, and nothing answers:

```sh
curl -s -o /dev/null -w "%{http_code}\n" http://plc1:8080/
```

```text
000
```

(`000` is `curl` for "could not connect".) The scaffold's `server.addr` is
`localhost:8080`, which means the controller listens on the box's own
loopback and nowhere else. That is a safe default, and it is why a fresh
install is unreachable.

## Open it to the network

In `nautilus.yaml`, make the controller listen on every interface:

```yaml
server:
  addr: "0.0.0.0:8080"
```

and update the box (the next lesson covers updates):

```sh
naut deploy update deploy@plc1
curl -s -o /dev/null -w "%{http_code}\n" http://10.0.0.5:8080/
```

```text
200
```

By the box's IP address (`10.0.0.5` here) the screen opens. Try the name an
operator will actually type:

```sh
curl -s -H "Host: plc1:8080" http://10.0.0.5:8080/api/state
```

```text
this controller answers "plc1:8080" only with an auth token: it is reached
without one by localhost, an IP address, or a name in NAUTILUS_ALLOWED_HOSTS
(a page on another site could rebind "plc1" to it); add the name to
NAUTILUS_ALLOWED_HOSTS, or set NAUTILUS_TOKEN
```

(shown wrapped; it is one line). A name can be pointed at any address by a
page on another site, so the controller refuses names it was not told about.
You tell it in a small file on the box, which the service reads and
`naut deploy` never writes:

```sh
ssh deploy@plc1
sudo install -d /etc/nautilus
printf 'NAUTILUS_ALLOWED_HOSTS=plc1\nNAUTILUS_TOKEN=%s\n' "$(openssl rand -hex 16)" | sudo tee /etc/nautilus/env >/dev/null
sudo chmod 600 /etc/nautilus/env
sudo systemctl restart nautilus
```

`NAUTILUS_ALLOWED_HOSTS` lists the names (comma-separated) that may be used.
`NAUTILUS_TOKEN` is a secret that every **write** must carry; the file is
readable only by root because it holds it. Now the name works, and a write
needs the token:

```sh
curl -s -H "Host: plc1:8080" -o /dev/null -w "%{http_code}\n" http://10.0.0.5:8080/api/state
curl -s -X POST http://10.0.0.5:8080/api/tags -d '{"name":"TempSP","value":70}'
curl -s -X POST http://10.0.0.5:8080/api/tags -H "Authorization: Bearer <the token>" -d '{"name":"TempSP","value":70}' -w "%{http_code}\n"
```

```text
200
missing or invalid auth token
204
```

(the second command prints the message, then its `401`.)

A shared token is a start. It does not say *who* wrote a value, and one
secret for everyone is hard to take back from one person.
[Who may operate it](/learn/deploy-and-operate/who-may-operate-it/) gives
each person their own sign-in.

## Next

[Update, roll back, and back up](/learn/deploy-and-operate/update-roll-back-back-up/).
The full details are in [Continuous
deployment](/guides/deployment/#letting-operators-open-the-screen).
