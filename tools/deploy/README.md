# tools/deploy: the black-box check for `naut deploy`

`naut deploy` installs a controller on a Linux box with systemd over SSH,
updates it, rolls it back with one command, and backs up and restores its
retained values (`naut deploy help`; the deployment guide, *To a box over
SSH*). Its on-box half and unit are `cmd/naut/deploybox/`.

`check.sh` proves it against a real box:

```sh
tools/deploy/check.sh root@ipc                     # naut on PATH deploys
NAUT=./naut tools/deploy/check.sh deployer@ipc -runtime v0.23.0
```

It deploys releases of `check-project/` that differ in one constant, then
on the box:

1. Install 1 and write a retained setpoint.
2. Update to 2, then update to 2 again, which is a no-op.
3. Roll back to 1.
4. Back up, change the setpoint, restore.
5. Deploy a release that cannot start, and one whose first scan hangs.
   Each must roll itself back and exit 3. The hung one is only caught
   under `Type=notify`, and takes the 60 s start timeout.
6. Roll forward with `rollback`.
7. Uninstall.

Every step asserts which release answers on the controller's API (reached
through the SSH connection, so nothing listens off the box) and that the
setpoint survived.

It refuses a box that already has a nautilus install. When it fails midway,
it prints the command that removes its own.
