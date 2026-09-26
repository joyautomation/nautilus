# Examples, rebuilt: real projects that show the whole product

Status: **done (2026-09-25).** Four projects shipped: `lift-station` (#40,
the flagship — SFC/FBD/LD/ST, Modbus, alarms, retain, online edits, a
custom-component HMI), `batch-skid` (#53 — SFC/FBD/LD, an EtherNet/IP line
handshake, a read-only `.L5X` diffed between revisions), `remote-fleet`
(#52 — three Sparkplug B edge sites and a Sparkplug host SCADA), and
`go-sdk` (the trimmed former `heated-tank`, the one remaining Go-tier
example). The nine superseded feature demos and the client-specific
`client60` gitignore carve-out are gone, the root README/website docs/CI
point at the new projects, and `naut check`/`naut test` cover every
project's every manifest in CI. See `examples/README.md` for the
start-here index and `docs/design/examples-dogfood.md` for the friction
log kept while building all four.

---

Status (original brief): **brief**, not started (2026-09-25). This is written for the session that
picks the work up.

## Why

`examples/` is the second most visited page in the repo after the front README.
It grew one feature at a time: `heated-tank` for the Go tier, `heated-tank-fbd`,
`tank-batch-sfc`, `ladder-subroutines`, `alarms`, `modbus`, `sparkplug-host`,
`pump-station` and `hmi-demo`. As a result:

- no example is a *project* a controls engineer would recognise as real work;
- each one shows one feature, and none shows how they fit together;
- three of them need Go for their plant model, which contradicts "the manifest
  form is the product";
- the languages are unevenly covered (only one `.sfc`, no mimic that ships with
  a control project).

The replacement is **a small number of believable plant projects that between
them cover everything nautilus supports.** Each is built the way a user would
build it, **through the VS Code extension, driven in the content repo's
container rig**, and that build is the dogfooding for the extension's stable
release. It also produces the footage and stills for launch content.

## Principles

1. **A project is a plant, not a feature demo.** Each has a one-paragraph
   process description, a P&ID-ish mimic, control narrative, alarms and tests.
   Scenarios are generic industry patterns such as water/wastewater, batching
   and remote sites. **No client material:** pomona and stax are Tier 3 (see
   `content/sourcing.md`), so no names, tag conventions or logic from them.
2. **Manifest-first.** Each project runs with `naut run` and has no Go; the
   plant model is ST (`sim.st`, `lib/physics`-style). Keep exactly one small
   Go-tier example (the SDK story: custom driver or richer sim), clearly
   labelled as the advanced path.
3. **Built through the editors, in the rig.** Where a language has a
   graphical editor (FBD, LD, SFC, mimic, component), author it with gestures
   in real VS Code: the palette, wiring, rungs, steps, drag and drop.
   - Use the extension from the **Marketplace pre-release channel** (the
     artifact users get) and `naut` from the latest GitHub Release, not source
     builds.
   - Text is fine where text is the natural medium (ST, YAML, tests), and
     typing it in the editor still exercises the language server.
   - Be honest about cost: gesture-driving a large diagram with xdotool is
     slow, so gesture-build the parts that make good footage and would catch
     bugs, and text-author the rest, then open, edit and verify them in the
     editor.
4. **Every project is tested.** `*_test.yaml` acceptance suites in virtual
   time cover the control narrative: start/stop, failover, alarm raise and
   clear, sequencing. They run in CI (`naut test`), so the examples can't rot.
5. **Every project has a README** that says: what the plant is, what to open
   first, which features it demonstrates (link to the docs), and how to run it.
   `examples/README.md` is the start-here index. It was written on 2026-09-19
   (fffc3a5), so check whether it reached main.

## Coverage to hit, across all projects

| Area | Must show |
|---|---|
| Languages | ST (programs, FBs, functions, TYPEs/UDTs, VAR_IN_OUT), FBD (incl. PID loop, seal-in, comments), LD (interlocks, permissives, TON/CTU, subroutines/ladder libs), SFC (alternative and simultaneous branches, actions, qualifiers N/S/R/P1, timers), Rockwell `.L5X` read-only ladder |
| Project structure | multiple tasks/scan classes, `lib/` shared code, multiple programs |
| Drivers | Memory/sim, Modbus TCP (with `naut modbus import`), EtherNet/IP, Sparkplug B edge node, Sparkplug host, `drivers:` (several on one scan) |
| Runtime | alarms (ISA-18.2: priorities, ack/shelve), historian, retain, leader election if it can be shown on one box, online edit (download/rollback) |
| HMI | a mimic per plant, bound to live tags, with at least one custom Svelte component and port overrides |
| Tooling | acceptance tests, `naut check`, diff vs HEAD / between revisions / vs controller, live values, Test Explorer |

## Candidate projects (the session should confirm or reshape)

1. **Lift station** (water/wastewater, the flagship and start-here project).
   - Duty/standby pump alternation and lead/lag in **SFC**.
   - Wet-well level control and a VFD speed loop in **FBD**.
   - Permissives, interlocks and the hand/off/auto logic in **LD**.
   - The wet-well and pump physics in **ST**.
   - Alarms (high-high level, pump fail), a historian trend and a mimic.
   - **Modbus** to the "VFDs", using the in-process slave for the bench.
   - Tests: pump failover, high-level alarm, alternation after N starts.
2. **Batch mixing skid.**
   - Recipe phases in **SFC**, recipes as **TYPEs/UDTs** in ST.
   - Dosing and a temperature PID in FBD.
   - An **EtherNet/IP** target, plus an `.L5X` of an "existing" line opened
     read-only and diffed across revisions, for the Logix story.
3. **Remote sites fleet.**
   - Three small sites as **Sparkplug B edge nodes**.
   - A central **Sparkplug host** project.
   - `drivers:` with more than one driver, and a store-and-forward demo of a
     link outage.
4. **SDK example** (the only Go one). A custom driver or a richer plant model,
   probably the current `heated-tank` Go tier trimmed down and renamed.

## Constraints

- **Tests use the current examples as fixtures:**
  - `acceptance/heated_tank_test.go` and `internal/lsp/testdoc_test.go` use
    `examples/heated-tank-nogo` (its physics and YAML suite).
  - `cmd/naut/check_manifest_test.go`, `internal/project/fbexternal_test.go`,
    `internal/project/ldlib_test.go`, `eip/codegen/tags_test.go` and
    `lang/sfc/selection_property_test.go` also read `examples/`.
  - Before deleting anything, move the fixtures those tests need to
    `testdata/`, or point the tests at the new projects, in a separate PR.
    Keep them green throughout.
- **Other references to update:**
  - The root README (heated-tank, the `go run ./examples/heated-tank`
    walkthrough).
  - The website docs.
  - `naut new` templates, if they point at examples.
  - The content repo's episode scripts and `verify.sh` files, which name
    example paths. grep `content/` before renaming.
- One PR per project. Extension or CLI bugs found while building get their
  own PRs: extension PRs go under CHANGELOG `[Unreleased]`, and a release PR
  bumps 0.11.x (see `RELEASING.md`).

## Dogfooding protocol

- Keep a running **dogfood log**, `docs/design/examples-dogfood.md` or a
  section in the PR bodies. It records every friction point, even small ones:
  what you did, what you expected, what happened, and whether it's a bug, a UX
  papercut or a docs gap.
- Bugs become issues or fix PRs. The extension's first stable release
  (`vscode-v0.10.0`) is gated on building the flagship project without a
  blocking bug.
- The rig (see `content/assets/capture/README.md`):
  - `record-vscode.sh` with `VSIX=` and `NAUT=` in an incus + Xvfb
    container. **Never drive mira1's real display.**
  - Recipes go in `content/assets/capture/<episode>/desktop/`, so every build
    step can be re-rendered as footage.
  - Run the smoke suite (`ext-stable/smoke/run.sh`) before any stable
    promotion.

## Content

Each project build is an episode or post candidate. The flagship lift station
is the "zero to running plant" video. Record ideas in `content/ideas.md` as you
go, and check `content/calendar.md` for where they fit. The gesture recipes
are the raw footage.

## Suggested order

1. Read this brief, the current examples, `content/assets/capture/README.md`
   and `content/sourcing.md`. Confirm the project list with James.
2. PR: move the test fixtures out of `examples/` (no behaviour change).
3. Lift station, end to end, built in the rig with the dogfood log. This is
   the stable gate.
4. Batch skid, then the remote fleet, then trim the SDK example.
5. Remove the superseded examples, rewrite `examples/README.md` and the root
   README's example section, and update the docs site.
