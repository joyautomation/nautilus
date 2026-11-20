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
  - writes the Openness allowlist entry for tiad's executable after every
    build: the Openness allowlist entry TIA requires (path, timestamp and
    SHA-256 hash of the exe) under `HKLM\Software\Siemens\Automation\Openness\AllowList`.
    That no consent dialog then appears is verified at the first TIA start.
- **Agent-side config, never in requests:**

  | Variable | Default | Purpose |
  |---|---|---|
  | `TIAD_ADDR` | `http://127.0.0.1:8190` | Listen address |
  | `TIAD_TOKEN` | — | Bearer token, or `TIAD_TOKEN_FILE` naming a file that holds it. Required: the agent refuses to start without one, on loopback too, since any process or browser on the machine reaches loopback |
  | `TIAD_WORKDIR` | `C:\tiad-work` | Every path in a request is relative to this |
  | `TIAD_TARGETS` | — | The only CPU addresses tiad will go online to or download to |
  | `TIAD_PGPC_INTERFACE` | — | The NIC TIA uses to reach the CPUs |
  | `TIAD_SESSION_IDLE` | `1h` | The idle reaper closes a session unused this long (a Go duration). The clock starts when a call ends; a session with a call in flight is never closed |
  | `TIAD_MAX_FILE_BYTES` | 1 GiB | The largest file a put takes (413 beyond it) |
  | `TIAD_NO_TIA` | — | `1` fails the `tia` gate, so no call can start TIA. tiad is installed and tested this way before a clone's trial may start |

- **No browser ever calls tiad.** A request that carries an `Origin`
  header is refused (403), whatever token it has. A JSON body must be
  `Content-Type: application/json` and a file `application/octet-stream`
  (415 otherwise), because a browser sends `text/plain` across origins
  without asking first.
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
| `bad_request` | 400 | Malformed body, or a path outside the work dir. 415: the wrong `Content-Type`. 413: a body over the cap |
| `unauthorized` | 401 | Missing or wrong token. 403: the request carries an `Origin` header (a browser) |
| `unreachable` | — | Client-side: no reply (agent down, or the context was cancelled; the cause unwraps). After a download, the outcome is **unknown**, so compare before retrying |
| `not_found` | 404 | No such session or file |
| `busy` | 409 | The project has a writer, the session has a call in flight, or the CPU belongs to another session's project |
| `gate` | 412 | A probe gate failed (TIA, group, licence). The message names it |
| `target_refused` | 403 | The CPU address is not in `TIAD_TARGETS` |
| `decision_required` | 409 | A download question has no answer in the request. `data.questions` lists them |
| `declined` | 409 | A download question was answered no. `data.questions` lists them, and nothing is loaded |
| `compile_failed` | 422 | Compile errors. `data` is the compile result |
| `not_compiled` | 409 | A download of a project not fully compiled since its last change. Nothing reached TIA |
| `operation_failed` | 500 | Openness threw (the message carries its text), or a device pin file is unreadable (§3.8) |
| `device_changed` | 409 | The CPU's identity differs from its device pin (§3.8). The message names both; nothing was loaded and the pin stands |
| `device_unverified` | 409 | The CPU has a device pin, but its identity could not be read, so the pin could not be checked (§3.8) |
| `cpu_mismatch` | 409 | An apply's spec does not name the project's CPU (name, order, firmware), or a device of the spec's shares a name or an address with one the project keeps (§3.3). `data.cpu` is the project's CPU; nothing was changed |
| `unclean_session` | 409 | An apply or an upload failed part-way on this session, so its project matches no spec: a later apply or save refuses. Close the session without saving (§3.3) |
| `outcome_unknown` | 500 | A download may have loaded the CPU, or not: TIA failed once the download was decided, or TIA's side broke the decide contract. Not fatal: compare on the same session before anything else. The client's `OutcomeUnknown` covers this and `unreachable` |

### 3.3 The station spec

This is the JSON form of `target.siemens` minus the agent fields.
Passwords are never in the spec or the repo: the caller sends them from its
own environment in `secrets` (§3.4).

