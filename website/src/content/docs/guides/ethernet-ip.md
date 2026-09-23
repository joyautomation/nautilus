---
title: EtherNet/IP
description: Poll an Allen-Bradley Logix controller from a committed, browsed manifest — UDTs as real ST types, scan classes, leaf-mode struct reads, member-wise writes, and a ControlLogix emulator that needs no hardware.
---

A `driver: {type: eip}` section puts a manifest project on an Allen-Bradley
Logix controller — ControlLogix, CompactLogix — over a pure-Go CIP stack: no
cgo, no RSLinx, no OPC server in the middle. Nothing about the controller is
typed by hand: `naut eip import` browses the live PLC and writes the UDT
shapes, the tag bindings, and the tag file. Polling policy is the part you
author.

```yaml
driver:
  type: eip
  host: 192.168.1.10
  slot: 0                        # processor backplane slot (default 0)
  manifest: eip_manifest.yaml    # from `naut eip import --format yaml`
  scan-rate: 500ms               # the default scan class (driver default 250ms)
  scan-classes: { fast: 100ms, slow: 10s }
  tag-classes:
    fast: ["Line1_PIT_*"]
    slow: ["*_Totals"]
tag-files: [tags/eip.yaml]
```

`host:` and `manifest:` are both required, and the manifest is decoded
strictly — a misspelled key is an error, not a silently dropped binding. All
of that fails `naut check`, not `naut run`, because **`New` never
dials**: it decodes the manifest, resolves every type reference, rejects
duplicate names and partitions the bindings into scan classes, all offline.
The connection is `Start`'s job, so `naut check` and
`naut build` pass in CI with no controller in sight. `examples/client60`
is a complete manifest project driving a Logix controller with a ladder
program, an HMI, and Sparkplug retransmission on top.

## Generating the manifest: `naut eip import`

Point the importer at the controller: it walks the Symbol class in every
scope, uploads the templates those tags depend on, and generates source:

```sh
naut eip browse --host 192.168.1.10          # what's on the controller
naut eip import --host 192.168.1.10 \
  --tags 'Line1*,Program:MainProgram.*' \
  --writable 'Line1Cmd*' \
  --format yaml
```

`browse` prints one line per tag — name, type name (the UDT's own, or the
elementary type) and array dimensions — which is how you find the patterns
worth importing. Both commands take `--slot` and `--port` (default 44818).

`import` always writes **`eip_types.st`**. With `--format yaml` it also writes
**`eip_manifest.yaml`** and **`tags/eip.yaml`**; with the default `--format
go` it writes **`eip_manifest.go`** instead (a `var EIPManifest =
eip.Manifest{…}` for an SDK project, named by `--package`). All are committed,
**never hand-edited**, and carry a header saying so with the command that
regenerates them.

- `eip_manifest.yaml` — `types:` (`name`, `fields:` with `name`/`type`/
  `arraylen`) and `tags:` (`name`, `device`, `type`, `arraylen`, `writable`,
  `scanclass`). This is what `manifest:` names.
- `tags/eip.yaml` — an ordinary tag file: `role: input`, or `role: output`
  for a binding matched by `--writable`, plus `type:` naming the UDT for
  struct bindings. Compose it with `tag-files:`.

Useful flags: `--tags` selects device tags by comma-separated globs (default:
every user tag **except** module I/O like `Local:1:I`, which needs an explicit
pattern), `--writable` marks the ones the program commands, `--out` picks the
output directory, `--tags-out` the tag file path, and `--tags-skip` leaves a
tag out of the tag file when the project declares it by hand — the escape
hatch for the one thing the import cannot know, an `init:` on an input.
Descriptions are deliberately not generated, because Logix keeps them in the
offline project file where a CIP browse cannot reach them: put those in
`nautilus.yaml`'s `tag-meta:`, which reaches only `unit` and `desc`, so
regenerating never argues with them.

`naut eip tags eip_manifest.yaml -o tags/eip.yaml` re-derives **just the
tag file** from the already-committed manifest — a pure function of the repo,
no controller needed, so it runs in review or in CI. Its `-skip` takes the
same globs as `--tags-skip`, and a skip pattern matching nothing is an error
rather than a silent regeneration of a hand-written tag.

