---
title: Allen-Bradley Logix
description: Read L5X exports as ladder and IEC types with no Rockwell software at all, and — with an agent beside a licensed Studio 5000 — convert projects, compile logic in CI, detect drift, and push a warm rung edit to a running controller.
---

Your Logix code lives in git as text. Every change is diffed and reviewed.
CI verifies it. The controller is checked for drift against the repo. You
watch live values and write setpoints from your editor.

**Nothing about the runtime changes.** Logix stays the controller and the
program of record; nautilus supplies the software engineering around it.

There are two halves, and the first needs nothing from Rockwell at all.

## Half one: reading L5X — no Rockwell software

An L5X is the XML Logix Designer writes on **File → Export**. `nautilus`
reads it directly, on any OS:

```bash
nautilus logix info    DemoLine.L5X     # what is in this export?
nautilus logix import  DemoLine.L5X     # UDTs -> IEC types, tags -> a tag file
nautilus logix graph   DemoLine.L5X     # RLL routines -> the ladder model
nautilus logix normalize DemoLine.L5X   # pin the attributes that move on every export
```

### Tags arrive documented

```bash
nautilus logix import DemoLine.L5X --scope '*'
# wrote logix_types.st (3 types) and tags/logix.yaml (8 tags)
```

```yaml
- { name: DwellTmr, role: input, type: TIMER, init: { PRE: 30000, ACC: 0 }, desc: "Dwell before the pump is allowed to restart" }
- { name: StartPB,  role: input, init: false, desc: "HS-101 start pushbutton" }
```

That `desc:` is the point. Logix keeps tag documentation in the **offline
project file**, so a live CIP browse cannot see it — `nautilus eip import`
has to leave descriptions empty. Reading the L5X recovers them.

UDTs come across as real IEC types. A Logix UDT has no BOOL members — an
authored BOOL is exported as a hidden byte host plus `BIT` overlays — so
nautilus drops the hosts and declares the BOOLs you actually wrote:

```
Analog_Input : STRUCT
  Raw : INT;
  EU : REAL;
  Alarms : Limits;
  History : ARRAY [0..9] OF REAL;
  Fault : BOOL;
END_STRUCT;
```

### Ladder, in your editor

`.L5X` files open in the ladder view exactly like nautilus `.ld` source —
contacts, branches, in-rung blocks, coils — and diff **as diagrams** between
git revisions. Rendering only: nautilus draws the rung Logix exported and
claims nothing about how it executes, which is why an instruction it has
never seen still draws, as a box with its operands.

L5X is **read-only** in that view. It is a vendor export; edit it in Logix
Designer.

### Drift detection

Every export stamps a new `ExportDate`, so a raw diff is never empty.
Normalizing pins that and the other volatile attributes, and then two
exports of unchanged code compare equal:

```bash
nautilus logix normalize --check controller.L5X repo.L5X
```

## Half two: driving a project — the `logixd` agent

Creating projects, compiling, downloading and online edits need the
**Studio 5000 Logix Designer SDK**, which is Windows-only, licensed, and
reachable only from a .NET client on the same machine — it is a gRPC
service bound to `127.0.0.1` with no remote protocol.

So nautilus does not talk to the SDK. It talks to **`logixd`**, a small
agent you run on the licensed Windows machine.

```
your laptop / CI  ──HTTPS+token──▶  logixd (Windows)  ──gRPC──▶  Studio 5000 SDK
     nautilus CLI                    tools/logixd                 127.0.0.1:53204
```

### What you need on the Windows machine

| | |
|---|---|
| Studio 5000 Logix Designer | **v31+**; **v37+** for `build`. Projects older than v31 are not supported. |
| Logix Designer SDK | 2.02.00 or later (installs `LdSdkService`). |
| .NET SDK | 10.x, to build `logixd`. |
| FactoryTalk Linx | The SDK supports **only** FactoryTalk Linx — RSLinx is not supported. Your controller must be visible in the FT Linx Network Browser. |
| OS | 64-bit only. |

### Build and run it