```json
{
  "cpu": {
    "name": "PLC_1", "order": "6ES7 510-1DK03-0AB0", "firmware": "V3.1",
    "address": "192.0.2.10", "subnetMask": "255.255.255.0", "router": "",
    "profinetName": "plc_1", "configurationProtection": "password"
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
- **`cpu.configurationProtection` is required on the wire**: the FW 3.1
  protection of the CPU's confidential configuration data (#789, decision
  of 2026-11-19). A FW 3.1 CPU does not compile until it is set up.
  Setting it up is not enough on its own: a new FW 3.1 CPU also needs a
  user with the runtime right Full access ("Users and roles"), which the
  access-control change after this one adds. Until then a new station
  still fails its hardware compile in TIA, and the fakes, which compile
  it, are ahead of TIA.
  - `password`: protected, with `secrets.configurationPassword`. A CPU
    already protected is left as it is: TIA cannot check a password
    offline, and a download asks for it. The apply's `notes` say so,
    because a changed secret does not reach the project: to change the
    password, apply `none` with the old one, then `password` with the new.
  - `none`: off, an explicit choice. Turning off a protected CPU needs its
    password. A CPU never set up is turned off by `Protect` with a
    throwaway password (16 random bytes, never logged or kept) and
    `Unprotect` with it, two proven steps. If anything fails after that
    `Protect`, tiad removes the CPU from the project, so no CPU is left
    protected by a password nobody holds, and the error says to close the
    session without saving (the apply failed part-way, see "Which CPU an
    apply uses").
  - `keep`: tiad changes nothing, for an adopted station. A CPU whose
    protection is not set up (TIA's default for a new CPU) refuses `keep`,
    because it would not compile.
  - The decision comes from the state the project holds after the
    station step, not from the spec applied before. After every write
    tiad reads the state back and refuses unless it is the promised one.
    A CPU without the setting (firmware before 3.1) takes only `keep`.
  - After an upload, a spec that names the uploaded CPU keeps it and its
    setting, so `keep` keeps what the CPU was found with.
  - In `nautilus.yaml` the field is `configuration-protection`. Left out,
    it is `password`: a new station is protected by default.
  - Proven offline on the TIA machine (2026-11-20, through the agent): a
    new CPU reads `WithoutPassword`; `Protect` moves it, or a CPU at
    `None`, to `WithPassword`; `Unprotect` with the password moves it to
    `None`; `none` on a new CPU ends at `None`. The read-back still
    refuses any state TIA did not promise.
    `Unprotect` with a wrong password is refused by TIA ("the provided
    PLC Master secret is not matching", proven 2026-11-20), and the
    protection stays on.
- **Which CPU an apply uses** (#789). The agent reads the project's CPU
  before it writes anything, and decides:
  - The project holds no CPU: the apply removes every device and makes
    the CPU from the spec.
  - The CPU device item's name, its order number and its firmware are the
    spec's: the apply keeps that CPU, with its program and its
    configuration protection, and sets the spec's CPU settings on it.
    Firmware compares exactly, a leading `V` aside: TIA stores the version
    the station was configured with, so `V3.1` is not `V3.1.3` (unlike the
    device pin, §3.8, there is no major.minor widening).
  - Anything else refuses with `cpu_mismatch`, naming what differs, with
    the project's CPU in `data.cpu`. tiad never replaces a project's CPU
    on an apply: to keep the CPU, apply a spec naming it (an upload
    returns the same `cpu`); for a new CPU, create the project again. This
    is a change from before #789's reuse, when an apply with another CPU
    deleted the station and made it anew without saying so.
  - The devices besides the CPU's station (an adopted station's PROFINET
    IO device, say) are kept as found: tiad does not configure IO devices
    yet. The apply lists them in `keptDevices` and in a note; a download
    lists them again in its result, and in the data of a
    `decision_required` or `declined` refusal, before anything is loaded,
    and journals them on its audit line (`kept=[...]`). A kept device that
    shares a name, a PROFINET name or an address with the spec refuses
    (`cpu_mismatch`) rather than coexist.
  - A refusal before any write (a mismatch, `keep` on a CPU that has
    nothing to keep, a missing password) leaves the session as it was.
  - An apply or an upload that fails part-way leaves the session unclean:
    its project matches no spec, so a later apply or save refuses with
    `unclean_session`, and the session can only be closed without saving.
    A CPU left half-applied is never reused, even by saving and reopening
    the project. A successful upload replaces the project and cleans the
    session.
  - Proven offline on the TIA machine (2026-11-20, a copy of the S2a
    upload, through the agent): a wrong name and a `V3.1.3` firmware
    refuse with the project's CPU in `data.cpu`; the CPU named as the
    project holds it is taken over, its IO device kept and listed, and
    the hardware compiles with 0 errors; a created project's second apply
    of the same spec changes nothing.
- **A password that may be set meets TIA's rules before any TIA call**
  (`password`): 10 to 120 characters (UTF-16 code units), at least one
  digit, one upper-case and one lower-case letter (ASCII classes, which
  can only be stricter than TIA). The refusal names every rule broken and
  never the password. A password that only unlocks (`none`, a download)
  is never checked: it was set under whatever rules TIA had then.
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
headless TIA process if it is not already running.

Each gate is `ok`, `failed` or `unknown`. A failed gate, or a state the
agent or client does not know, stops every TIA call with `gate`. An
unknown gate is reported honestly but blocks nothing. The `licence` gate
is `unknown` ("not readable until TIA's first start"): no headless licence
query was found, and the trial is created at the first start. The TIA side
latches it `failed` when a TIA start reports a licence error and `ok` once
TIA starts licensed. S2a compares the licence manager's and ProgramData's
state across the first start, to find a read that works before it.

| Call | Body → data |
|---|---|
| `GET /v1/health` | → `{version, tia, openness, sessions, targets}` |
| `GET /v1/probe` | → `{usable, gates: [{name, ok, state, detail, remedy}]}`. Gates: `tia`, `openness-group`, `openness-allowlist`, `licence`, `workdir`. `usable`: no gate blocks. Always 200 |
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
| `PUT /v1/sessions/{id}/station` | `{station, secrets}` → `{changes: [string], notes?: [string], keptDevices?: [{name, profinetName, address}]}` (P2, P5). Keeps the project's CPU or makes one (§3.3, "Which CPU an apply uses"). `notes` is what the caller should know besides the changes (a protected CPU kept its password, devices kept as found) |
| `POST /v1/sessions/{id}/software` | `{group, sources, tagTables, prune}` → `{results: [{path, ok, messages}], blocks: [{name, kind}]}`. Generates blocks into the group named `group`. With `prune`, blocks in that group that no source produced are deleted |
| `POST /v1/sessions/{id}/compile` | `{hardware, software}` → `{result: "success" or "warning" or "error", errors, warnings, messages: [{object, severity, text}]}` |
| `POST /v1/sessions/{id}/download` | §3.5 (P3) |
| `POST /v1/sessions/{id}/compare` | → `{equal, differences: [{object, state}]}`. An online compare |
| `POST /v1/sessions/{id}/export` | `{dir, blocks: "scl" or "xml" or "", tagTables, hardware, gsdml}` → `{files: [path]}` (P6 for `gsdml`) |
| `POST /v1/sessions/{id}/upload` | `{trustUnverifiedCertificate?}` → `{trustedBlindly, device, pin, cpu: {name, order, firmware}}`. CPU into the session's project, for bringing a legacy CPU's program into nautilus (S2a: works). Pins the CPU's identity on first use, or checks it (§3.8). `cpu` is what a spec must name to keep the uploaded CPU |
| `POST /v1/sessions/{id}/save` | `{archive?}` → `{path}`. Refused (`unclean_session`) after an apply or upload failed part-way |
| `GET /v1/pins`, `POST /v1/pins/{address}` | The device pins (§3.8). An upload pins the CPU on first use and must match after; compare and download check the pin first |

`secrets` is `{"webUsers": {"naut": "<password>"}, "protection": "<password>", "configurationPassword": "<password>"}`.
Each password answers only its own question: the protection password
never answers `configuration-password`, and the other way round.
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
  "secrets": { "protection": "<password>", "configurationPassword": "<password>" }
}
```

