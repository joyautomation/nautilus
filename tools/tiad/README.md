# tiad: TIA Portal Openness, over HTTP, for nautilus

tiad is to a Siemens S7-1500 what `tools/logixd` is to Logix. It is an agent on
a Windows machine with TIA Portal. It drives Openness without a user interface
and serves JSON over HTTP, so `naut` on Linux can build, compile and download
a CPU's project. The contract is `docs/design/siemens-ide.md` §3. The Go client
is `siemens/tiad`, and `siemens/tiad/tiadtest` holds an in-memory fake and the
contract suite.

**Status:** the core serves every call (health, probe, files, GSDML install,
projects, sessions, station, software, compile, download, compare, export,
upload, save). `tiad.exe`, the agent with its Windows host, and
`install-tiad.ps1` are tested on a TIA machine without starting TIA.
`tiad.Openness`, the TIA side, is written and compiles against the Openness
V21 public API, but it has never run: its first run is the first TIA start
(S2a). The plan is on #789.

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
| `host/` | `tiad.exe`: the core with the Windows host. It reads the machine gates and loads `tiad.Openness.dll` from beside itself on the first TIA call |
| `openness/` | `tiad.Openness.dll`: the TIA side, `ITia` over Openness V21. The only assembly that references Siemens.Engineering, and glue only (below). `install-tiad.ps1` builds it on the TIA machine, never with `-NoTia` |
| `test/` | `tiad-test.exe`: unit tests of everything `tiad.Openness` decides (`core/TiaRules.cs`). `build-fake.sh` builds and runs it |
| `fake/` | `tiad-fake.exe`: the core with a scripted host instead of TIA, for tests only. `install-tiad.ps1` never builds it |
| `install-tiad.ps1` | Builds `tiad.exe` on the TIA machine with its stock `csc` and installs it (below) |
| `check-install.ps1` | Checks an install's security properties on the TIA machine (below) |
| `build-fake.sh` | Builds `out/tiad-core.dll` and `out/tiad-fake.exe` with Mono's `mcs`, and `out/tiad.exe` only to hold the host to C# 5; builds and runs `out/tiad-test.exe` |

## The TIA side

`tiad.exe` loads `tiad.Openness.dll` from beside itself at the first call
that needs TIA, after every gate holds, and resolves `Siemens.Engineering*`
assemblies only from the public API folder TIA's setup registered. Its
constructor starts TIA Portal without a user interface; it is the one place
tiad starts TIA.

CI has no Siemens assemblies, so `tiad.Openness` decides nothing itself. Every
decision is pure code in `core/TiaRules.cs`, unit-tested by `test/` under
Mono:

- **Download, one check at a time.** Openness calls the pre-download
  callback once per configuration and lists them nowhere: a configuration
  has no parent, and no Openness type holds them (#789 S2b, V21). So
  `ITia.Download` calls `answer` once per check, in TIA's order, and each
  is answered on its own, only from the request; the first one not
  answered yes cancels there (throws from the callback). Start-after is
  the request's `startAfter`, decided before TIA is called and written in
  the after-download callback, `NoAction` unless decided or after a
  cancel. It also cancels on a configuration asked twice, an answer it
  cannot apply, or a write that fails. Answers that can change the CPU
  (stop, start, overwrite, consistent download, alarm texts, all blocks,
  reinitialize, anything unknown; never a
  start-after answered no, which writes `NoAction`:
  `DownloadChecks.Acting`) are kept as written: TIA may act on one before
  it asks the next, so a cancel after one is `outcome_unknown`, naming it
  and what to check. Each callback is logged as TIA hands it over (its type
  and its parent's; every read dropped if it fails) before anything is
  decided. Once TIA's download call is entered, any failure answers
  `outcome_unknown`. A real download succeeds only when TIA's result says
  loaded AND the agent's online compare right after (PLC software only:
  Openness has no online hardware compare) is equal. Equal is decided in
  core from TIA's compare tree (`TiaNames.CompareEqual`): an identical
  root, at least one object compared below it, nothing non-identical
  anywhere, and every program block the project holds (`ITia.Compare`
  reads them in the same call) in the tree as an identical element of
  its label (`TiaNames.BlockLabel`, `Main [OB1]`); an empty tree, or one
  that leaves out a project block, is never equal. A hardware download
  says `hardwareVerified: false`: tiad cannot verify the hardware, so
  one is refused before TIA hears of it unless the request says
  `decisions.acceptUnverifiedHardware: true` and carries the operator's
  `attestation` (`by`, `text`), on every CPU, the bench included
  (`HardwareRule`; siemens-ide.md §3.5). How a download ended is decided in core too
  (`DownloadDecider.Finish`). Before TIA starts, every selection tiad may
  write is checked against this Openness's enum types.
- **No dry runs yet.** With no pre-download callback there is nothing to
  cancel from, so a dry run could load. `ITia.CanDryRun` is false for
  `tiad.Openness`, and the agent refuses every dry run before TIA hears of
  it, until S2b proves a cancel at the first check loads nothing (a dry
  run sees only that first check: TIA asks one at a time).
- **Names are checked, not guessed.** The CPU's attributes (address, subnet
  mask, router, PROFINET name, generated names off) are checked against
  `GetAttributeInfos` before each is set; a missing one fails, naming the
  ones there are, and the first time tiad meets a kind of object it logs
  its attribute names. The PC interface is the only PN/IE one TIA lists,
  or `TIAD_PC_INTERFACE`.
