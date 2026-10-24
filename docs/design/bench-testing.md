# Design: testing on the bench controllers (hardware in the loop)

Status: **proposal, 2026-10-24.** Nothing here is built yet. §9 lists
the decisions James owes before any of it runs on the bench. Context:
**verification** (CI, `tools/`) over the **field drivers** `optommp/` and
`plcnext/` and the `rt/plcnext-io` C tier. Companion to
`docs/design/open-controllers.md` (what was built) and
`docs/design/testing.md` (the acceptance format, which this does not
change).

## 0. Why the fakes are not enough

Every target-specific bug so far was found on the bench, by hand, after
the fakes were green:

| found on the bench | why a fake missed it |
|---|---|
| one NaN channel blanked `/api/state` and the SSE stream | the fake chassis had no unwired ICTD |
| memory-map address arithmetic overflowed `int` | CI builds and tests on 64-bit only; ARMv7 has a 32-bit `int` |
| the PLCnext data-access service reads array elements but not arrays, sees MainProcess ports only, and samples BusConductor edges at 100 ms | the fake was written from the documentation first |
| a custom-area pointer into packed values costs `mmpServer` 0.5 ms per resolve (90 % of a core at 36 pointers / 20 ms) | cost lives inside Opto's server |
| the EPIC's stream configuration persists to flash | only visible after a restart |
| the PLCnext runtime restart, and the shim's lateness with `naut` running (0.7 %, unresolved) | timing and lifecycle, not logic |

