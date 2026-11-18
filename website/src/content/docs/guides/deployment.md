---
title: Continuous deployment
description: Commit-to-running-controller — every merge that passes its acceptance suites becomes a controller image, rolled out by sha to a redundant pair.
---

`naut new` with the **Kubernetes deploy** feature (or `--deploy`)
scaffolds the whole pipeline:

```
Dockerfile                      the controller image (distroless, ~binary-sized)
deploy/k8s.yaml                 redundant pair: RBAC, Deployment ×2, Service
.github/workflows/deploy.yml    check → test → build → image → (rollout)
```

The chain is the pitch made concrete: a PR that fails its acceptance
suites cannot merge (`ci.yml`); a merge that passes becomes an image
tagged with its **sha**; the rollout step points the Deployment at that
sha. Every controller running on the floor is attributable to one commit,
and `kubectl rollout undo` is a one-command downgrade.

## The image

`naut build` already emits a self-contained controller — runtime plus
project, one static binary — so the Dockerfile is three meaningful lines
on `distroless/static`: no shell, no package manager, CA roots included
for MQTT/TLS. The base has no libc, so the binary must be static: release
CLIs are, and the workflow sets `CGO_ENABLED=0` for its own `go install`.

A controller for another platform builds from any machine: give
`naut build` that platform's `naut` as its runtime. Take it from the same
release as the `naut` you build with, since one checks the project and
the other runs it.

```sh
tar xzf nautilus_<version>_linux_arm64.tar.gz naut
mv naut naut-arm64
naut build -runtime ./naut-arm64 -o my-plant-arm64
# built my-plant-arm64 — … — for linux/arm64
```

The controller is that binary byte for byte with the project appended,
so it runs on an ARM64 IPC and reports that release's version. Pass the
`naut` inside the archive, not the archive itself; `naut build` refuses
anything that is not an executable.

## The cluster

`deploy/k8s.yaml` runs **two replicas** with the manifest's `retain: {}`
and `redundancy: {}` sections (scaffolded together): a Lease elects the
scanning leader, setpoints and online edits ride a ConfigMap across
restarts and failovers, and either replica answers the Service — a
standby proxies API traffic to the leader. The RBAC is exactly the verbs
those two objects need, delete not among them. See the
[Redundancy guide](/guides/redundancy/) for how takeover works.

Kill the leader and watch the tank not care:

```sh
kubectl delete pod -l app=<name> --field-selector ... # or just delete the leader
# the standby's next 1s tick acquires the lease; worst case 4s
```

## The rollout