- **Refused until S2a:** GSDML install (Openness has no public call for
  it), PROFINET devices, rack modules, the web server, the I-device GSDML
  export, and any upload configuration (a password, missing products).
- **The licence latch.** The `licence` gate is unknown until TIA starts,
  then latches: `ok` once TIA started, `failed` at the first licence error,
  from the start or any later TIA call. Failed blocks every TIA call until
  the agent restarts. A headless start does not activate TIA's trial.
  After installing or activating a licence, restart the agent by rerunning
  `install-tiad.ps1` with the same options, which stops the old `tiad.exe`
  (stopping the task alone can leave it running), so a latched `failed`
  clears. Check that no TIA Portal process the old agent started is left
  over.

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
| `TIAD_ACCESS_KEY_FILE` | none | The access-record key (64 hex characters): it signs tiad's record of the Full access password it set in a project (`cpu.accessControl`). The installer makes it and keeps it across reinstalls. Without it the key is the run's own, and a later run reads every record as another key's |
| `TIAD_MAX_FILE_BYTES` | 1 GiB | The largest file a put takes (413 beyond it) |
| `TIAD_WORKDIR` | `C:\tiad-work` | Required off Windows |
| `TIAD_PINS_DIR` | `%ProgramData%\tiad\pins` | The device pins (siemens-ide.md §3.8). Required off Windows; refused inside `TIAD_WORKDIR`, which the files API reaches. `install-tiad.ps1` locks it and never wipes it |
| `TIAD_TARGETS` | none | The only CPU addresses tiad goes online or downloads to. Strict dotted quads |
| `TIAD_SESSION_IDLE` | `1h` | The idle reaper closes sessions unused this long (a Go duration) |
| `TIAD_RETIRE_WATCH` | `3m` | How long a retire of a TIA instance that trusted blindly may take before tiad counts TIA as gone (a Go duration) |
| `TIAD_NO_TIA` | unset | `1` fails the `tia` gate, so no call can start TIA (or a trial clock) |
| `TIAD_PC_INTERFACE` | none | The PC interface TIA reaches CPUs through, when it lists more than one in PN/IE mode (`install-tiad.ps1 -PcInterface`) |
| `TIAD_EXIT_ON_STDIN` | unset | `1` ends the agent when its stdin closes (the test harness sets it). Off by default: a scheduled task's stdin may be closed from the start |

## Gates

