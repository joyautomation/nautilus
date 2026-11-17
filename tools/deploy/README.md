# tools/deploy: releases on a box with systemd

`naut-release.sh` installs a `naut build` controller on a Linux box over
SSH, updates it, rolls it back with one command, and backs up and
restores its retained values. It was written for an ARM64 IPC (a Toradex
Verdin, #721) and assumes nothing about the box beyond systemd, a POSIX
`sh`, coreutils and `tar`.

```sh
tools/deploy/naut-release.sh build my-plant -runtime v0.22.0 -arch arm64   # -> my-plant-<sha>
tools/deploy/naut-release.sh install  root@ipc my-plant-1a2b3c4
tools/deploy/naut-release.sh update   root@ipc my-plant-5d6e7f8
tools/deploy/naut-release.sh rollback root@ipc
tools/deploy/naut-release.sh status   root@ipc
tools/deploy/naut-release.sh backup   root@ipc            # -> nautilus-retain-<target>-<time>.tgz
tools/deploy/naut-release.sh restore  root@ipc nautilus-retain-….tgz
```

## On the box

```
/opt/nautilus/releases/<id>/controller   one per release; the newest 3 besides current and previous are kept
/opt/nautilus/current  -> releases/<id>  what nautilus.service runs
/opt/nautilus/previous -> releases/<id>  what rollback returns to
/var/lib/nautilus/retain.json            retained values: in no release, so update and rollback keep them
/var/lib/nautilus/backups/               a copy of retain.json taken before every update (newest 10)
/etc/systemd/system/nautilus.service     from nautilus.service here
/etc/nautilus/env                        optional: NAUTILUS_ADDR, NAUTILUS_TOKEN, ...
```

- **update** puts the new release in, points `previous` at the old one
  and `current` at the new one, and restarts. If the new release does not
  start, or exits within 3 s of starting, it puts both links back,
  restarts the old release and exits 3. Under `Type=notify` "start" means
  its first scan completed: a release whose first scan never finishes is
  rolled back when `TimeoutStartSec` (60 s) runs out. Under `Type=simple`
  that case is not caught.
- Everything is `sync`ed before a link moves, so power lost right after
  an update never boots a link to a half-written binary.
- **rollback** swaps `current` and `previous` and restarts. Running it
  again goes forward again.
- **backup** and **restore** carry `retain.json` and the id of the release
  it ran on. Restore stops the controller first: it saves every 2 s and
  once more on its way out, either of which would overwrite the restored
  file.
- The project needs a `retain:` block for anything to be retained (see
  the redundancy guide, *Retained state*).
- The unit is `Type=notify` with `WatchdogSec=10s`: `systemctl restart`
  returns once the new release has scanned, and a scan that hangs gets
  the process restarted. A controller built on a naut that predates
  systemd notification gets `Type=simple` without a watchdog instead,
  chosen per release on every update and rollback.
- The service runs as a dynamic user with `CAP_SYS_NICE` (for
  `priority:`) and `CAP_NET_BIND_SERVICE` (ports under 1024) and nothing
  else.

The SSH user must be root or have `sudo` without a password.
`NAUT_SSH_OPTS` passes ssh options (`-p 2222 -i ~/.ssh/ipc`).

## The check

```sh
tools/deploy/check.sh root@ipc -runtime ./naut-arm64
```

builds releases of `check-project/` that differ in one constant, then
on the box: install 1, write a retained setpoint, update to 2, roll back
to 1, back up, change the setpoint, restore, deploy a release that cannot
start and one whose first scan hangs (each must roll itself back, exit 3;
the second only under `Type=notify`, and it takes the 60 s start
timeout), roll forward with `rollback`, uninstall. Every step asserts which release answers on the controller's
API (through the SSH connection, so nothing listens off the box) and that
the setpoint survived. It refuses a box that already has a nautilus
install, and when it fails midway it prints the command that removes
its own.