At every connect the driver **validates the manifest against the live
controller**: a bound tag that no longer exists, a tag whose template is now a
different type, a UDT that lost a member. Drift fails the connection with
every problem listed and a pointer at re-import — it never mis-decodes a
changed program into plausible numbers.

## Types: UDTs become ST `TYPE`s

`eip_types.st` is a real ST `TYPE` block, generated through the same builder
the compiler validates, so a UDT is a first-class struct in your program
rather than flattened scalars:

```iecst
TYPE
  Analog_Input : STRUCT
    AI : REAL;
    SIGNALFAIL : BOOL;
    LOWSCL : REAL;
  END_STRUCT;
END_TYPE

(* in your program *)
VAR_EXTERNAL
  RTU60_ZONE13_PIT_001 : Analog_Input;
END_VAR
IF NOT RTU60_ZONE13_PIT_001.SIGNALFAIL THEN
  Pressure := RTU60_ZONE13_PIT_001.AI;
END_IF;
```

Member names are the controller's, verbatim, in template wire order — nested
UDTs are emitted dependencies-first so the block compiles top to bottom, and a
ready-to-paste `VAR_EXTERNAL` block for every binding is appended as a
comment. A template with the Logix string shape (a `DINT LEN` plus a
`SINT[] DATA`) becomes the type `STRING`, not a struct. Array members become
`ARRAY[0..n-1]`. `STIME`, `DATE` and `TIME_OF_DAY` map to `DINT` and
`DATE_AND_TIME` to `LINT` — ST has no direct equivalent, and those match the
width.

Tag names are sanitized into identifiers: `Program:MainProgram.Motor` becomes
`MainProgram_Motor`, every other non-alphanumeric becomes `_`, runs collapse,
a leading digit gets a `Tag_` prefix, and a collision gets `_2`; the device
path stays in the manifest's `device:`, so the mapping stays visible. What
does not come across is reported, not guessed — multi-dimensional arrays,
members whose type code the stack does not decode, and templates that could
not be uploaded print as `skipped:` lines with a reason and never reach the
manifest.

## Scan classes and leaf mode

Every class shares one connection. A binding's class comes from the
manifest's `scanclass`, overridden by `tag-classes:` globs matched against
either the nautilus tag name or the device path (later assignments win);
anything unassigned is the `default` class, whose rate is `scan-rate`.
Naming a class without defining its rate is a load error. The reserved class
**`none`** catalogs a tag without ever polling it — a command tag the program
only writes belongs there, and it stays a valid write target. Every class is
polled once immediately on connect, so the snapshot fills without waiting out
a slow class. How a binding is read depends on its shape:

- **Elementary scalars** are batched into Multiple Service Packets, sized to
  the negotiated Class 3 connection (500 bytes), so a class of two hundred
  DINTs and REALs costs a handful of round trips rather than two hundred.
- **Structs, strings and arrays** are read whole — one Read Tag for the root
  — and fall back to Read Tag Fragmented transparently when the value
  outgrows the connection.
- **Leaf mode** is the fallback for a struct root the controller *refuses*.
  Logix answers a whole-struct read on a tag whose type carries
  access-restricted members (AOI backing tags, most often) with a privilege
  violation. That binding then drops to leaf mode for the life of the
  session: the manifest type is flattened into its elementary members, every
  member is read individually (batched, same as scalars), and the struct is
  assembled client-side in manifest field order.

Inside leaf mode, a member the controller will *never* serve — an internal
word like `TIMER.Control`, anything the UDT marks external-access none — is
recognized by its CIP status, logged once for the whole tag, dropped from the
poll set, and assembled as the IEC zero for its type. A member that fails for
any other reason holds the binding's **last good value** for that cycle: a
partial struct is never published. `/api/drivers` counts the bindings in leaf
mode as `leaf structs`.

## Per-tag quality

**The eip driver does not implement `io.QualityReporter`.** Unlike the Modbus
and Sparkplug-host drivers it reports no per-tag verdict of its own, so
quality comes entirely from the runtime's own derivation: while the last input
read failed, every driver-bound input is `Stale`; otherwise every tag is
`Good`.

