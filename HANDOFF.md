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
API to the leader. mini-scada source of truth: `/home/joyja/mini-scada-build`
(NOT ~/Development/mini-scada — and read-only, never modify it).

Next, in rough priority:

1. **CD scaffold** — container target for `nautilus build`, a deploy job in
   the scaffolded CI (build image → push → rollout), and a worked k8s
   example (Deployment with replicas + RBAC from the redundancy guide).
   This is the commit-to-running-controller story the content calendar's
   wk 14/N-14/N-16 need.
2. **Grow the server package** — program-history endpoint (mini-scada's
   `internal/server` has the shapes; get/set + hot-swap already exist).
3. **Native-Go function blocks** alongside ST (both lowering to the IR).
4. **Extension 0.10.0** — first stable-channel Marketplace release, when the
   Test Explorer + schema work has soaked on the pre-release channel.
