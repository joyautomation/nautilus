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
  scan stops finishing, judged by the same check that closes the
  [write gate](/guides/redundancy/#redundancy), about 2 to 4 s into the
  hang. systemd then kills the process (`SIGABRT`, so the journal holds
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
SSH user must be root or have `sudo` without a password. ssh is the
system's, so `~/.ssh/config`, keys and the agent work as usual, and
`NAUT_SSH_OPTS` adds options (`-p 2222 -i ~/.ssh/ipc`).

- **What it builds on.** For a box of another architecture, naut
  downloads its own release for that architecture and checks it against
  the release's `checksums.txt`. A box like the machine it runs on uses
  naut itself. `-runtime vX.Y.Z` or `-runtime <naut>` overrides it.
  Builds are reproducible, so the same project on the same naut version
  is the same controller, whether a person deploys it or CD does.
- **Releases.** Each release is
  `/opt/nautilus/releases/<project>-<git describe>-<sha256:8>`, for
  example `plant-v1.4.0-3f2a91bc`. `current` and `previous` link to two
  of them, and `status` names both. The same binary again is a no-op. The
  same commit built on another naut is a new release.
- **An update that does not start rolls itself back.** That covers a
  release that will not start, one that exits within 3 s, and, under
  `Type=notify`, one whose first scan never finishes. The last waits for
  the 60 s start timeout. In each case `update` puts both links back,
  restarts the release before it, and exits 3.
- **Retained values stay where they are.** They live in
  `/var/lib/nautilus`, outside every release, so update and rollback keep
  them. A copy is taken before each update. `restore` stops the controller
  first, because it saves every 2 s and once more on its way out.
- **The unit.** `Type=notify` with `WatchdogSec=10s` and the start limit
  above. It runs as a dynamic user holding only `CAP_SYS_NICE` (for
  `priority:`) and `CAP_NET_BIND_SERVICE` (ports under 1024). A
  controller built on a naut that predates notification gets
  `Type=simple` without a watchdog instead; this is chosen per release.
- Everything is `sync`ed before a link moves. If power is lost
  mid-update, the box boots the old release or the whole new one.

### Through CD

A deployment repository holds the project and pins the naut version
that builds it. A tag runs `naut deploy update` on a runner that can
reach the box. Plant boxes are rarely reachable from GitHub's hosted
runners, so that is a self-hosted runner on the plant network:

```yaml
# .github/workflows/deploy.yml
name: deploy
on:
  push:
    tags: ["v*"]
concurrency: deploy # one at a time
jobs:
  deploy:
    runs-on: [self-hosted, plant] # a runner that reaches the box over SSH
    environment: plant # optional: a reviewer approves each deploy
    env:
      NAUT_VERSION: v0.23.0 # the naut every build uses; change it in a commit
      TARGET: deploy@10.0.0.20
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0 # git describe names the release by its tag
      - name: install naut
        # Outside the checkout: naut build embeds the project directory,
        # and a naut binary left in it would ship inside the controller.
        run: |
          case $(uname -m) in aarch64) a=arm64 ;; *) a=amd64 ;; esac
          v=$NAUT_VERSION
          curl -fsSL -o "$RUNNER_TEMP/naut.tar.gz" "https://github.com/joyautomation/nautilus/releases/download/$v/nautilus_${v#v}_linux_$a.tar.gz"
          tar xzf "$RUNNER_TEMP/naut.tar.gz" -C "$RUNNER_TEMP" naut
          echo "$RUNNER_TEMP" >>"$GITHUB_PATH"
      - run: naut check . && naut test .
      - name: deploy
        env:
          SSH_KEY: ${{ secrets.DEPLOY_SSH_KEY }}
          KNOWN_HOSTS: ${{ secrets.DEPLOY_KNOWN_HOSTS }}
        run: |
          trap 'rm -f "$RUNNER_TEMP/key"' EXIT
          (umask 077; printf '%s\n' "$SSH_KEY" >"$RUNNER_TEMP/key")
          printf '%s\n' "$KNOWN_HOSTS" >"$RUNNER_TEMP/known_hosts"
          export NAUT_SSH_OPTS="-i $RUNNER_TEMP/key -o UserKnownHostsFile=$RUNNER_TEMP/known_hosts"
          naut deploy update "$TARGET"
```

A person deploying by hand from a clean checkout of the same tag, with
the same naut version, gets the same release: the build embeds the
project directory, so an untracked file in it changes the controller. Going back is `naut deploy rollback`
from either.

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
