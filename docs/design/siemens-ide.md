# Design brief: nautilus as the IDE for a Siemens S7-1500

Status: **design, nothing built.** Issue #789, part of #725. Decisions D1–D6
(§8) are open with the maintainer.

> Siemens target: an anti-corruption layer in both directions, the same
> place on the map as the Logix target. Its writer, agent and importer are
> the only places Siemens spellings appear: OB, FB, DB, SCL, `"quoted"`
> globals, `#` locals, SimaticML. Upstream is Languages and the Manifest.
> `siemens/facade` is an application layer for `naut siemens serve`, like
> `logix/facade`.

## 0. Goal and done-when

Nautilus is the IDE for a Siemens S7 PLC. Editing, compiling, downloading,
monitoring and forcing all happen from nautilus and its VS Code extension.
The Siemens CPU is the runtime in place of `naut`, and nobody opens TIA
Portal. Importing or viewing Siemens files is a side benefit. A step that
only gets as far as "we can read their files" does not count as done.

**Done when** someone with a fresh clone of a nautilus project:

1. edits ST or LD in VS Code,
2. runs one command (or clicks one button) that compiles and downloads it to
   an S7-1500,
3. watches live values and the ladder overlay, writes a value, changes a
   rung and downloads again,

and no TIA window is open at any point. This is proven on an ET 200SP CPU
1510SP-1 PN, FW V3.1, which is in the S7-1500 family.

The shape is copied from the Logix path ([logix-authoring.md](logix-authoring.md)):

| Logix | Siemens |
|---|---|
| `logix/writer` | `siemens/writer` |
| `logixd` | `tiad` |
| `naut logix serve` + the rung overlay | `naut siemens serve` + the same overlay |

One thing is easier than with Logix: TIA builds blocks from SCL text
sources, so the codegen direction is a documented import path, not a
reverse-engineered one *(verify in S2)*.

## 1. The loop

```
VS Code ── edit .st/.ld ──▶ naut check --target siemens   (one rule table with the writer)
                │
                ▼
     naut siemens deploy          (or the extension's Download button → POST /api/siemens/deploy, NDJSON)
       1. writer: project → SCL sources (.scl/.db/.udt) + tag table XML + station spec
       2. tiad: create/open project, apply station, import sources, compile
          (messages mapped back to nautilus lines)
       3. tiad: download dry run → "software only, stays in RUN" or
          "needs STOP / reinitialises DB X"; the second needs an explicit yes
       4. tiad: download, start; online compare (project == CPU)
                │
                ▼
     naut siemens serve           (Linux, pure Go; the normal /api/* runtime API)
       PLC Web API (JSON-RPC over HTTPS): values, writes, RUN/STOP, diagnostics
       monitor, ladder overlay, HMI, alarms, history and Sparkplug work unchanged
```

| nautilus feature | On a Siemens target |
|---|---|
| ST, LD, FBD, user FBs and functions, UDTs, arrays | **Maps** (§2) |
| Tasks: interval / continuous | **Maps**: cyclic interrupt OBs / OB1 |
| Manifest tags | **Maps**: one global DB; a PLC tag table for I/O-bound tags |
| Live values, write, RUN/STOP, diagnostics | **Maps** via the Web API (§4) |
| Ladder overlay | **Maps** for named operands, since values arrive by name. Power flow inside a rung is shown only if the writer keeps it in statics (an option, at a memory cost) |
| HMI, alarms, history, Sparkplug publish | **Maps**: `naut siemens serve` hosts them against the CPU's values |
| Download of a change | **Partly.** Changed blocks load in RUN. An interface change reinitialises a DB unless it has a memory reserve *(verify)*, so the writer reserves memory on generated DBs and the dry run names any DB that would be reinitialised. There is no warm reload with state migration |
| Force | **No direct mapping** (D4). The Web API writes but does not force, and Siemens' own force is I/O-only and a TIA online function. Until D4, `/api/forces` answers 501 as the Logix facade does. A write is never called a force |
| SFC | Refused in v1 |
| Field drivers (Modbus, OPC UA client, EtherNet/IP, MQTT, IT) | **Do not map**: they run in `naut`. `naut check --target siemens` refuses them. The CPU's I/O comes from its local modules and its PROFINET devices |
| Tests | `naut test` runs on nautilus in virtual time. `naut test --target siemens` replays the same scenarios on the CPU in real time |

## 2. Codegen

