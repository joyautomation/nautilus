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

An L5X is the XML Logix Designer writes on **File → Export**. `naut`
reads it directly, on any OS:

```bash
naut logix info    DemoLine.L5X     # what is in this export?
naut logix import  DemoLine.L5X     # UDTs -> IEC types, tags -> a tag file
naut logix graph   DemoLine.L5X     # RLL routines -> the ladder model
naut logix normalize DemoLine.L5X   # pin the attributes that move on every export
```

### Tags arrive documented

```bash
naut logix import DemoLine.L5X --scope '*'
# wrote logix_types.st (3 types) and tags/logix.yaml (8 tags)
```

```yaml
- { name: DwellTmr, role: input, type: TIMER, init: { PRE: 30000, ACC: 0 }, desc: "Dwell before the pump is allowed to restart" }
- { name: StartPB,  role: input, init: false, desc: "HS-101 start pushbutton" }
```

That `desc:` is the point. Logix keeps tag documentation in the **offline
project file**, so a live CIP browse cannot see it — `naut eip import`
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
naut logix normalize --check controller.L5X repo.L5X
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

### Install it

```powershell
cd tools\logixd
.\install.ps1 -Listen 0.0.0.0 -Port 8188      # elevated PowerShell
```

That is the whole setup. It checks the prerequisites, builds `logixd`, mints
a bearer token, registers it to start at logon, adds a firewall rule, then
starts it and prints the licensing probe gate by gate — so you find out
whether the SDK is actually usable before you trust it, not later.

Re-running it is an upgrade; `-Uninstall` reverses it. The token lands in
`%ProgramData%\logixd\logixd.token`, readable by administrators only.

:::danger[Never set plain `DOTNET_ROOT`]
The installer **refuses to run** while `DOTNET_ROOT` is set, and prints the
two commands that fix it. Set **`DOTNET_ROOT_X64`** instead.

With plain `DOTNET_ROOT` pointed at an x64 install, `logixd` starts
normally, answers health checks, and then **every SDK call that needs a
FactoryTalk token fails with a bare `System.TimeoutException`** — no
message, no error code, nothing in any Windows or FactoryTalk log.

The SDK authenticates by launching `FtspAdapterLDSDK.exe`, which is a
**32-bit** process. A 32-bit app cannot resolve a runtime from an x64
`DOTNET_ROOT`, so it dies before it can answer and the client waits out a
five-second timeout. `DOTNET_ROOT_X64` steers only the x64 host.

This failure looks exactly like a licensing or FactoryTalk configuration
problem and is neither. It cost us a full day, which is why the installer
will not let you reproduce it.
:::

:::caution[Somebody has to be logged in]
The agent runs as an **interactive scheduled task at logon, not a service**,
because FactoryTalk authentication does not work from session 0. After a
reboot, logixd will not come back until someone logs in at the console.

That is a property of the SDK, not of the packaging. If you need it
unattended, the machine needs an auto-login.
:::

### When something isn't working

Start with `naut logix probe`. Every gate that fails now prints what to
do about it, underneath the failure. If the probe itself cannot connect, or
the symptom is in a verb rather than a gate, find it here.

| What you see | What it is | What to do |
|---|---|---|
| `logixd: unreachable ... connection refused` | The agent is not running. After a reboot this is almost always it. | Log in at the machine's console, then `Start-ScheduledTask -TaskName logixd`. The agent is an interactive task, not a service — see above. |
| `logixd: unauthorized` / HTTP 401 | Wrong bearer token. Re-installing mints a new one. | Re-read `%ProgramData%\logixd\logixd.token`. |
| A bare `System.TimeoutException` from any SDK call, nothing in any log | `DOTNET_ROOT` is set to an x64 .NET. | Unset it, set `DOTNET_ROOT_X64` instead, restart the agent. The installer refuses to proceed while it is set. |
| `RxCMP_E_AUDIT_INVALIDOPTYPE - Invalid type.` on `build` or `download` | An instruction the project uses does not exist under that name. The L5X importer does **not** validate instruction names, so a bad one imports cleanly and only fails at verify. | Check the mnemonic. Neutral text uses the short spellings: `GE` `GT` `LE` `LT` `EQ` `NE` `MOVE` `LIMIT` — **not** the `GEQ`/`GRT`/`MOV`/`LIM` captions the ladder editor shows. Opening the project in Logix Designer and verifying names the rung and the fix; the SDK does not pass that detail through. |
| `RxCL_E_CANNOT_UPLOAD_PHYS_ADDR` going online | You passed a project file to an online edit. | Don't — an online edit takes no project file. See [Online edits](#online-edits). |
| `drift` reports a difference of megabytes on a project you just downloaded | A detailed export compared against a basic one. | Fixed in current builds: `drift` matches the repo file's export kind. If you see it, your CLI is older than your agent. |
| `build` fails on a project Logix Designer opens fine | Usually a real verify error the SDK reports only as a code. | Open the project in Logix Designer and run Verify Controller. Its error list names the rung and the instruction; that is currently the fastest way to a diagnosis. |

If a gate fails and the remedy does not resolve it, `naut logix probe
--json` gives the whole result, including which Logix revisions the agent
found installed.

### Check it before you trust it

```bash
naut logix probe --agent http://plc-box:8188 --token $TOKEN
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
naut logix convert Plant.ACD Plant.L5X
naut logix convert Plant.L5X Plant.ACD

# CI for control logic: compile it. No controller, no downtime, no risk.
naut logix build Plant.ACD

# Does the controller still match the repo?
naut logix drift repo.L5X --comm-path 'AB_ETH-1\10.0.0.5\Backplane\0'

# A warm rung edit on a RUNNING controller.
naut logix push Plant.ACD rungs.L5X \
  --program MainProgram --routine F07_Alarms \
  --at 12 --replace 1 \
  --comm-path 'AB_ETH-1\10.0.0.5\Backplane\0' --finalize
```

Don't transcribe the comm path out of a GUI. Ask:

```bash
naut logix browse
# AB_ETH-1\10.0.0.5\Backplane\0   PlantCtl   (1756-L85E)
```

`browse` reads what FactoryTalk Linx has **already discovered** — it does
not scan the network — and prints the controller sitting on the end of each
path, so you pick the one you meant rather than assembling a string and
hoping. If it finds nothing, browse to the controller once in the FT Linx
Network Browser and try again.

### Online edits

`--accept` sends the change to the controller; `--finalize` also assembles
it if the controller is in Run. Both require `--comm-path`: offline the SDK
**ignores** the option, so without it the edit would land as pending edits
nobody accepted while the command reported success. nautilus refuses
instead.

An online edit takes **no project file**:

```sh
naut logix push --comm-path 'AB_ETH-1\10.0.0.5\Backplane\0' \
  --program MainProgram --routine MainRoutine --at 1 --replace 1 \
  --accept rungs.L5X
```

It uploads the program the controller is running, imports into that, and
sends it back. This is not a convenience — it is the only thing that works.
A project file on disk **cannot go online** even when its logic matches the
controller byte for byte, because downloading stamps match information into
the project and that copy stays on the machine that did the download. Push
your repo's own `.ACD` and the SDK fails with:

```
RxCL_E_CANNOT_UPLOAD_PHYS_ADDR - Failed to upload physical address information.
```

which is about as far from "wrong project file" as an error message gets.
Pass `-o` if you want to keep the resulting project locally.

To check the controller still matches your repo, use `naut logix drift`
— it exits 1 on a mismatch, so it works as a CI gate.

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
