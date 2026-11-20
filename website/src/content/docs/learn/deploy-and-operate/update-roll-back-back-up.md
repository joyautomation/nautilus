---
title: Update, roll back, and back up
description: "Change the logic on a running box with one command, go back to the release before, and save and restore the values operators have set."
sidebar:
  order: 2
---

A controller on a box has to change: a setpoint's default, a new rung, a
fixed bug. This lesson changes it safely, undoes the change, and saves the
values operators have set. About ten minutes, on the box from the [last
lesson](/learn/deploy-and-operate/put-it-on-a-box/).

## Update

Change something in the project, then, from its folder:

```sh
naut deploy update deploy@plc1
```

```text
naut deploy: update plant-38cc3b03 on deploy@plc1
updated plant-19bfe859 -> plant-38cc3b03 (rollback returns to plant-19bfe859)
```

`naut` built the new controller, put it beside the old one, switched the
service to it and restarted. Every build is a **release** named for the
project and a hash of its contents, kept on the box
(`/opt/nautilus/releases/`), with two links: `current` and `previous`.

Run it again with nothing changed and nothing happens:

```text
plant-38cc3b03 is already current and running
```

The same project built by the same `naut` is the same release, so a repeated
update, from you or from a CI job that retries, is safe.

An update that does not start (the new release will not run, or exits in its
first seconds) puts the old release back, restores the values it may have
saved over, and exits with code 3, so a script knows. An *install* that does
not start leaves nothing installed.

## Roll back

If the new release runs but is wrong, go back to the one before:

```sh
naut deploy rollback deploy@plc1
```

```text
rolled back plant-38cc3b03 -> plant-19bfe859 (rollback again returns to plant-38cc3b03)
```

Rolling back again goes forward. `naut deploy status deploy@plc1` shows the
service's state: `ActiveState=active`, `SubState=running`, the status line
(`StatusText=scanning` once the first scan has run) and `NRestarts`, which
should be 0.

## What a release does not hold: the operators' values

The values people set (setpoints, tuning) live outside every release, in
`/var/lib/nautilus`, so an update and a rollback keep them. They are saved if
the project has a `retain:` section:

```yaml
retain:
  file: retain.json
```

Every `role: setpoint` tag is kept this way, as are tags you mark
`retain: true`. (Outputs and inputs are not: they come back from the logic
and the field.)

## Back up and restore

```sh
naut deploy backup deploy@plc1
```

This writes `nautilus-retain-<host>-<time>.tgz` in the current folder: the
retained values, and a note of which release they came from. Set a
setpoint to a new value, then put the saved ones back:

```sh
naut deploy restore deploy@plc1 nautilus-retain-deploy_plc1-20261120T000659.tgz
```

```text
restored retained values (taken on plant-1ffaa53c)
```

The controller stops while it restores (it saves its values every two
seconds and once more on the way out, so a restore under a running
controller would be overwritten) and starts again, with the saved setpoints.
A backup is taken for you before every update, and is what a rolled-back
update puts back.

## Next

[Who may operate it](/learn/deploy-and-operate/who-may-operate-it/):
sign-ins, roles and tokens for the people and programs that use the box.
The full list of `naut deploy` commands and flags is in [Continuous
deployment](/guides/deployment/#to-a-box-over-ssh).