Be precise about what that covers, because `ReadInputs` fails only while the
driver has **never** delivered a snapshot. Before the first successful poll —
dialing, browsing, validating, or failing to connect at all — every input
reads `Stale`. After a connection *drops*, the driver keeps serving its last
snapshot, so those tags stay `Good` holding their last field values: the
honest signal that the link is gone is the driver row on `/api/drivers`
(`state`, `lastError`, `reconnects`), not tag quality. An HMI that interlocks
on comms health should bind the driver status, not infer it from a tag. (A
dotted path resolves to its root tag — a UDT arrives whole, so
`P101.Drive.Speed` is exactly as trustworthy as `P101`.)

## Writes

A binding marked `writable` in the manifest is a `role: output` tag. The
runtime pushes output changes — the first scan, then any scan where an output
moved — and the driver diffs again against its own last-written set, so an
unchanged value never reaches the wire twice. Queued writes flush between
polls on the same connection: a write kicks the poll loop awake rather than
waiting out the next interval.

- **An elementary scalar** goes as one CIP Write Tag with its Logix type code.
- **A struct** is written member by member, symbolically
  (`Device.Member.Sub`), and only the members that actually changed since the
  last write — every member on the first write. Nested structs recurse.
- **Strings and array members inside a struct are not written**, and a
  top-level `STRING` or array binding is not a writable target either.

**There is no baseline rule here.** Where the Modbus and Sparkplug-host
drivers record the first snapshot after `Start` and send it to nobody, this
driver commands what the first scan produces — an output tag's `init:` reaches
the controller. Give every output an `init:` that is safe to command.

A write that fails because the **connection broke** is requeued and retried
after reconnect, latest value per tag. A write the controller *refuses* on a
live connection is logged and dropped for that flush; it goes again the next
time the value changes.

**Writes are not gated.** The driver has no `SetWriteGate` — the redundancy
hook the Modbus driver exposes — so nothing inside it suppresses a command.
Redundancy is handled a level up: a standby replica skips scans entirely, so
it never produces an output to hand over, while its driver keeps polling.

## `/api/drivers`

`Kind: "ethernet-ip"`, one row per driver, named by host, with
`Detail: "192.168.1.10 · slot 0"`. The state is `connected` (message
`Polling N tags`), `connecting`, `error` (`Connect failed — retrying`, with
`lastError`), or `degraded` — a connection that is up while every poll fails.
Metrics: `tags`, `polls`, `poll errors`, `reconnects`, `leaf structs` when any
binding is in leaf mode, and `last poll` as a moment the client renders an age
from. Reconnects back off from 1s to roughly 30s, reset on success.

## No PLC? The Logix emulator

`eip/logixserver` is an in-repo ControlLogix target: Forward_Open, symbolic
Read/Write Tag (plain and fragmented), Multiple Service Packet batches,
tag-list upload, the Template services, and the Identity and Program Name
objects — the surface both nautilus's own client and pycomm3 speak. It is a Go
package, not a CLI, so you run it from a few lines:

```go
package main

import (
	"context"
	"log"

	ls "github.com/joyautomation/nautilus/eip/logixserver"
)

func main() {
	// The tag surface is declarative: templates, symbols, and the leaf paths
	// that resolve. Instance ids, symbol-type bits and member offsets are the
	// loader's job. ls.LoadTagSurface reads the same shape from a JSON file.
	schema, tags, name, err := ls.CompileSurface(&ls.TagSurfaceSpec{
		ControllerName: "TestController",
		Templates: []ls.TemplateSpec{{Name: "Header_Type", Members: []ls.MemberSpec{
			{Name: "Displacement", Datatype: "REAL"}, {Name: "Valid", Datatype: "BOOL"}}}},
		Symbols: []ls.SymbolSpec{{Name: "Speed", Datatype: "REAL"},
			{Name: "TRS", Datatype: "Header_Type"}},
		Tags: []ls.TagSpec{{Path: "Speed", Datatype: "REAL"},
			{Path: "TRS.Displacement", Datatype: "REAL"}, {Path: "TRS.Valid", Datatype: "BOOL"}},
	})
	if err != nil {
		log.Fatal(err)
	}
	store := ls.NewTagStore()
	for _, t := range tags {
		store.Set(t.Path, t.LeafType, t.Default)
	}
	store.UpdateValue("Speed", 42.5) // drive values while it runs
	log.Fatal(ls.NewServer(store, schema, name, ":44818", nil).Run(context.Background()))
}
```

