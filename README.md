# nautilus

**SCADA as software.** A Go + SvelteKit toolkit for building industrial
control and supervisory systems the way software engineers already work —
version control, tests, CI/CD, code review — instead of inside a proprietary
vendor IDE.

Write your control logic in **IEC 61131-3 Structured Text** (portable, and the
same language [tentacle](https://github.com/joyautomation) runs) or in **native
Go**. Host it on a deterministic scan loop. Bring your own field I/O,
redundancy, historian, and HMI through small, documented interfaces. Develop it
in **VS Code**. Ship it like any other binary.

> nautilus is the high-code sibling to tentacle's low-code, general-purpose
> platform: same IEC substrate, different authoring surface — for teams that
> want the libraries and the seams, not a walled garden.

## Why

A PLC program should be *code*: diffable, reviewable, unit-tested, built by CI,
rolled back with one command, and observable from your editor. Vendor tooling
makes almost none of that possible. nautilus makes control logic a first-class
software artifact and keeps the runtime a tiny, pure-stdlib core you can read.

## Architecture

```
runtime/     scan loop · tag bus · program host (compile, hot-swap, retained state)
lang/st      IEC 61131-3 Structured Text: lexer, parser, lowering
lang/ir      typed IR + tree-walking virtual machine (pure stdlib)
io/          Driver seam — bring your own bus (Modbus, EtherNet/IP, OPC-UA, sim)
eip/         EtherNet/IP driver for Allen-Bradley Logix: pure-Go CIP stack,
             tag browse + UDT import codegen, polling io.Driver, Logix emulator
server/      tag API over HTTP: JSON snapshot, SSE stream, tag writes
cmd/nautilus the developer CLI: `new` (scaffold) · `check` (CI compile) · `lsp`
hmi/         SvelteKit digital-twin component kit + realtime SSE client
tools/vscode-iec/   VS Code extension: syntax, diagnostics, go-to-def, live values
examples/heated-tank/   a complete controller built on the libraries
```

**The public API is the seams.** You implement interfaces to bring your world:

| Interface | You provide |
|---|---|
| `io.Driver` | your field bus (Modbus / EtherNet-IP / OPC-UA / REST rack / sim) |
| *retain store* | where retained memory persists (file / k8s ConfigMap / db) — *coming* |
| *coordinator* | redundancy / leader election (k8s Lease / raft) — *coming* |
| *historian sink* | where process history is archived (Postgres / TSDB) — *coming* |

## Getting started

**Prerequisites:** Go 1.24+ with `$(go env GOPATH)/bin` on your `PATH`, and
VS Code for the editor experience.

**1. Install the CLI**

```sh
go install github.com/joyautomation/nautilus/cmd/nautilus@latest
```

This gives you `nautilus new` (scaffold a project), `nautilus check`
(headless Structured Text compile for CI), and `nautilus lsp` (the language
server the VS Code extension uses).

**2. Scaffold a project**

```sh
nautilus new my-plant
```

Interactive, sv-create style — pick the module path and features (a simulated
plant, a CI workflow, VS Code setup, git init). You get `main.go`,
`program.st` (your control logic), a simulated `plant.go`, acceptance tests,
CI, and `.vscode/` recommendations.

**3. Run and test it**

```sh
cd my-plant
go mod tidy      # resolves github.com/joyautomation/nautilus from the proxy
go run .         # scan loop + tag API on http://localhost:8080
go test ./...    # the program's acceptance tests
```

Open **http://localhost:8080** for the built-in live dashboard, or
`GET /api/state` for the raw tag snapshot. Reads are open; set
`NAUTILUS_TOKEN=<secret>` to require a token on writes.

**4. Develop in VS Code**

Install **nautilus IEC 61131-3** from the
[VS Code Marketplace](https://marketplace.visualstudio.com/items?itemName=joyautomation.vscode-iec)
or [Open VSX](https://open-vsx.org/extension/joyautomation/vscode-iec). It
currently ships on the **pre-release** channel, so use *Install Pre-Release
Version* (or `code --install-extension joyautomation.vscode-iec --pre-release`).
Open your project folder — it recommends the extension — and with `go run .`
running you get compile diagnostics as you type, go-to-definition / hover /
completion, and **live tag values as pills** next to identifiers in
`program.st`.

**5. Make it yours**

- Write control logic in `program.st` (IEC 61131-3 Structured Text).
- Swap `plant.go` for a real `io.Driver` — Modbus, EtherNet/IP, OPC-UA, your
  bus — when you have hardware. The control logic doesn't change.
- Add an HMI: `npm install @joyautomation/nautilus-hmi` in a SvelteKit app for
  SCADA faceplates and an SSE realtime client.
- Ship it like any Go binary: `go build`, deploy. The scaffolded CI gates on
  `go test` and `nautilus check`.

Under the scaffold, a complete controller — an IEC program on a 10 Hz scan
loop driving a field device — is about 30 lines:

```go
rt, _ := runtime.New(runtime.Options{
    Program: program,             // IEC 61131-3 Structured Text (go:embed)
    Driver:  NewPlant(),          // anything implementing io.Driver
    Scan:    100 * time.Millisecond,
    Inputs:  []string{"LevelPct", "TempC"},
    Outputs: []string{"PumpRun", "Heater"},
    DtTag:   "ScanDtS",
    Seed:    nio.Values{"TempSP": 65.0, "Kp": 12.0, "Ki": 0.15},
})
go rt.Run(ctx)                    // read inputs → run program → write outputs, every scan
```

Or, from a clone of this repo, run the worked example:

```sh
go run ./examples/heated-tank
```

```
nautilus · heated-tank — Ctrl+C to stop
level  60.0%  temp  60.0°C  pump off  heater  61%  scans 9
level  59.9%  temp  60.4°C  pump off  heater  63%  scans 20
...
```

The control logic itself lives in [`examples/heated-tank/program.st`](examples/heated-tank/program.st) —
pump hysteresis and a PI temperature loop, in plain Structured Text.

### Talking to a real PLC (EtherNet/IP)

Point the importer at an Allen-Bradley Logix controller and it generates the
types and bindings your project needs — committed source, not runtime config:

```sh
nautilus eip browse --host 192.168.1.10                 # see what's on the controller
nautilus eip import --host 192.168.1.10 \
  --tags 'Line1*,Program:MainProgram.*' \
  --writable 'Line1Cmd*'
```

That writes `eip_types.st` (a TYPE block mirroring the controller's UDTs plus
suggested VAR_EXTERNAL declarations) and `eip_manifest.go` (the tag manifest).
Wire it into `main.go`:

```go
driver, err := eip.New("192.168.1.10", EIPManifest,
    eip.WithSlot(0),
    // Polling policy is configuration, not codegen: re-running the import
    // refreshes the tag catalog without touching these.
    eip.WithScanRate(500*time.Millisecond),           // default scan class
    eip.WithScanClass("fast", 100*time.Millisecond),
    eip.WithScanClass("slow", 10*time.Second),
    eip.WithTagClass("fast", "Line1_PIT_*"),          // globs on tag/device names
    eip.WithTagClass("slow", "*_Totals"),
    eip.WithTagClass(eip.NoPoll, "Line1Cmd*"),        // cataloged + writable, never polled
)
driver.Start(ctx)
rt, err := runtime.New(runtime.Options{
    Program: program,
    Driver:  driver,
    Inputs:  driver.InputNames(),
    Outputs: driver.OutputNames(),
})
```

The driver polls each scan class on its own interval over one connection
(UDTs arrive as real struct values in ST), validates the manifest against
the live controller at startup so type drift fails loudly, and writes
changed outputs back on change — the runtime behaves like a PLC peer on the
network. Pure Go, no cgo; tested against an in-repo ControlLogix emulator
(`eip/logixserver`).

### Online edits — change logic while it runs

nautilus has two planes. The **cold plane** — connections, the tag manifest,
scan classes, server wiring — is Go, and changes ship through CI/CD as a new
binary. The **hot plane** — the ST program and tag values — can change live,
the way you online-edit a traditional PLC. Because the program is data on a
VM, a warm swap carries retained state (PID integrals, timers, counters)
across by name and type; a failed compile leaves the running program
untouched, so a typo can never fault the controller.

Enable it per controller (off by default — pushing logic is code execution
on a control system):

```go
srv := server.New(rt, server.Options{OnlineEdits: true})
```

Then from VS Code: **Download Program to Controller** warm-swaps your open
program, **Diff Program with Controller** shows running-vs-workspace, and a
status-bar indicator flags when the controller runs something other than the
committed file. Edits are ephemeral by design — a restart reverts to the
program the binary embeds, so **committing the ST to git is the only way an
edit becomes permanent**. The rule of thumb falls out of the two planes:
logic you want to tune online, write in ST; infrastructure, write in Go.

Pulling a field edit back to git closes the loop. **Pull Program from
Controller** (VS Code) or `nautilus pull --host <controller>` writes the
running program back into your program file — the inverse of download — so
you review it with `git diff` and commit. Only the program file is rewritten;
generated type files are never touched. `nautilus pull --check` reports drift
and exits non-zero, so CI can fail a build when a controller has un-pulled
edits. Composition is a single definition shared by the runtime, the language
server, download, and pull, so a program round-trips losslessly.

## Status

Early. This is the extracted, generalized core of a working demo
([mini-scada](https://github.com/joyautomation)). What's here now:

- ✅ `lang/st` + `lang/ir` — the Structured Text VM (pure stdlib, tested)
- ✅ `runtime` — scan loop, tag bus, program host + hot-swap, and PLC-style
  **online edits**: warm-swap the ST program while it runs, carrying retained
  state (PID integrals, timers, counters) across by name and type, with
  one-step rollback
- ✅ `io` — the Driver seam + an in-memory driver
- ✅ `eip` — EtherNet/IP driver for ControlLogix/CompactLogix: pure-Go (no
  cgo) CIP client with connected messaging and batched reads, tag-list + UDT
  template upload, `nautilus eip import` codegen (ST TYPE block + Go tag
  manifest), write-on-change outputs, and a Logix controller emulator
  (`eip/logixserver`) for hermetic integration tests
- ✅ `server` — tag API: JSON snapshot, SSE stream, tag writes (HMI + editor),
  and a gated program API for online edits (`GET/PUT /api/program`, rollback)
- ✅ `tools/vscode-iec/` online edits — Download Program to Controller, diff
  running-vs-workspace, rollback, and a sync-status indicator
- ✅ `cmd/nautilus` — CLI: interactive project scaffold, headless ST compile
  check for CI, and the ST language server
- ✅ `tools/vscode-iec/` — VS Code extension: syntax, compile diagnostics,
  go-to-definition, hover, completion, inline live tag values
- ✅ `examples/heated-tank` — a runnable controller serving the tag API
- 🚧 `hmi/` — SvelteKit component kit (in progress; not yet on npm)

## Roadmap

- Retained-memory, redundancy, and historian packages behind clean interfaces
- Publish `@joyautomation/nautilus-hmi` and add an HMI starter to `nautilus new`
- Native-Go function blocks alongside ST (both lowering to the same IR)
- Ladder (LD), Function Block (FBD), and SFC front-ends to the same IR, edited
  as text that projects to a diagram in VS Code
- A test harness for acceptance tests that gate deploys (from mini-scada)

## License

Apache License 2.0 — see [LICENSE](LICENSE). Copyright © Joy Automation.
