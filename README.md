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

## Quickstart

```sh
go install github.com/joyautomation/nautilus/cmd/nautilus@latest
nautilus new my-plant     # interactive: pick features, get a runnable project
cd my-plant && go mod tidy && go run .
```

You get a controller on a 10 Hz scan loop, acceptance tests (`go test`), CI
that compiles your control logic (`nautilus check`), and — with the
**nautilus IEC 61131-3** VS Code extension — compile diagnostics as you type
plus **live tag values inline in your source** while the controller runs.

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

Run the worked example:

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

## Status

Early. This is the extracted, generalized core of a working demo
([mini-scada](https://github.com/joyautomation)). What's here now:

- ✅ `lang/st` + `lang/ir` — the Structured Text VM (pure stdlib, tested)
- ✅ `runtime` — scan loop, tag bus, program host + hot-swap
- ✅ `io` — the Driver seam + an in-memory driver
- ✅ `server` — tag API: JSON snapshot, SSE stream, tag writes (HMI + editor)
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
