# Design brief: nautilus as the IDE for a Siemens S7-1500

Status: **design.** Issue #789, part of #725. Decisions D1–D6 are taken
(§8, 2026-11-17).

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
       3. tiad: download dry run (provisional, §3.5) → "software only, stays in RUN"
          or "needs STOP / reinitialises DB X"; the second needs an explicit yes
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
| Download of a change | **Partly.** Changed blocks load in RUN. An interface change reinitialises a DB unless it has a memory reserve *(verify)*, so the writer reserves memory on generated DBs and the download's checks name any DB that would be reinitialised (through the dry run once S2 proves it, §3.5). There is no warm reload with state migration |
| Force | **No direct mapping** (D4). The Web API writes but does not force, and Siemens' own force is I/O-only and a TIA online function. D4: in v1 `/api/forces` answers 501, as the Logix facade does. The opt-in override layer for I/O-bound tags comes after the loop works, and it is required. A write is never called a force |
| SFC | Not in the first writer subset. v1.x: an SCL state machine with step flags and step timers in statics, so the SFC view shows active steps live |
| Field drivers (Modbus, OPC UA client, EtherNet/IP, MQTT, IT) | **Do not map**: they run in `naut`. `naut check --target siemens` refuses them. The CPU's I/O comes from its local modules and its PROFINET devices |
| Tests | `naut test` runs on nautilus in virtual time. `naut test --target siemens` replays the same scenarios on the CPU in real time |

## 2. Codegen

**Everything is emitted as SCL source, LD and FBD included.** Nobody opens
TIA, so the shape of the code inside TIA matters only for a later handover.
Native LAD (SimaticML FlgNet) is D3.

| nautilus | SCL / TIA |
|---|---|
| Program | FB with an instance DB, called from its task's OB |
| The task (`scan:`) | Cyclic interrupt OB30 at that cycle, 1 ms to 60 s. **One task in v1**: nautilus copies shared tags in at a task's scan start and commits them at its end, while cyclic interrupt OBs preempt each other and touch the tags DB in place. Copy-in/copy-out for several tasks is its own later item. nautilus has no free-running task, so there is no OB1 |
| Manifest tags | Global DB `"Tags"` (optimized). Members are visible and writable to Web API/HMI/OPC UA *(verify the attribute spelling in SCL source)* |
| I/O-bound tags | PLC tag table, `%I`/`%Q` addresses from the station spec |
| UDT | PLC data type (`.udt`) |
| User FB / function | FB / FC |
| TON TOF TP R_TRIG F_TRIG | The IEC instructions as multi-instances (`TON_TIME`, …); the members already match (`.Q` `.ET`) |
| CTU CTD CTUD | `CTU_LINT` etc.: nautilus's counter value is its 64-bit int and keeps counting past 32767 |
| SR RS | Written inline as set/reset logic on a static, since they are believed to be LAD/FBD boxes that SCL cannot call *(verify)* |
| Elementary types, arrays, structs | Direct. DT becomes one of DT, LDT or DTL, chosen and documented *(verify each on FW 3.1)* |
| `VAR RETAIN` | Retentive members *(verify the SCL source syntax for per-member retain)* |
| `dialect: siemens` (reserved) | Siemens-semantics blocks, only where no IEC form exists |

The rules:

- **Compute what nautilus computes, or refuse.** A semantic difference is
  either written exactly or refused at check time; it is never a warning.
  nautilus's own lowering runs first, so what nautilus cannot run is
  refused before anything else.