Response: `{dryRun, downloaded, questions: [{id, message, detail, objects}], state}`.

**The dry run is provisional.** Openness is believed to answer TIA's
pre-download checks through a callback inside the real download call,
not in a separate pass *(verify in S2)*. If so, tiad's dry run is a
download whose callback records every check and then cancels it, and S2
must show that a cancelled download loads nothing. Until S2, the dry run
and the question IDs are the contract tiad aims for, not a measured
behaviour. So, until then:

- **A dry run must be announced too** (`announced: true`): it reaches
  the CPU.
- **Its response says `provisional: true`.**
- **TIA's side calls decide exactly once, before anything loads, even
  when TIA asks nothing**, so a dry run without questions still cancels.
  decide returns one resolved answer per question (or cancels), and TIA's
  side applies those answers as given; it never derives one itself.
- **Anything that leaves the CPU in doubt answers `outcome_unknown`**:
  TIA failing once the download was decided (a cancel included, since
  "a cancel loads nothing" is what S2 must prove), a load tiad did not
  approve, decide called zero times or twice. Never success, never a
  plain refusal. The session stays open for the compare.
- **How `tiad.Openness` keeps "decide once".** Openness calls its
  pre-download callback once per configuration. At the first callback
  `tiad.Openness` lists every pending configuration from that
  configuration's parent, calls decide once with all of them as questions
  (plus `start-after` when none asks it, so starting the CPU is decided
  before loading and applied in the after-download callback), then writes
  each configuration's resolved answer in its own callback, `NoAction`
  included. A configuration counts as answered only once its write
  succeeded; a list it cannot read, a configuration missing from it or
  asked twice, or a write that fails cancels the download, and the cancel
  stands even if TIA swallows the exception. After loading, start-after
  is always written (`NoAction` unless decided), never left to TIA's
  default. Before TIA starts, every selection tiad may write is checked
  against this Openness's own enum types. Once TIA's download call is
  entered, any failure answers `outcome_unknown`. The
  `certificate` question's fingerprint is not read yet, so a trust decision
  cannot match it: a TLS check refuses until S2b. All of this is pure code
  in `tools/tiad/core/TiaRules.cs`, unit-tested without TIA.
