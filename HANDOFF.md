# nautilus — session handoff

Working notes for picking up development in a fresh session. See `README.md`
for the vision/architecture and `RELEASING.md` for the release pipeline; this
file is the practical state + next steps. Last refreshed: 2026-08-22.

## What this is

**nautilus** = "SCADA, built like software": a Go + SvelteKit framework for
building industrial control/supervisory systems like real software (version
control, tests, CI/CD, VS Code) instead of a vendor IDE. The **manifest form
is the product**: `nautilus.yaml` + IEC 61131-3 sources + `*_test.yaml`
acceptance suites, no toolchain required (`nautilus run/build/test/check`).
Go is the SDK tier for custom field buses and richer simulation.

Extracted from the **mini-scada** demo (`~/Development/mini-scada`).
mini-scada stays as the reference demo — **do not modify it** when working on
nautilus; copy/adapt from it.

## Repo / status

- GitHub: `joyautomation/nautilus` (private). `main` is the release branch.
- Go module `github.com/joyautomation/nautilus`; core is pure stdlib.
- **CI only runs on pushes to main and on PRs.** A long-lived branch with no
  PR gets zero CI — open the PR early. (This bit the acceptance-testing
  branch: 23 commits accumulated with a red test suite nobody saw.)
- Releases: see `RELEASING.md`. CLI ships on `v*` tags (GoReleaser);
  extension + HMI publish-on-bump from main (`publish.yml`); `version-sync`
  in CI fails any push where a registry is ahead of the repo. Extension
  channel: odd minor = pre-release, even minor = stable.

## Layout

```
lang/st, lang/ir     IEC 61131-3 ST compiler + VM (shared substrate)
runtime/             scan loop, Tags bus, program host; injectable Clock (virtual time)
acceptance/          deterministic virtual-time test harness; runs *_test.yaml suites
internal/project     manifest loader — builds exactly what `nautilus run` runs
internal/lsp         LSP: ST diagnostics/hover/completion, manifest-aware tags,
                     ST expectation regions inside *_test.yaml
io/, eip/            driver seam + Memory driver; EtherNet/IP (incl. logixserver)
sparkplug/           Sparkplug B (TCK conformance test in CI)
server/              tag API (state/SSE/write) + branded dashboard
cmd/nautilus         CLI: new, run, build, check, test, lsp, pull
examples/            heated-tank (Go tier), heated-tank-nogo (manifest flagship:
                     4 tasks, 3 IEC languages, sim in ST), FBD + SFC examples
hmi/                 @joyautomation/nautilus-hmi (Svelte 5): realtime SSE, Mimic
tools/vscode-iec/    VS Code extension: grammar, LSP client, inline live values,
                     Test Explorer for *_test.yaml, JSON schemas
website/, docs/      docs site (deploys from main); design briefs in docs/design/
```

## Design briefs (docs/design/)

- `testing.md` — manifest-tier acceptance testing. **Built and running.**
- `tags.md` — tag generation, shape verification, UDTs. **Built.**
- `sfc.md` — SFC front-end notes.

## Gotchas

- `examples/heated-tank-nogo` is a shared test fixture: the Go acceptance
  tests (`acceptance/heated_tank_test.go`) and the LSP tests
  (`internal/lsp/testdoc_test.go`) both run against it. If you change its
  physics or its YAML suite, run `go test ./acceptance/ ./internal/lsp/`.
  The LSP tests locate targets by content (`lineWith`), so pure line-number
  shifts are safe; renaming tags or expressions is not.
- npm trusted-publisher for the HMI verifies the workflow *filename*
  (`publish.yml`); repo variable `PUBLISH_HMI=true` arms it.
- `GOTOOLCHAIN=local` in CI keeps the pinned Go a floor (this bit v0.4.1).
- Toolchain is standard now: `go` on PATH (1.25.x local, CI pins 1.24),
  `npm`/`node` via `~/.local/node/bin` on PATH for hmi/extension work.

## Working habits

- **Content as you go**: when work produces something demo-able (feature,
  war story, design rationale), record the episode/post idea in
  `~/Development/joyautomation/content` before wrapping up.

## Roadmap / where to pick up

Done through the acceptance-testing branch: virtual-time harness +
`nautilus test`, manifest-first docs/README, manifest-aware LSP + Test
Explorer, tag files/UDTs/shape check, branded dashboard, Process Overview
demo with flow-balance physics.

