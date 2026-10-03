# Registering a rig runner

The nightly (`.github/workflows/rig-nightly.yml`) runs on any self-hosted box
labelled `self-hosted linux rig`. The rig needs Incus and a real CPU, so it
cannot run on GitHub's hosted runners.

## What a box needs

- Linux with **Incus** installed and initialised (`incus admin init`).
- The runner's user in the **`incus` group** (`sudo usermod -aG incus <user>`,
  then restart the runner service so the group applies).
- **Go** (see `go.mod` for the version) and **Node 22+** with npm on the
  runner's `PATH`; `git`, `gh`, `tar`, `awk`. Network access to the Marketplace
  CDNs and npm, since the container downloads VS Code on first use.
- Disk for the cached image (`nautilus-vscode-rig`, a few GB) and about 2 GB
  of scratch per run.
- **One runner per box.** A run owns the box's incus containers, and the
  nightly's concurrency group serialises runs across boxes anyway.

## Register

1. Repo → Settings → Actions → Runners → New self-hosted runner → Linux.
   Follow the download and `./config.sh` commands it prints.
2. When asked for labels, give exactly: `rig` (the defaults `self-hosted` and
   `linux` are added for you). **Use the same labels on every box** so that
   any idle machine takes the job.
3. Install as a service: `sudo ./svc.sh install <user> && sudo ./svc.sh start`.

## Container names

Every entry point's EXIT trap deletes its container, so a name must belong to
one run at a time. The workflow derives `RIG_NAME` from the hostname
(`nautilus-smoke-<host>`, `nautilus-verbs-<host>`), which keeps boxes that
share an incus remote apart. If you run the rig by hand on the same box while
a nightly could fire, set your own `RIG_NAME`.

## First test

The nightly has not run on a real runner yet. After registering a box, run
**Actions → rig-nightly → Run workflow** (`workflow_dispatch`) and watch it
through: build, smoke, self-test, artifact upload. A dispatch run never
touches the rolling issue, so it is safe. Then confirm the red path once by
making a check fail on purpose in a scratch branch (scheduled runs only read
the default branch, so test the issue step by temporarily dropping the
`github.event_name == 'schedule'` condition on that branch).

## When it goes red

Open the "rig nightly red" issue: it carries the smoke and self-test TSVs.
The run's `rig-out-*` artifact has the PNGs and `build.log`. Reproduce on any
rig box with `tools/rig/smoke/run.sh <check>` or `G_PACE=fast tools/rig/selftest.sh`.
The issue closes itself on the next green scheduled run.
