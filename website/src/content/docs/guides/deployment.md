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

## The artifact carries its own provenance

`naut build` embeds the project's git history in the binary it emits —
`— N commits of program history embedded` on the build line. The deployed
controller then serves `GET /api/program/history` with no git, no `.git`
dir, and no network: which commit it was built from, every change as a
diff, and the ability to warm-swap back to any of them. The loop this page
closes — commit to running controller — gets its mirror: see
[Program history](/guides/program-history/).
