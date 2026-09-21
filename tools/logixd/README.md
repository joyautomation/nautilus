# logixd — the Logix Designer SDK, over HTTP

`logixd` is a small agent that runs on a Windows machine with Studio 5000
and the Logix Designer SDK installed, and exposes that SDK as JSON over
HTTP so nautilus can drive a Logix project from anywhere.

## Why it has to exist

The SDK is a gRPC service bound to `127.0.0.1` only, reachable exclusively
through its .NET client (the Python SDK is pythonnet over the same DLL).
There is no remote protocol and no Go binding. See
`docs/design/logix-sdk-api.md` §2.

So the choice is "run everything on Windows" or "put an agent next to the
licence". This is the agent.

## What it adds over calling the SDK directly

All four come from the SDK's own documented behaviour, and all four are
things a caller would otherwise have to get right by itself:

| | |
|---|---|
| **Serialized opens** | The SDK forbids opening two projects at once — a Logix Designer restriction. Operations on *different* open projects still run in parallel. |
| **One writer per project** | The SDK copies the project server-side and leaves your file unlocked, so two savers silently clobber. `logixd` refuses to open a path another session holds. |
| **Events on every reply** | A failed partial import says almost nothing through its exception and a great deal through the SDK's event stream. Both ride along, on success and failure. |
| **A licensing probe that names the gate** | The SDK needs three independent things (FactoryTalk activation, a FlexNet feature, a CodeMeter entitlement) and each fails differently. Whichever fails first masks the others. |

## Build

Requires the .NET 10 SDK and the Logix Designer SDK's NuGet package, which
is **not on nuget.org** — the installer drops it beside its examples, and
`nuget.config` points there.

```powershell
dotnet build -c Release -o C:\logixd-bin
```

### Gotcha: the ASP.NET runtime

`logixd.exe` resolves its runtime through the machine's default `DOTNET_ROOT`,
which on a Rockwell box usually points at a .NET install that has **no
ASP.NET Core shared framework** (the Rockwell stack ships x86). If it starts
with *"The framework 'Microsoft.AspNetCore.App' was not found"*, launch it
through the runtime that does have it:

```powershell
$env:DOTNET_ROOT = "C:\dotnet10"
& C:\dotnet10\dotnet.exe C:\logixd-bin\logixd.dll
```

## Run

```powershell
$env:LOGIXD_ADDR     = "http://127.0.0.1:8188"   # loopback by default
$env:LOGIXD_TOKEN    = "<a long random string>"  # REQUIRED off loopback
$env:LOGIXD_WORKDIR  = "C:\logixd-work"          # the file sandbox
$env:LOGIXD_COMM_ALLOW = "backplane\0,AB_ETHIP-1\10.0.0.5\Backplane\0"
& C:\dotnet10\dotnet.exe C:\logixd-bin\logixd.dll
```

| Variable | Default | Meaning |
|---|---|---|
| `LOGIXD_ADDR` | `http://127.0.0.1:8188` | Bind address. |
| `LOGIXD_TOKEN` | — | Bearer token. **Binding a non-loopback address without one is refused at startup.** |
| `LOGIXD_WORKDIR` | `C:\logixd-work` | Every path in the API is relative to this, and escaping it is rejected. |
| `LOGIXD_COMM_ALLOW` | empty (any) | Comma-separated comm paths a caller may target. Agent-side config, never a request parameter. |
| `LOGIXD_IDLE_MINUTES` | `30` | Idle sessions are closed; a leaked one pins a project server-side. |

### Gotcha: it must survive the shell that started it

A process started from an SSH session dies with that session. Detach it —
a scheduled task, a service wrapper, or:

```powershell
Invoke-CimMethod -ClassName Win32_Process -MethodName Create `
  -Arguments @{ CommandLine = "cmd.exe /c C:\logixd-bin\run-logixd.bat" }
```

**It does not need an interactive desktop session.** That was tested: a
`CreateNewProject` timeout looked like session-0 FactoryTalk trouble, and
running the identical probe from an interactive logon produced the identical
timeout. The cause was a missing activation, not the session.

### Firewall

Off loopback you also need an inbound rule, and it should be scoped:

```powershell
New-NetFirewallRule -DisplayName "logixd 8188" -Direction Inbound -Action Allow `
  -Protocol TCP -LocalPort 8188 -RemoteAddress 100.64.0.0/10
```

## Check it works

```powershell
C:\dotnet10\dotnet.exe C:\logixd-bin\logixd.dll probe     # no port bound; exits 1 if unusable
```

or from anywhere:

```bash
nautilus logix probe --agent http://host:8188 --token ...
```

