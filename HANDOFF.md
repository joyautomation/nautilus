# nautilus — session handoff

Working notes for picking up development in a fresh session. See `README.md`
for the vision/architecture; this file is the practical state + next steps.
(Untracked working doc — commit, gitignore, or delete as you like.)

## What this is

**nautilus** = "SCADA as software": a Go + SvelteKit framework for building
industrial control/supervisory systems like real software (version control,
tests, CI/CD, VS Code) instead of a vendor IDE. Control logic in **IEC 61131-3
Structured Text** (or, later, native Go), on a deterministic scan loop, with
bring-your-own field I/O / redundancy / historian / HMI through small
interfaces. It's the **high-code sibling** to Joy Automation's low-code
*tentacle* product — same IEC substrate, different authoring surface.

It was extracted from the **mini-scada** demo (`~/Development/mini-scada`, repo
`joyautomation/mini-scada`). mini-scada stays as the reference demo — **do not
modify it** when working on nautilus; copy/adapt from it.

## Repo / status

- GitHub: `joyautomation/nautilus` (private). SSH remote
  `git@github.com:joyautomation/nautilus.git`. **CI is green.**
- Go module `github.com/joyautomation/nautilus`, go 1.24, pure-stdlib core.
- **P1 is done and pushed** (initial commit). Everything below builds/tests.

## Layout (P1 + P2 editor slice complete)

```
lang/st, lang/ir     IEC 61131-3 Structured Text VM (from mini-scada/tentacle; tests pass)
runtime/             scan loop, Tags bus, Program host (compile, hot-swap, retained frame)
                     Runtime.Run(ctx) or manual Runtime.Scan(); Options{Program,Driver,Scan,Inputs,Outputs,DtTag,Seed}
io/                  io.Driver seam (ReadInputs/WriteOutputs) + Memory driver
server/              tag API: GET /api/state, GET /api/stream (SSE, 250ms), POST /api/tags. CORS on.
                     server.New(rt) + go srv.Run(ctx) + http.ListenAndServe(addr, srv.Handler())
internal/lsp/        LSP 3.17 server (pure stdlib JSON-RPC over stdio): diagnostics (st.Parse+st.Lower),
                     definition/hover/completion, POU-scoped symbol lookup. Tested end-to-end.
cmd/nautilus/        CLI: `lsp` (stdio LSP), `check` (headless .st compile, gcc-style diags, exit 1),
                     `new` (interactive scaffold, charmbracelet/huh; --no-input for CI; go:embed templates)
examples/heated-tank/  runnable controller: plant.go (in-process Driver), program.st (pump+PI), main.go
                     now also serves the tag API on localhost:8080
hmi/                 @joyautomation/nautilus-hmi — Svelte 5 kit + realtime.svelte.ts (SSE, frame-generic).
                     NOT yet published to npm (blocks the `new` HMI starter).
tools/vscode-iec/    VS Code extension v0.2: grammar + language client (spawns `nautilus lsp`) +
                     inline live values (SSE from server pkg; scanner in src/scan.ts, node:test'd).
                     npm install && npm test. engines.vscode ^1.82. out/ is gitignored.
.github/workflows/ci.yml   go: vet/test/build + `nautilus check examples`; node: extension compile+test
```

### P2 slice notes (2026-07-07)

- **Entry-point story**: `go install .../cmd/nautilus@latest` → `nautilus new`
  (sv-create-style interactive: plant sim / CI / VS Code / git-init features)
  → open in VS Code → `nautilus lsp` auto-spawned by the extension →
  `go run .` serves tags → inline live values light up. `nautilus check` gates CI.
- Parser accepts FUNCTION_BLOCKs only *before* END_PROGRAM — FBs after the
  program are silently ignored (LSP symbol tests document this; worth a
  parser diagnostic later).
- charmbracelet/huh dep is CLI-only; core stays pure-stdlib. Module pruning
  keeps it out of library consumers.
- Extension can't be integration-tested headlessly here; scanner logic is
  unit-tested, LSP is tested from Go. Manual QA: F5 dev host + heated-tank.

## Toolchain (NOT on PATH — this machine keeps them in ~/.local)

```sh
GO=~/.local/go/bin/go
NODE_PATH_PREFIX='PATH="$HOME/.local/node/bin:$PATH"'   # prepend for npm/node
GIT=~/.local/git-pkg/usr/bin/git                        # with:
export GIT_EXEC_PATH=~/.local/git-pkg/usr/lib/git-core
# gh IS on PATH, but it CANNOT shell out to the ~/.local git. So to make a
# repo: `gh repo create <name> --private ...` then add remote + push with $GIT.
```

Verify everything still works:

```sh
cd ~/Development/joyautomation/nautilus
~/.local/go/bin/go build ./... && ~/.local/go/bin/go test ./...     # Go core
~/.local/go/bin/go run ./examples/heated-tank                       # runnable controller (Ctrl+C)
cd hmi && PATH="$HOME/.local/node/bin:$PATH" npm run check          # HMI kit type-check
```

Git author for this repo is set locally (James Joy <joyja@joyautomation.com>).
End commit messages with `Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>`.

## Roadmap / where to pick up (P3+)

Done in P2: ✅ LSP + inline live values, ✅ server package (thin slice of the
old item 3), ✅ `nautilus` CLI with interactive scaffold.

Prioritized next:

1. **Publish the pieces**: tag a release so `go install .../cmd/nautilus@latest`
   resolves; publish `@joyautomation/nautilus-hmi` to npm; package/publish the
   VS Code extension (vsce). Then add the HMI starter to `nautilus new`.
2. **Extract the infra seams behind interfaces**, from mini-scada
   (`~/Development/mini-scada/plc/internal/`):
   - retain store — `retain/` (file + k8s ConfigMap impls)
   - coordinator / redundancy — `leader/` (k8s Lease leader election)
   - historian sink — `hist/` + `cmd/historian` (Postgres, lib/pq)
   Define small Go interfaces in nautilus (`RetainStore`, `Coordinator`,
   `HistorianSink`) and provide the impls as sub-packages.
3. **Grow the server package** — program get/set + hot-swap over HTTP and a
   program-history endpoint (mini-scada's `internal/server` has the reference
   shapes); the tag snapshot/stream/write slice shipped in P2.
4. **Native-Go function blocks** alongside ST (both lowering to the IR).
5. **LD / FBD / SFC** front-ends to the same IR (text that projects to a
   diagram in VS Code). Correctness risk lives in network eval order + SFC
   action qualifiers.

## Gotchas noted

- gh ↔ git PATH issue (above).
- The example plant physics (`examples/heated-tank/plant.go`) are
  demo-plausible but lightly tuned (heater vs. ambient-loss balance).
- HMI kit has no demo/gallery route yet (the app-specific stories were left in
  mini-scada); a small `src/routes/` demo page would help local visual QA.
