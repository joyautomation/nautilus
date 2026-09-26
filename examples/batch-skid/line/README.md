# `line/Line.L5X` — the existing line PLC

An "existing" Allen-Bradley Logix controller export, standing in for a
line that was here before this skid and already answers a ready/accept
handshake over EtherNet/IP. Generic names throughout: `LineController`,
tags `Line_Ready`/`Line_Accept`/`Line_TankLevel`/`Line_Fault`/
`Line_Request`/`Line_BatchId`, one small UDT `Line_Status` (`Mode : DINT`,
`Alarm : BOOL`), and one ladder routine, `Receive`, in `MainProgram`.

This file is **read-only** for this project — the skid only talks to it
over the wire (`line.yaml`'s `driver: {type: eip}`), never edits it.
Open it anyway: right-click `Line.L5X` → **Open With → Ladder Diagram**
shows `Receive`'s four rungs exactly as the controller would export them.

## What `Receive` does

```
Rung 0   LES(Line_TankLevel,80.0)OTE(Line_Ready);
Rung 1   XIC(Line_Request)XIC(Line_Ready)XIO(Line_Fault)TON(AcceptTmr,?,?);
Rung 2   XIC(AcceptTmr.DN)OTE(Line_Accept);
Rung 3   XIC(Line_Fault)OTE(Line_Status.Alarm);
```

Ready while the receiving tank has headroom; a request against a ready,
unfaulted line starts a 2 s settling timer, and `Line_Accept` follows.

## Two revisions, one interlock

`Line.L5X` has two commits in this project's history. The first is the
four rungs above. The second adds a fifth: a high-level cutoff that forces
`Line_Accept` off if the receiving tank creeps above 95 % after the
handshake already settled —

```
Rung 4   GE(Line_TankLevel,95.0)OTU(Line_Accept);
```

— a genuine interlock a line's own maintainers might add later, entirely
independent of the skid. Diff it with **nautilus: Diff Ladder Diagram
(between git revisions…)**, pick the two commits, and the added rung
renders in cyan against the four that didn't change. The same gesture
works on any Rockwell `.L5X` a project happens to carry, not just
nautilus's own ladder files.

## Verify it independently

```sh
naut logix import line/Line.L5X          # parses this export offline
naut logix emulate --l5x line/Line.L5X --listen 127.0.0.1:44818
                                          # serves its tag surface, no PLC
```

**What the emulator does not do:** it serves the tag surface — every tag
`Receive` declares, with the export's own initial values — but it does
not execute `Receive`'s ladder. Writes from a client land straight in its
tag store; nothing on the emulator side ever runs `TON(AcceptTmr,?,?)` or
flips `Line_Accept` on its own. That's a CIP-protocol-conformance tool,
not a logic simulator (see `docs/design/examples-dogfood.md`). For a
fully autonomous bench demo, `nautilus.yaml`'s `sim.st` answers the
handshake itself, independent of the emulator, with the same 2 s settling
shape `Receive` uses. Against the emulator (`line.yaml`), proving the
round trip either means running the batch far enough to watch
`Line_Request` actually reach it (independently readable — a second
`naut eip browse`/import, or a raw tag read, both show it), or writing
`Line_Accept` on the emulator directly to stand in for what a real
controller's own `Receive` routine would have done.
