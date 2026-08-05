---
title: EtherNet/IP (Allen-Bradley Logix)
description: Talk to a real PLC — tag browse, UDT import codegen, scan classes, and a pure-Go CIP stack with a Logix emulator for tests.
---

Point the importer at an Allen-Bradley Logix controller and it generates the
types and bindings your project needs — committed source, not runtime config:

```sh
nautilus eip browse --host 192.168.1.10                 # see what's on the controller
nautilus eip import --host 192.168.1.10 \
  --tags 'Line1*,Program:MainProgram.*' \
  --writable 'Line1Cmd*'
```

That writes `eip_types.st` (a TYPE block mirroring the controller's UDTs plus
suggested VAR_EXTERNAL declarations) and the tag manifest. In an SDK
project (`nautilus new --template sdk`) you wire that into `main.go`:

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
(UDTs arrive as real struct values in your IEC program), validates the
manifest against the live controller at startup so type drift fails loudly,
and writes changed outputs back on change — the runtime behaves like a PLC
peer on the network.

## No PLC? Use the emulator

The stack is pure Go, no cgo, and it's tested against an in-repo
ControlLogix emulator (`eip/logixserver`) — the same emulator is available
for your own hermetic integration tests, so a CI pipeline can exercise the
full browse → import → poll → write path without hardware.

## In a manifest project

In a manifest project — what `nautilus new` scaffolds by default — the
EtherNet/IP driver is configuration rather than Go code:

```yaml
driver:
  type: eip
  host: 192.168.1.10
  manifest: eip_manifest.yaml   # from `nautilus eip import`
  scan-rate: 500ms
  scan-classes: { fast: 100ms, slow: 10s }
  tag-classes:
    fast: ["Line1_PIT_*"]
    slow: ["*_Totals"]
```

`examples/client60` is a complete manifest project driving a Logix
controller with a ladder program and an HMI.

`nautilus test` never opens a socket: whatever `driver:` says, the tests
substitute a stub, so an EtherNet/IP project is fully testable on a laptop
with nothing on the network. Feed the `role: input` tags from `given:` and
the logic runs exactly as it would against the field.