- **Integer arithmetic** is 64-bit in nautilus, wrapped only at the store
  (#800). The writer widens every narrow operand to LINT and wraps
  explicitly at the store. Where it cannot (an FB input, a function
  argument, a CASE selector, a REAL target), integer arithmetic is refused.
- **Refused in the first subset:** division by anything but a non-zero
  constant (nautilus gives 0 and counts it; the CPU's answer is not pinned
  down), TRUNC (LINT in nautilus, DINT in TIA), LTIME (milliseconds in
  nautilus, nanoseconds in TIA), TIME arithmetic (64-bit against 32-bit
  milliseconds), bitwise operators, STRING (TIA's holds 254 characters, a
  nautilus STRING has no length), arrays, UDTs, enums, user POUs and SR/RS.
- **Known divergence, documented rather than refused:** a REAL → integer
  conversion out of range faults the scan in nautilus, while the CPU sets
  ENO FALSE and carries on.
- Temps have no start value in TIA. nautilus re-sets a temp to its init on
  every scan (#203), so the body starts by assigning each one.
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

## 2a. Adopting an existing project (D5)

Adopting an existing TIA project is what lets nautilus drive a legacy PLC.
It is the Logix pattern: import, edit in nautilus, write back, download
only what changed, and verify that nothing else moved.

**Ownership.** An adopted project declares the blocks nautilus owns.
Everything else is carried through untouched, and the online compare after
a download must show it unchanged.

**Where the project lives.** Its source of truth is in the repo: the
project archive as last uploaded or committed, plus the nautilus sources
for the owned blocks. Each deploy then:

1. opens the archive on a fresh clone;
2. replaces the owned blocks;
3. compiles;
4. downloads only what changed;
5. runs the online compare.

| TIA block language | v1 | Full adoption needs |
|---|---|---|
| SCL | Import → nautilus ST, edit, write back as SCL, download that block only. Logic equality after the round trip (formatting may change) | A tolerant SCL front end (`#`, `"quoted"` globals, `REGION`, instruction names) over the existing ST parser |
| LAD, FBD | Imported for viewing and live monitoring, never written. Editing one is an explicit opt-in conversion to SCL, which changes its language in TIA | SimaticML LAD/FBD reader, then a writer, sized by how much of a real project is LAD (S3) |
| GRAPH | View-only later; untouched | A GRAPH reader |
| STL | Untouched; viewable as text | None planned |
| F-blocks, know-how-protected blocks | Never touched. A deploy that would change one refuses | — |
| Hardware config, tag tables, UDTs, global DBs, technology objects, HMI, libraries | Preserved as they are | Per-object import later |

## 3. tiad: the Openness agent

### 3.1 Process

A C# agent on a Windows VM built from the `STACK=tia` golden image. It runs
TIA Portal Openness without a user interface and serves JSON over HTTP. It
copies `logixd`, the Logix agent:

- **Stateless across clones (D1).** The TIA VM is recreated from the golden
  image, so tiad keeps nothing that must survive a clone. Every deploy
  rebuilds the TIA project from what naut sends: the station spec and
  sources, plus the project archive for an adopted project. The work dir is
  scratch, and anything worth keeping (exports, uploads) goes back to naut
  as files and into git.
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

- **Client side:**
  - naut finds tiad through `NAUTILUS_TIAD_URL`, then `--agent`, then the
    manifest's `target.siemens.agent`.
  - The token comes from `NAUTILUS_TIAD_TOKEN`, or from the file
    `NAUTILUS_TIAD_TOKEN_FILE` names (a copy of the agent's token file).
  - The token is never a command-line flag, since argv is visible to every
    user on the machine, and never in the manifest.

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
| `unreachable` | — | Client-side: no reply (agent down, or the context was cancelled; the cause unwraps). After a download, the outcome is **unknown**, so compare before retrying |
| `not_found` | 404 | No such session or file |
| `busy` | 409 | The project has a writer |
| `gate` | 412 | A probe gate failed (TIA, group, licence). The message names it |
| `target_refused` | 403 | The CPU address is not in `TIAD_TARGETS` |
| `decision_required` | 409 | A download question has no answer in the request. `data.questions` lists them |
| `declined` | 409 | A download question was answered no. `data.questions` lists them, and nothing is loaded |
| `compile_failed` | 422 | Compile errors. `data` is the compile result |
| `operation_failed` | 500 | Openness threw (the message carries its text), or a download was asked of a project not fully compiled since its last change |

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
  "modules": [ { "slot": 2, "order": "6ES7 131-6BH01-0BA0", "firmware": "V2.0", "inputStart": 0 } ],
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
  Names follow the PROFINET rules:
  - lowercase DNS-style labels;
  - at most 240 characters;
  - no n.n.n.n form (n = 0-999);
  - no first label of the form `port-xyz` or `port-xyz-abcde`.
- **Slot 1 is the CPU's own.** Local modules go in slot 2 and up
  *(verify on the S2a upload)*.
- **The update time is at least 16 ms.** For a nautilus device (a
  `profinet-manifest:` reference) it is also one its GSDML offers:
  RT_CLASS_1 at a 1 ms send clock, reduction ratio 1 … 512, so 16, 32,
  64, 128, 256 or 512 ms and not below the device's `min-cycle`. A
  reference with no update time gets the first of those at or above
  `min-cycle`, set explicitly (#898).
- **Modules are found by their GSDML module ID.** I/O starts are optional,
  and TIA assigns any that are left out.
- **Applying a spec makes the project match it.** A device or module that
  is not in the spec is removed. That is how naut-profinet's
  module-difference test removes a module.
- **Every device's GSDML must already be installed** (`gsd/install`).
- **Every web user needs a password** in `secrets.webUsers`. Its rights are
  any of `read`, `write`, `mode` (change RUN/STOP) and `diagnostics`.
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

Which calls start TIA: `health`, `probe` and the file calls never do,
so they cost no trial time on a fresh clone. Every other call starts the
headless TIA process if it is not already running. `probe`'s `licence`
gate has to read the licence state without starting TIA, through the
licence manager *(verify that this is possible)*.

| Call | Body → data |
|---|---|
| `GET /v1/health` | → `{version, tia, openness, sessions, targets}` |
| `GET /v1/probe` | → `{usable, gates: [{name, ok, detail}]}`. Gates: `tia`, `openness-group`, `openness-allowlist`, `licence` (detail e.g. "trial, 12 days left"), `workdir`. Always 200 |
| `PUT /v1/files/{path}` | raw bytes → `{path, size}` |
| `GET /v1/files/{path}` | → raw bytes. `GET /v1/files` lists the work dir: `{files: [{path, size}]}` |

Every path in a request obeys one sandbox rule (`tiad.CleanPath`):

- relative, with forward slashes;
- no `..`, no drive letter or other `:`;
- no control characters or `<>"|?*`;
- no segment ending in a dot or a space;
- no Windows device name (`CON`, `NUL`, `COM1`, …, with or without an
  extension).

The client checks each path and the agent checks again.
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
| `POST /v1/sessions/{id}/upload` | → `{}`. CPU into the session's project, for bringing a legacy CPU's program into nautilus *(verify Openness supports it)* |
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

Response: `{dryRun, downloaded, questions: [{id, message, detail, objects}], state}`.

**The dry run is provisional.** Openness is believed to answer TIA's
pre-download checks through a callback inside the real download call,
not in a separate pass *(verify in S2)*. If so, tiad's dry run is a
download whose callback records every check and then cancels it, and S2
must show that a cancelled download loads nothing. Until S2, the dry run
and the question IDs are the contract tiad aims for, not a measured
behaviour.

- **A dry run** (provisional, see above) runs TIA's pre-download checks
  and returns them as `questions`. The `id` is one of:
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
  (one station spec per CPU). In v1 this rules out a first download to a
  CPU that still sits at another address, and changing a CPU's address
  through a download. On the bench the address is already set.
- **The project must be fully compiled** (hardware and software) since its
  last change.
- **An answer of no** to `stop-cpu`, `overwrite` or `certificate` fails
  the call with `declined`, and nothing is loaded. `start-after: false` is
  a decision, not a refusal: load and stay in STOP.
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
*(verify)*. D1: we run on the trial licence, and the TIA VM is recreated
from the golden image for testing, so every clone starts with its own
fresh trial. The golden image never starts TIA, so no trial time is baked
into it. On a clone, the trial starts at the first call that starts TIA
(§3.4). Whether that start needs one GUI acknowledgement is an S2b
question. If a clone's trial ends, `probe` fails the `licence` gate and
every call that needs TIA fails with `gate`.

**Redone on every clone.** Nothing survives one, by design:

- the tiad install and its token. The token file is new, so clients
  re-read it.
- the Openness allowlist entry;
- GSDML installs (`gsd/install`);
- the work dir and every project in it (rebuilt from the repo per deploy);
- the TIA licence state (a fresh trial).

**Kept outside the clone:** everything in git, meaning sources, the
station spec and an adopted project's archive.

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
| `siemens/station` | The manifest form of a station spec and every station rule, stdlib only, so `internal/project` holds it without importing the agent client. `naut check` reports its problems; loading never fails on them |
| `siemens/tiad` | Go client for tiad: the station spec and its validation, the download rule (`Resolve`), the sandbox rule (`CleanPath`). `tiadtest` is an in-memory fake agent that enforces §3.2–§3.5 and is the executable half of this contract |
| `siemens/webapi` | Go Web API client |
| `siemens/writer` | nautilus → SCL, tag tables, station spec |
| `siemens/facade` | `naut siemens serve` |
| `tools/tiad` | The C# agent and its installer |
| `cmd/naut/siemens*.go` | `naut siemens agent probe`, `deploy`, `serve` |

`siemens/*` follows the driver rule on the map, enforced by
`internal/contextmap`: `siemens/tiad`, `siemens/webapi`, `siemens/writer`
and `siemens/importer` never import `runtime`, `server`, `alarm`,
`acceptance`, `internal/project`, `internal/lsp` or the CLI. The exception
is `siemens/facade`, which is an application layer, as `logix/facade` is.
The CLI hands the writer what it needs from the manifest.

## 7. Order

1. **S1, read-only (done 2026-11-17).**
   - The S7 identity read confirmed the CPU and FW 3.1.3.
   - The web server is off (80/443 closed), and OPC UA and SNMP don't answer.
   - The Web API checks therefore move to S2b.
2. **S2a, the first TIA session, read-only towards the CPU.**
   - probe;
   - upload from the CPU;
   - export everything (SCL, SimaticML, station) twice and diff the two
     exports (determinism);
   - learn the rack modules and the protection level.
3. **S2b, the regenerate loop on the bench** (naut-lead told first).
   - The station spec is the uploaded modules plus the web server and a
     user, with a minimal SCL program.
   - compile → dry run → download → compare.
   - Then the Web API checks run (`Api.Version`, `Api.GetMethods`).
   - Also in S2b: does the trial start headless on a clone's first TIA
     start, and can a download's checks be listed and cancelled with
     nothing loaded (§3.5)?
4. **S3, the adoption spike (§2a).**
   - Import the SCL blocks of a legacy-style project and edit one.
   - Download only that block, in RUN.
   - Show with the online compare that everything else is untouched.
   - Watch live values in the extension.
   - Count blocks by language.
5. **Then:**
   - writer breadth (SFC) and serve polish;
   - the override force layer (D4);
   - the LAD reader/writer sized by S3.

**Code that doesn't need the image,** in order:
1. the Web API client;
2. `target: siemens`;
3. the writer's first subset;
4. the SCL importer;
5. the tiad C# source;
6. the serve facade.

The SimaticML reader waits for real S2a exports to use as fixtures.

## 8. Decisions (2026-11-17)

| | Decision |
|---|---|
| D1 | The trial licence. The TIA VM is recreated from the golden image, so tiad is stateless across clones (§3.1) |
| D2 | No SIMATIC AX |
| D3 | SCL only in v1; nautilus shows the ladder (§1, §2a) |
| D4 | Write-only first, then the opt-in override layer for I/O-bound tags. Forcing is required, so it is on the roadmap. The CPU's FORCE LED stays dark under it, and the docs must say so |
| D5 | Adoption early: S3 follows the first bench download (§2a, §7) |
| D6 | The read-only S1 probe is approved. Anything that is not read-only is announced to naut-lead first |
