# nautilus — session handoff

Working notes for picking up development in a fresh session. See `README.md`
for the vision/architecture and `RELEASING.md` for the release pipeline; this
file is the practical state + next steps. Last refreshed: 2026-09-19.

## What this is

**nautilus** = "SCADA, built like software": a Go + SvelteKit framework for
building industrial control/supervisory systems like real software (version
control, tests, CI/CD, VS Code) instead of a vendor IDE. The **manifest form
is the product**: `nautilus.yaml` + IEC 61131-3 sources + `*_test.yaml`
acceptance suites, no toolchain required (`naut run/build/test/check`).
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
- **State on 2026-09-19:** PRs #1–#9 and #11 are merged; `main` carries
  everything below. Artifacts: CLI **v0.9.2**, extension **0.9.27**, HMI
  **0.6.0**. PR #10 (`diff-revisions`: diagram diffs between git revisions,
  carries the extension 0.9.28 bump) is open. The `demo-integration` worktree
  (`~/Development/joyautomation/nautilus-demo`) is the only other branch off
  main, and only for the demo build (its one net diff, a Modbus case in
  `driverHealth`, still wants cherry-picking to main).
- Releases: see `RELEASING.md`. CLI ships on `v*` tags (GoReleaser);
  extension + HMI publish-on-bump from main (`publish.yml`); `version-sync`
  in CI fails any push where a registry is ahead of the repo. Extension
  channel: odd minor = pre-release, even minor = stable.

## Layout