- **The gap: a download TIA asks nothing about.** With no pre-download
  callback, `tiad.Openness` has nothing to cancel from, so "decide even
  when TIA asks nothing" cannot hold: such a download loads, and the agent
  answers `outcome_unknown` (decide ran zero times). So **the agent
  refuses every dry run** (400, before TIA hears of it, no CPU claimed)
  while its TIA side says it cannot promise the cancel (`ITia.CanDryRun`):
  `tiad.Openness` says so until S2b shows TIA always asks at least one
  check. The fakes still model the dry run the contract aims for.
- **Start-after is always asked.** A real download needs a `startAfter`
  decision, or it answers `decision_required`, even when TIA would not stop
  the CPU. Both fakes ask it the same way.
- **Every download is audited**: one log line with the address, the
  question IDs asked (never TIA's text), the decisions (request-supplied
  names quoted), whether a protection password and a configuration
  password were set (never their values) and the outcome, refusals
  included.

- **A dry run** (provisional, see above) runs TIA's pre-download checks
  and returns them as `questions`. The `id` is one of:
  - `stop-cpu`
  - `reinitialize` (`objects` lists the DBs)
  - `overwrite`
  - `start-after`
  - `password`
  - `configuration-password` (TIA's `PlcMasterSecretPassword`: the
    password protecting the configuration data, answered only by
    `secrets.configurationPassword`; unproven until S2b)
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
  last change (`not_compiled` otherwise). A change that fails part-way
  counts as a change.
- **One project per CPU.** The first download from a session, a dry run
  included, claims the CPU for that session until it is closed (a broken
  session keeps the claim). Another session's download to that CPU is
  `busy`.
- **An upload replaces the project with the CPU's program**: the
  project no longer matches its spec, so a download needs a spec applied
  and a compile again. Compare and export still know the CPU.
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
into it. **S2a found that a headless Openness start does not activate the
trial:** TIA starts, creates a project, and then refuses the first call
that needs STEP 7 ("Necessary license 'STEP 7 Professional' is missing"),
and the first start leaves no licence state behind. So a licence has to be
in place before tiad is useful: the trial, activated once through TIA's
own UI, or a real one. Until TIA's first start the `licence` gate is
`unknown`; it latches `failed` at the first licence refusal, from the
start or any later call, and then every call that needs TIA fails with
`gate` until the agent restarts.

**Redone on every clone.** Nothing survives one, by design:

- the tiad install and its token. The token file is new, so clients
  re-read it.
- the Openness allowlist entry;
- GSDML installs (`gsd/install`);
- the work dir and every project in it (rebuilt from the repo per deploy);
- the TIA licence state (a fresh trial);
- the device pins (§3.8). A fresh clone trusts the CPU again on its first
  upload, unless the pin is set by hand (`POST /v1/pins/{address}`) before
  its first contact.

**Kept outside the clone:** everything in git, meaning sources, the
station spec and an adopted project's archive.

### 3.8 Pins: trust on first use, then pin

Plan and decisions: #789 (the plan, approved 2026-11-19; the device-pin
fields, decided 2026-11-20). The bench CPU uses secure PG/PC communication,
and TIA shows no certificate to check (S2a Gate B), so the first contact
is trusted as found and what was found is pinned.

**The device pin (built).** The first upload from a CPU pins its identity
per address: its **order number and firmware**, read from the station the
upload brought into the project. The response says `pin: "first-trust"`,
and the audit line names the fields the pin holds. From then on:

- every upload must report the same identity (`pin: "matched"`);
- every compare and download, a dry run included, first reads the CPU's
  identity online and must match. This happens before the CPU is claimed
  and before TIA's download begins;
- a different identity fails with `device_changed`, naming the pinned and
  the new values. The pin stands;
