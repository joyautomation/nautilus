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

## askpass-bw.sh

`naut deploy` signs in with keys only unless a password can come from
somewhere (the deployment guide, *To a box over SSH*). For a box whose sshd
refuses keys, such as a PLCnext's `admin`, `askpass-bw.sh` supplies the
password from a Bitwarden item, unattended:

```sh
export NAUT_SSH_ASKPASS=tools/deploy/askpass-bw.sh NAUT_BW_ITEM=<item name or id>
export NAUT_BW_SESSION_FILE=<file holding the session key>   # or BW_SESSION
naut deploy update admin@plc
```

It never unlocks the vault or prompts (`bw --nointeraction`), prints the
password to ssh's stdout and nowhere else, and refuses every prompt that is
not a password prompt, so an unknown host key's yes/no is never answered
with it: make the host key known first (connect once by hand, or
`ssh-keyscan`).

## Boot time

```sh
naut deploy install root@ipc tools/deploy/boot-probe
tools/deploy/boottime.py root@ipc -n 3
naut deploy uninstall root@ipc -purge
```

`boot-probe` is a project whose tag API listens on every interface and
whose `Scans` counter is not retained, with its task at `priority: 80` on
`cpu: 1`. `boottime.py` reboots the box over SSH and times, from this
machine's clock, when SSH and the tag API come back and when `Scans > 0`
(the first good scan). It also reports the box's own times for
`nautilus.service`'s start and its first scan, from kernel start. A
software reboot is a warm boot; a cold power-on needs a power cycle.