**Everything is emitted as SCL source, LD and FBD included.** Nobody opens
TIA, so the shape of the code inside TIA matters only for a later handover.
Native LAD (SimaticML FlgNet) is D3.

| nautilus | SCL / TIA |
|---|---|
| Program | FB with an instance DB, called from its task's OB |
| Task with `interval:` | Cyclic interrupt OB (OB30+) at that cycle |
| Continuous task | OB1 |
| Manifest tags | Global DB `"Tags"` (optimized). Members are visible and writable to Web API/HMI/OPC UA *(verify the attribute spelling in SCL source)* |
| I/O-bound tags | PLC tag table, `%I`/`%Q` addresses from the station spec |
| UDT | PLC data type (`.udt`) |
| User FB / function | FB / FC |
| TON TOF TP R_TRIG F_TRIG CTU CTD CTUD SR RS | The IEC instructions of the same names as multi-instances; the members already match (`.Q` `.ET` `.CV`) |
| Elementary types, arrays, structs | Direct. DT becomes one of DTL or LDT, chosen and documented |
| `VAR RETAIN` | Retentive members |
| `dialect: siemens` (reserved) | Siemens-semantics blocks, only where no IEC form exists |

The rules:

- A semantic difference is either written exactly or refused at check
  time. It is never a warning. Examples: integer ÷0, overflow (the #782
  wrap rule), string defaults, TIME resolution, the first scan.
- Generated state lives in DB members (FB statics), never in temps, so the
  Web API can read it.
- Diagnostics use the Logix writer's shape, `Diag{Rule, Line, Message}`, and
  the rule IDs are `siemens/<rule>`.
- The writer consumes the parsed project. It does not change `lang/st`.

**A hardware config is part of every download.** It holds the web server
and its user, the CPU's address and the modules. It comes from a
declarative *station spec* (§3.3) under `target: siemens` in `nautilus.yaml`.