Each of these should be a regression that a machine catches overnight,
not a rediscovery. Measured numbers in the guides (CPU, memory, exchange
time) should come from a run that anyone can look at, not a session's
notes. The run also provides the testing beat for the content plan (oc04
in the content repo's `series-open-controllers.md`).

## 1. Tiers

| tier | where | when | what |
|---|---|---|---|
| **T0** | GitHub-hosted | every PR (exists) | fakes: `optommp/optommptest`, the plcnext fake data-access service, the shm layout drift test; both bench examples' acceptance suites in virtual time |
| **T0a** | GitHub-hosted | every PR (**new, cheap**) | `GOARCH=arm GOARM=7` build plus `go test` of the driver, runtime and server packages under `qemu-arm` user mode. Catches the 32-bit `int` class without hardware |
| **T1** | self-hosted `bench` runner | nightly, dispatch, maintainer label | the bench suite (§6): deploy this commit's ARMv7 build to each controller and test through real I/O |
| **T2** | self-hosted `bench` runner | weekly | disruptive and slow checks: PLCnext runtime restart, 1 h soak, reboot proof if approved |
| **gate** | self-hosted `bench` runner | on a `v*` tag | T1 against the release's own `linux_armv7` archive, so the file users download is the file that was tested |

T0a is worth doing first and on its own: it needs no hardware, no runner
and no decision.

## 2. The runner

`mira1` already hosts the rig runner (`~/actions-runner`, user systemd
unit `github-runner-nautilus.service`, label `rig`) and can reach the
bench LAN. Add a **second runner instance** beside it, in
`~/actions-runner-bench` with its own user unit, labelled `bench`. The
rig's one-runner-per-box rule exists because a rig run owns the box's
Incus containers. A bench job uses only the network and the two
controllers, so the two instances do not contend, and the 2 h rig
nightly does not queue bench work behind it.

The earlier "any idle box" decision for rig runners does not carry over:
only a box on the bench LAN may carry the `bench` label.

**Credentials stay on the runner**, in `~/.config/nautilus-bench/env`
(mode 0600: hosts, users, and SSH key paths). They are not GitHub secrets,
so no workflow file a pull request can edit ever receives them. This is
the reasoning `logix.yml` already uses for the Studio 5000 runner.
Prefer SSH keys over passwords: an `authorized_keys` entry for the EPIC
shell user and for the PLCnext `admin` replaces the `sshpass` helpers
used by hand today.

## 3. A public repository and a runner beside real I/O

Copy `logix.yml`'s guard exactly. A bench job:

- **never** runs for a pull request from a fork (that would be arbitrary
  code with a route to live outputs);
- runs on `schedule`, `workflow_dispatch`, `push` to `main` touching
  `optommp/**`, `plcnext/**`, `rt/plcnext-io/**`, `io/**`, `runtime/**`
  or `server/**`, and same-repository pull requests that a maintainer
  labels `bench`;
- relies on repo setting *Require approval for all external
  contributors* as a second fence, not the first.

## 4. Sharing the bench: a lease and a restore

The bench is shared by James, by Claude sessions and by the nightly. Two
mechanisms keep them from stepping on each other.

**The lease.** On each controller, `mkdir /tmp/nautilus-bench.lease`
(atomic) holds an owner, a purpose and an expiry. The harness takes it
before touching anything, refuses if another live lease holds it, and
releases it in an `always()` step. `tools/bench/lease take|release|show
<device>` is the same thing by hand, and the open-controllers memory
note should tell sessions to take it. In Actions, one concurrency group
per device (`bench-epic`, `bench-plcnext`) serialises runs.

**Snapshot and restore.** Before the suite, record what the device was
doing. After it, put it back, whatever happened in between:

| device | snapshot → restore |
|---|---|
| EPIC | running `naut` processes and their projects; stream configuration (`F03FFFC0` block), custom-area pointers, comm watchdog; all DOs end **off** |
| PLCnext | the `current` project symlink, the `gds.config` checksum, whether the shim and a `naut` are running and which build; the keepalive (see below); all DOs end **off** |

The PLCnext keepalive (`naut-keepalive.sh`, run by cron every minute,
from the `plcnext-shm` branch) would restart the bench's resident `naut`
in the middle of a test. It needs a pause file that it honours
(`/tmp/nautilus-bench.lease` existing is enough) — a two-line change on
that branch.

**Never, from the harness:** firmware or module-firmware updates,
groov Manage or module configuration, switching the PLCnext project, a
reboot (except the weekly reboot proof, if approved), anything that
needs the EPIC shell user's sudo.

## 5. Wiring the bench needs

- **EPIC:** DO3→DI6, DO4→DI8, DO5→DI10, DO6→DI12 are already
  field-wired; that covers digital loopback. The EPIC has no analog
  output module, so analog checks are the ICTD probe (a plausible
  ambient range) and an unwired ICTD channel (NaN, quality code 9, a
  free negative test).
- **PLCnext:** nothing is looped yet. Wanted: four DO16→DI16 loopbacks,
  the UP supply to slot 4 (without it the analog module reads the
  supply-missing marker `0x8020`), then AO0→AI0.
- **Optional:** a switched outlet (USB relay or a smart plug on the
  bench LAN) per controller makes a real power-cycle test possible.
  Without it, the reboot proof stays manual.

## 6. The suite

Each check writes one TSV row (check, device, mode, verdict, numbers,
duration), like the rig. "Catches" names the bench finding it would have
caught.

### groov EPIC (nightly, ~10 min)

| id | check | catches |
|---|---|---|
| E1 | deploy this commit's ARMv7 build; `naut version` reports the commit | build/packaging |
| E2 | `naut optommp browse`: four modules, expected part numbers and channel types | memory map, importer |
| E3 | import twice: byte-identical, and equal to the committed `examples/epic-bench` files | determinism, drift |
| E4 | digital loopback through the runtime **on** the EPIC: write patterns to DO3–6 via `POST /api/tags`, read DI6/8/10/12 within N scans; repeated for each driver mode (Modbus projection, OptoMMP poll, custom area, stream 20 ms) | driver regressions per mode |
| E5 | quality: unwired ICTD → `null` in `/api/state` and quality bad; wired ICTD good and in range | the NaN class |
| E6 | budgets: `naut` and `mmpServer` CPU over 60 s per mode, against a stored baseline (fail at +50 %); RSS | the pointer-cost class; the evidence for guide numbers |
| E7 | link loss: `naut` on the runner targets the EPIC through a harness TCP proxy; cut it → quality goes stale within the timeout; restore → recovers without a restart | reconnect logic (no root needed anywhere) |
| E8 | comm watchdog (**only if approved**): set 500 ms, kill `naut`, the DI loopback sees DO3 drop within ~0.5 s | the one check that moves outputs through a fault |
| E9 | restore: stream configuration, pointers, watchdog equal the snapshot | the persists-to-flash class |

### PLCnext (nightly, ~10 min, more after wiring)

| id | check | catches |
|---|---|---|
| P1 | deploy ARMv7 build and project; version | packaging |
| P2 | `naut plcnext browse`: CONFIGURED, NUM_MODULES = 4, RUN | handshake, firmware facts |
| P3 | digital loopback (after wiring), for the gRPC transport and the shm transport | both transports |
| P4 | AO0→AI0 within tolerance (after the UP supply) | analog codec, offsets |
| P5 | shm output watchdog: kill `naut`, the shim drops outputs within 1 s (DI loopback) | the safety behaviour the shm branch claims |
| P6 | budgets: `naut` CPU and RSS; shim lateness recorded, not gated, until the 0.7 % is understood | timing drift |

### Weekly (T2)

| id | check |
|---|---|
| W1 | PLCnext runtime restart (`sudo /etc/init.d/plcnext restart`, ~1 min, on admin's allowlist): the shim and `naut` re-handshake and the heartbeat resumes unattended |
| W2 | 1 h soak per device: lateness does not grow, RSS stays flat, no reconnects |
| W3 | reboot proof (if approved): start on boot brings the runtime up and the heartbeat back |

## 7. Harness shape

Mirror `tools/rig`, so whoever knows one knows both:

```
tools/bench/
  README.md        what each check needs, how to run one by hand
  RUNNER.md        registering the second runner instance on a bench-LAN box
  lease            take | release | show <device>
  deploy.sh        <device>: build ARMv7, copy binary + project, start under the lease
  run.sh           [check…] → out/<run>/results.tsv + logs + CPU samples
  restore.sh       <device>: the snapshot back, DOs off; also runs on its own after a killed run
  checks/          Go tests, build tag `bench`, configured from the runner's env file;
                   they talk to the runtime's HTTP API, the optommp wire client, and
                   `naut plcnext` over SSH
```

The existing env-gated live test (`NAUT_OPTOMMP_LIVE=… go test ./optommp
-run Live`, on the `optommp-driver` branch) becomes check E4's poll mode
rather than a second copy.

`.github/workflows/bench-nightly.yml`: 05:00 UTC, an hour ahead of the
rig, so they do not overlap on `mira1`'s network. Jobs `epic` and
`plcnext` run in parallel because they touch different devices. It keeps
one rolling **bench nightly red** issue, uploads `bench-out-<run>` for 90
days, and a green run dispatches `docs.yml`. `bench-weekly.yml` runs the
T2 checks on Sundays and comments on the same issue when red.

**Evidence.** The claims files `docs/claims/groov-epic.yaml` and
`plcnext.yaml` already carry the bench measurements as untested claims
(EPIC-006, PLX-007). `tools/evidence` gains a third test kind, `bench
<check-id>`, resolved against the last green bench run's TSV, so
`/verified/` shows the measured number and the date it was measured,
not a number copied into a page.

## 8. Order

1. **T0a**: the ARMv7 cross-build and `qemu-arm` tests in `ci.yml`. No
   decision needed.
2. The runner instance, the env file with SSH keys, `lease`,
   `restore.sh`.
3. EPIC E1–E6 and E9 (no new wiring), nightly, advisory.
4. PLCnext P1–P2 nightly; W1 weekly; P3–P5 once the bench is wired.
5. E7 link loss, W2 soak, the release gate.
6. The `bench` evidence kind and the guide numbers sourced from it.

Steps 3–4 should land after the three open-controller branches merge
(`plcnext-groov`, then `optommp-driver` and `plcnext-shm`), so the
suite tests `main`.

## 9. Decisions for James

1. **Runner:** a second instance on `mira1` labelled `bench`
   (recommended), or a small dedicated box on the bench, so the bench
   does not depend on `mira1`?
2. **Credentials:** a runner-local env file plus SSH keys installed for
   the EPIC shell user and the PLCnext `admin`. Is that acceptable?
3. **Wiring the PLCnext:** four DO→DI loopbacks, UP to slot 4, AO0→AI0.
4. **Outputs:** may the nightly toggle bench outputs every night? Is
   anything besides LEDs and the loopback wires on them? May the weekly
   restart the PLCnext runtime?
5. **E8:** may the suite enable the EPIC's comm watchdog for the length
   of one check?
6. **Reboot proof:** buy a switched outlet, do it by hand monthly, or
   skip it?
7. **Gate or advisory:** recommended advisory on PRs, as the claims
   are, and a gate on release tags.
8. **The lease:** a 05:00 UTC (22:00 Pacific) nightly that takes the
   bench, with sessions and people expected to take the lease too. Is
   that acceptable?