```
lang/st, lang/ir     IEC 61131-3 ST compiler + VM (shared substrate)
runtime/             scan loop, Tags bus, program host; injectable Clock (virtual time)
acceptance/          deterministic virtual-time test harness; runs *_test.yaml suites
internal/project     manifest loader — builds exactly what `naut run` runs
internal/lsp         LSP: ST diagnostics/hover/completion, manifest-aware tags,
                     ST expectation regions inside *_test.yaml
io/, eip/            driver seam + Memory driver; EtherNet/IP (incl. logixserver)
sparkplug/           Sparkplug B edge node (TCK edge profile in CI)
sparkplug/host       Sparkplug B host application driver + `naut sparkplug
                     import|browse|tags` codegen (TCK host profile in CI)
modbus/              Modbus TCP driver: wire, codec, block planner, per-source polling,
                     in-process slave, codegen for `naut modbus import|browse|serve|tags`
retain/, leader/, hist/  retained state (file/ConfigMap), Lease election, historian
server/              tag API (state/SSE/write) + branded dashboard
cmd/naut         CLI: new, run, build, check, test, lsp, pull
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
- `sparkplug-host.md` — the host application driver. **Built** (PR #6, merged 2026-09-10).
- `modbus.md` — the Modbus TCP driver. **Built** (PR #8); generic port of the brief the driver was written against.

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
  `runtime/hostdriver_bench_test.go`, `sparkplug/publish_bench_test.go`,
  `server/stream_bench_test.go`. The SSE delta stream is the newest consumer:
  a client's whole subscription state is ONE `uint64` (the generation it was
  last brought up to date at), and `Tags.ChangedSince(gen, dst)` is the sweep.
  Its correctness rests on one rule — a client's generation advances only when
  a frame is actually ENQUEUED, so a dropped frame costs latency, never
  content. Do not "optimise" that by advancing it at build time.
- **Quality is answered lazily, off the scan loop.** `Runtime.Quality()` asks
  the driver on demand (server tick / `/api/state`), so a driver's `Quality()`
  is called from a goroutine other than the one calling `ReadInputs` and must
  be safe for that. Per-tag quality costs the control cycle nothing.
- `examples/heated-tank-nogo` is a shared test fixture: the Go acceptance
  tests (`acceptance/heated_tank_test.go`) and the LSP tests
  (`internal/lsp/testdoc_test.go`) both run against it. If you change its
  physics or its YAML suite, run `go test ./acceptance/ ./internal/lsp/`.
  The LSP tests locate targets by content (`lineWith`), so pure line-number
  shifts are safe; renaming tags or expressions is not.
- npm trusted-publisher for the HMI verifies the workflow *filename*
  (`publish.yml`); repo variable `PUBLISH_HMI=true` arms it.
- `GOTOOLCHAIN=local` in CI keeps the pinned Go a floor (this bit v0.4.1).
- **`go test ./...` can hang for the full 10-minute default in
  `sparkplug/host`** (seen once, 2026-09-10: goroutines parked in mochi-mqtt's
  `(*Clients).Delete` lock under the parallel suite; the package passes alone
  in ~13 s and a rerun passed). Run full suites with `-timeout 240s` so a hang
  costs four minutes, and rerun before diagnosing.
- **paho never completes a QoS 0 publish token issued on a connection that
  is then torn down**, and it can lose and re-establish the connection inside
  any sane timeout — so `IsConnectionOpen()` is the wrong question for a
  pending token. Every publish in `sparkplug/` goes through `Node.publish`
  (bounded by `tokenTimeout` AND a per-connection lost signal the
  connection-lost handler closes); never call `.Wait()` on a token there.
  `scripts/repro-sparkplug-silent-link.sh` (SIGSTOP the node past the
  keepalive) is the end-to-end check; closing a socket does NOT reproduce it.
  Full story: `docs/handover/2026-09-19-sparkplug-edge-findings.md`.
- **The extension's Marketplace upload in `publish.yml` can time out**
  (`Request timeout: /_apis/gallery`, twice on 2026-09-10). It is transient:
  `gh run rerun <id> --failed` published cleanly. The Open VSX step is skipped
  when it fails, so both registries lag until the rerun.
- Toolchain is standard now: `go` on PATH (1.25.x local, CI pins 1.24),
  `npm`/`node` via `~/.local/node/bin` on PATH for hmi/extension work.

## Working habits

- **Content as you go**: when work produces something demo-able (feature,
  war story, design rationale), record the episode/post idea in
  `~/Development/joyautomation/content` before wrapping up.

## Roadmap / where to pick up

Done through the acceptance-testing branch: virtual-time harness +
`naut test`, manifest-first docs/README, manifest-aware LSP + Test
Explorer, tag files/UDTs/shape check, branded dashboard, Process Overview
demo with flow-balance physics.

Done 2026-08-17: the three mini-scada seams — `retain/` (file + ConfigMap),
`leader/` (Lease elector; `runtime.Coordinator` gates the scan loop),
`hist/` + `naut historian` (Postgres, `hist.Sink`). Manifest sections
`retain:`/`redundancy:`/`server.historian`; standby replicas proxy their
API to the leader. Also the CD scaffold: `naut new --deploy` emits
Dockerfile + redundant-pair k8s + deploy workflow (commit-to-running-
controller). mini-scada source of truth: `/home/joyja/mini-scada-build`
(NOT ~/Development/mini-scada — and read-only, never modify it).

Done 2026-08-18: **program history + activation** — the controller serves
its own git provenance. `internal/vcs` captures commits + diffs + deduped
file snapshots (git blob ids); `naut build` embeds it as the `.history`
archive entry, `naut run` captures live (lazily, on first request);
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
by struct TYPE + member, materialized once at load — `naut alarms list`
dumps the expansion, `naut check` validates it offline (unknown member
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

Done 2026-08-24 (st-struct-pins, uncommitted): **per-tag quality + SSE
deltas/filters** — the two platform seams the HMI phase needs. `io.Quality`
(good/stale/bad/notConnected) + optional `io.QualityReporter` on a driver;
`Runtime.Quality()/TagQuality()/ReportsQuality()` (driver-reported wins,
runtime derives Stale for driver-bound inputs when the last input READ
failed — not on a failed write); `Tags.ChangedSince(gen, dst)` and exported
`runtime.Plain`. Server: `?delta=1` (per-client `lastGen`, `seq`+`full`
markers, resync on tag-set change or `Options.ResyncInterval`, default 30s),
`?tags=glob,glob` on both /api/stream and /api/state, `?full=1` escape
hatch, `quality` map on every frame (non-Good entries only), `/api/meta`
`quality`+`deltas` flags. Kit: `RealtimeClient({tags, delta})` — **delta
defaults on**, merges into a complete `frame.tags`; `quality(tag)`/
`isGood(tag)`; pure `delta.ts` merge + `npm test` (tests/harness.ts, a
60-line vitest subset, no new dependency). Measured 10k tags / 5% churn:
**280 kB full → 17 kB delta, 17× smaller** (51× at 1%, 4.8× at 20%); a
50-client delta broadcast costs ~1/10 of a full one (encodings are memoised
per generation+filter, so the fleet shares one). Deltas are OPT-IN on the
wire — the plain stream is byte-identical to what it always was, since the
VS Code extension and any curl client depend on it. Guide:
website/.../guides/streaming.md.

Done 2026-08-24 (st-struct-pins, uncommitted): **the SSE frame floor** — the
non-tag blocks gated like tags. Measured on the WTP host, every frame
carried ~17.9 kB that had nothing to do with tags (driver status ~12.8 kB —
55 device rows + `extra`; scan diagnostics ~5 kB; alarm summary), so a
client filtered down to NO tags still pulled 4.35 MB/min. Now, for a client
that asks `?blocks=delta` (kit sends it whenever `delta` is on, opt-in
again on top of deltas — an older kit merges tags but not blocks and would
blank its driver panel): `drivers` on change (`hashDrivers` — a 64-bit FNV
over everything an operator would call a change, EXCLUDING `AsOfMs`,
`DriverMetric.Volatile` counters, `AtMs`, the `Text` of a metric that has
an `AtMs`, and `VolatileExtra` keys); `alarms` on `Summary.Rev` (and the
summary is not computed when nobody is owed it); `scan` on a cadence
(`Options.DiagnosticsInterval`, default 3s — the block is a 180-sample
history ring, so a cadence under the ring's span loses nothing) with
`Frame.Scan` now `*runtime.ScanStats`. Per-client `blockRevs` advance only
on a successful enqueue, exactly like `lastGen`; full frames (first +
resync) always carry everything. Ages moved client-side:
`DriverMetric.AtMs` + `DriverStatus.AsOfMs` (server-stamped), rendered as
`asOfMs − atMs` so a block sent 20 s ago does not creep upward; `Text` kept
for older readers, `/api/meta` gains `blockDeltas`. Kit: `mergeDelta`
retains `scan`/`drivers`/`alarms` (`DeltaState.blocks`), full frames
replace the retained set, `quality` deliberately NOT retained.
**Measured 4.3 MB/min → 0.15 MB/min, ~28× smaller** for a no-tags delta
client at 5% churn (`-bench FrameFloor`).

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
`naut check`/`build` pass with no broker in sight). `nautilus
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
the Riverbend WTP demo at `~/Development/riverbend/wtp` — a ~60-site fleet is
the real target this driver is being built for.