**One station spec per CPU.** A download replaces the whole hardware
config, so two projects aimed at one CPU erase each other. The shared bench
CPU has exactly one spec, in `examples/siemens-bench`, and the PROFINET
work owns its `profinet:` section (#723).

## 3. tiad: the Openness agent

### 3.1 Process

A C# agent on a Windows VM built from the `STACK=tia` golden image. It runs
TIA Portal Openness without a user interface and serves JSON over HTTP. It
copies `logixd`, the Logix agent:

- **.NET Framework 4.8.** The Openness assemblies do not load under modern .NET.
- **Interactive scheduled task at logon, not a service.** Clones boot
  logged in. TIA and its licence manager are assumed to behave like
  FactoryTalk does in session 0 *(verify)*.
- **Installed from source** by `install-tiad.ps1`, because
  `Siemens.Engineering.dll` exists only where TIA is installed and is never
  redistributed. The installer:
  - adds the account to the "Siemens TIA Openness" group;
  - writes the Openness allowlist entry for tiad's executable, so no
    consent dialog appears *(verify)*.
- **Agent-side config, never in requests:**

  | Variable | Default | Purpose |
  |---|---|---|
  | `TIAD_ADDR` | `http://127.0.0.1:8190` | Listen address |
  | `TIAD_TOKEN` | — | Bearer token. Required off loopback |
  | `TIAD_WORKDIR` | `C:\tiad-work` | Every path in a request is relative to this |
  | `TIAD_TARGETS` | — | The only CPU addresses tiad will go online to or download to |
  | `TIAD_PGPC_INTERFACE` | — | The NIC TIA uses to reach the CPUs |

- **Client side:** naut finds tiad through `NAUTILUS_TIAD_URL` and
  `NAUTILUS_TIAD_TOKEN`, then `--agent` and `--token`, then the manifest's
  `target.siemens.agent`. The token never goes in the manifest.

### 3.2 Envelope, sessions, errors

Calls are synchronous. The envelope is logixd's:

```json
{"ok": true,  "data": {}, "events": [{"kind": "info", "source": "compile", "message": "..."}]}
{"ok": false, "error": {"kind": "decision_required", "message": "...", "fatal": false}, "data": {}, "events": []}
```

A session is an open project. Opens are serialized, each project has one
writer, and an idle reaper closes forgotten sessions. `fatal: true` means
the session is gone.

| `error.kind` | HTTP | Meaning |
|---|---|---|
| `bad_request` | 400 | Malformed body, or a path outside the work dir |
| `unauthorized` | 401 | Missing or wrong token |
| `not_found` | 404 | No such session or file |
| `busy` | 409 | The project has a writer |
| `gate` | 412 | A probe gate failed (TIA, group, licence). The message names it |
| `target_refused` | 403 | The CPU address is not in `TIAD_TARGETS` |
| `decision_required` | 409 | A download question has no answer in the request. `data.questions` lists them |
| `declined` | 409 | A download question was answered no. `data.questions` lists them, and nothing is loaded |
| `compile_failed` | 422 | Compile errors. `data` is the compile result |
| `operation_failed` | 500 | Openness threw. The message carries its text |

### 3.3 The station spec

This is the JSON form of `target.siemens` minus the agent fields.
Passwords are never in the spec or the repo: the caller sends them from its
own environment in `secrets` (§3.4).

```json
{
  "cpu": {
    "name": "PLC_1", "order": "6ES7 510-1DK03-0AB0", "firmware": "V3.1",
    "address": "192.0.2.10", "subnetMask": "255.255.255.0", "router": "",
    "profinetName": "plc_1"
  },
  "webServer": { "enabled": true, "users": [ { "name": "naut", "rights": ["read", "write", "mode"] } ] },
  "modules": [ { "slot": 1, "order": "6ES7 131-6BH01-0BA0", "firmware": "V2.0", "inputStart": 0 } ],
  "profinet": {
    "updateTime": "16ms",
    "devices": [ {
      "gsdml": "GSDML-V2.4-Vendor-Device-20261117.xml",
      "dap": "DAP_1",
      "name": "io-device-1", "address": "192.0.2.20",
      "updateTime": "16ms",
      "modules": [ { "slot": 1, "subslot": 1, "module": "ID_Mod_In4Out2", "inputStart": 100, "outputStart": 100 } ]
    } ]
  }
}
```

The rules for applying a spec:

- **Device names are always explicit.** Automatic name generation is off.
- **The update time is at least 16 ms.**
- **Modules are found by their GSDML module ID.** I/O starts are optional,
  and TIA assigns any that are left out.
- **Applying a spec makes the project match it.** A device or module that
  is not in the spec is removed. That is how naut-profinet's
  module-difference test removes a module.
- **Every device's GSDML must already be installed** (`gsd/install`).
- **Every web user needs a password** in `secrets.webUsers`.
- **I-device transfer areas come later**, as `iDevice:` (P6).

**Where a PROFINET device's entry comes from.** In `nautilus.yaml`, a
nautilus PROFINET device is a reference, not a copy:
`{profinet-manifest: <path>, address: <host address>, update-time: 16ms}`.
naut expands the reference into the explicit JSON above with the profinet
package's exported manifest parser (subslots, identity, generated GSDML).
That way the PLC's config and the device's own driver read one file. A copy
could drift, and drift is exactly the module difference the PROFINET tests
create on purpose. tiad itself only ever sees the explicit form.

### 3.4 Calls

| Call | Body → data |
|---|---|
| `GET /v1/health` | → `{version, tia, openness, sessions, targets}` |
| `GET /v1/probe` | → `{usable, gates: [{name, ok, detail}]}`. Gates: `tia`, `openness-group`, `openness-allowlist`, `licence` (detail e.g. "trial, 12 days left"), `workdir`. Always 200 |
| `PUT /v1/files/{path}` | raw bytes → `{path, size}` |
| `GET /v1/files/{path}` | → raw bytes. `GET /v1/files` lists the work dir: `{files: [{path, size}]}` |
| `DELETE /v1/files/{path}` | → `{}` |
| `POST /v1/gsd/install` | `{path}` → `{path, alreadyInstalled}` (P1) |
| `POST /v1/projects` | `{name, station, secrets}` → `{session, project}`. A fresh project built from the spec |
| `POST /v1/sessions` | `{path}` (a project or archive in the work dir) → `{session, project}` |
| `GET /v1/sessions` | → `{sessions: [{session, project, idleSeconds}]}` |
| `DELETE /v1/sessions/{id}` | → `{}`. Closes without saving |
| `PUT /v1/sessions/{id}/station` | `{station, secrets}` → `{changes: [string]}` (P2, P5) |
| `POST /v1/sessions/{id}/software` | `{group, sources, tagTables, prune}` → `{results: [{path, ok, messages}], blocks: [{name, kind}]}`. Generates blocks into the group named `group`. With `prune`, blocks in that group that no source produced are deleted |
| `POST /v1/sessions/{id}/compile` | `{hardware, software}` → `{result: "success" or "warning" or "error", errors, warnings, messages: [{object, severity, text}]}` |
| `POST /v1/sessions/{id}/download` | §3.5 (P3) |
| `POST /v1/sessions/{id}/compare` | → `{equal, differences: [{object, state}]}`. An online compare |
| `POST /v1/sessions/{id}/export` | `{dir, blocks: "scl" or "xml" or "", tagTables, hardware, gsdml}` → `{files: [path]}` (P6 for `gsdml`) |
| `POST /v1/sessions/{id}/upload` | → `{}`. CPU into the session's project, for bringing a legacy CPU's program into nautilus |
| `POST /v1/sessions/{id}/save` | `{archive?}` → `{path}` |

`secrets` is `{"webUsers": {"naut": "<password>"}, "protection": "<password>"}`.
tiad never logs it and never writes it to the work dir. Run tiad on
loopback or across the tailnet, because the requests are plain HTTP.

### 3.5 Download: every decision explicit

Request:

```json
{
  "address": "192.0.2.10",
  "hardware": true, "software": true,
  "dryRun": false,
  "decisions": { "stopCpu": true, "reinitialize": ["Tags"], "overwrite": true,
                 "startAfter": true, "trustCertificate": "<sha256 fingerprint>" },
  "announced": true,
  "secrets": { "protection": "<password>" }
}
```

Response: `{dryRun, downloaded, questions: [{id, message, detail}], state}`.

- **A dry run** runs TIA's pre-download checks and returns them as
  `questions`. The `id` is one of:
  - `stop-cpu`
  - `reinitialize` (`objects` lists the DBs)
  - `overwrite`
  - `start-after`
  - `password`
  - `certificate` (detail is the fingerprint)
  - `other` (the check's own text)
- **A real download** answers each question only from `decisions`.
  - An unanswered question fails the call with `decision_required`, and
    nothing is loaded.
  - A `reinitialize` answer must name every DB the check named.
  - tiad never picks a default.
  - An `other` question always refuses, so a check tiad has never seen
    cannot be answered by accident.
- **`address`** must be in `TIAD_TARGETS`, or the call fails with
  `target_refused`. It must also equal the station's `cpu.address`
  (one station spec per CPU).
- **The project must be fully compiled** (hardware and software) since its
  last change.
- **An answer of no** fails the call with `declined`. Nothing is loaded.
- **`announced`** must be true for a real download. It records that the
  shared-CPU rule was followed (tell naut-lead first). It is a convention,
  not a safety.
- **`state`** is the CPU's mode afterwards, `RUN` or `STOP`, when tiad can
  read it.

### 3.6 PROFINET's requirements (#723)

| | Need | Call |
|---|---|---|
| P1 | Install the GSDML from `naut profinet gsdml` | `gsd/install` |
| P2 | Device in the IO system: explicit name and IP, modules by ID, update time | `station` |
| P3 | Compile, download HW+SW, RUN, announced first | `compile`, then `download` with `startAfter` and `announced` |
| P4 | Device state and its I/O | Not through TIA (§4) |
| P5 | Change or remove a module, download again | Edit the spec, `station`, `download` |
| P6 | 1510SP as an I-device, export its GSDML | `iDevice:` in the spec, `export {gsdml}` |

Layer 2: a download addresses the CPU by IP and should work across a router
*(verify in S2)*. Only DCP functions need layer 2 (accessible devices,
naming a device by hand), and tiad uses none of them, because the IO
controller names its devices from the hardware config.

### 3.7 Licence

Openness comes with STEP 7, and Professional is needed for the S7-1500
*(verify)*. The trial runs for 21 days from first start and is bound to the
machine's hardware ID. Clones are never used to restart it. After it ends,
`probe` fails the `licence` gate and every call that needs TIA fails with
`gate`. Code is written before the trial starts, and the trial is spent on
runs (D1).

## 4. Online plane

| | Web API | OPC UA | Classic S7 (PUT/GET) |
|---|---|---|---|
| Licence | none | per CPU | none |
| Optimized DBs, by symbol | yes | yes | no |
| Push | no; polling | subscriptions | no |
| Force | no | no | no |
| Bench FW 3.1 | Browse/Read/Write (≥ 2.8), operating mode (≥ 2.9). Diagnostics, modules and alarms vary by method from 3.1 to 4.1, so `Api.GetMethods` on the bench settles it | with a licence | yes |
| Role | **primary** | where the licence already exists; reuse the `opcua-client` driver | S7-300/400 and classic S7-1200, later |

Web API limits to design for:

- A request body of at most 128 kB on FW ≥ 3.1. The client splits batches.
- A token that expires after 120 s idle, so the client sends keep-alives.
- About 50 sessions on a 1510-class CPU.

The current Web Server manual covers FW ≥ 4.1, so the bench needs the
earlier edition for 3.1.

**Device state (P4)** comes from a generated diagnostic block that calls
`DeviceStates` and `ModuleStates` for the IO system into a DB. The Web API
reads that DB: connected, module difference, failed. This works on any
firmware.

Every cycle, the generated bench program also:

- copies the device's input bytes into a DB;
- copies a DB member to the device's output address.

Web API writes therefore never touch `%Q` directly. The CPU's operating
mode (RUN/STOP) comes from `Plc.ReadOperatingMode`.

**The station spec sets the CPU-side config:** the web server, a web user
with read/write rights, the protection level, and Web API visibility on
generated members.

## 5. Verification and kill criteria

Copied from [logix-authoring.md](logix-authoring.md) §5 and §7a.

**Verification:**
- `naut check --target siemens` and the writer share one rule table.
- Golden SCL snapshots are seeded from real exported SCL, never only from
  what we expect TIA to accept.
- Every writer PR is compiled in TIA once a runner with tiad exists.
- Behavioural equivalence: the same `_test.yaml` scenarios run on nautilus
  and on the CPU.
- Every download is followed by an online compare.

**Stop if:**
- any step needs the TIA GUI;
- generated blocks cannot be loaded in RUN for a logic-only change;
- the licence terms forbid headless use.

**Re-scope if:**
- edit-to-running takes more than 30 s (median of 20);
- generate + compile + download takes more than 2 min;
- errors surface at TIA compile instead of at check time;
- the passing subset cannot express a real program.

## 6. Packages

| Package | Role |
|---|---|
| `siemens/tiad` | Go client for tiad: the station spec and its validation, the download rule (`Resolve`), the sandbox rule (`CleanPath`). `tiadtest` is an in-memory fake agent that enforces §3.2–§3.5 and is the executable half of this contract |
| `siemens/webapi` | Go Web API client |
| `siemens/writer` | nautilus → SCL, tag tables, station spec |
| `siemens/facade` | `naut siemens serve` |
| `tools/tiad` | The C# agent and its installer |
| `cmd/naut/siemens*.go` | `naut siemens agent probe`, `deploy`, `serve` |

`siemens/*` follows the driver rule on the map: it never imports `runtime`,
`server` or `internal/project`. The exception is `siemens/facade`, which is
an application layer, as `logix/facade` is.

## 7. Order

**Now, without the TIA image or the bench:**
1. this brief;
2. the tiad client and fake;
3. the Web API client against a fake JSON-RPC server;
4. `target: siemens` in the manifest (additive);
5. the writer's minimal subset with goldens;
6. the C# source for tiad (untested until the image);
7. the serve facade.

**Then:**
- **S1, bench, read-only, needs the maintainer's OK:** `Api.Version`,
  `Api.GetMethods`.
- **S2, image plus trial:**
  - two exports, diffed;
  - SCL import → compile → dry run → download → compare, after telling
    naut-lead;
  - measure edit-to-running.

## 8. Decisions

| | Decision | Proposal |
|---|---|---|
| D1 | Trial for the spikes, or buy STEP 7 Professional now (get a quote)? | Trial for S1/S2; decide at S2's go/no-go |
| D2 | SIMATIC AX? | Not now. It covers the S7-1500 only, is sales-gated and still needs TIA for hardware |
| D3 | How far LAD goes | SCL in v1; native LAD only if a site needs TIA-side ladder |
| D4 | Force | (a) none, writes only; (b) an opt-in generated override layer for I/O-bound tags (inputs before the scan, outputs after it), shown in nautilus, but the CPU's FORCE LED stays dark; (c) Siemens' force through Openness, if it can activate one *(verify, unlikely)*. Proposal: (a) now, (b) after the loop works |
| D5 | Project ownership | Regenerate the whole TIA project from the repo on every build. Adopting an existing project comes later, for legacy sites |
| D6 | Bench | OK for the read-only S1 probe? Which modules are on the rack, is there a protection password, is the web server on? |
