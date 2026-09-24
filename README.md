# nautilus

**SCADA, built like software.** A Go + SvelteKit toolkit for building
industrial control and supervisory systems the way software engineers already
work, with version control, tests, CI/CD, and code review, instead of inside a
proprietary vendor IDE.

Write your control logic in **IEC 61131-3** (Structured Text, Ladder, Function
Block, or Sequential Function Chart, the portable standard languages) or in
**native Go**. Host it on a deterministic scan loop. Build the operator screens on the included
**SvelteKit component kit**. Bring your own field I/O, redundancy and historian
through small, documented interfaces. Develop it in **VS Code**. Ship it like
any other binary.

> For teams that want the libraries and the seams, not a walled garden.

## Why

A PLC program should be *code*: diffable, reviewable, unit-tested, built by CI,
rolled back with one command, and observable from your editor. Vendor tooling
makes almost none of that possible. nautilus makes control logic a first-class
software artifact and keeps the runtime a tiny, pure-stdlib core you can read.

## Architecture

```
runtime/     scan loop · tag bus · program host (compile, hot-swap, retained state)
lang/st      IEC 61131-3 Structured Text: lexer, parser, lowering
lang/fbd     Function Block Diagram: netlist text ⇄ diagram model, transpiles to ST
lang/ld      Ladder Diagram: rung text ⇄ ladder model, transpiles to FBD
lang/sfc     Sequential Function Chart: steps, transitions, actions, transpiles to ST
lang/ir      typed IR + tree-walking virtual machine (pure stdlib)
io/          Driver seam — bring your own bus (Modbus, EtherNet/IP, OPC-UA, sim)
eip/         EtherNet/IP driver for Allen-Bradley Logix: pure-Go CIP stack,
             tag browse + UDT import codegen, polling io.Driver, Logix emulator
sparkplug/   Sparkplug B edge node (and host application) over MQTT
retain/      retained-memory stores: file, Kubernetes ConfigMap
leader/      redundancy: Kubernetes Lease leader election
hist/        historian seam + Postgres sink, `naut historian`
alarm/       ISA-18.2 alarms: rules over UDT members, ack/shelve, journal
acceptance/  virtual-time acceptance tests (`naut test`, `*_test.yaml`)
server/      tag API over HTTP: JSON snapshot, SSE stream, tag writes, alarms, program history
cmd/naut the developer CLI: new · run · test · check · build · pull · lsp · eip · sparkplug · historian · alarms
hmi/         SvelteKit digital-twin component kit + realtime SSE client
tools/vscode-iec/   VS Code extension: syntax, diagnostics, go-to-def, live values, diagram editors
examples/    heated-tank-nogo (manifest project, four tasks, three languages), hmi-demo, tank-batch-sfc, …
```

**The public API is the seams.** You implement interfaces to bring your world:

| Interface | You provide |
|---|---|
| `io.Driver` | your field bus (Modbus / EtherNet-IP / OPC-UA / REST rack / sim) |
| `retain.Store` | where retained memory persists — file and k8s ConfigMap ship in `retain/` |
| `runtime.Coordinator` | redundancy / leader election — a k8s Lease elector ships in `leader/` |
| `hist.Sink` | where process history is archived — Postgres + `naut historian` ship in `hist/` |
| `alarm.Journal` / `alarm.Notifier` | where alarm events land — a ring, rotated JSONL, and Postgres ship in `alarm/` |

## Getting started