The scaffolded workflow pushes to GHCR out of the box (nothing to
configure — it uses the repo's own `GITHUB_TOKEN`). The final step —
`kubectl set image` by sha + `rollout status` — ships commented, because
it needs one secret only you can add: a namespace-scoped kubeconfig as
`KUBE_CONFIG`. Uncomment it and the loop closes.

Rolling by sha rather than `latest` is deliberate: a sha pins the code,
while a Deployment that says `:latest` redeploys to whatever was pushed most
recently.

## Without a cluster: systemd

On a single industrial PC the controller is a systemd service. Make it
`Type=notify` and give it a watchdog:

```ini
[Service]
Type=notify
ExecStart=/opt/nautilus/current/controller
WorkingDirectory=/var/lib/nautilus
WatchdogSec=10s
Restart=on-failure
RestartSec=2
```

- **Started means scanning.** The controller tells systemd it is ready
  (`READY=1`) once its first scan has completed, or, as a redundant
  replica, once it has decided it is the standby. `systemctl start` and
  `restart` return only then, so a release that cannot start fails the
  command instead of reporting success.
- **A hung scan is restarted.** While the scans complete, the controller
  sends systemd a heartbeat at half of `WatchdogSec`. It stops when a
  scan stops finishing, or when a task stops starting its scans on
  schedule (starved: its slot overdue, a higher-priority thread on its
  core, say). Both are judged by the same check that closes the
  [write gate](/guides/redundancy/#redundancy), about 2 to 4 s in. systemd then kills the process (`SIGABRT`, so the journal holds
  every goroutine's stack) and `Restart=` starts it again.
  `systemctl status` shows why, in its status line: `scans not live: …`.
- **How long to set.** At least 8 s; naut warns below that, where a
  scan that overruns and then finishes could get a healthy controller
  killed. Measured with 10 s: the kill came 10 s after the last
  heartbeat and the restart 1 s after that (`RestartSec=1`).
- A retained value that hangs the scan comes back with the restart, so a
  controller can restart in a loop. systemd's default start limit (five
  starts in 10 s) never catches it, because each round takes the
  watchdog's 10 s and more. To stop after five rounds, widen the window
  in `[Unit]`: `StartLimitIntervalSec=5min` and `StartLimitBurst=5`.

## To a box over SSH

`naut deploy` installs that service on a Linux box and moves it from one
release to the next, from the project's directory:

```sh
naut deploy install deploy@ipc       # build, install nautilus.service, start
naut deploy update deploy@ipc        # build, swap in, restart; exit 3 if it rolled back
naut deploy rollback deploy@ipc      # back to the release before; again goes forward
naut deploy status deploy@ipc
naut deploy backup deploy@ipc        # retained values -> nautilus-retain-….tgz
naut deploy restore deploy@ipc nautilus-retain-….tgz
```

The box needs systemd, `sh`, coreutils and `tar`, and nothing else. The
SSH user must be root, have `sudo` without a password, or be allowed one
command, [the release helper](#with-sudo-for-one-command-only-the-release-helper). ssh is the
system's, so `~/.ssh/config`, keys and the agent work as usual, and
`NAUT_SSH_OPTS` adds options (`-p 2222 -i ~/.ssh/ipc`).

Where a password can come from decides how ssh signs in:

- **A person at a terminal** gets ssh's own prompts on any box: the
  password, when keys do not get in, and an unknown host key's yes/no.
- **No terminal (CD)** signs in with keys only (`BatchMode`), so a job
  fails in seconds instead of waiting at a prompt nobody will answer.
- **`NAUT_SSH_ASKPASS=<program>`** lets an unattended run use a password:
  the program prints it. `tools/deploy/askpass-bw.sh` reads it from a
  Bitwarden item named by `NAUT_BW_ITEM`, from an already unlocked vault,
  and answers nothing but a password prompt, so make the host key known
  first. The standard `SSH_ASKPASS` with `SSH_ASKPASS_REQUIRE=force`
  works too.

Whenever a password can be asked, ssh asks once per connection, so a
wrong answer is one failed login, not three, on a box that locks an
account out. Each command asks once (one connection serves it all), and
a box that does not answer gives up after 10 seconds.

- **What it builds on.** For a box of another architecture, naut
  downloads its own release for that architecture and checks it against
  the release's `checksums.txt`. A box like the machine it runs on uses
  naut itself. `-runtime vX.Y.Z` or `-runtime <naut>` overrides it.
  Builds are reproducible: the same project on the same naut release
  binary is the same controller, whether a person deploys it or CD does.
  A naut from `go install` is a different binary from the release, so for
  a box like its own machine it builds a different controller. Pass
  `-runtime vX.Y.Z` to match CD.
- **Releases.** Each release is
  `/opt/nautilus/releases/<project>-<git describe>-<sha256:8>`, for
  example `plant-v1.4.0-3f2a91bc`. `current` and `previous` link to two
  of them, and `status` names both. The same binary again is a no-op. The
  same commit built on another naut is a new release.
- **An update that does not start rolls itself back.** That covers a
  release that will not start, one that exits within 3 s, and, under
  `Type=notify`, one whose first scan never finishes. The last waits for
  the 60 s start timeout. In each case `update` puts both links back,
  puts back the retained values the failed release may have saved over,
  restarts the release before it, and exits 3. An `install` that does not
  start leaves nothing installed, so it can simply run again. Updating to
  the release that is already current is a no-op while it runs, and
  starts it if it is down. A retried CD job therefore cannot pass while
  the controller is stopped.
- **Retained values stay where they are.** They live in
  `/var/lib/nautilus`, outside every release, so update and rollback keep
  them. A copy is taken before each update, with the controller stopped,
  and is what a rolled-back update puts back. `restore` stops the controller
  first, because it saves every 2 s and once more on its way out.
- **The unit.** `Type=notify` with `WatchdogSec=10s` and the start limit
  above. It runs as a dynamic user holding only `CAP_SYS_NICE` (for
  `priority:`) and `CAP_NET_BIND_SERVICE` (ports under 1024). A
  controller built on a naut that predates notification gets
  `Type=simple` without a watchdog instead; this is chosen per release.
- Everything is `sync`ed before a link moves. If power is lost
  mid-update, the box boots the old release or the whole new one.

### With sudo for one command only: the release helper

A box whose SSH user should not have unrestricted `sudo` can grant it
one command instead, the way controller vendors' own sudoers drop-ins
do. `naut deploy helper` writes the three files someone with root
installs once:

```sh
naut deploy helper -user deploy ./helper     # naut-deploy-box, nautilus.service, nautilus-deploy
# then, on the box, as root:
install -d -m 0755 /usr/local/sbin /usr/local/share/nautilus
install -m 0755 naut-deploy-box  /usr/local/sbin/naut-deploy-box
install -m 0644 nautilus.service /usr/local/share/nautilus/nautilus.service
visudo -cf nautilus-deploy && install -m 0440 nautilus-deploy /etc/sudoers.d/nautilus-deploy
```

From then on `naut deploy` finds the helper by itself and runs
`sudo -n /usr/local/sbin/naut-deploy-box <command>` instead of a root
shell. The helper is the boundary between that user and root, so:

- it pins its paths, mode, `PATH` and umask itself, takes nothing from
  the environment, and accepts only its own commands and plain release
  names;
- the unit is always its root-owned template, whatever is uploaded;
- `status` does not run the uploaded controller;
- like every mode, it never reads, writes or `chown`s through a link
  in `/var/lib/nautilus` (the controller owns that directory and could
  leave one there), and `restore` reads the backup's two members as
  data rather than unpacking it.

The helper is the on-box half of the `naut` that wrote it. When a
newer `naut` changes that half or the unit, `naut deploy` refuses and
says to install the helper again from `naut deploy helper`.

### Without systemd or root: cron

Some controllers run Linux without systemd and give their SSH user no
root: a PLCnext has sysvinit, and `admin` has only an allowlisted
`sudo`. Install once with `-supervisor cron` and a directory that user
can write:

```sh
naut deploy install admin@plc -supervisor cron -root /opt/plcnext/apps/nautilus
naut deploy update admin@plc         # the box remembers cron mode and the root
```

Nothing runs under `sudo`. The layout is the same under `-root`
(`releases/`, `current`, `previous`), with retained values and the log
in `state/` and optional variables in `env`. `env` is read by `sh`, not
systemd, so it is shell syntax: quote a value with spaces
(`NAUTILUS_TOKEN='a b'`). Two lines in that user's crontab run
`keepalive.sh`: every minute, which is what keeps it running, and at
boot, which only makes the first start sooner (some crons ignore
`@reboot`). It starts `current` unless it is already running. Update, rollback, backup and
restore work as above. An update that does not start, or exits within
3 s, rolls back and exits 3; as under `Type=simple`, a first scan that
hangs is not caught. A crash is restarted within a minute rather than
2 s. If `naut-launch` (from the PLCnext I/O release) sits in the root,
the keepalive starts the controller through it, so `priority:` keeps
working across updates without root (see the
[PLCnext guide](/guides/plcnext/#start-on-boot)). Uninstall removes
the crontab lines and what deploy made, and leaves the root and
`naut-launch`.

### Through CD

A deployment repository holds the project and pins the naut version
that builds it. A tag runs `naut deploy update` on a runner that can
reach the box. **GitHub's hosted runners cannot reach a plant LAN**: run a
self-hosted runner on the plant network, or add a step that joins the
runner to a tailnet or VPN that reaches the box.

`naut new <name> --deploy-ssh` writes the workflow,
`.github/workflows/deploy-ssh.yml`. For a repository that already
exists, scaffold a throwaway project with the flag and copy the file
across. On every `v*` tag it:

- fails unless the tag's commit is on the default branch, so a tag pushed
  on an unmerged branch never deploys, and waits for a required reviewer
  of the `plant` environment to approve;
- installs the pinned naut release for the runner's architecture into
  the runner's temp directory (never the checkout, which `naut build`
  would embed in the controller), checked against the release's
  `checksums.txt`. `NAUT_VERSION` is the naut that scaffolded it; a
  development naut leaves a placeholder that fails the job until it is
  set;
- runs `naut check` and `naut test`;
- writes the SSH key to a file only the runner's user can read, and the
  box's host key to its own known-hosts file, both removed when the step
  ends, and runs `naut deploy update` on the box named by the repository
  variable `DEPLOY_TARGET`. ssh accepts only that key and that host key,
  whatever the runner's own ssh configuration says. An update the box
  rolled back fails the job.

The job's token can only read the repository (`permissions: contents:
read`), `actions/checkout` is pinned to a commit and leaves no
credentials in the checkout, and one deploy runs at a time: a newer tag
queued behind a running deploy replaces an older one still waiting.

Its header lists what to set up once: the runner's `plant` label, the
`DEPLOY_TARGET` variable, the `DEPLOY_SSH_KEY` and `DEPLOY_KNOWN_HOSTS`
secrets, and the `plant` environment with its required reviewers. No
address or key goes in the repository.

A person deploying by hand from a clean checkout of the same tag, with
the same naut release, gets the same release: the build embeds the
project directory, so an untracked file in it changes the controller.
Going back is `naut deploy rollback` from either.

`tools/deploy/check.sh <ssh-target>` proves all of this against a real
box. It installs, updates, rolls back, backs up and restores, deploys a
release that cannot start and one that hangs, and asserts after every
step which release answers and that a retained setpoint survived.

## The artifact carries its own provenance

`naut build` embeds the project's git history in the binary it emits —
`— N commits of program history embedded` on the build line. The deployed
controller then serves `GET /api/program/history` with no git, no `.git`
dir, and no network: which commit it was built from, every change as a
diff, and the ability to warm-swap back to any of them. The loop this page
closes — commit to running controller — gets its mirror: see
[Program history](/guides/program-history/).