```powershell
cd tools\logixd
dotnet build -c Release -o C:\logixd-bin

$env:DOTNET_ROOT_X64 = "C:\dotnet10"         # see the warning below
$env:LOGIXD_TOKEN    = "<a long random string>"
$env:LOGIXD_ADDR     = "http://0.0.0.0:8188"
$env:LOGIXD_WORKDIR  = "C:\logixd-work"
$env:LOGIXD_COMM_ALLOW = "AB_ETH-1\10.0.0.5\Backplane\0"
& C:\dotnet10\dotnet.exe C:\logixd-bin\logixd.dll
```

:::danger[Never set plain `DOTNET_ROOT`]
Set **`DOTNET_ROOT_X64`**, not `DOTNET_ROOT`.

With plain `DOTNET_ROOT` pointed at an x64 install, `logixd` starts
normally, answers health checks, and then **every SDK call that needs a
FactoryTalk token fails with a bare `System.TimeoutException`** — no
message, no error code, nothing in any Windows or FactoryTalk log.

The SDK authenticates by launching `FtspAdapterLDSDK.exe`, which is a
**32-bit** process. A 32-bit app cannot resolve a runtime from an x64
`DOTNET_ROOT`, so it dies before it can answer and the client waits out a
five-second timeout. `DOTNET_ROOT_X64` steers only the x64 host.

This failure looks exactly like a licensing or FactoryTalk configuration
problem and is neither. It cost us a full day.
:::

The agent must **outlive the shell that started it** — use a scheduled task
or a service wrapper, not a bare `Start-Process` from an SSH session.

### Check it before you trust it

```bash
nautilus logix probe --agent http://plc-box:8188 --token $TOKEN
```

```
ok    sdk-service          LdSdkServer running
ok    logix-designer       installed: v38
ok    interactive-session  a user is logged on at the console
ok    live-sdk-call        v38 offers 106 processor types, e.g. 1756-L71
ok    create-project       created and closed a v38 1756-L71 project
```

The SDK needs several independent things — a FactoryTalk token, a FlexNet
feature, a CodeMeter entitlement — and each fails differently and
uninformatively, with whichever fails first masking the rest. `probe`
reports them separately so you know which one to fix.

### What you can then do

```bash
# Format conversion, either direction — two SDK calls, no GUI.
nautilus logix convert Plant.ACD Plant.L5X
nautilus logix convert Plant.L5X Plant.ACD

# CI for control logic: compile it. No controller, no downtime, no risk.
nautilus logix build Plant.ACD

# Does the controller still match the repo?
nautilus logix drift repo.L5X --comm-path 'AB_ETH-1\10.0.0.5\Backplane\0'

# A warm rung edit on a RUNNING controller.
nautilus logix push Plant.ACD rungs.L5X \
  --program MainProgram --routine F07_Alarms \
  --at 12 --replace 1 \
  --comm-path 'AB_ETH-1\10.0.0.5\Backplane\0' --finalize
```

The comm path is a FactoryTalk Linx browse path — read it off the FT Linx
Network Browser tree: driver, device address, `Backplane`, slot.

### Online edits

`--accept` sends the change to the controller; `--finalize` also assembles
it if the controller is in Run. Both require `--comm-path`: offline the SDK
**ignores** the option, so without it the edit would land as pending edits
nobody accepted while the command reported success. nautilus refuses
instead.

The project you push from must be **correlated with the controller** — the
same project that was downloaded to it — or going online is refused.

### Security

`logixd` can stop a controller. Treat it accordingly.

- Loopback by default. Binding a routable address **requires** a token, and
  the agent refuses to start without one.
- `LOGIXD_COMM_ALLOW` is agent-side configuration, never a request
  parameter: a pipeline cannot name a controller the operator has not
  blessed.
- Every path the agent touches is confined to `LOGIXD_WORKDIR`, compared
  after full resolution so traversal is rejected rather than string-matched.
- `download` never changes the controller mode unless explicitly asked.

None of that makes it safe to expose to a plant network. It is an agent for
a trusted host on a trusted link.

## What nautilus will not pretend to do

A **download stops the controller and resets tags to project values**, and
the SDK — unlike the GUI — neither changes the mode for you nor checks that
it is right. Gate it: a reviewed pipeline, an agent-side allowlist, and,
best of all, a permissive tag operators set from the HMI.

Byte-exact round-tripping of a hand-edited L5X is not offered, and nautilus
does not claim a generated rung and a hand-written one compute the same
thing. Where something cannot be done, the tooling says so rather than
imitating it.