Done 2026-08-24: **Per-tag quality on sparkplug-host** — `Driver.Quality()` implements `io.QualityReporter` (its seam ported from `st-struct-pins`' `io/quality.go`, byte-identical apart from the Memory-driver half that branch's differing `io.go` doesn't support here yet): NotConnected for a data binding never delivered (never birthed, or the metric a birth simply never carries), Stale for one with a value on file whose node/device is offline or gone stale, Good (omitted) once delivered and online; writable and companion tags are always Good.

Done 2026-09-10: **integration debt cleared.** PR #6 (sparkplug-host)
merged as a merge commit; HMI 0.6.0 and extension 0.9.25 published; the
Modbus branch stack merged with main (three small conflicts — the CLI's
`modbus` subcommand, and `driverStatusFuncs` in `internal/project/drivers.go`,
which is the multi-driver branch's replacement for main's inline type
switches and already covers the Sparkplug host). Docs: the four language pages
carry VS Code screenshots (`website/src/assets/editors/`) of text-left,
diagram-right with live values, taken against `examples/heated-tank-nogo`
and `examples/tank-batch-sfc`. README's sparkplug-host paragraph now says
writes to an offline site are queued, matching the driver.

Done 2026-09-13: **Modbus TCP finished and up for review (PR #8).** Beyond
the driver that was already on the branch: a foreign-implementation test —
`modbus/testdata/sim/pymodbus_sim.py` (pymodbus 3.15, four units, every
format in both orders, an absent range that answers exception 0x02) driven
by `modbus/foreign_test.go` (gated on `NAUTILUS_MODBUS_SIM`, plus
`NAUTILUS_MODBUS_SIM_SLOW` for the latency case) as the `modbus-sim` CI job;
`scripts/modbus-sim.sh` runs the same on a laptop. It found one real driver
gap: a request timeout re-dialed immediately with no backoff (only a failed
dial backed off), and a connected-but-silent device's tags read Good — now
the ladder climbs on a broken connection and resets only once a read was
answered, and a never-delivered tag is NotConnected even while the socket is
up. In-process slave tests cover block parking/un-parking, a wrong-unit-id
reply, and one slow block leaving the others coherent. `docs/design/modbus.md`
is the generic brief; the guide is `guides/modbus.md`; README gained its
section; extension 0.9.26 ships the `modbus` schema. `multi-driver`
followed as PR #9 (merged the same day): `drivers:` on one scan via
`io.Multi`, documented in the Modbus guide and README, extension 0.9.27.
Released as **v0.9.0** (Modbus, tagged one commit early) and **v0.9.1**
(drivers:); CLI v0.8.0 → v0.9.1 is the jump that adds `naut modbus`. Not done: a run against real hardware (checklist
below), and `naut modbus serve --from <url>` (feed the bench slave from a
running controller's /api/state so a sim project drives the "devices").

Done 2026-09-19 (PR #11, **v0.9.2**): the Sparkplug edge findings from the
Mantle-for-Ignition integration suite (`~/Development/joyautomation/ignition`,
`integration/`; it builds nautilus from this checkout). (1) A silent link —
SIGSTOP past the keepalive — wedged the publish goroutine forever in an
unbounded paho `Wait()`; every token wait is now bounded through
`Node.publish` with a per-connection lost signal, no publish while the
transport is down, seq assigned at publish time in wire order (a message
that did not go out hands its number back; this also fixed a seq inversion
on every store-and-forward drain), a failed tick's messages buffered when
store-and-forward is on. (2) An untyped manifest tag's `init:` seeds as the
type the program's `VAR_EXTERNAL` declares — `init: 0` on a DINT births as
Int64, not Double. (3) N/DBIRTH metrics carry `engUnit`/`documentation`
from `unit:`/`desc:` (template members under their dotted path); the
decoder keeps properties and `naut sparkplug import` fills `unit:`/
`desc:` from a live birth. (4) Store-and-forward now buffers across a broker
outage, not only a primary-host outage. Handover + Outcome:
`docs/handover/2026-09-19-sparkplug-edge-findings.md`. Content idea N-34.

Next, in rough priority:

1. **Modbus real-device run** — when the bench devices are available:
   `naut modbus browse` each for word order and addressing; run
   `examples/modbus` with a device map for the real units; confirm exception
   behaviour on an unimplemented register and reconnect after a cable pull;
   record each device's quirks in a "devices we have met" table in the guide.
   Also still open: `naut modbus serve --from <url>` (feed the bench
   slave from a running controller's /api/state), and rebuilding the demo
   binary from `demo-integration` (that worktree exists only for that).
2. **HMI Versions page** — render /api/program/history in
   @joyautomation/nautilus-hmi (mini-scada's Versions page is the
   reference): commit list, diffs, activate button. The demo moment for
   the content calendar ("your PLC shows its own git log").
3. **Alarm engine + fleet HMI patterns** — driven by the Riverbend WTP demo
   (`~/Development/riverbend/wtp`): a real alarm/annunciation model over a
   sparkplug-host fleet (priorities, ack/shelve, per-site rollups), and
   the HMI components a multi-site SCADA screen actually needs beyond
   `DriverStatusPanel`. Worth a look while there: IEC 62923's silence-with-
   timer state and warning→alarm escalation (evaluated 2026-09-09 against
   OpenBridge; not adopted as code, the ISA-18.2 skeleton stays).
4. **Remote counter RESET coil / task scan-order guarantee / remote-program FB
   pin reads** — asks from a real ControlLogix transpile (see the Riverbend
   demo's sites/aep/README.md limitations table; abstract it as "a real
   ControlLogix transpile").
5. **Alarm notifiers beyond log/webhook**.
6. **Native-Go function blocks** alongside ST (both lowering to the IR).
7. **Extension 0.10.0** — first stable-channel Marketplace release, when the
   Test Explorer + schema work has soaked on the pre-release channel.
8. **Amber badge → lightbulb** (content review, 2026-09-18). The "no tag on
   the controller" diagnostic is honest but leaves the person to do two
   things by hand: a write to create the tag now (`nautilus: Set Live
   Value…` already does it) and a manifest line to keep it (typed by hand
   in wk04 beat 3). Offer both as code actions on the diagnostic: "Set a
   live value…" and "Add `<name>` to nautilus.yaml as a setpoint" (role,
   init, unit prompted; inserted next to the tag it belongs with). The
   durable path should be the easy one. Related caution to keep in the
   guide: any API write creates a tag, so a typo'd name from an HMI makes
   a new tag instead of failing — the driver allowlist and the write token
   are the only guards today.

- **VS Code extension (2026-08-22 check):** the ladder-FB webview work (ldPreview.ts, LadderView.svelte, ladder.ts) compiles, svelte-checks, vite-builds and tests green (59+84). Pre-existing, unrelated: `tools/vscode-iec/webview-ui/package.json` pins `typescript: ^7.0.2`, which svelte-check 4.7.x cannot load (needs TS ^5||^6 — `ts.sys` gone); run `npm install --no-save typescript@^5.9` to check locally, and 39 older svelte-check errors exist in App/Sfc/mimic/test files (missing @types/node, allowImportingTsExtensions, @xyflow .d.ts). Track separately.