A probe **always answers 200** — "the SDK is unusable" is the answer you
asked for, not a failure to answer. The verdict is `data.usable`.

### Reading a failed probe

A **missing FactoryTalk activation shows up as a bare `TimeoutException`**,
not as a licence error. Check, in this order:

```powershell
FTACmdUtility listAvailable          # no activations at all?
Get-Content "C:\Users\Public\Documents\Rockwell Automation\Activations\Logs\RSsvr.log" -Tail 40
cmu --list-content                   # CodeMeter, where present
```

`flexsvr` reports *absent* and *expired* features identically, so
`UNSUPPORTED: "..." No such feature exists` means "not licensed here" and
nothing more precise.

## The API

Everything answers the same envelope:

```json
{ "ok": true,  "data": { ... }, "events": [ { "kind": "status", "source": "...", "message": "..." } ] }
{ "ok": false, "error": { "kind": "operation_failed", "message": "...", "type": "OperationFailedException", "fatal": false } }
```

`fatal` is the SDK's own distinction, and callers act on it:
`OperationFailed` means the request or the controller's state was wrong and
you can fix it; `OperationNotPerformed` means the SDK or the channel broke
and the session is dead.

| Method | Path | |
|---|---|---|
| GET | `/v1/health` | version, SDK client version, open sessions |
| GET | `/v1/probe` | the licensing gates |
| GET/PUT/DELETE | `/v1/files/{path}` | the work-directory sandbox |
| GET | `/v1/files`, `/v1/workdir` | listing, root |
| POST/GET/DELETE | `/v1/sessions[/{id}]` | open, list, close a project |
| POST | `/v1/create` | new project |
| POST | `/v1/convert` | ACD ↔ L5K ↔ L5X, by extension |
| POST | `/v1/upload-to-new` | upload a controller into a new project |
| POST | `/v1/sessions/{id}/save` | save, or save-as another format |
| POST | `/v1/sessions/{id}/build` | compile (v37+) |
| GET | `/v1/sessions/{id}/executables` | every routine and AOI, as XPaths |
| POST | `/v1/sessions/{id}/partial-export` | component → L5X |
| POST | `/v1/sessions/{id}/partial-import` | L5X → project, **offline only** |
| POST | `/v1/sessions/{id}/partial-import-with-target` | component import, **online capable** |
| POST | `/v1/sessions/{id}/import-rungs` | rung range, **online capable** |
| POST/GET | `/v1/sessions/{id}/comm-path`, `/state`, `/online`, `/offline`, `/mode` | controller connection |
| POST | `/v1/sessions/{id}/download` | **stops the controller** |
| POST | `/v1/sessions/{id}/tag/get`, `/tag/set` | one tag, offline or online |

### The online-edit endpoints

`import-rungs` and `partial-import-with-target` take an `onlineOption`:

- `LeaveEdits` — leave the change as pending offline edits (test)
- `AcceptEdits` — accept and send it down to the controller
- `FinalizeEdits` — accept, send down, and assemble if the controller is in Run

It is **ignored for an offline import**, which is why `nautilus logix push`
refuses `--accept`/`--finalize` without a `--comm-path`: otherwise the
option is silently dropped and the command reports success over a change
that never landed.

## Safety posture

- Loopback by default; a routable bind **requires** a token.
- `LOGIXD_COMM_ALLOW` is held here, not passed in — a pipeline cannot name
  a controller the operator has not blessed.
- The file sandbox compares the *fully resolved* path against the root, so
  traversal and absolute paths are rejected rather than string-matched.
- `download` never changes the controller mode unless explicitly asked
  (`ensureProgramMode`), because the SDK will happily try and fail, and
  because nobody should stop a line by omission.

None of that makes this safe to expose to a plant network. It is an agent
for a trusted host on a trusted link.

## Tests

- `logix/logixd/client_test.go` — the Go client, against a fake agent.
- `logix/logixd/integration_test.go` — against a real one:
  - `TestAgent*` needs **no licence** and must pass wherever `logixd` runs.
  - `TestSDK*` asks the probe first and **skips** with the failing gate
    named, so an unlicensed machine reports "skipped", never a red build.

```bash
NAUTILUS_LOGIXD_URL=http://host:8188 NAUTILUS_LOGIXD_TOKEN=... \
  go test ./logix/logixd/ -run 'TestAgent|TestSDK' -v
```

The online-edit test additionally wants `NAUTILUS_LOGIXD_COMM_PATH` and
`NAUTILUS_LOGIXD_PROJECT`. It exports a routine's own rungs and imports them
straight back with `FinalizeEdits`, so the change is a semantic no-op while
the path exercised is the real online-edit cycle.