**Platforms:** nautilus is one static binary with no runtime dependencies.
Every release ships builds for **macOS** (Apple Silicon and Intel),
**Linux** (x86-64 and arm64), and **Windows** (x86-64 and arm64); the
controller it builds runs anywhere the same binary does, from a laptop to a
Raspberry Pi to a Kubernetes pod. VS Code is the editor experience on all
three. The
[release page](https://github.com/joyautomation/nautilus/releases/latest)
has the archives and a `checksums.txt`.

**1. Install the CLI**

*macOS* — pick `arm64` for Apple Silicon, `amd64` for Intel:

```sh
v=$(curl -fsSL https://api.github.com/repos/joyautomation/nautilus/releases/latest | grep -m1 '"tag_name"' | cut -d'"' -f4)
curl -fsSL "https://github.com/joyautomation/nautilus/releases/download/$v/nautilus_${v#v}_darwin_arm64.tar.gz" | tar xz naut
sudo mv naut /usr/local/bin/
```

Downloading with `curl` skips Gatekeeper's quarantine. If you fetched the
archive in a browser instead and macOS refuses to open the binary, clear the
flag once: `xattr -d com.apple.quarantine /usr/local/bin/naut`.

*Linux* — `amd64` or `arm64`:

```sh
v=$(curl -fsSL https://api.github.com/repos/joyautomation/nautilus/releases/latest | grep -m1 '"tag_name"' | cut -d'"' -f4)
curl -fsSL "https://github.com/joyautomation/nautilus/releases/download/$v/nautilus_${v#v}_linux_amd64.tar.gz" | tar xz naut
sudo install naut /usr/local/bin/naut
```

Check it:

```sh
naut version
```

> **The command is `naut`, not `nautilus`.** GNOME's file manager already
> owns `/usr/bin/nautilus` on essentially every Linux desktop, and sharing
> the name broke both ways: install ours on `PATH` and clicking *Files* in
> the dock launched the CLI (`org.gnome.Nautilus.desktop` runs
> `Exec=nautilus` by bare name); leave it off `PATH` and typing `nautilus`
> opened the file manager, which read the argument as a folder and showed
> *"Unable to find … Please check the spelling and try again."*
>
> If you installed an earlier build as `nautilus`, remove it:
> `sudo rm -f /usr/local/bin/nautilus`. The project is still called nautilus
> — only the command changed.


*Windows* — PowerShell, `amd64` or `arm64`:

```powershell
$v = (Invoke-RestMethod https://api.github.com/repos/joyautomation/nautilus/releases/latest).tag_name
Invoke-WebRequest "https://github.com/joyautomation/nautilus/releases/download/$v/nautilus_$($v.TrimStart('v'))_windows_amd64.zip" -OutFile nautilus.zip
Expand-Archive nautilus.zip -DestinationPath "$env:LOCALAPPDATA\nautilus" -Force
[Environment]::SetEnvironmentVariable("Path", "$env:LOCALAPPDATA\nautilus;" + [Environment]::GetEnvironmentVariable("Path", "User"), "User")
```

Open a new terminal afterwards so the `Path` change is picked up.

*Any OS, with Go 1.24+ installed:*

```sh
go install github.com/joyautomation/nautilus/cmd/naut@latest
```

This puts the binary in `$(go env GOPATH)/bin`, which needs to be on your
`PATH`. Whichever route you took, `naut version` should now answer.

The one binary is the whole toolchain: `naut new` (scaffold a
project), `run`, `test`, `check` (the CI gate: compiles every program and
cross-checks it against the manifest), `build`, `pull` (bring a controller's
running program back into the repo), `lsp` (the language server the VS Code
extension uses), and the `eip`, `modbus`, `sparkplug`, and `historian` tools.

**2. Scaffold a project**

```sh
naut new my-plant                      # the tour: 3 tasks, 3 IEC languages, simulated plant
naut new my-plant --template minimal   # one task, one program, one test
naut new my-plant --template sdk       # Go project, for a custom field bus
naut new my-plant --template sdk-demo  # Go project with plant physics in Go
```

A nautilus project is your logic and a manifest. Run, test, and ship it
with the CLI alone — no toolchain:

```sh
cd my-plant
naut run        # scan loop + dashboard + tag API on http://localhost:8080
naut test       # acceptance tests, in virtual time
naut build      # emit ./my-plant — a self-contained controller binary
```

`nautilus.yaml` declares the tasks (one program file each, any language,
own scan rates), the tags by role, the server, and the field driver
(loopback for bring-up, EtherNet/IP by configuration). `naut build`
appends the project to the runner and emits one deployable binary — no Go
toolchain anywhere.

**Your logic gets real tests.** `*_test.yaml` files run against a **virtual
clock**, so a ten-second on-delay or a PI loop's settling time is asserted
exactly, deterministically, in milliseconds — the assertions a wall clock
makes impossible:

```yaml
  - name: low-temp alarm waits its full 10 s
    suspend: [sim]                    # freeze the plant; drive the value directly
    given: { TempC: 45.0 }
    steps:
      - advance: 9.5s
        expect: { TempLowAlm: false } # the TON has not elapsed
      - advance: 1s
        expect: { TempLowAlm: true }  # ... and now it has
```

The fixture is `nautilus.yaml` itself — tasks, rates, roles, seeds — so a
retuned gain can't drift away from what the tests verify. Assertions are
tag matchers or ST expressions (`ABS(TempC - TempSP) < 0.5`) compiled by
the same compiler as your logic.

Go is the **SDK**, not the base: `--template sdk` when you need a custom
field bus or richer simulation physics. Same runtime, with the manifest
written as code.

Run it bare for the interactive form, sv-create style — pick the template,
the program language, and the features (CI workflow, VS Code setup, git
init). An `sdk` template also asks for a module path, and gives you
`main.go`, `program.st`, a `driver.go` seam to fill in, Go acceptance
tests, CI, and `.vscode/` recommendations.

**3. Run and test it**

```sh
cd my-plant
naut test    # the acceptance suite, in virtual time
naut run     # scan loop + tag API on http://localhost:8080
```

An `sdk` project is an ordinary Go program — `go mod tidy`, `go run .`,
`go test ./...` — and its Go tests get the same virtual clock through the
`acceptance` package.

Open **http://localhost:8080** for the built-in live dashboard, or
`GET /api/state` for the raw tag snapshot. Setpoints are click-to-set right
in the tag table (BOOLs get a toggle); inputs and outputs are not editable,
because the driver rewrites one before every scan and the logic rewrites the
other after. Reads are open; set `NAUTILUS_TOKEN=<secret>` to require a token
on writes.

**4. Develop in VS Code**

Install **nautilus IEC 61131-3** from the
[VS Code Marketplace](https://marketplace.visualstudio.com/items?itemName=joyauto.vscode-iec)
or [Open VSX](https://open-vsx.org/extension/joyauto/vscode-iec). It
currently ships on the **pre-release** channel, so use *Install Pre-Release
Version* (or `code --install-extension joyauto.vscode-iec --pre-release`).
Open your project folder — it recommends the extension — and with `go run .`
running you get compile diagnostics as you type, go-to-definition / hover /
completion, and **live tag values as pills** next to identifiers in
`program.st`.

On macOS, VS Code launched from the Dock or Spotlight gets the login `PATH`,
not your shell's, so it may not find `naut` even though your terminal
does. If the extension reports it could not start the language server, set
`nautilus.cliPath` to the full path (`which naut`), or launch VS Code
from a terminal with `code .`.

**5. Make it yours**

- Write control logic in `program.st`, or in `.ld`, `.fbd`, or `.sfc`; the
  graphical languages open in full diagram editors in VS Code.
- Swap `plant.go` for a real `io.Driver` — Modbus, EtherNet/IP, OPC-UA, your
  bus — when you have hardware. The control logic doesn't change.
- Add an HMI: `npm install @joyautomation/nautilus-hmi` in a SvelteKit app for
  SCADA faceplates and an SSE realtime client. Build it with `adapter-static`
  and a manifest project can serve the build itself — `server: { hmi:
  ./hmi/build }` — so one binary is the whole deploy; see the tag-model guide's
  "Serving the HMI from the controller".
- Ship it as one binary: `naut build` for a manifest project, `go build`
  for an SDK project. The scaffolded CI gates on `naut check`,
  `naut test`, and `naut build`; `naut new --deploy` adds a
  Dockerfile, a redundant-pair Kubernetes manifest, and the workflow that
  ships a merged commit to the controller.

Under the scaffold, a complete controller — an IEC program on a 10 Hz scan
loop driving a field device — is about 30 lines:

```go
rt, _ := runtime.New(runtime.Options{
    Program: program,             // IEC 61131-3 Structured Text (go:embed)
    Driver:  NewPlant(),          // anything implementing io.Driver
    Scan:    100 * time.Millisecond,
    DtTag:   "ScanDtS",
    Tags: []runtime.TagDef{      // each tag's role, seed, and HMI docs — once
        runtime.Input("LevelPct", runtime.Unit("%")),
        runtime.Input("TempC", runtime.Unit("°C")),
        runtime.Setpoint("TempSP", 65.0, runtime.Unit("°C")),
        runtime.Setpoint("Kp", 12.0),
        runtime.Setpoint("Ki", 0.15),
        runtime.Output("PumpRun", runtime.Init(false)), // logic reads it back
        runtime.Output("Heater", runtime.Unit("%")),
    },
})
go rt.Run(ctx)                    // read inputs → run program → write outputs, every scan
```

How tags work — where they come from, which role fits which job, and the
one rule that bites (reads fault, writes create) — is spelled out in
[The tag model](#the-tag-model) below.

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
naut eip browse --host 192.168.1.10                 # see what's on the controller
naut eip import --host 192.168.1.10 \
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

### Talking to Modbus TCP devices

Field devices that aren't a Logix PLC — PID loops behind a gateway, VFDs,
analysers, power meters — come from a committed **device map**: register
maps off the datasheet once per device *type*, hosts and unit-ids once per
*instance*.

```sh
naut modbus import --map devices.yaml --plan
```

That writes `modbus_manifest.yaml` and `tags/modbus.yaml` — both generated,
never hand-edited, byte-identical on re-run — and `--plan` prints the
block-read plan: a four-channel analyser's floats coalesce into one FC3
request instead of four. In a manifest project the driver is configuration:

```yaml
driver:
  type: modbus
  manifest: modbus_manifest.yaml   # from `naut modbus import`
  scan-rate: 1s
  scan-classes: { fast: 500ms, slow: 2.5s }
tag-files: [tags/modbus.yaml]
```

Addresses are 0-based PDU addresses (40001 is holding 0); word and byte
order are per source, because the same hardware ships both ways. A Modbus
exception marks just that block bad and keeps polling; a transport failure
reconnects with backoff while values hold and `<source>__Online` goes false.
`naut modbus serve` stands in for the whole plant on one listener, so
the bench needs no hardware. `examples/modbus` is a complete plant, and the
[Modbus TCP guide](https://nautilus.joyautomation.com/guides/modbus/) covers
the rest.

#### More than one bus: `drivers:`

A controller that polls a Modbus skid *and* consumes a Sparkplug fleet on
the same scan lists both. `drivers:` is the plural of `driver:` — each entry
is the same shape, with an optional `name` that status rows and error
messages use (default: the type, deduped as `eip-2` when a type repeats):

```yaml
drivers:
  - type: modbus
    manifest: modbus_manifest.yaml
  - type: sparkplug-host
    name: fleet
    broker: "tcp://mqtt.plant:1883"
    group-id: Plant
    host-id: plant-scada
    manifest: sparkplug_manifest.yaml
```

Reads fan out and merge, writes route to the driver whose bindings claim the
tag, and ownership is disjoint by construction: a tag delivered by two
drivers is a load error naming both, the same no-last-wins rule tag files
keep. `/api/drivers` shows one row per driver, and a Sparkplug `device:`
counts as healthy only when every child bus is. The `memory` loopback cannot
join a list — it owns whatever is written to it, which is exactly what makes
it unroutable next to another driver.

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
Controller** (VS Code) or `naut pull --host <controller>` writes the
running program back into your program file — the inverse of download — so
you review it with `git diff` and commit. Only the program file is rewritten;
generated type files are never touched. `naut pull --check` reports drift
and exits non-zero, so CI can fail a build when a controller has un-pulled
edits. Composition is a single definition shared by the runtime, the language
server, download, and pull, so a program round-trips losslessly.

**Working against a remote controller.** All of this — live values, online
edits, pull — works over the network, not just against a local process. A
scaffolded controller binds loopback by default; set `NAUTILUS_ADDR=0.0.0.0:8080`
to expose the tag API to other machines, and point the editor at it with the
`nautilus.runtimeUrl` setting (`naut pull` takes `--host`). Exposing the
API on the network also exposes its write surface, so set `NAUTILUS_TOKEN` on
the controller and the matching `nautilus.token` in the editor — reads and
`naut pull` stay open, but tag writes and online edits then require the
token.

### Alarms

An alarm engine is a small thing when alarms are just tags. Your logic
already computes the bits — a limit comparison, a fault contact, a `.HH`
member an edge node published. `alarms:` in the manifest adds the part a
*person* needs: an ISA-18.2 active list, acknowledge, a shelf that always
expires, and an append-only journal, served at `/api/alarms*` with a
counts-only summary on every stream frame. `rules:` generate definitions in
bulk by matching a UDT type and a member, so a dozen rules cover a fleet's
thousands of alarms and `naut alarms list` prints what they became.
Acknowledgement is host state — never written back to the edge, persisted
through `retain`, so a restart or a failover cannot resurrect four hundred
acked alarms as unacked. Acceptance tests get an `alarms:` key and
`ack:`/`shelve:` verbs, and the engine reads the runtime's clock, so a
five-minute on-delay is asserted exactly in virtual time. See
`examples/alarms` and the [Alarms guide](https://nautilus.joyautomation.com/guides/alarms/).

### Publishing to MQTT (Sparkplug B)

Expose a controller's tags to a Sparkplug host (Ignition, any Sparkplug-aware
SCADA) with the `sparkplug` package. The runtime is the edge node; each
`io.Driver` becomes a device whose birth/death follows its link:

```go
node, _ := sparkplug.New(rt, sparkplug.Config{
    BrokerURL: "tcp://broker:1883", GroupID: "Plant", EdgeNode: "Line1",
    BdSeqFile: "/var/lib/nautilus/line1.bdseq",
},
    // Publish classes are report-by-exception groups, like scan classes:
    sparkplug.WithDefaultRBE(sparkplug.RBE{Deadband: 0.5, MaxInterval: 30 * time.Second}),
    sparkplug.WithPublishClass("fast", sparkplug.RBE{Deadband: 0.1, MaxInterval: 5 * time.Second}),
    sparkplug.WithMetricClass("fast", "PIT_*"),
    // The EtherNet/IP driver's tags become a device; DBIRTH/DDEATH track its health.
    sparkplug.WithDevice(sparkplug.Device{
        ID: "plc1", Tags: driver.InputNames(),
        Health: func() bool { return driver.Health().Connected },
    }),
)
node.Start(ctx)
```

Types map faithfully (BOOL→Boolean, integer→Int64, REAL→Double, UDT→Template),
a SCADA host can write tags back via NCMD, and `Node Control/Rebirth` is
honored. The node passes the **Sparkplug TCK edge-node profile** — CI runs the
`joyautomation/sparkplug-tck-go` harness against a live node on every push. MQTT
and protobuf live only in this package; the runtime core stays stdlib-only.

#### Consuming Sparkplug B (host)

The other side of the wire: `driver: {type: sparkplug-host}` makes a
project a Sparkplug B **host application**, consuming a whole group of
edge nodes as `role: input` tags instead of publishing its own:

```yaml
driver:
  type: sparkplug-host
  broker: "tcp://mqtt.plant:1883"
  group-id: Plant
  host-id: plant-scada          # STATE topic spBv1.0/STATE/plant-scada
  manifest: sparkplug_manifest.yaml   # from `naut sparkplug import`
tag-files: [tags/sparkplug.yaml]
```

`naut sparkplug import --broker ... --group ...` (live) or
`--sites sites.yaml` (offline, no broker — CI-buildable) generates the
manifest, tag file, and Template types. Reads fault until a site's first
birth, so guard logic on the driver-synthesized `<site>__Online`
companion; writes leave as NCMD/DCMD, and a write to a site that is
offline is queued per node and delivered on its next birth, unless the
birth already reports that value. A UDT is never written as a whole — bind its
controls per member (`member: Speed`, or `--writable 'Motor1.START,*.HSP'`)
and each write goes out as a partial template the edge merges, leaving the
members the site is driving untouched. Both the edge-node and host-application **TCK
profiles** pass in CI. See `examples/sparkplug-host` and the
[host guide](https://nautilus.joyautomation.com/guides/sparkplug-host/).

## Four languages, one program model

A program file is `.st`, `.fbd`, `.ld`, or `.sfc`: pick per task, mix freely
in one controller. The graphical languages are **text first**: an `.fbd` is a
netlist, an `.ld` is rung text, an `.sfc` is a step/transition list, all of
them diff/review/merge like code, and the VS Code extension projects them
into a full graphical editor (right-click → "Open With → FBD Diagram" /
"Ladder Diagram" / "SFC Diagram") where every gesture is a structural edit
to the text underneath.

```iecst
(* interlocks.ld *)
PROGRAM Interlocks
VAR_EXTERNAL
    TempC : REAL; HiTempAlm : BOOL; HornAck : BOOL; Horn : BOOL;
END_VAR
LD
  RUNG hitemp (* comparison as a contact, with a 5 s on-delay *)
    GT(TempC, 90.0) t2:TON(PT := T#5S) ( HiTempAlm )

  RUNG horn (* the alarm sounds the horn until the operator acks *)
    HiTempAlm /HornAck ( Horn )
END_LD
END_PROGRAM
```

Rung grammar: series = AND, `[ a | b ]` = OR (nests), `/x` = NC contact,
`( x )` / `( S x )` / `( R x )` coils, `FN(args)` as a contact (must yield
BOOL), `inst:TYPE(args)` puts a timer/counter in the rung with power
driving its standard pin, and `ET => Var` captures block outputs. Each
language transpiles one hop (`ld → fbd → st`) into the same IR, so
functions, user FUNCTION_BLOCKs, arrays, diagnostics, live values, online
edits, and visual diffs work identically everywhere. `naut new
--language ld` (or `fbd`) scaffolds a project in that language. The full
reference — evaluation semantics and every built-in — is
[docs/functions.md](docs/functions.md).

## The tag model

The tag store is the seam everything meets at: the IEC program, the field
driver, the HMI/API, and the editor's live values all read and write the
same named values. One scan looks like this:

```
driver.ReadInputs() ──(Input tags)──▶ ┌───────────┐ ──(Output tags)──▶ driver.WriteOutputs()
                                      │ tag store │
HMI / POST /api/tags ──(writes)─────▶ │           │ ──(reads)────────▶ HMI / editor pills
                                      └───────────┘
                                        ▲       │
                                 program writes │ program reads
                                     (coils)    ▼
```

**A `VAR_EXTERNAL` declaration binds a name; it does not create the tag.**
Declaring `TempC : REAL;` in your program means "resolve this name in the
tag store at scan time", and nothing more. Existence
comes from a write, and there are exactly four writers:

1. a **seed** in the Go composition (initial value, exists from scan one),
2. a **driver** delivering it as an input each scan,
3. an **operator** writing it through the HMI or `POST /api/tags` (a whole
   tag, or one member of a UDT tag — see below),
4. the **program itself** assigning it (a coil write creates the tag).

The one rule that bites: **writes create, reads fault.** Reading a tag
nobody has ever written stops the scan with `undefined tag <name>` —
deliberately, so a mis-wired binding fails loudly instead of running on a
silent zero. The classic trap is a seal-in latch that *reads its own coil*
on scan one, before the first write; the fix is a seed. (In the FBD
diagram, with live values on, externals with no backing tag are flagged —
an amber *no tag* badge in the vars panel and on any chip that reads one —
so you see this before the download, not after.)

### Declaring tags: one entry per tag

`runtime.Options.Tags` states each tag's **role** once — which direction it
moves each scan, its initial value, and its HMI documentation — instead of
spreading the name across the flat `Inputs`/`Outputs`/`Seed`/`Meta` fields
(all of which still work and compose with `Tags`):

```go
Tags: []runtime.TagDef{
    // Field input: the driver produces it. NOT seeded — if the driver
    // doesn't deliver it, you want the loud fault, not a silent zero.
    // (The name must also appear in the driver's ReadInputs map.)
    runtime.Input("TempC", runtime.Desc("Tank temperature"), runtime.Unit("°C")),

    // Operator setpoint / config: seeded so it exists from scan one;
    // the HMI or POST /api/tags writes it later.
    runtime.Setpoint("TempSP", 65.0, runtime.Unit("°C")),

    // Field output: the logic produces it, the driver consumes it.
    // Init() any output the logic also READS — the seal-in latch.
    runtime.Output("PumpRun", runtime.Init(false), runtime.Desc("Pump run command")),

    // Logic-owned state that must survive read-before-write: latches,
    // integrators, mode words.
    runtime.State("Mode", int64(0), runtime.Desc("0=off 1=auto 2=manual")),
},
```

Picking a role, by use case:

| You want a tag that…                        | Use                          | Backed by                                   |
| ------------------------------------------- | ---------------------------- | ------------------------------------------- |
| comes from a sensor / the field             | `Input(name, …)`             | driver `ReadInputs` map + this entry        |
| goes to an actuator / the field             | `Output(name, …)`            | your logic writes it (coil)                 |
| …and your logic also reads it (latch)       | `Output(name, Init(v), …)`   | the seed covers scan one, then the coil     |
| an operator adjusts (setpoint, gain, limit) | `Setpoint(name, initial, …)` | the seed, then HMI / `POST /api/tags`       |
| your logic owns across scans (integrator)   | `State(name, initial, …)`    | the seed, then the coil                     |
| the HMI watches but the field never sees    | plain coil write — no entry  | the program creates it on first write       |

Adding a **new field input** end to end is three lines in three places, all
by the same name: declare it in the program (`VAR_EXTERNAL testExt : REAL;`
— or from the diagram's vars panel), produce it in the driver
(`"testExt": p.testExt` in the `ReadInputs` map), and bind it in the
composition (`runtime.Input("testExt")`). The `Inputs` list is a deliberate
allowlist — a driver can't spray arbitrary names into the store — which is
why the middle step alone isn't enough.

### Writing a member: `POST /api/tags` with `Tag.Member`

A UDT tag is one value in the store, so an operator writing one of its
members is a read-modify-write of the whole struct. `POST /api/tags` does
that for you — `name` takes a dotted member path, to any depth:

```sh
curl -X POST localhost:8080/api/tags -d '{"name": "P101.START", "value": true}'
curl -X POST localhost:8080/api/tags -d '{"name": "P101.LVL.CTL1HSP", "value": 60}'
```

`value` may also be an **object**, to set several members of one tag at
once: `{"name": "P101", "value": {"START": true, "LVL": {"CTL1HSP": 60}}}`.
That is a **merge** — members the object doesn't name keep their current
values. (Deliberately unlike `init:` seeding, which zero-fills what its
mapping omits: seeding builds a value from nothing, a write edits one the
plant is already running on.)

The rules:

- **Nothing is created.** An unknown tag, a misspelled member, or a member
  path into a scalar tag is a `400` with the reason —
  `tag P101: unknown member STRAT (did you mean START?)` — never a new
  top-level tag literally named `P101.STRAT`, which no program reads and a
  Sparkplug edge would publish as a bogus metric.
- **The leaf keeps its type.** A number lands on a `REAL` member as a REAL
  and on a `DINT` member as an integer; a mismatch is
  `tag P101.START: want BOOL, got a number` rather than a silent retype.
- **The role rule is the root tag's.** A member of a driver-owned `Input`
  is refused (the driver replaces the whole tag before the next scan, so
  the edit could not survive one cycle); setpoints, state and outputs are
  writable — an output being exactly how a Sparkplug host commands an edge
  node. A struct-typed `Output` needs an `Init(...)` to exist before the
  first write, like any tag the operator addresses.
- **`GET /api/meta`** reports `"memberWrites": true`, so an HMI can tell a
  controller that resolves member paths from an older one that would have
  swallowed them.

The same paths work everywhere else a tag is addressed: a test manifest's
`given:`/`expect:`, the dashboard's tag table (expand a writable struct and
each leaf is editable), and the HMI kit's `rt.writeTag('P101.START', true)`,
which returns the controller's reason when it refuses.

## Structuring logic: functions and function blocks

When a program outgrows one file, IEC 61131-3's unit of reuse is the
**`FUNCTION_BLOCK`** (stateful — each instance keeps its own timers,
integrals, latches) and the **`FUNCTION`** (stateless). nautilus sticks to
the standard here on purpose: there is no vendor-style "call another
program" — a program is a scheduling unit, a function block is a reuse
unit, and reaching for reuse means writing a block.

Blocks live in **library files** — a `.st` file holding only `TYPE`,
`FUNCTION`, and `FUNCTION_BLOCK` declarations, or a `.ld` / `.fbd` file
with no `PROGRAM` — and compose ahead of the program:

```go
//go:embed blocks.st
var blocks string

rt, _ := runtime.New(runtime.Options{
    Program:   program,            // .st or .fbd
    Libraries: []string{blocks},   // TYPEs, FUNCTIONs, FUNCTION_BLOCKs
    ...
})
```

```iecst
(* blocks.st *)
FUNCTION_BLOCK PI
VAR_INPUT  SP : REAL; PV : REAL; KP : REAL; KI : REAL; DT : REAL; END_VAR
VAR_OUTPUT OUT : REAL; END_VAR
VAR integral : REAL; err : REAL; END_VAR
err := SP - PV;
integral := LIMIT(0.0, integral + KI * err * DT, 100.0);
OUT := LIMIT(0.0, KP * err + integral, 100.0);
END_FUNCTION_BLOCK
```

```iecst
(* program.st — one instance per control loop *)
VAR tic : PI; END_VAR
tic(SP := TempSP, PV := TempC, KP := Kp, KI := Ki, DT := ScanDtS);
Heater := tic.OUT;
```

The pieces that make this first-class rather than a convention:

- **Callable from any IEC language, and writable in any.** The same `PI`
  block instantiates from an FBD diagram (`tic : PI(SP := TempSP, ...)`)
  exactly like a built-in TON. Blocks are also AUTHORED in any language:
  a `.ld` file with no PROGRAM is a library of ladder subroutines —
  `FUNCTION_BLOCK`s whose bodies are rungs, with pins and per-instance
  retained state, which is what IEC gives you instead of a JSR. See
  [docs/functions.md](docs/functions.md#function-blocks-in-ladder) and
  [examples/ladder-subroutines](examples/ladder-subroutines).
- **The tooling composes the same way.** The VS Code extension, the LSP,
  `naut check`, and `naut pull` all treat sibling library files
  as in-scope for the program, byte-identically to `Libraries` — so
  online edits round-trip losslessly and CI sees what the runtime sees.
- **Instance state is retained.** A block's `VAR` section persists
  across scans, and PLC-style online edits carry it across program swaps
  by name and type — a `PI` keeps its integral through a live logic
  change, like a real controller.
- **Pins are typed by your model, not just by scalars.** A pin may be a
  user `TYPE` from the same library — `VAR_INPUT IN : AnalogInput;` —
  and `VAR_IN_OUT AI : AnalogInput;` binds the caller's variable (a
  local, a struct field, or a `VAR_EXTERNAL` UDT tag) so the block's
  writes land back in it. A block whose UDT already names its own
  inputs and outputs takes one pin instead of thirty. See
  [docs/functions.md](docs/functions.md#user-function-blocks).

`naut new` scaffolds this shape: the PI controller ships in
`blocks.st`, instantiated from `program.st`. (This one is worth writing
by hand once to see how it works; for real loops reach for the built-in
`PID` — anti-windup, bumpless auto/manual, derivative-on-PV filtering,
diagnostics — see [docs/functions.md](docs/functions.md#pid-closed-loop-control).)

### More than one program: tasks

The spec's answer to "many programs" isn't calling between them — it's the
**resource/task model**: several programs scheduled at their own rates
against one shared tag store. `Options.Tasks` is exactly that:

```go
rt, _ := runtime.New(runtime.Options{
    Program: fastLogic,           // the MAIN task: owns field I/O
    Scan:    10 * time.Millisecond,
    Tasks: []runtime.Task{
        {Name: "temperature", Program: pidLoops, Scan: 250 * time.Millisecond, DtTag: "PidDtS"},
        {Name: "totals", Program: totalizers, Scan: time.Second, DtTag: "TotDtS"},
    },
})
```

Scans never overlap — tasks serialize on one lock, so every scan sees a
consistent tag snapshot. The main task reads inputs and writes outputs;
additional tasks compute against the store at their own pace, each with
its own measured-`dt` tag and its own health in `Stats().Tasks` (rendered
in the built-in dashboard and the HMI kit's `ScanDiagnostics`).

**Every program online-edits, both directions.** Programs are addressed by
POU name — `PROGRAM <Name>` is a program's identity. `GET /api/program`
lists the resource's programs; a `PUT` routes automatically by the POU name
in the submitted source, and `?pou=` / `?task=` select one explicitly for
GET/rollback. In VS Code that means a workspace with one program file per
task Just Works: open the file, Download/Diff/Rollback target that task's
program, retained state carries across the swap. And `naut pull`
reconciles the whole resource: every controller program pulls back into
the workspace file declaring its POU (a new program lands in
`<POU>.st`/`.fbd`), so a field edit to any task is reviewable and
committable — `--check` fails CI on drift in any of them.

The full language reference — how a scan evaluates each language, ladder
power-flow semantics, and every built-in operator, function, and function
block with signatures and behavior — is in
[docs/functions.md](docs/functions.md).

## Status

Pre-1.0, and the foundation Joy Automation builds SCADA systems on. Every artifact is versioned
on its own: the CLI by `v*` tags, the VS Code extension on the Marketplace
pre-release channel, the HMI kit on npm. What ships today:

- ✅ `lang/st` + `lang/ir` — the Structured Text VM (pure stdlib, tested)
- ✅ `lang/stgen` — build ST type declarations functionally in Go and render
  them to source (`stgen.Struct("Motor", stgen.Field("Speed", stgen.REAL), …)`),
  for generating UDTs from a schema; validates output by compiling it. This is
  the codegen path — the compiler stays the single source of truth
- ✅ `runtime` — scan loop, tag bus, program host + hot-swap, and PLC-style
  **online edits**: warm-swap the ST program while it runs, carrying retained
  state (PID integrals, timers, counters) across by name and type, with
  one-step rollback
- ✅ `io` — the Driver seam + an in-memory driver
- ✅ `alarm` — ISA-18.2 alarms declared in the manifest: rules that expand
  over UDT members, on/off delays, acknowledge and time-boxed shelve
  (retained across restart and failover), suppression on a dark site, and a
  journal with ring / rotated-JSONL / Postgres sinks. Five `/api/alarms*`
  routes, a summary on the SSE frame, and `alarms:` matchers in the
  acceptance harness
- ✅ `sparkplug` — MQTT **Sparkplug B edge node**: publishes the tag store to a
  broker (the runtime is the edge node; each io.Driver is a device whose
  DBIRTH/DDEATH tracks its connection health), faithful datatypes, UDTs as
  templates, NCMD writeback, and **publish classes** with report-by-exception
  (deadband + min/max interval), mirroring scan classes. Passes the
  joyautomation/sparkplug-tck-go edge-node conformance profile in CI
- ✅ `eip` — EtherNet/IP driver for ControlLogix/CompactLogix: pure-Go (no
  cgo) CIP client with connected messaging and batched reads, tag-list + UDT
  template upload, `naut eip import` codegen (ST TYPE block + Go tag
  manifest), write-on-change outputs, and a Logix controller emulator
  (`eip/logixserver`) for hermetic integration tests
- ✅ `modbus` — Modbus TCP driver: block-read planner (one request per device
  instead of one per variable), per-source word/byte order, scan classes,
  per-tag quality that tells a refused register (exception → that block bad,
  siblings good) from a dead link (reconnect backoff, values hold),
  keep-alive `rewrite:` outputs, `naut modbus import|browse|serve|tags`
  codegen and commissioning tools, and an in-process slave plus a pymodbus
  foreign-stack run in CI
- ✅ `server` — tag API: JSON snapshot, SSE stream, tag writes (HMI + editor),
  a gated program API for online edits (`GET/PUT /api/program`, rollback),
  and (`server.hmi`) serving a built HMI at "/" with SPA fallback, so the
  controller can be the whole deploy — no separate web server
- ✅ `tools/vscode-iec/` online edits — Download Program to Controller, diff
  running-vs-workspace, rollback, and a sync-status indicator
- ✅ `cmd/naut` — CLI: interactive project scaffold, headless ST compile
  check for CI, and the ST language server
- ✅ `tools/vscode-iec/` — VS Code extension: syntax, compile diagnostics,
  go-to-definition, hover, completion, inline live tag values
- ✅ `examples/heated-tank` — a runnable controller serving the tag API
- ✅ `examples/heated-tank-nogo` — the same plant as a manifest project:
  four tasks in three IEC languages (physics simulated in ST), zero Go,
  `naut run` / `naut build`
- ✅ `examples/hmi-demo` — a SvelteKit operator screen on the HMI kit:
  tank faceplate, trends, setpoint write-back, driver-connection cards,
  and scan diagnostics from one SSE stream
- ✅ `hmi/` — [`@joyautomation/nautilus-hmi`](https://www.npmjs.com/package/@joyautomation/nautilus-hmi)
  on npm: Svelte 5 SCADA faceplates (Tank, Gauge, Trend, Pump, Valve…), app
  primitives, a generic SSE realtime client, and a themeable token layer
- ✅ `lang/sfc` — Sequential Function Chart: steps, transitions, and actions
  on the same IR, with LSP support, a graphical VS Code editor, and a batch
  example (`examples/tank-batch-sfc`)
- ✅ `examples/ladder-subroutines` — `FUNCTION_BLOCK`s written as rungs in a
  PROGRAM-less `.ld` library, instantiated per pump from a ladder program
  and callable from ST/FBD — ladder's answer to a JSR

## Roadmap

- An HMI starter in `naut new`
- Native-Go function blocks alongside ST (both lowering to the same IR)
- Vendor-format import (Studio 5000 L5X, TIA, PLCopen XML) → nautilus

## License

Apache License 2.0 — see [LICENSE](LICENSE). Copyright © Joy Automation.