Done 2026-08-17: the three mini-scada seams — `retain/` (file + ConfigMap),
`leader/` (Lease elector; `runtime.Coordinator` gates the scan loop),
`hist/` + `nautilus historian` (Postgres, `hist.Sink`). Manifest sections
`retain:`/`redundancy:`/`server.historian`; standby replicas proxy their
API to the leader. Also the CD scaffold: `nautilus new --deploy` emits
Dockerfile + redundant-pair k8s + deploy workflow (commit-to-running-
controller). mini-scada source of truth: `/home/joyja/mini-scada-build`
(NOT ~/Development/mini-scada — and read-only, never modify it).

Done 2026-08-18: **program history + activation** — the controller serves
its own git provenance. `internal/vcs` captures commits + diffs + deduped
file snapshots (git blob ids); `nautilus build` embeds it as the `.history`
archive entry, `nautilus run` captures live (lazily, on first request);
`GET /api/program/history` / `POST /api/program/activate {sha}` warm-swap
the whole resource to any captured commit (validate-all-then-swap-all,
topology mismatch → 409 "deploy that commit instead").
`project.Sources(fsys, manifest)` composes task→source from any fs.FS —
pointed at a snapshot it rebuilds the past exactly as boot composes the
present. Guide: website .../guides/program-history.md.

Done 2026-08-19: **Sparkplug manifest tier finished** — `store-forward:`
joined the `sparkplug:` section (project.go + schema, the schema-sync
test enforces the pair), client60 uses it, and the sparkplug guide was
rewritten manifest-first (YAML leads, Go tier demoted to a "From Go"
section — the house pattern for all guides). Content: N-13 (comms/MQTT
episode) developed in ~/Development/joyautomation/content — angle, beat
sketch, Tier-3 sourcing note; still gated on wk 16 shipping.

Done 2026-08-22: **Sparkplug B host application driver** — the other side
of the wire from the edge node. `sparkplug/host` (package `host`), a
manifest-tier `io.Driver` (`driver: {type: sparkplug-host}`), never dials
(`New` builds offline; `Start` connects — same split as `eip`, so
`nautilus check`/`build` pass with no broker in sight). `nautilus
sparkplug import|browse|tags` generates `sparkplug_types.st` +
`sparkplug_manifest.yaml` + `tags/sparkplug.yaml`, live (`--broker`) or
offline from a committed `--sites` file — byte-identical output either
way. Quality rides on driver-synthesized `__Online`/`__LastBirthMs`/
`__Rebirth` companions (Sparkplug keeps the last value through a death;
"reads fault until first birth" — guard on `__Online`). Passes the
Sparkplug TCK **host-application** profile (81/0/3 — 81 PASS, 0 FAIL, 3
N/A) alongside the existing edge-node profile, both gated in CI.
`examples/sparkplug-host` (a 3-site fleet, generated via `--sites`,
`fleet.st` rollups, `fleet_test.yaml` in virtual time) and the manifest-
first guide (`guides/sparkplug-host.md`, linked from the edge-node guide).
`st-struct-pins` (worktree `~/Development/joyautomation/nautilus-st`) is
a separate branch in flight; `sparkplug-host` is now correct under BOTH
output contracts ahead of the `demo-integration` merge — a write to an
offline node is queued per site and delivered once on its next birth
(unless the birth already reports that value) instead of being dropped
and re-raised by a next scan change-push never makes, and the driver
implements `io.BatchReader`'s `ReadInputsInto`. Driving project:
the Pomona WRD demo at `~/Development/pomona/wrd` — a ~60-site fleet is
the real target this driver is being built for.

Done 2026-08-24: **Per-tag quality on sparkplug-host** — `Driver.Quality()` implements `io.QualityReporter` (its seam ported from `st-struct-pins`' `io/quality.go`, byte-identical apart from the Memory-driver half that branch's differing `io.go` doesn't support here yet): NotConnected for a data binding never delivered (never birthed, or the metric a birth simply never carries), Stale for one with a value on file whose node/device is offline or gone stale, Good (omitted) once delivered and online; writable and companion tags are always Good.

Next, in rough priority:

1. **HMI Versions page** — render /api/program/history in
   @joyautomation/nautilus-hmi (mini-scada's Versions page is the
   reference): commit list, diffs, activate button. The demo moment for
   the content calendar ("your PLC shows its own git log").
2. **Alarm engine + fleet HMI patterns** — driven by the Pomona WRD demo
   (`~/Development/pomona/wrd`): a real alarm/annunciation model over a
   sparkplug-host fleet (priorities, ack/shelve, per-site rollups), and
   the HMI components a multi-site SCADA screen actually needs beyond
   `DriverStatusPanel`.
3. **Native-Go function blocks** alongside ST (both lowering to the IR).
4. **Extension 0.10.0** — first stable-channel Marketplace release, when the
   Test Explorer + schema work has soaked on the pre-release channel.