`probe` reports five gates, each `ok`, `failed` or `unknown`. A failed gate
(or a state the agent does not know) stops every TIA call with 412 `gate`.
An unknown one is reported but blocks nothing. `ok` in the JSON is true only
in state `ok`. The host computes them without starting TIA (`core/Machine.cs`,
a pure evaluator over the machine's facts):

| Gate | Check |
|---|---|
| `tia` | Openness V21 is registered in HKLM (64-bit view), and its net48 `Siemens.Engineering.Base.dll` and `.Step7.dll` exist. Failed with `TIAD_NO_TIA=1` |
| `openness-group` | The process token holds "Siemens TIA Openness". A member added after logon must log off and on |
| `openness-allowlist` | The Openness allowlist entry TIA requires: an entry under `HKLM\Software\Siemens\Automation\Openness\AllowList\tiad.exe` whose `Path` names this exe (case-insensitive), whose `DateModified` is its last write time (UTC, `yyyy/MM/dd HH:mm:ss.fff`) and whose `FileHash` is its SHA-256 (base64). Every rebuild needs a new entry; the installer writes it |
| `licence` | `unknown` until TIA's first start. Then `ok` once TIA started, or `failed` at the first licence error, from the start or any later call (TIA starts headless without a licence and refuses the first call that needs one), until the agent restarts |
| `workdir` | The work dir is writable |

## Installing on a TIA machine

Copy `tools/tiad` to the machine and run, in an elevated Windows PowerShell:

```
.\install-tiad.ps1 -NoTia            # installed, but TIA cannot start
.\install-tiad.ps1 -Targets 192.0.2.10
.\install-tiad.ps1 -Uninstall
```

It checks for TIA V21 and the compiler, adds the account to the Openness
group, builds `tiad-core.dll`, `tiad.exe` and (without `-NoTia`)
`tiad.Openness.dll` into `C:\Program Files\tiad`,
writes the allowlist entry for that build, and writes the token to
`%ProgramData%\tiad\tiad.token` (the agent reads it through
`TIAD_TOKEN_FILE`). It then registers an interactive scheduled task at
logon (someone must be logged in at the console) and checks the agent, on
the address it listens on, with health and probe; neither starts TIA. Logs
go to `%ProgramData%\tiad`.

**The agent runs elevated**, so nothing under it may be writable by another
local account:
- The install dir, `%ProgramData%\tiad`, the token and the work dir are
  owned by Administrators, with Administrators and SYSTEM the only entries
  and inheritance off.
- The directory is locked before the token is written into it.
- A token file Administrators do not own is replaced, never reused.
- A directory another account made, or one holding a link, a junction or
  another account's file, makes the install refuse.

`-Listen` takes one IPv4 address: loopback (the default), or this
machine's own address on a trusted network such as a tailnet. Never
`0.0.0.0`; the agent refuses that in `TIAD_ADDR` too. A non-loopback
address gets one firewall rule for that address and port. Every reinstall
and uninstall removes all of that install's rules first, whatever port they
were for.

`check-install.ps1` checks all of this on a TIA machine. It reinstalls with
`-NoTia` several times (TIA never starts) and exits with the number of
failed checks. It also checks that `-NoTia` builds no `tiad.Openness.dll`
and removes one left behind.

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

On the TIA machine, the real agent runs the part of the contract that never
needs TIA (`ContractNoTIA`), plus its real gates, all with `TIAD_NO_TIA=1`
and a tripwire that fails if TIA Portal runs before or after a test:

```
GOOS=windows go test -c -o tiadtest.exe ./siemens/tiad/tiadtest
# on the machine, after install-tiad.ps1:
$env:TIAD_BIN = 'C:\Program Files\tiad\tiad.exe'; .\tiadtest.exe -test.run RealAgent -test.v
```

`tiad-fake` answers control calls the real agent never has
(`fake/FakeMain.cs`):

| Call | |
|---|---|
| `PUT /fake/gates` | The machine gates probe reports. The work-dir gate stays real |
| `PUT /fake/script` | What compile returns, and `ITia` calls that are slow or fail the way TIA fails |
| `PUT /fake/machine` | A scripted machine (registry values, files, the exe's path, time and hash, the group). Probe then runs the real gate evaluator over it |
| `GET /fake/calls` | Every `ITia` call so far, with its thread. `start` is when TIA would start |

Its `ITia` keeps projects in memory and answers what the Go fake answers. Like
TIA, it refuses to create a project over an existing one.
