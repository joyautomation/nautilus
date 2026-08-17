---
title: Continuous deployment
description: Commit-to-running-controller — every merge that passes its acceptance suites becomes a controller image, rolled out by sha to a redundant pair.
---

`nautilus new` with the **Kubernetes deploy** feature (or `--deploy`)
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

`nautilus build` already emits a self-contained controller — runtime plus
project, one static binary — so the Dockerfile is three meaningful lines
on `distroless/static`: no shell, no package manager, CA roots included
for MQTT/TLS. The base has no libc, so the binary must be static: release
CLIs are, and the workflow sets `CGO_ENABLED=0` for its own `go install`.

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

Rolling by sha rather than `latest` is deliberate: a Deployment that says
`:latest` redeploys to *whatever was pushed most recently*, which is a
statement about time, not about code. A sha is a statement about code.