Point `host:`/`--port` at it and the whole browse → import → poll → write path
runs with nothing on the network. CI exercises exactly this — `go test ./...`
runs the driver, the `logix` client and the codegen against the emulator on
every push, no build tags, no env gating — and its `DenyStructRoots` switch
refuses struct roots the way a real controller refuses an AOI backing tag,
which is how leaf mode is tested.

## Testing

`naut test` never opens a socket: whatever `driver:` configures, every
acceptance test substitutes the in-memory loopback driver, so an EtherNet/IP
project is fully testable on a laptop with nothing on the network, no separate
config needed.

```sh
naut check . && naut test .
```

`given:` writes the driver's input image exactly the way a poll would —
including struct bindings field by field, using the `type:` the generated tag
file carries — so the same `*_test.yaml` files pass unchanged once the real
driver is polling. Suites run in virtual time, so a ten-second interlock or a
loop's settling time is asserted deterministically, in milliseconds. `-m`
takes a second project file if you want one for logic-only runs
(`naut test -m memory.yaml .`).

## Not supported

- **Non-Logix CIP devices.** The driver speaks Logix symbolic tag access
  (Read/Write Tag, fragmented variants, Multiple Service Packet) over a
  Class 3 connection to the Message Router. A drive, valve, or I/O block with
  no tag database is not addressable through it, and generic
  class/instance/attribute messaging — which `eip/cip` can encode — is not
  exposed as a binding.
- **Implicit (Class 1) cyclic I/O.** `eip/cip` carries a Class 1
  producer/consumer and an originator, but nothing in the driver wires them:
  an I/O assembly connection is not a thing a manifest can declare.
- **PCCC / SLC / PLC-5 and Micro800.** There is no PCCC encapsulation in the
  tree at all.
- **Multi-dimensional arrays.** A one-dimensional array tag binds as a whole;
  a `[10,10]` tag is skipped by the import with a reason.
- **Writing strings and arrays.** They poll fine; the write path covers only
  elementary scalars and UDT structs.

## From Go

The `driver:` section is the manifest form of the `eip` package. An SDK
project (`naut new --template sdk`, `--format go` on the import) wires it
directly as an `io.Driver`:

```go
import "github.com/joyautomation/nautilus/eip"

driver, err := eip.New("192.168.1.10", EIPManifest,
    eip.WithSlot(0),                                  // WithPort overrides 44818
    // Polling policy is configuration, not codegen: re-running the import
    // refreshes the tag catalog without touching any of this.
    eip.WithScanRate(500*time.Millisecond),           // the default class
    eip.WithScanClass("fast", 100*time.Millisecond),
    eip.WithScanClass("slow", 10*time.Second),
    eip.WithTagClass("fast", "Line1_PIT_*"),          // globs on name or device path
    eip.WithTagClass("slow", "*_Totals"),
    eip.WithTagClass(eip.NoPoll, "Line1Cmd*"),        // cataloged + writable, never polled
    eip.WithLogger(slog.Default().With("driver", "eip")),
)
driver.Start(ctx)

rt, err := runtime.New(runtime.Options{
    Program: program,
    Driver:  driver,
    Inputs:  driver.InputNames(),
    Outputs: driver.OutputNames(),
})
```

`EIPManifest` is the generated `eip.Manifest` value — the same structure the
project loader decodes from `eip_manifest.yaml`, so a catalog computed rather
than configured can be built in code. `ScanClasses()` returns the resolved
poll groups (class → tag names) for diagnostics and `Health()` the row behind
`/api/drivers`. `Stop` cancels the loop and waits for it to close the
connection; `New` never opened one.