- an identity that cannot be read fails with `device_unverified`.
  `tiad.Openness` has no proven online read yet (a read-only probe is
  planned on #789). **So today, once a CPU is pinned, its compare and
  download are refused** until an online read is proven: that is the
  fail-closed design (decided on #789, 2026-11-20). A re-pin records a new
  identity but does not lift this, and no call removes a pin. On the bench
  CPU nothing is lost, since its TLS question refuses them already. A CPU
  that asks no TLS question could be compared before its first upload and
  cannot after it.

**What it catches, and what it does not.** The device pin catches a
different model, or firmware of another major.minor version, at the
address. The firmware it holds is the station's configured version
(`V3.1`), not the CPU's full one: the bench CPU runs 3.1.3, and a patch
update (3.1.2 to 3.1.3) is not in the project, so it is **not** caught. A
future online read returns the full version; the plan is to compare the
firmware at the pin's precision (a `V3.1` pin matches any 3.1.x), with a
three-part value recorded only through an audited re-pin and then compared
exactly. That is a rule change in both copies, made with the read. It
does **not** catch an identical CPU swapped in, and it does not catch a machine in the middle
on the first contact. The serial number and the MAC address would catch a
like-for-like swap, but the uploaded project holds neither. So they are
fields of the pin now, held as **not read** (`null`, never `""`, which
could match), and filled only by an audited re-pin once an online read of
them is proven. A field the pin holds must be read again on every check:
one that was not read fails closed. Nothing catches a like-for-like swap
yet. The certificate pin below is meant to, once S2b proves it.

**The certificate pin (after S2b, not built).** After nautilus's first
download, the project owns the CPU's communication certificate. tiad will
pin its SHA-256 per address, require TIA to ask no TLS question for a
pinned address, and refuse the blind upload opt-in there.

**Calls (no TIA):**

| Call | Body → data |
|---|---|
| `GET /v1/pins` | → `{pins: [{address, device: {order, firmware, serial, mac}, holds}]}`. `holds` names the fields the pin actually holds; an unread field is `null` |
| `POST /v1/pins/{address}` | `{device, replace, announced}` → `{address, device, holds, previous?}`. A first pin before any contact (`replace: false`), or a re-pin after a deliberate replacement or firmware update (`replace: true`, which needs an existing pin) |

The address must be a strict dotted quad in `TIAD_TARGETS`, and the call
must be announced. Every pin set or replaced, every first trust, and every
online call a pin refuses (changed, unverified or unreadable) is an audit
line with the values involved; so is the pin inventory at each start, so
a pin that vanished between runs shows. A malformed pin request (400) is
not. A pin is never deleted or changed silently.

**Storage.** One file per address in `TIAD_PINS_DIR`
(`%ProgramData%\tiad\pins`): `<address>.device`, whose first line is the
identity as JSON, followed by comment lines saying what it is, when, and
how to re-pin. `install-tiad.ps1` locks the directory like the token
(Administrators and SYSTEM only) and never wipes it, so a reinstall keeps
every pin. The agent refuses to start with the directory inside the work
dir, where the files API would reach it, and when it cannot create, list
and write it. Only a pin file that is not there means "no pin": any other
failure to read one, a missing or unreadable directory included, fails
closed, and online calls to that address are refused.
- A pin whose **content** is corrupt can be replaced through the audited
  re-pin (`replace: true`). Its audit line keeps why it was corrupt and
  its first line, bounded.
- A pin that **cannot be read** (an I/O or permission error) is never
  replaced: its identity is unknown, not wrong. Fix the access first.

Editing or deleting a pin file by hand is unaudited and not a supported
path.

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
2. **S2a, the first TIA session, read-only towards the CPU.** Two steps,
   each OK'd first.
   - **A, TIA starts, no CPU contact:** reinstall without `-NoTia`; diff
     the licence manager's and ProgramData's state across the first start;
     show the allowlist entry spares the consent prompt; Openness thread
     affinity; create a project with the bench CPU's spec offline (the
     attribute names are checked and logged); compile; export twice and
     diff; list the PC interfaces; observe one GSDML install through TIA's
     own UI, to find the route Openness lacks.
   - **B, read-only towards the CPU:** probe; upload from the CPU into a
     fresh project; export everything (SCL, SimaticML, station) twice and
     diff the two exports (determinism); learn the rack modules and the
     protection level.
3. **S2b, the regenerate loop on the bench** (naut-lead told first).
   - The station spec is the uploaded modules plus the web server and a
     user, with a minimal SCL program.
   - compile → dry run → download → compare.
   - Then the Web API checks run (`Api.Version`, `Api.GetMethods`).
   - First, before any dry run: does TIA always make at least one
     pre-download callback, does a configuration's parent list them all,
     and does a cancel load nothing (§3.5)?
   - Also in S2b: does the trial start headless on a clone's first TIA
     start?
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
