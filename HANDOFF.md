# nautilus — session handoff

Working notes for picking up development in a fresh session. See `README.md`
for the vision/architecture and `RELEASING.md` for the release pipeline; this
file is the practical state + next steps. Last refreshed: 2026-08-16.

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
alarm/               ISA-18.2 alarm engine: defs/rules, state machine, journal,
                     notifiers. Manifest `alarms:`, `/api/alarms*`, retained ack
examples/            heated-tank (Go tier), heated-tank-nogo (manifest flagship:
                     4 tasks, 3 IEC languages, sim in ST), alarms, FBD + SFC
hmi/                 @joyautomation/nautilus-hmi (Svelte 5): realtime SSE, Mimic
tools/vscode-iec/    VS Code extension: grammar, LSP client, inline live values,
                     Test Explorer for *_test.yaml, JSON schemas
website/, docs/      docs site (deploys from main); design briefs in docs/design/
```

## Design briefs (docs/design/)

- `testing.md` — manifest-tier acceptance testing. **Built and running.**
- `tags.md` — tag generation, shape verification, UDTs. **Built.**
- `sfc.md` — SFC front-end notes.
- `alarms.md` — the alarm subsystem. **Built** (see 2026-08-22 below).

## Gotchas

- **The tag store is generation-stamped.** `runtime.Tags` bumps a counter
  only on a write that CHANGES a value; a write of the value already there
  is a no-op. Everything downstream asks "did this move?" with an integer
  compare instead of a walk: the Sparkplug RBE pass, the SSE frame, and the
  driver output push. Consequences to remember when touching this code:
  `WriteOutputs` is called on CHANGE (first scan, a moved output, after a
  failed write, after a takeover) — `runtime.Options.AlwaysWriteOutputs`
  restores per-scan calls for a driver that needs a watchdog re-armed; a
  driver may implement `io.BatchReader` to be handed the runtime's own input
  map instead of allocating one per scan; and anything caching per-tag state
  keyed by NAME should invalidate on `Tags.NameGeneration()`. Full write-up:
  `runtime/tags.go` ("Write generations") and the tag-model guide's
  "Performance notes". Benchmarks that pin it: `runtime/bigstore_bench_test.go`,
  `runtime/hostdriver_bench_test.go`, `sparkplug/publish_bench_test.go`.
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

Done 2026-08-22: **alarms** — `alarm/` turns BOOL tags into ISA-18.2 state
(active list, ack, shelve, journal, notifiers), wired through every tier.
Manifest `alarms:` + `alarm-files:` (mirrors `tag-files:`, duplicate id
across sources = error naming both); `rules:` generate definitions in bulk
by struct TYPE + member, materialized once at load — `nautilus alarms list`
dumps the expansion, `nautilus check` validates it offline (unknown member
= error, dead rule / undeclared tag = warning). `internal/project`
composes and builds the engine over a compiled runtime
(`NewAlarms` for `run`, `AlarmEngine` for tests, `AlarmDefs`/`CheckAlarms`
offline); evaluation rides `Runtime.OnScan`, timestamps ride the runtime
Clock so virtual time drives on-delays. Server: five `/api/alarms*` routes
(writes through `authorizeWrite`, 404 with no engine), `Frame.Alarms`
summary, `/api/meta` capability flag + shelve times. Retain:
`Runtime.SetAlarms` puts ack/shelf in `retain.State.Alarms`, restored on
every takeover — a failover cannot resurrect acked alarms as unacked.
Acceptance: a sibling `alarms:` key plus `ack:`/`shelve:`/`unshelve:`
verbs (the deliberate exception to "no new keys — write an ST
expression": alarm state is not in the tag store and no ST expression can
see it). New example `examples/alarms`; guide at
website/.../guides/alarms.md; HMI kit components were already in `hmi/`.
Secrets stay in the environment: `journal.dsn-env`, `notify[].header-env`.

Done 2026-08-22 (st-struct-pins):

- FB struct pins/VAR_IN_OUT/field assignment (3ab4362)
- Four fixes a real site project exposed (ae30474)
- Struct-typed tags per-member init: (76d6ecf)
- Struct-member API writes — Tags.SetPath, POST /api/tags (358380e)
- Acceptance dotted given: edits on struct tags compose (51afacd)
- Built-in PID function block — ISA form, anti-windup, bumpless auto/manual (4bd5e32)
- Nested block comments in ST; LexErrors on unterminated comment (c2099f9)
- HMI alarm kit — AlarmBanner, AlarmTable, AlarmJournal, createAlarmClient (2ffdb11)
- Server HMI tier — serve SPA from controller, dashboard at /_nautilus/, retained struct restore (2416d77)
- Historian struct members as dotted leaves, deadband/min-interval change filter (8ac5fbd)
- Historian server-side aggregates (/history/agg min|max|avg|sum|count|first|last|delta|ontime, bucketed) (1c56011)
- Runtime OnScan observer seam and Tags.ReadPath for dotted reads (35b80d1)
- Alarm engine core — ISA-18.2 state machine, ack/shelve, ring/file/Postgres journal, notifiers (e17cb88)
- Alarms manifest tier — alarms:/alarm-files:, rules expansion, offline check, acceptance ack:/shelve:/unshelve:, examples/alarms, guide (a5b6232)
- HMI alarms.ts → alarms.svelte.ts (runes module requires .svelte.ts suffix) (e55399c)
- LD FB-only rungs, edge contacts +x/-x and P/N coils, negated function contacts (baba4db)
- Ladder FUNCTION_BLOCKs — library .ld/.fbd files, multi-POU, power-pin resolution, struct-field bindings, LSP + editor (d094337)

Next, in rough priority:

1. **HMI Versions page** — render /api/program/history in
   @joyautomation/nautilus-hmi (mini-scada's Versions page is the
   reference): commit list, diffs, activate button. The demo moment for
   the content calendar ("your PLC shows its own git log").
2. **Verify VS Code ladder editor webview build** (FB rung groups) and cut an extension pre-release.
3. **Publish @joyautomation/nautilus-hmi** (alarm kit, alarms.svelte.ts rename) to npm.
4. **Remote counter RESET coil / task scan-order guarantee / remote-program FB pin reads** — asks from a real ControlLogix transpile (see the Pomona demo's sites/aep/README.md limitations table, abstract it as "a real ControlLogix transpile").
5. **Alarm notifiers beyond log/webhook**.
6. **Merge PRs #6 and #7** (note: the `demo-integration` worktree ~/Development/joyautomation/nautilus-demo exists only to build the demo binary).
7. **Native-Go function blocks** alongside ST (both lowering to the IR).
8. **Extension stable release** — first stable-channel Marketplace release, when the Test Explorer + schema work has soaked on the pre-release channel.
