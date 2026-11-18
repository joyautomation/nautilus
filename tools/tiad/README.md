# tiad: TIA Portal Openness, over HTTP, for nautilus

tiad is to a Siemens S7-1500 what `tools/logixd` is to Logix. It is an agent on
a Windows machine with TIA Portal. It drives Openness without a user interface
and serves JSON over HTTP, so `naut` on Linux can build, compile and download
a CPU's project. The contract is `docs/design/siemens-ide.md` §3. The Go client
is `siemens/tiad`, and `siemens/tiad/tiadtest` holds an in-memory fake and the
contract suite.

**Status:** the core, which serves every call up to compile (health, probe,
files, GSDML install, projects, sessions, station, software, compile), and
`tiad-fake`. Download, compare, export, upload and save follow, then the TIA
side (`tiad.Openness`) and `install-tiad.ps1`. The plan is on #789.

## How a TIA call runs

Every TIA call goes through `ITia` (`core/Tia.cs`), on one STA thread behind
a queue. The agent makes the `ITia` on that thread with the first call that
needs it. Health, probe, the files and the session list never need it, so
they cost no trial time. Before any `ITia` call the agent checks every gate
(412 `gate`) and applies its own rules: the sandbox, the station rules, the
project name, one writer per project (compared case-insensitively, as
Windows compares paths). A failure TIA reports answers `operation_failed`
with TIA's text, and the thread carries on.

## Layout

| Path | What |
|---|---|
| `core/` | `tiad-core.dll`: HTTP, the envelope, the token, the work-dir sandbox, the agent's calls. It references nothing of Siemens's |
| `fake/` | `tiad-fake.exe`: the core with a scripted host instead of TIA, for tests only. `install-tiad.ps1` never builds it |
| `build-fake.sh` | Builds `out/tiad-core.dll` and `out/tiad-fake.exe` with Mono's `mcs` |

## C# 5, no project file

The TIA machine has the .NET Framework 4.8 and its stock compiler
(`C:\Windows\Microsoft.NET\Framework64\v4.0.30319\csc.exe`), and no SDK, NuGet
or network. That compiler knows C# 5, so these sources are C# 5: no string
interpolation, no `?.`, no `nameof`, no expression-bodied members, no
`out var`. `build-fake.sh` compiles with `mcs -langversion:5`, so anything
newer fails here first. The sources are ASCII only, because that `csc` reads
a file without a BOM in the machine's code page: write `"\u00b5s"`, not the
character. `build-fake.sh` refuses anything else. JSON goes through the framework's own
`JavaScriptSerializer`, read strictly by `Reader` (unknown fields and wrong
types are refused, as the Go side refuses them).

## Agent configuration

From the environment only; nothing in a request changes it.

| Variable | Default | |
|---|---|---|
| `TIAD_ADDR` | `http://127.0.0.1:8190` | Port 0 picks a free port (tests) |
| `TIAD_TOKEN` / `TIAD_TOKEN_FILE` | none | The bearer token, or a file holding it. Required, on loopback too: the agent refuses to start without one |
| `TIAD_MAX_FILE_BYTES` | 1 GiB | The largest file a put takes (413 beyond it) |
| `TIAD_WORKDIR` | `C:\tiad-work` | Required off Windows |
| `TIAD_TARGETS` | none | The only CPU addresses tiad goes online or downloads to. Strict dotted quads |
| `TIAD_SESSION_IDLE` | `1h` | The idle reaper closes sessions unused this long (a Go duration) |

A request with an `Origin` header (a browser) is refused with 403, whatever
its token. A JSON body must be `application/json` and a file
`application/octet-stream` (415 otherwise). An unexpected failure answers
`operation_failed` with the exception's type only; the detail, which can
carry the machine's paths, goes to the log.

On start it prints `tiad: listening on http://host:port` to stdout. It logs
one line per call to stderr: method, path, status, kind and time. It never
logs a header or a body.

## Testing

```
apt-get install -y mono-devel   # mcs, mono, and the 4.8 reference assemblies
tools/tiad/build-fake.sh
TIAD_FAKE_BIN=$PWD/tools/tiad/out/tiad-fake.exe NAUTILUS_REQUIRE_TIAD=1 go test ./siemens/tiad/...
```

`siemens/tiad/tiadtest.Contract` runs the same scenarios against the Go fake
and against `tiad-fake` under Mono (`TIAD_MONO` names another `mono`). Each
test starts its own `tiad-fake` with its own temporary work dir. The fake
exits when its stdin closes, so a killed test run leaves no process behind.
Without `TIAD_FAKE_BIN` those tests skip. With `NAUTILUS_REQUIRE_TIAD=1` (CI)
a missing binary fails the run instead, so a job meant to test the C# agent
never passes by testing nothing.

The rules exist twice, in Go (`siemens/tiad`, `siemens/station`) and in C#
(`core/`). The suite checks them over the wire with one table
(`RefusedPaths`, `UncleanURLs`, `AcceptedPaths`). A change to a rule changes
both copies and the table.

`tiad-fake` answers control calls the real agent never has
(`fake/FakeMain.cs`):

| Call | |
|---|---|
| `PUT /fake/gates` | The machine gates probe reports. The work-dir gate stays real |
| `PUT /fake/script` | What compile returns, and `ITia` calls that are slow or fail the way TIA fails |
| `GET /fake/calls` | Every `ITia` call so far, with its thread. `start` is when TIA would start |

Its `ITia` keeps projects in memory and answers what the Go fake answers. Like
TIA, it refuses to create a project over an existing one.
