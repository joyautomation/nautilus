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
| Program | FB `"naut_Main"` with an instance DB `"naut_Main_DB"`, called from its task's OB `"naut_OB_main"`. Every generated block name carries the block prefix (below) |
| The task (`scan:`) | Cyclic interrupt OB30 at that cycle, 1 ms to 60 s. **One task in v1**: nautilus copies shared tags in at a task's scan start and commits them at its end, while cyclic interrupt OBs preempt each other and touch the tags DB in place. Copy-in/copy-out for several tasks is its own later item. nautilus has no free-running task, so there is no OB1 |
| Manifest tags | Global DB `"naut_Tags"` (optimized), the Web API symbol `"naut_Tags".Level`. Members are visible and writable to Web API/HMI/OPC UA *(verify the attribute spelling in SCL source)* |
| I/O-bound tags | PLC tag table, `%I`/`%Q` addresses from the station spec |
| UDT | PLC data type (`.udt`) |
| User FB / function | FB / FC |
| TON TOF TP R_TRIG F_TRIG | The IEC instructions as multi-instances (`TON_TIME`, …); the members already match (`.Q` `.ET`) |
| CTU CTD CTUD | `CTU_LINT` etc.: nautilus's counter value is its 64-bit int and keeps counting past 32767 |
| SR RS | Written inline as set/reset logic on a static, since they are believed to be LAD/FBD boxes that SCL cannot call *(verify)* |
| Elementary types, arrays, structs | Direct. DT becomes one of DT, LDT or DTL, chosen and documented *(verify each on FW 3.1)* |
| `VAR RETAIN` | Retentive members *(verify the SCL source syntax for per-member retain)* |
| `dialect: siemens` (reserved) | Siemens-semantics blocks, only where no IEC form exists |

**The block prefix (#789).** `target.siemens.block-prefix` (default
`naut_`) is put in front of every block, DB and tag table the writer
generates: the program FB, its instance DB, the tags DB and the OBs, and
every reference to them inside the SCL (`"naut_Tags".x`, the
`"naut_Main_DB"()` call). Without it, `PROGRAM Main` became FB `"Main"`,
which collides with the `"Main"` [OB1] TIA gives every CPU and with a
customer's blocks on adoption (§2a). The Web API symbols follow
(`"naut_Main_DB".count`), and `naut siemens serve` browses the prefixed
tags DB by default. The prefix must be an SCL identifier start (letters,
digits and `_`, not beginning with a digit). An empty prefix is refused by
`naut check` for now; leaving it out gives the default.

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

**Built (check):** `naut check --target siemens` runs the writer itself
on the project, so check and the deploy share one rule table by
construction. A project with a `target: siemens` section gets it without
the flag. The CLI reads the manifest into the writer's input in one
place (`cmd/naut/siemens_input.go`), which the deploy will reuse: task
names and the default scan as the runtime gives them, tag types by the
runtime's rule (`type:`, else what `init:` implies), and retain where the
runtime keeps a tag (with a `retain:` section, every setpoint and every
`retain: true` tag; without one, none). A diagnostic with no source line
(a task, a block name) is reported against the manifest. Not yet: the
refusal of field drivers (§1's table).

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
  | `TIAD_RETIRE_WATCH` | `3m` | How long a retire of a TIA instance that trusted blindly may take before tiad counts TIA as gone (§3.8; a Go duration, at most `1h`). TIA's dispose has no bound of its own |
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

A session is an open project. The agent holds one at a time. TIA V21
opens one project per instance (#616), and refuses a second only after a
create has replaced its directory. So tiad refuses up front, with `busy`,
and nothing is touched. It refuses:
- a create or an open while a session is open. The refusal names it
  (`data.openSession`, `data.openProject`). If that session is still
  opening, try again once it is open. If its close failed, close it again,
  or restart the agent;
- a create or an open while a closed session's project is still closing
  in TIA. This refusal names no session.

Close the open session first. A create whose cleanup close fails (after
TIA refused the station) keeps its session, broken, because TIA may still
hold the project. A failed create or open retires a TIA instance that
trusted blindly once no session is left (§3.8). Opens are serialized, and an idle reaper
closes forgotten sessions. `fatal: true` means the session is gone.

| `error.kind` | HTTP | Meaning |
|---|---|---|
| `bad_request` | 400 | Malformed body, or a path outside the work dir. 415: the wrong `Content-Type`. 413: a body over the cap |
| `unauthorized` | 401 | Missing or wrong token. 403: the request carries an `Origin` header (a browser) |
| `unreachable` | — | Client-side: no reply (agent down, or the context was cancelled; the cause unwraps). After a download, the outcome is **unknown**, so compare before retrying |
| `not_found` | 404 | No such session or file |
| `busy` | 409 | Another session is open or still closing (one project at a time: `data.openSession` names it), the session has a call in flight, or the CPU belongs to another session's project |
| `gate` | 412 | A probe gate failed (TIA, group, licence). The message names it. Once TIA Portal has died the `tia` gate fails for good: restart the agent |
| `target_refused` | 403 | The CPU address is not in `TIAD_TARGETS` |
| `decision_required` | 409 | A download question has no answer in the request. `data.questions` lists them |
| `declined` | 409 | A download question was answered no. `data.questions` lists them, and nothing is loaded |
| `compile_failed` | 422 | Compile errors. `data` is the compile result |
| `not_compiled` | 409 | A download of a project not fully compiled since its last change. Nothing reached TIA |
| `operation_failed` | 500 | Openness threw (the message carries its text), or a device pin file is unreadable (§3.8) |
| `device_changed` | 409 | The CPU's identity differs from its device pin (§3.8). The message names both; nothing was loaded and the pin stands |
| `device_unverified` | 409 | The CPU has a device pin, but its identity could not be read, so the pin could not be checked (§3.8) |
| `cpu_mismatch` | 409 | An apply's spec does not name the project's CPU (name, order, firmware), or a device of the spec's shares a name or an address with one the project keeps (§3.3). `data.cpu` is the project's CPU; nothing was changed. A deploy verify whose archived project's CPU is not at the record's address (§3.5, "Verifying a deploy"): `data.recordAddress`, `data.projectAddress`; nothing went online |
| `unclean_session` | 409 | An apply or an upload failed part-way on this session, so its project matches no spec: a later apply or save refuses. Close the session without saving (§3.3). Also every save, deploy archive (so every download) and import after a software import changed blocks outside its group (§3.4) |
| `foreign_block` | 409 | A software import's sources declare a program block that exists outside the import's group, which TIA would replace (§3.4). The message names each block, its number and its group. Nothing was imported |
| `outcome_unknown` | 500 | A download may have loaded the CPU, or not: TIA failed once it asked a check, tiad cancelled after an answer that can change the CPU (`data.actingAnswers` names them), the compare right after a load failed or differs, or TIA's side broke its contract. Not fatal: compare on the same session before anything else. The exception: when TIA Portal itself died in the download, it is fatal and names the fix (restart the agent, then compare in a fresh session), since every project open on that TIA is gone. The client's `OutcomeUnknown` covers this and `unreachable` |

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
  module-difference test removes a module. The exception, until tiad
  configures IO devices: an apply that keeps the project's CPU keeps the
  devices besides its station as found, and lists them ("Which CPU an
  apply uses" below).
- **Every device's GSDML must already be installed** (`gsd/install`).
- **Every web user needs a password** in `secrets.webUsers` when the server
  is on. It meets TIA's password rules and then the project's own policy
  for local users, and it is its own secret: never the protection,
  configuration or Full access password, never another web user's. Its
  rights are any of `read`, `write`, `mode` (change RUN/STOP) and
  `diagnostics`. A name is 1 to 32 letters, digits, `_`, `-` and `.`,
  starting with a letter; `nautilus` and `Anonymous` are reserved in any
  case, and names are compared ignoring case. `webServer.users` with
  `enabled: false` is refused: a server that is off has no users.
- **The web server comes on only over access control on** (#789 stage 5
  (A) step 2). With access control off, a FW 3.1 CPU's web server knows
  one user, "Anonymous", with full rights over the CPU (S7-1500 Web server
  manual, 11/2023): anyone who reaches it could write tags and stop the
  CPU.
  - `webServer.enabled: true` sets the CPU's `WebserverActivate`, then
    `WebserverHttpsOnly` (every such CPU has a user with a password, so
    never plain HTTP), then `UseWebServerForInterface` on the PROFINET
    interface that carries the CPU's address (a device item attribute, not
    the node's), writing only what differs, and reads all three back.
  - It needs the CPU's access control to read exactly
    `EnabledWithAccessControl`. `Disabled`, a CPU without the setting and
    any other state refuse, naming the fix. One rule, checked twice:
    before any write for a kept CPU (beside the `cpu.accessControl`
    check), and in the station step after access control is applied,
    before the server is switched on, for any CPU.
  - `enabled: false` turns both off. It is never refused.
  - `webServer` left out keeps a kept CPU's web server as found and turns
    a new CPU's off. An adopted CPU with access control off is applied
    without `webServer`. As found is never silent: a kept CPU whose server
    is on over access control off gets a WARNING note (and log line)
    naming `enabled: false` as the fix, since a download would load it.
  - **Web users** (step 2B). Each is a local user of the project with
    its password from `secrets.webUsers` and tiad's password record in its
    comment (as `nautilus`), holding one role, `nautilus web <name>`,
    which tiad makes and marks by its comment. That role holds exactly
    the user's rights on the CPU: `read` = `WebReadTags`, `write` =
    `WebWriteTags`, `mode` = `WebChangeOperatingMode`, `diagnostics` =
    `WebAllowDiagnostics`. Nothing is implied (`write` does not give
    `read`; a user with `write` or `mode` and no `read` gets a note).
    Exact means: missing rights granted, any other right revoked (Full
    access too), the user holds that role alone (system roles too), no
    other user holds it, the user is active; all read back. Every right is
    resolved against what the CPU offers before the first user or role is
    written.
  - Refused before any write: a user or role of that name tiad did not
    make (never taken over, no record-only route), and an active
    anonymous user (Openness `UmacConfigurator.AnonymousUser`; anyone who
    reaches the server would get its roles' rights) while the server is
    to be on. tiad never activates or deactivates the anonymous user.
  - A found web user's password is settled before the station step, as
    the Full access user's: unchanged when the record matches, else the
    record first, then the password, the record put back when TIA refuses
    it. A refusal after an earlier user's password was set leaves that one
    set (its record matches it) and the session uncompiled.
    Web users have no record-only route: under another agent key's record
    (the agent's key lost), an unchanged web password is refused as one
    the user had, so it must change: the refusal says so and names
    `secrets.webUsers[<name>]`. Rotating a web password is cheap and
    fails closed.
  - The limit of "exact": tiad sees a user's roles through Openness's
    `ProjectUser.Roles` and the project's local users. A role reaching a
    web user through a user group, a UMC user or group holding a tiad web
    role, and the role's rights on other devices or its engineering
    rights are not checked; each needs someone to edit a tiad-marked user
    or role in TIA by hand.
  - Stale web users: a role tiad marked for a user no longer in
    `webServer.users` is deleted, and so is that user when it carries a
    tiad record. Only the role's marker finds them, so `nautilus` and
    users or roles tiad did not make are never removed. `enabled: false`
    removes them all; `webServer` left out keeps them on a kept CPU and
    removes them on a new one (whose server goes off). An upload removes
    the web users and roles its session made, as it does `nautilus`.
  - Left out on a kept CPU whose server is on over access control on,
    two more WARNING notes: plain HTTP allowed (web passwords in clear),
    and an active anonymous user. A read that fails is logged and said,
    never failing the apply.
  - The agent logs every runtime right the CPU offers, the project's
    users with their roles and the anonymous user's state, once a run.
  - Proven offline on the TIA machine (2026-11-22, through the agent, no
    CPU contact): a new 1510SP FW 3.1 station with `fullAccessUser`, the
    server on and two web users (`read`; all four rights) compiles with 0
    errors and 0 warnings. Applying it again leaves both passwords
    unchanged; a rotated password is set; a dropped user goes with its
    role; a 32-character name is taken; `enabled: false` removes the last
    one and compiles. A new project has no anonymous user (Openness reads
    `AnonymousUser` as none). A compile after a users-only change
    reported the hardware "up-to-date" and compiled the PKI container.
  - Compiled offline, not proven on a CPU: that these rights let a web
    user log on to the Web API and read, write, change the mode and read
    the diagnostic buffer, and that a download after a users-only change
    carries the new users (the first access-control bench window).
  - Proven offline on the TIA machine (2026-11-22, through the agent, no
    CPU contact): a new 1510SP FW 3.1 station with `fullAccessUser` and
    the server on compiles with 0 errors and 0 warnings, and so do off and
    on again. Only once the server is on does TIA offer
    `WebserverHttpsOnly`, `WebserverCertificate`,
    `WebserverDataAccessViaWebApiOnly` and the other `Webserver*`
    attributes. The CPU's web runtime rights include `WebReadTags`,
    `WebWriteTags`, `WebChangeOperatingMode`, `WebAllowDiagnostics`,
    `WebReadSyslog` and `WebDownloadServiceData` (step 2B maps the spec's
    rights onto them). A new project's users hold only `nautilus`: no
    "Anonymous" appears among the project users. A second run proved
    the final shape: a create without `webServer` writes nothing and
    reads back off; on (with HTTPS only), off and on again each read back
    and compile with 0 errors.
  - Unproven: whether a kept project can give "Anonymous" a role (it is
    not among Openness's project users, but `UmacConfigurator` offers it
    as `AnonymousUser`); that a hardware download of a
    station with the server on serves the Web API (the first
    access-control bench window).
- **`cpu.configurationProtection` is required on the wire**: the FW 3.1
  protection of the CPU's confidential configuration data (#789, decision
  of 2026-11-19). A FW 3.1 CPU does not compile until it is set up.
  Setting it up is not enough on its own: a new FW 3.1 CPU also needs a
  user with the runtime right Full access ("Users and roles"), which
  `cpu.accessControl` gives it (below).
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
- **`cpu.accessControl` is required on the wire**: the CPU's PLC access
  control, users and roles (#789, decision of 2026-11-20). A new FW 3.1
  CPU has access control on (TIA's default, `EnabledWithAccessControl`)
  and does not compile until a user holds the runtime right Full access
  ("Users and roles: At least one user must have the following runtime
  right: Full access").
  - `fullAccessUser`: access control on, with one local user,
    `nautilus`, in one custom role, `nautilus full access`, which holds
    the CPU's runtime right Full access. The user's password is
    `secrets.fullAccessPassword`, its own secret, never the protection or
    configuration password.
    - On a CPU the apply makes, tiad sets access control on (TIA's
      default already is) and reads it back.
    - On a CPU the apply keeps, access control must already be on: tiad
      never turns it on for a CPU it did not make. Anything else
      (`Disabled`, as the S2a upload found the bench CPU, the legacy
      access levels, a CPU without the setting) refuses before any write,
      naming `keep`.
    - The user and the role are found or made, by name. tiad takes over
      neither unasked: a found role must carry tiad's comment, and a
      found user tiad's record (below), or the apply refuses before any
      write, naming `keep`. A found user without a record can be taken
      over only explicitly, with `fullAccessRecordOnly` (journalled); a
      found role never is.
    - **tiad's record.** TIA refuses to set a password the user has had
      (its current one included, as many as the project's policy blocks
      for reuse, 5 by default), and cannot check one offline. So tiad
      keeps a record of the password it set in the user's comment in the
      project, never the password: `tiad password record
      v2:<key id>:<salt>:<HMAC-SHA256>` of the salt, the user and the
      password, under the agent's own random key (`TIAD_ACCESS_KEY_FILE`,
      made by the installer, locked like the token, kept across reinstalls
      and an uninstall; not the token). The salt is random per record, so
      one password gives a different record in every project, and the key
      id tells this agent's records from another key's.
    - **A found user's password, before any write.** The apply settles
      it first, so a refusal leaves the session as it was:
      - the record matches the secret: left ("unchanged" in `notes`);
      - otherwise tiad writes the new record, then sets the password
        ("now secrets.fullAccessPassword"), so a rotated secret reaches a
        reused project; if TIA refuses the password as one the user had,
        tiad puts the old record back and refuses: "choose another
        password". If putting it back fails, the session is unclean, and an
        upload (which replaces devices, not users) refuses too;
      - the record is another agent key's (a new machine, a lost key) and
        TIA refuses the unchanged secret: tiad cannot tell the current
        password from an older one, says so, and names
        `fullAccessRecordOnly`;
      - `fullAccessRecordOnly` (on `PUT .../station`, with
        `fullAccessUser`) records the secret as the user's password without
        setting it, journalled on the agent's log.
      Openness has no typed error for TIA's refusal, so the glue matches
      its one phrase ("The specified password has already been used") and
      nothing wider; any other failure stays TIA's own.
    - The record tells what tiad last set or recorded in this project, not
      what the user has now: a password changed in TIA by hand still reads
      "unchanged". Nor does it say anything about the CPU until a download
      loads the users (below).
    - After the writes tiad reads back that the user holds Full access
      through the role on this CPU, and refuses otherwise.
    - The password meets TIA's rules (below) before any TIA call; it is
      none of the other secrets (protection, configuration, a web user's);
      and it meets the project's own password policy for local users
      before any write of an apply (TIA's default: 10 characters, a
      digit, upper and lower case; a project may ask more; a special
      character is ASCII punctuation only, never looser than TIA). The
      policy is checked even when the password will not be set. On
      `POST /v1/projects` the new project's policy is read once it is
      made, so a refusal there has replaced the old project of that name,
      as any create does. The refusal names every rule and never the
      password.
  - `keep`: tiad changes nothing, for an adopted station. Unlike
    `configurationProtection`, a new CPU takes `keep` too; it then fails
    its hardware compile, which names the missing Full access user.
  - Users and roles belong to the project, not to the CPU: they outlive
    a device delete (proven for a CPU deleted in TIA; for an upload,
    modelled by the fakes, unproven live). An upload removes, before it
    uploads, exactly the user and role that applies on the same session
    made (taking the role off every user first), and reports what it
    removed in `removedAccess`, so a create, upload and apply on one
    session leave an adopted project with no user tiad made for the
    throwaway station. A project saved, reopened and uploaded keeps them,
    as does a user or role the project held before (one an earlier deploy
    made on purpose).
  - A plain local user does not protect the project: it still opens
    without a login (only a project administrator does that, which tiad
    never makes).
  - In `nautilus.yaml` the field is `access-control`: `full-access-user`
    (the default, left out) or `keep`.
  - A download raises TIA's `UserManagementDownload` (keep the CPU's
    users, update them but keep its passwords, or load the project's).
    tiad answers it with keep (`user-management`,
    `userManagementKeepOnline`) only for a session whose specs have only
    ever kept access control: under `fullAccessUser`, or `keep` after
    `fullAccessUser` on the same session, it is refused. On a hardware
    load, where TIA refuses keep, it may answer reset to the project's
    (`userManagementResetToProject`) only in the cases §3.5 lists: to a
    project with access control off, or to the user `nautilus` this
    session set with the request's password; over a CPU whose access
    control is attested off, or one tiad has logged on to as `nautilus`
    in the same download. A new station's first live download (its users
    loaded over a CPU with access control off) goes that way; whether
    TIA takes it is for the first bench window to show. The rule is per session: a project an earlier
    session set to `fullAccessUser`, saved, then reopened and kept, counts
    as found like any adopted project (it still holds tiad's Full access
    user). Its first hardware download with `userManagementKeepOnline`
    is a watched bench step until TIA's handling of that is proven.
  - Proven offline on the TIA machine (2026-11-20, through the agent):
    a new station with `password` and `fullAccessUser` compiles with 0
    errors, applying it again finds the user and changes nothing, and the
    saved project reopens without a login; a new CPU with `keep` fails
    its compile on Full access; the S2a copy (`Disabled`) refuses
    `fullAccessUser` naming `keep`, and `keep` compiles; the user and role
    survive a CPU deleted in TIA, and an apply that makes the CPU anew
    finds them and grants Full access on it (0 errors); an upload's
    removal runs on TIA, the role taken off its users first; with the
    record: the same secret is left, a rotated one is set and is then
    unchanged, one the user had is refused before any write (the same
    session then applies and saves), and a reopened project's record
    still matches. TIA serves the runtime rights from the station,
    and Full access is the right `ProtectionLevelFullAccess`. Unproven:
    whether TIA refuses to make a user again, under a removed user's name,
    with a password that user had (an upload, then an apply; it needs a
    live upload); whether TIA deletes a role a user still holds without
    that (tiad never asks it to).
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
    IO device, say), wherever the project holds them (its own, ungrouped,
    or in a user device group at any depth), are kept as found: tiad does
    not configure IO devices yet. The apply lists them in `keptDevices`
    and in a note. A download lists them again in its result (a dry run's
    too) and in the data of a `decision_required` or `declined` refusal,
    and journals them on its audit line (`kept=[...]`). Loading them is a
    download question of its own (`kept-devices`, §3.5): a real download
    needs `decisions.keptDevices` naming exactly them, and the audit line
    journals that answer too. A kept device that
    shares a name, a PROFINET name or an address with the spec refuses
    (`cpu_mismatch`) rather than coexist.
  - A refusal before any write leaves the session as it was: a mismatch,
    a kept-device collision, a part of the spec this agent cannot
    configure yet, and, when the CPU is reused, `keep` with nothing to
    keep or a missing password. When the apply makes the CPU, the
    protection is decided only once the new CPU exists, so such a
    refusal there comes after the write and leaves the session unclean.
  - A project with more than one CPU refuses: tiad handles one.
  - Each kept device is listed once per network node (every interface),
    so a collision on any of its addresses or PROFINET names is seen.
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
  (`configurationProtection: password`, `accessControl: fullAccessUser`): 10 to 120 characters (UTF-16 code units), at least one
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
| `PUT /v1/sessions/{id}/station` | `{station, secrets}` → `{changes: [string], notes?: [string], keptDevices?: [{name, profinetName, address}]}` (P2, P5). Keeps the project's CPU or makes one (§3.3, "Which CPU an apply uses"). `notes` is what the caller should know besides the changes (a protected CPU kept its password, the Full access user made or found, devices kept as found) |
| `POST /v1/sessions/{id}/software` | `{group, sources, tagTables, prune, obs?: [{name, number, cycleMs}]}` → `{results: [{path, ok, messages}], blocks: [{name, kind}], obs?: [{name, action, was?, now?}]}`. Generates blocks into the group named `group`; `blocks` lists that group's. `obs` are the cyclic interrupt OBs the agent makes first (see "Cyclic interrupt OBs" below). With `prune`, blocks in that group that no source produced are deleted. An import never touches a block outside the group: see "Blocks outside the group" below |
| `POST /v1/sessions/{id}/compile` | `{hardware, software}` → `{result: "success" or "warning" or "error", errors, warnings, messages: [{object, severity, text}]}` |
| `POST /v1/sessions/{id}/download` | §3.5 (P3) |
| `POST /v1/sessions/{id}/compare` | `{secrets?: {fullAccessPassword}}` (§3.9) → `{equal, differences: [{object, state}], blindTrust}`. An online compare. `blindTrust` is the TIA instance's (§3.8), or `null` |
| `POST /v1/sessions/{id}/fingerprint` | `{trustUnverifiedCertificate?, secrets?: {fullAccessPassword}}` → `{address, items: [{identifier, value}], trustedBlindly, blindTrust}`. TIA's fingerprint of the session's CPU, read online and read-only (§3.8) |
| `POST /v1/sessions/{id}/export` | `{dir, blocks: "scl" or "xml" or "", tagTables, hardware, gsdml}` → `{files: [path]}` (P6 for `gsdml`) |
| `POST /v1/sessions/{id}/upload` | `{trustUnverifiedCertificate?, secrets?: {fullAccessPassword}}` → `{trustedBlindly, blindTrust, device, pin, cpu: {name, order, firmware}, removedAccess?: [string]}`. CPU into the session's project, for bringing a legacy CPU's program into nautilus (S2a: works). Pins the CPU's identity on first use, or checks it (§3.8). `cpu` is what a spec must name to keep the uploaded CPU. `removedAccess` names the user and role this session had made, removed first (§3.3, `cpu.accessControl`) |
| `POST /v1/sessions/{id}/save` | `{archive?}` → `{path}`. Refused (`unclean_session`) after an apply or upload failed part-way |
| `GET /v1/pins`, `POST /v1/pins/{address}` | The device pins (§3.8). An upload pins the CPU on first use and must match after; compare and download check the pin first |
| `GET /v1/deploys[?address=]` | → `{deploys: [{id, at, session, project, address, hardware, software, source, tia, archive, attestation, keptDevices, blindTrust, outcome, status}], unreadableLines}`. The deploy record, newest first (§3.5, "The deploy record"). Never starts TIA |
| `POST /v1/deploys/verify` | `{deploy, fingerprint?: {trustUnverifiedCertificate?}, secrets?: {fullAccessPassword}}` → `{deploy, address, status, latest, unreadableLines, verdict, broken?, session?, differences?, blocks?, blindTrust?, changedOutside, fingerprint?, fingerprintError?, closeError?}` (a `broken` verdict has no session, differences, blocks or blind trust). Whether the CPU still runs a deploy: its archive opened in a session of its own and compared online (§3.5, "Verifying a deploy"). `verdict` is `equal`, `different` or `broken` |

**Blocks outside the group (#789).** TIA's generate-from-source does not
stop at the group it is given. On the TIA VM a source declaring
`FUNCTION_BLOCK "Main"` silently replaced the project's own `Main` [OB1],
outside the group, with an FB. On an adopted project that is the
customer's program gone, and §2a promises everything outside the owned
blocks is carried through untouched. So:

- Before anything is imported, tiad reads the block every source header
  declares (`ORGANIZATION_BLOCK`, `FUNCTION_BLOCK`, `FUNCTION`,
  `DATA_BLOCK`; any case, quoted or bare, after comments). A name that
  matches a program block outside the group, in TIA's case-insensitive
  names, refuses the whole call with `foreign_block`, before anything is
  imported. The fix is a name the project does not use: the writer's
  block prefix.
- After the import, tiad compares every block outside the group with the
  blocks before it. Any change (a block gone, or there with another kind,
  number or group, or a new one) fails the call with `operation_failed`
  naming it. That session is then never saved, archived for a deploy (so
  never downloaded) or imported into again (`unclean_session`); it can
  only be closed without saving.
- Not covered yet: data types (`TYPE`). Where TIA puts a type that a
  source generates is unproven, so no rule is guessed.

**Cyclic interrupt OBs (#789).** A nautilus task runs in a cyclic
interrupt OB at the task's cycle, and SCL source cannot make one. Proven on
the VM:
- An `ORGANIZATION_BLOCK` source with no OB of its name becomes a program
  cycle OB numbered from 123.
- Openness has no `CreateOB`, and an OB's class (`SecondaryType`) is
  read-only.
- A SimaticML import of an empty LAD OB (`CyclicInterrupt`, its number,
  `CyclicTime` in µs) lands as asked. An empty SCL one is refused, since SCL
  needs a compile unit.
- An SCL source of the same name then replaces the body and keeps the
  class, number and cycle, again on a second load.
- `CyclicTime` cannot be written afterwards.

So `/software` takes `obs: [{name, number, cycleMs}]` (the writer's
`Output.OBs`), and before the sources the agent:
- keeps an OB of the group that is already as asked;
- creates a missing one by that import;
- recreates one with another cycle, number or class (deletes it and
  imports it again);
- with `prune`, removes another OB of the group that holds the number.

The reply's `obs` lists each step (`created`, `kept`, `recreated` with
`was` and `now`, `removed`). A deploy shows them before it asks to
download, so a cycle change is seen before it is confirmed.

Refused before anything is imported:
- a number outside 30-38 and 123-32767, or a cycle outside 1 ms-60 s;
- an `ORGANIZATION_BLOCK` source with no entry (it would silently be a
  program cycle OB), and an entry no source declares;
- a number held by a block outside the group (`foreign_block`), or by an
  OB of the group without `prune`;
- a block named `OB_<x>` while an OB `x` exists or is asked for. TIA
  keeps a system constant `OB_<name>` for every OB (`OB_Main` for Main),
  block names are case-insensitive, and TIA refuses the clash with `Type
  conflict: Object "OB_x" cannot be overwritten`. The VM's first golden
  run hit this (the writer's `OB_main` against Main), not an OB number,
  as an earlier note here guessed.

After the import each asked OB must be a cyclic interrupt OB in the group
with its number and cycle. Anything else fails the call and leaves the
session unusable, as a change outside the group does.

`secrets` is `{"webUsers": {"naut": "<password>"}, "protection": "<password>", "configurationPassword": "<password>", "fullAccessPassword": "<password>"}`.
Each password answers only its own question: the protection password
never answers `configuration-password`, and the other way round. The Full
access user's password answers no download question: it answers only TIA's
credentials question going online (§3.9), and it is the only secret
compare, fingerprint, upload and verify take.
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
                 "consistentDownload": true, "alarmTexts": true, "allBlocks": true,
                 "identificationDataLoadNothing": true,
                 "startAfter": true, "trustCertificate": "<sha256 fingerprint>",
                 "keptDevices": ["<device name>"], "acceptUnverifiedHardware": true,
                 "acceptBlindTrust": true },
  "announced": true,
  "attestation": { "by": "<who>", "text": "<the CPU's hardware configuration is what I intend to load>" },
  "secrets": { "protection": "<password>", "configurationPassword": "<password>" },
  "source": { "commit": "<git commit id>", "hash": "sha256:<hash of the sources sent>" },
  "fingerprint": { "trustUnverifiedCertificate": true }
}
```

Response: `{dryRun, downloaded, questions: [{id, message, detail, objects}], state, stateSource, keptDevices, blindTrust}`,
and on a download that loaded hardware `hardwareVerified: false`, a
`warning` and the `attestation` as journalled (`{by, at, text,
cpuAccessControl}`); on a download that reset the CPU's user management,
`userManagementReset`, the route that let it (`upload`, `attestation`,
`logon` or `instance-logon`).
The example leaves `user-management` out: a software load answers it
with `userManagementKeepOnline: true`, while a hardware load, where TIA
refuses keep, needs `userManagementResetToProject` under its rule
(`user-management`, below).

**What a download verifies, and what it does not.** A download verifies
the PLC software: it succeeds only when an online compare right after it
is equal, with every block the project holds seen identical (below). It
does **not** verify the hardware configuration, module parameters
included. Openness V21 has no online hardware compare, and every way
round it failed on the bench (#789: `HardwareObject.CompareTo` compares
only offline objects, the CPU compares different against any copy of
itself, and uploading the CPU next to the project's station fails or
crashes TIA). So a download that loads hardware (`hardware: true`) is
refused, before anything reaches the CPU, unless the request carries
both:
- `decisions.acceptUnverifiedHardware: true`, the opt-in, and
- `attestation`: the operator's statement that the CPU's hardware
  configuration is what they intend to load, `by` who (at most 200
  bytes) and `text` (at most 2000 bytes), neither blank. One over a
  limit is a `bad_request`, journalled by its size only. It may also
  say the CPU's access control as it is now, `cpuAccessControl`:
  `disabled` or `enabled` (any other word is a `bad_request`, journalled
  as `invalid`); a user-management reset rests on `disabled`, or on
  `enabled` together with tiad's logon (`user-management`, below). It
  must be the operator's own observation of the CPU, never a value a
  client fills in by default.

This holds on every CPU, the bench included: tiad cannot tell a bench
CPU from a customer's, so it never tries. Without the opt-in the
refusal is `decision_required` for the agent's own question
`unverified-hardware`, naming the fix; `acceptUnverifiedHardware: false`
is `declined` (download the software only). `by` is the caller's claim,
since tiad knows only a bearer token: a naut client fills it from the
principal it authenticated. `at` is tiad's own UTC clock when it
accepted the download, never the request's. The audit line journals
the attestation (`attestation={by=... at=... text=... cpuAccessControl=...}`,
`by` and `text` quoted) on every
outcome, refusals and `outcome_unknown` included; a successful download
echoes it for the caller's deploy record (§7). A client shows
`hardwareVerified: false` as a warning, never as a silent success. A
later check of modules and their parameters through the CPU's Web API
(§4) may sit beside the attestation; it would not replace it.

**`state` is TIA's word, not a read of the CPU** (#789). Openness has no
read of the CPU's operating mode, so `state` (`RUN` or `STOP`) comes
from TIA's own download messages, one per step ("PLC_1 stopped.",
"'Main' was loaded successfully.", "PLC_1 started." on the bench): the
last message that is exactly "<cpu> started." or "<cpu> stopped." for
the CPU loaded decides. `stateSource: "tia-message"` labels it as that
evidence. No such message means no `state`: a download that never
stopped the CPU (TIA says nothing of its mode then), or TIA in a language
other than English. A download that loaded and then ends in doubt (the
compare after it fails or differs) carries the same `state` and
`stateSource` in its `outcome_unknown` data (Go: `tiad.StateOf`). A
proof of the mode is the Web API's `Plc.ReadOperatingMode` (§4),
separate from the download.

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
- **TIA asks its checks one at a time, and lists them nowhere** (#789
  S2b, measured on the bench CPU and by reflecting over Openness V21
  offline): the pre-download callback gets one configuration per call,
  that configuration has no parent, and no Openness type holds the pending
  ones; Siemens documents only "called for each configuration that
  requires an action". So each check is answered on its own, only from
  the request, as TIA asks it, and the first one the request does not
  answer yes cancels the download there. TIA's side applies the answers as
  given; it never derives one itself.
- **Answers that can change the CPU are acting** (`stop-cpu`,
  `start-after` answered yes, `overwrite`, `consistent-download`,
  `alarm-texts`, `all-blocks`, `reinitialize`, and any check
  tiad does not know; a password, a certificate, `user-management`,
  `identification-data` or a start-after answered no is not). TIA may act on an answer
  before it asks the next check (Siemens's own sample says StopModules'
  StopAll "will set PLC into Stop mode"), so a cancel after an acting
  answer is never a clean refusal: it answers `outcome_unknown`, lists the
  acting answers in `data.actingAnswers`, and says plainly what they may
  have done ("TIA may already have stopped the CPU: check its mode and
  start it"). The audit line lists them too. Answer every check you are
  prepared to answer in the first request: a later cancel after a stop
  can leave the CPU in STOP. Whether TIA acts between checks is the
  question for the next bench window that walks past the first check.
- **A real download succeeds only on evidence** (#789): TIA's result
  says it loaded (`Success`, `Information` or `Warning`; TIA's messages
  come back in `messages`), AND an online compare of the PLC software
  right after, in the same call, is equal (`compareEqual: true`).
  Anything less is `outcome_unknown`, naming the part that failed: TIA's
  result (an `Error` state is never loaded, asked or not), the compare
  failing, or the compare differing (with the differing objects). This
  holds whether TIA asked no check or many. It fails closed, by design,
  where the compare cannot be equal: a CPU whose online access asks for
  credentials the request does not carry, or refuses them (§3.9: the
  compare fails), a hardware-only
  download onto software that already differs, a kept device or system
  block that compares different. Equal is the agent's call on TIA's
  compare tree, never TIA's tree alone: its root says identical, at
  least one object below the root was compared and found identical, no
  element anywhere is in a non-identical state, and every user program
  block the project holds (read in the same call, in its block group and
  every user group) is in
  the tree, wherever it sits, as an identical element of its label
  (`Main [OB1]`: name, kind, number). An empty tree, a tree of empty
  folders, a folder that says its contents differ without showing a
  differing child, or a tree that leaves out a block of the project is
  not equal and names why (`nothing compared`, `not compared`,
  `contents differ`, `<block> [FB1] not compared`); so is a block tiad
  cannot label, and a project with no blocks, or whose blocks were not
  read. So a download's success proves every block it sent landed, by
  name: the response's `blocks` (and `/compare`'s) and the audit line
  list them, so a caller checks the blocks it sent are among them.
  System blocks, software units, data types and tag tables are not
  required by name, only never different; and a tag table named exactly
  like a block the tree leaves out would stand in for it (no object
  tiad makes is so named).
  The shape was pinned from the real tree an equal bench CPU reported
  (#789 dry-run window); the label of an FB, FC or DB is the same shape
  by reasoning, until a bench compare shows one. When the request said start
  the CPU and the download ends in doubt after TIA's call,
  `actingAnswers` names `start-after` too: TIA may have started it after
  loading.
  **The hardware configuration is never verified here**: Openness V21
  has no online hardware compare (`HardwareObject.CompareTo` compares two
  offline objects; the only online compare is
  `PlcSoftware.CompareToOnline`). So a download that loaded hardware
  says `hardwareVerified: false` and a `warning` in words, the audit line
  says `hardwareVerified=false`, and a client shows it as a warning, never
  a silent success. No hardware check stands in for it: the opt-in and
  the operator's attestation do ("What a download verifies, and what it
  does not", above; #789 revised gate).
- **Anything that leaves the CPU in doubt answers `outcome_unknown`**:
  TIA failing once it asked a check (a cancel included, since "a cancel
  loads nothing" is what S2 must prove), a cancel after an acting answer,
  a load tiad did not approve, a configuration asked twice. Never success,
  never a plain refusal. The session stays open for the compare, unless
  TIA itself died (below).
- **How `tiad.Openness` answers.** In each pre-download callback it first
  logs the configuration as handed over (its type and its parent's; each
  read dropped if it fails, so a diagnostic never changes how the
  download ends, and type names only, since password configurations pass
  there too), then asks the agent for that one answer and writes it,
  `NoAction` included. When TIA refuses a selection tiad writes, the log
  line names it with the configuration's public members, each enum with
  its value names and each list with its first 20 elements, and never
  reading a property whose name says password, secret, credential,
  token or key, so the next window
  shows what TIA offers instead (#789 stage 4: a hardware load refused
  `KeepOnlineUserManagementData`). A password configuration is never
  described. A configuration counts as written only once its
  write succeeded; one asked twice, an answer it cannot apply, or a write
  that fails cancels the download, and the cancel stands even if TIA
  swallows the exception. Start-after is the request's `startAfter`,
  decided before TIA is called and written in the after-download
  callback (`NoAction` unless decided yes, and always `NoAction` after a
  cancel), never left to TIA's default. Before TIA starts, every
  selection tiad may write is checked against this Openness's own enum
  types. Once TIA's download call is entered, any failure answers
  `outcome_unknown`, whatever its kind, including TIA itself dying inside
  the call (#789 stage 2): detaching tiad's handlers afterwards never
  throws, so a TIA that is gone cannot turn the answer into
  `operation_failed` (`DownloadSteps` in core).
  **What tiad throws back into TIA.** TIA Portal runs in its own process
  and calls tiad's delegates from there: the download's pre- and
  after-download callbacks, the upload's callback, the online handler.
  Whatever a delegate throws goes back to TIA's process, which must
  deserialize it and has none of tiad's assemblies; in #789 stage 2 a
  tiad exception thrown from the pre-download callback killed TIA Portal
  that way. So each of those delegates is only `Boundary.Call` (core): it
  records the failure on tiad's side first (the decider's cancel, the
  question asked, the upload's configuration), logs the reason and each
  check as handed over, then throws TIA a plain `OperationCanceledException`
  with that message and nothing else. How the download ended is read from
  that kept state, never from getting the thrown object back: a decline
  counts only when the decider declined, TIA called no callback after
  it (neither another pre-download one nor the after-download one), and
  what TIA threw carries the decline's token (`tiad-decline:` and 16
  random hex digits, new for every download; TIA's own text never has
  it, however close its words); anything else is `outcome_unknown`. Provisional until a
  download on a CPU shows what TIA does with that cancel.
  **When TIA itself dies** (`ITia.Gone`: TIA Portal's `Disposed` event, a
  handler that cannot be detached from its objects (failing closed: that
  counts as gone even if TIA lives on), or its process gone),
  every project open on it goes with it, and the agent starts TIA only
  once. So the call it died in ends its session (fatal, `outcome_unknown`
  once the download call was entered, `operation_failed` before) and
  names the fix: restart the agent, then compare in a fresh session (the
  read-only after-check). From then on the `tia` gate fails, every call
  that needs TIA is refused with that fix (412 `gate`), a call already
  queued behind the dying one included, and closing a session drops it
  without calling TIA. The `certificate` question's fingerprint is not read yet, so a trust decision
  cannot match it: a TLS check refuses until S2b. All of this is pure code
  in `tools/tiad/core/TiaRules.cs`, unit-tested without TIA.
- **A download TIA asks nothing about.** Siemens calls the callback only
  for a configuration that needs an answer, so a real download may ask
  nothing: then the request alone approved it, it loads, and it succeeds
  only on the evidence above. A dry run never passes a start-after to
  TIA (it is always `NoAction`), so even a dry run TIA loads unasked never
  starts the CPU. A dry run
  has nothing to cancel from then: TIA loads, and the agent answers
  `outcome_unknown`. So **the agent refuses every dry run** (400, before
  TIA hears of it, no CPU claimed) while its TIA side says it cannot
  promise the cancel (`ITia.CanDryRun`): `tiad.Openness` says so until
  S2b shows a cancel at the first check loads nothing. The fakes model
  the dry run the contract aims for.
- **Start-after is decided before TIA is called.** A real download needs
  a `startAfter` decision, or it answers `decision_required` before
  anything reaches the CPU, even when TIA would not stop the CPU. Both
  fakes do the same.
- **Every download is audited**: one log line with the address, the
  question IDs asked (never TIA's text), the acting answers given, the
  decisions (request-supplied names quoted), whether a protection password and a configuration
  password were set (never their values), the hardware attestation
  (quoted, `at=unset` until accepted), what came of a user-management
  reset (`userManagementReset`) and the outcome, refusals included.

- **A dry run** (provisional, see above) cancels at TIA's first
  pre-download check and returns that one as `questions`, with
  `questionsIncomplete: true`: TIA asks one check at a time, so a dry run
  sees no more (and the agent's own `unverified-hardware` question for a
  download that loads hardware, and the kept-devices question, when
  there is one). The
  `id` is one of:
  - `stop-cpu`
  - `reinitialize` (`objects` lists the DBs)
  - `overwrite`
  - `consistent-download` (TIA's `ConsistentBlocksDownload`, "Download
    software to device": loading the PLC software as one consistent
    whole. A real software download onto a CPU in RUN asks it right after
    `stop-cpu`, #789 walk-past window; V21 offers only yes). A dry run
    sees only the first check, so it never shows this one: **a software
    download should answer `consistentDownload` up front**. Left out, the
    download cancels there after `stop-cpu` was written, which is
    `outcome_unknown` with the CPU possibly stopped.
  - `alarm-texts` (TIA's `AlarmTextLibrariesDownload`: load the alarm and
    text list texts with the software; acting. Asked third on the bench.
    tiad never writes `NoAction`: texts that disagree with the program are
    not a load)
  - `all-blocks` (`AllBlocksDownload`: load every block; acting)
  - `overwrite` (`OverwriteSystemData`, "Delete and replace system
    data in target"): the bench's hardware loads asked it second, right
    after `stop-cpu` (#789 stage 4), so a hardware download should answer
    `overwrite: true` up front. Acting.
  - `user-management` (`UserManagementDownload`): answered by one of two
    decisions, never both (both yes is a `bad_request`; with no yes, a no
    declines):
    - `userManagementKeepOnline: true` keeps the CPU's online users as
      they are (`KeepOnlineUserManagementData`), only when the spec's
      `cpu.accessControl` is `keep` and no apply on the session ever
      asked for `fullAccessUser`; otherwise it stays unanswered, naming
      why. Passive. On a hardware load the bench's TIA refused it (#789
      stage 4: "The configuration provided is invalid").
    - `userManagementResetToProject: true` resets the CPU's user
      management to the project's
      (`DownloadAllUserManagementDataResetToProject`, which TIA
      preselects on a hardware load and words "Delete user management
      data and disable access control in runtime" when the project's
      access control is off). Acting: it deletes every user the CPU
      holds and sets its access control and users as the project has
      them. tiad gives it only on a hardware download, and only for
      these cases; everything else is refused:

      | Project after the reset | The CPU now, and the evidence | Route |
      |---|---|---|
      | access control `Disabled`, spec `keep` | off: `attestation.cpuAccessControl: "disabled"` | `attestation` |
      | access control `Disabled`, spec `keep` | off: this session uploaded from it (built, OFF) | `upload` |
      | access control `Disabled`, spec `keep` | on: `"enabled"`, and tiad logged on as `nautilus` in this download | `logon` |
      | access control `Disabled`, spec `keep` | on: `"enabled"`, the download asked no credentials, and an equal compare in this TIA instance logged on | `instance-logon` |
      | tiad's `nautilus`, spec `fullAccessUser` | off: `"disabled"` | `attestation` |
      | tiad's `nautilus`, spec `fullAccessUser` | on: `"enabled"`, and tiad logged on as `nautilus` in this download | `logon` |
      | tiad's `nautilus`, spec `fullAccessUser` | on: `"enabled"`, the download asked no credentials, and an equal compare in this TIA instance logged on | `instance-logon` |

      - "spec `keep`": the spec's `cpu.accessControl` is `keep`, no
        apply on the session ever asked for `fullAccessUser`, and the
        project's access control, read by tiad, is exactly `Disabled`.
        The reset leaves the CPU without access control.
      - "tiad's `nautilus`": the spec is `fullAccessUser` as applied on
        this session, the project's access control is exactly
        `EnabledWithAccessControl`, and the request's
        `secrets.fullAccessPassword` is the one tiad's record of the user
        `nautilus` holds (§3.3). After the reset the CPU's Full access
        user is the one tiad logs on as, with the password tiad set, so
        the compare after the load can log on. A password the record does
        not hold is refused: the CPU would end with a password the
        request lacks. A session on which an apply asked for
        `fullAccessRecordOnly`, failed or not, is refused too: its record
        shows only what tiad was told, not what TIA holds, and a wrong one
        would turn the CPU's access control on with a password tiad cannot
        log on with. The refusal lasts for the session only: the record
        stays in the project, and a later session that reopens it and
        applies the same password finds it "unchanged". Marking
        record-only records themselves is a follow-up (#616); until then,
        never reset to a project a record-only apply touched.
      - Every case needs `by` and `text` in the attestation, and
        `cpuAccessControl` one of the two words. Nothing attested is
        refused, naming both.
      - Keep after `fullAccessUser` on the same session, a `keep`
        project whose access control is on (an adopted CPU's users), and
        `EnabledWithAccessControlViaAccessLevel` are refused.

      The `logon` route is decided in two steps. Before anything reaches
      the CPU, it needs `secrets.fullAccessPassword` and an agent
      instance that trusts no certificate blindly (both refused as
      `bad_request` or `decision_required`, "nothing was sent to the
      CPU"). When TIA asks the check, tiad answers reset only if this
      download logged on as `nautilus` (§3.9) and answered nothing no
      since. tiad reads TIA asking the check as the CPU having taken the
      logon (an inference the bench window checks, §3.9). If the
      download did not log on, tiad lets the reset there only by the
      `instance-logon` route (below), and otherwise refuses it, naming why
      and the fix: compare first, then download again; restart the agent
      if the compare asks no credentials either (tiad retires a TIA
      instance only after a blind trust, so closing sessions does not
      give a fresh one); or attest `disabled`. The other routes rest on the CPU's access
      control being off, so a CPU that asked credentials in the download,
      whatever tiad answered (TIA may go on after a no), refuses them the
      same way, as does a download in which tiad answered any question
      going online no (a credentials question may be refused before tiad
      can tell what it is). That guard is best effort: a TIA instance that logged on
      to the CPU earlier may ask no credentials at all (§3.9). A refusal
      at TIA's check after an
      acting answer (`stop-cpu` on the bench's order) is
      `outcome_unknown`, carrying the reason. The CPU's logon checks the
      CPU's users, not the project's, so the restore of an as-found
      project with no users logs on as `nautilus` all the same (the
      fakes model it; the bench window proves it).

      **The `instance-logon` route (#789 P1 fallback).** TIA may keep a
      logon for its instance's life and ask no credentials in a later
      download, or ask its user-management check before credentials.
      Either way the download has not logged on when TIA asks. The
      `logon` route, let before TIA, is then let at TIA's check as
      `instance-logon` when:
      - the download asked no credentials and answered nothing no before
        the check (one that asked and did not log on contradicts it);
      - this agent's TIA instance holds the evidence of a compare (the
        `compare` and `verify` calls, and the compare right after a
        download) to the same address that logged on as `nautilus` with
        the request's `secrets.fullAccessPassword`, with no blind trust,
        and came back equal.

      The agent keeps that evidence in memory only, per address: the
      instance it was made in, and the password as an HMAC under a random
      key made for it, never written or logged. It holds across session
      closes (the restore runs from another session) and goes when
      anything could change or contradict what it shows: any blind trust,
      a new or retired instance, a download to the address that may have
      acted (one TIA ended cleanly, loading nothing, with no acting
      answer, keeps it; the compare after a load may record it again),
      a credentials question on the address that tiad answered no with a
      password, or answered with another password (upload, fingerprint,
      compare, download), and a compare that failed after TIA asked
      credentials. Otherwise it lasts the instance's life, with no age
      limit. A practical way to earn it is to verify the last deploy (its
      archived project compares equal). The audit says
      `given:instance-logon`, with a line naming the evidence (when,
      which call, which session). It is a second loosening, on #616 with
      the first.

      The same knowledge tightens the off routes: once tiad answered
      credentials for an address in a TIA instance, `attestation` and
      `upload` resets to it are refused at TIA's check for the rest of
      that instance, since TIA may ride on the kept logon and ask the
      download nothing. If the CPU's access control really is off, restart
      the agent.

      Openness V21 offers no online "Reset to factory settings" or memory
      reset for tiad to fall back on (reflection over its assemblies,
      2026-11-22): only `ResetModule` (`NoAction`, `DeleteAll`), a check
      TIA may ask inside a download, which tiad does not answer. Past
      these two routes, the fallback is physical, at the bench.

      **A loosening (#789 option 1, naut-lead 2026-11-22, a tighten-later
      candidate on #616).** Before this, tiad never reset a CPU whose
      access control was on. The `logon` route does: it deletes every
      user such a CPU holds, and may turn its access control off, on the
      strength of tiad holding the password of a user the CPU took. It
      exists so that every load of the access-control window, the restore
      included, is tiad's (James: "never have to touch TIA Portal").

      The upload route assumes an upload brings the CPU's access control
      into the project, and only the bench CPU (`Disabled`) has been
      uploaded. It is built and tested but OFF (`UploadRouteEnabled`,
      naut-lead 2026-11-21), and turned on (agent and Go together) only
      with a bench upload of a CPU with access control on that reads back
      as not `Disabled`. Until then every refusal says it is not yet
      available.

      A reset given, then a cancel at a later check, is
      `outcome_unknown` naming `user-management`. The audit line says
      what came of it
      (`userManagementReset=unset|refused|allowed:<route>|given:<route>`:
      `unset` when no reset was asked or the download ended before tiad
      checked it; `given` once tiad answered TIA's check reset, even if
      TIA then refused the write, the safe side; `refused-at-check:<route>`
      when one let before TIA was refused at TIA's check, which also has
      its own `audit user-management-reset` line). A successful download
      returns the route (`userManagementReset`), and the deploy record's
      outcome line carries the route of a reset tiad gave whatever the
      end, so a download in doubt after one says by which route the CPU's
      users may have been deleted.
    Updating the CPU's users (`UpdateUserManagementDataButKeepOnlinePassword`)
    is never tiad's choice.
  - `identification-data` (`LoadIdentificationData`): answered only by
    `identificationDataLoadNothing: true`. Passive.
  Every other download configuration V21 defines (memory card,
  protection level, initialise or reset memory, selective delete, H
  systems, web applications, HMI data and the rest; the full list is on
  #789) is `other` and refused.
  - `start-after`
  - `password`
  - `configuration-password` (TIA's `PlcMasterSecretPassword`: the
    password protecting the configuration data, answered only by
    `secrets.configurationPassword`; unproven until S2b)
  - `certificate` (detail is the fingerprint)
  - `kept-devices`: the agent's own question, never TIA's, asked when the
    last apply kept devices as found ("Which CPU an apply uses"). `objects`
    names them.
  - `unverified-hardware`: the agent's own question, asked of every
    download that loads hardware; answered only by
    `acceptUnverifiedHardware: true` with an `attestation` (above).
  - `blind-trust`: the agent's own question, asked of every download, a
    dry run too, in a TIA instance that trusted a CPU's certificate
    blindly (§3.8); answered only by `acceptBlindTrust: true`, checked
    before anything reaches the CPU. `false` is `declined`. The yes
    answers no TLS question the download asks itself: that still takes
    `trustCertificate`. The error's data and a successful download carry
    `blindTrust`, the record it rode on, for the deploy record.
  - `other` (the check's own text).
- **A real download** answers each question only from `decisions`.
  - The first check the request does not answer fails the call with
    `decision_required`, naming that one, and nothing is loaded; answer
    it, and TIA may ask another. After an acting answer it is
    `outcome_unknown` instead (above).
  - A `reinitialize` answer must name every DB the check named.
  - A download that loads hardware needs `acceptUnverifiedHardware: true`
    and an `attestation`, checked before anything reaches the CPU
    ("What a download verifies", above).
  - `decisions.keptDevices` must name exactly the kept devices, checked
    before anything reaches the CPU. Missing, or naming another set (an
    older yes carried by a script), it fails with `decision_required`.
    No kept devices, no question, and a `keptDevices` answer then refuses.
  - tiad never picks a default.
  - An `other` question always refuses, so a check tiad has never seen
    cannot be answered by accident.
  - `tiad.Openness` does not read a `reinitialize` check's DBs or a
    `certificate` check's fingerprint yet, so on a real CPU both always
    refuse. After a `stop-cpu` yes that refusal is `outcome_unknown` (the
    CPU may be in STOP): until those are read, a download that would
    reinitialize DBs cannot run, and the next bench window must not
    expect one.
  - A password answer is passive: a decline after it is still a clean
    refusal, though TIA may already have used the password to log on to
    the CPU. That changes neither the CPU's program nor its mode.
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
  `busy`. Since tiad holds one session at a time (§3.2), a second session
  cannot open while the first is open, so the claim stays only as a
  backstop.
- **An upload replaces the project with the CPU's program**: the
  project no longer matches its spec, so a download needs a spec applied
  and a compile again. Compare and export still know the CPU.
- **An answer of no** to `stop-cpu`, `overwrite`, `consistent-download`
  or `certificate` fails
  the call with `declined`, and nothing is loaded (after an acting answer,
  `outcome_unknown`). `start-after: false` is
  a decision, not a refusal: load and stay in STOP.
- **`announced`** must be true for a real download. It records that the
  shared-CPU rule was followed (tell naut-lead first). It is a convention,
  not a safety.
- **`state`** is the CPU's mode afterwards, `RUN` or `STOP`, as TIA's
  download messages say it (`stateSource: "tia-message"`, above); absent
  when TIA said nothing.

**The deploy record (D7, §7).** Every real download is recorded under
`TIAD_DEPLOYS_DIR`, which is outside the work dir, so the files API never
reaches it. A dry run, and a download tiad refuses itself, leave no record.
A dry run can leave none only while it cannot load. Before `CanDryRun` is
ever true for `tiad.Openness`, a dry run that TIA loads without asking must
get a record too, just as it must get the hardware opt-in (#1180 review).
- **Archive first.** Once every refusal tiad makes itself has passed, the
  download's own TIA work item does the following:
  - It saves the session's project and archives it (`.zap21`).
  - It flushes the archive to disk, hashes it and moves it to
    `archives/<sha256>.zap21`. A file already at that name is replaced,
    never taken by its name.
  - It writes an intent line to `deploys.jsonl`.
  - Only then does it call TIA's download.

  Because this is one work item, what is archived is what TIA loads. An
  archive or an intent line that cannot be written refuses the download
  (`operation_failed`, "nothing was sent to the CPU").
- **The intent line** holds the following:
  - the deploy id, the session, the project and the address;
  - `hardware` and `software`;
  - `source`, `{commit, hash, claimedBy: "caller"}`. This is the caller's
    claim; tiad checks only its shape. A commit is 40 or 64 lower-case hex
    digits, and a hash is `sha256:` and 64 more. Any other shape is a
    `bad_request`;
  - `tia`, `{version, openness, agent}`;
  - `archive`, `{file, sha256, bytes}`;
  - the attestation of a hardware load, the kept devices, and the blind
    trust the download was checked against.
- **An outcome line on every end.** `status` is one of:
  - `loaded`: TIA loaded and the compare right after it was equal;
  - `not-loaded`: a check cancelled the download (`decision_required`,
    `declined`), or TIA failed before it asked one (`operation_failed`).
    An internal error of tiad's own during TIA's download is
    `outcome_unknown`, not `operation_failed`;
  - `in-doubt`: everything else, `outcome_unknown` included.

  The line also holds `outcome` (`loaded` or the error's kind) and the
  evidence: `state`, the blocks, `hardwareVerified`,
  `userManagementReset`, the acting answers, and the fingerprint read after
  the load. A failure carries its message, at most 4000 characters.
- **When the outcome line is missing.** A record with an intent line and no
  outcome line lists as `in-doubt`: the agent died during the download. An
  outcome line that cannot be written leaves the load as it is. The
  response then says `deploy.recorded: false`, with `recordError`. An error
  after the intent line names its record in `data.deploy`.
- **The fingerprint after the load.** After a load that compared equal,
  tiad reads the CPU's fingerprint (§3.8) in the same session, through the
  fingerprint call's trust path and audit line (with `deploy=<id>`).
  - `DownloadId` names the load.
  - `Hardware` changes on the first tiad load and on a user-management
    reset. #789 stage (C) saw it change on the first hardware load and stay
    the same on the next one.
  - Its opt-in is the request's own, `fingerprint.trustUnverifiedCertificate`.
    The bench CPU's read asks TLS trust, while its download does not. With
    that opt-in, TIA's instance trusts blindly from then on, so a later
    download in it needs `acceptBlindTrust`. Close the session after a
    deploy: the agent then retires that instance.
  - A read that fails is recorded (`fingerprintError`) and changes nothing
    of the verdict. A failed read with the opt-in may still have trusted,
    so the instance counts it as trust. `blindTrustAfter`, in the response
    and the outcome line, is the instance's trust once the read ended,
    whether it read or failed.
- **Retention.** Every attempt past tiad's own refusals keeps its archive,
  including one a TIA check cancels. Nothing prunes them yet. A full disk
  refuses every download, which is safe but is an outage. Pruning is a
  follow-up (#616).
- **The response.** A successful download returns `deploy: {id, archive,
  source, fingerprint, fingerprintError?, downloadId?, blindTrustAfter,
  recorded, recordError?}`. `GET /v1/deploys` lists the records.

**Verifying a deploy (D1b).** `POST /v1/deploys/verify {deploy}` answers
whether the CPU still runs a deploy. It changes nothing on the CPU, in the
archive or in the journal (the compare touches only the verify's own copy);
an `audit verify ...` line says what it found or why it failed.
1. **The record.** The verify's session is reserved first (TIA V21 opens
   one project per instance), so a verify while another session is open is
   `busy`, naming it, before anything is read or copied. tiad then finds
   the deploy by id (`not_found` otherwise). Its address must be a target
   (`target_refused`); a record whose address is not an IPv4 address is
   **`broken`**.
2. **The archive.** tiad copies the archive into a staging directory of
   the verify's own under `TIAD_DEPLOYS_DIR` (`staging/v-*`, never the work
   dir) and hashes the copy. A copy whose sha256 is not the record's, an
   archive that is gone, or an archive entry tiad does not write makes the
   verdict **`broken`**, never `equal`. A broken archive is never opened,
   and `changedOutside` is `null`.
3. **The session.** The copy is opened (retrieved) as the verify's
   session, project `deploy:<id>`.
4. **The address.** tiad reads the address of the archived project's CPU,
   as the fingerprint read checks it. If it is not the record's address,
   the verify is refused (`cpu_mismatch`, `data: {recordAddress,
   projectAddress}`) before anything goes online: a record and an archive
   that disagree name no CPU to compare. The record's address is used,
   never a station the caller puts: a PUT would change the project under
   verification. What this cannot catch: a journal line edited to carry
   another record's whole archive entry for the same CPU verifies that
   archive, under the wrong id.
5. **The compare.** The device pin is checked, then the compare runs as a
   session's compare does: `equal` or `different`, with the differences
   and the blocks.
6. **The fingerprint, when asked.** With `fingerprint`, tiad reads the
   CPU's fingerprint after the compare, through the fingerprint call's
   trust path. `downloadIdSame` and `hardwareSame` say whether those items
   are the record's (`null` when either side has none). A `DownloadId`
   that differs means the CPU was loaded since, whatever the compare says.
   A failed read is `fingerprintError` and leaves the verdict as it is.
7. **The close.** The session is closed, as a `DELETE` closes one (a
   blindly trusting instance is then retired), and then its staging is
   removed. A close that fails keeps the session, broken, with its staging;
   the verdict is still answered, with `closeError` naming the session to
   close again (a failed verify's error says so too). A `DELETE`, or the
   idle reaper, closes it later and removes the staging.

`latest` is the newest record for the address that may have loaded it
(`loaded` or `in-doubt`). `changedOutside` is set only for a `loaded`
record that compared: `true` when it is `latest` and compared `different`,
since the CPU then changed without a deploy of this agent; `false`
otherwise (a superseded record is expected to differ). It is `null` for a
record that is not `loaded` (an in-doubt one that differs may simply not
have loaded), and while the journal has lines tiad cannot read
(`unreadableLines`), since one of them may be a newer deploy. A changed
`DownloadId` is reported beside it, not folded in.

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
  identity but does not lift this, and no call removes a pin. A CPU can
  be compared before its first upload and cannot after it: going online
  and comparing ask no TLS question on the bench CPU (#789 stage 1b), so
  nothing but the pin refuses them.

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

**The fingerprint read (built).** `POST /v1/sessions/{id}/fingerprint`
goes online from the session's CPU, reads what TIA's fingerprint service
reports for the CPU, and goes offline (after a failure, as far as TIA
still answers: the project may stay online if TIA is failing). It writes nothing and
saves nothing. On the bench CPU (#789 stages 1b and (C)) it returns eight
items, in TIA's order and named as TIA names them: `TextLists`,
`Hardware`, `Certificates`, `Routing` and `Connection` checksums,
`DownloadId`, `ChangeCounterPersistent` and `ChangeCounterRetain`.

- **They are checksums and counters, not the CPU's identity**: no order
  number, no serial, no program checksum. They say whether what the CPU
  holds changed. `DownloadId` and both counters changed on every bench
  load. `Hardware` changed on the first tiad hardware load and stayed the
  same on the next, an identical one. That first load also reset the
  CPU's user management, and one observation cannot tell which of the two
  changed it: read it as "the first tiad load or a user-management reset
  changes it once" until more loads show which.
- **It fails, never answers part of a read**: the CPU not reached
  online, any question not answered, any failure in TIA's side, no
  items, an item with no identifier, one named twice, more than 64, an
  identifier over 128 bytes or a value over 1024 bytes. A failure leaves
  nothing to compare against.
- **Same gates as a compare, before the read:** the session's CPU
  address, `TIAD_TARGETS`, and the device pin (whose check reads the
  CPU's identity through TIA when a pin exists).
- **Journalled:** every read, refusals too (a bad request, a gate), is an audit line with each
  item quoted, what it trusted, and the instance's blind trust before and
  after it.
- **The call sequence is bench-proven** (the stage-1b probe build). The
  fail-closed shape is proven on the fakes only. TIA's portal service
  alone, with no project online, never reached the bench CPU (stage 1),
  so the read goes online from the project first.

**Blind trust (built).** Going online and comparing ask no TLS question
on the bench CPU, but the fingerprint read asks one ("certificate not
matching", no certificate shown), and an upload may. Only the request's
`trustUnverifiedCertificate: true` answers it, blindly; without it the
read fails, naming the opt-in. TIA then keeps that trust **for the rest
of its instance**: a later read without the opt-in asks nothing and
succeeds. Nothing is saved, and a TIA restart forgets it (stage 1b). The
agent records the instance's blind trust (`blindTrust: {since, call,
session, address, questions}`, `since` by tiad's clock) and keeps it for
as long as that instance runs:

- the first trust sets the record and is an audit line; later ones are
  logged beside it;
- a call that took the opt-in and then failed in TIA's side counts as
  trust (`questions: ["unknown: ..."]`), since TIA may have trusted before
  it failed. That includes a failure before going online (no CPU, another
  address): the safe side, since tiad cannot tell how far it got;
- every later fingerprint read, compare, upload and download reports it
  (`blindTrust`, `null` while there is none) and journals it: a compare
  before it goes online, a download as the TIA thread read it just before
  TIA's download (never a trust given later, while it ran);
- a download, a dry run too, that would ride on it is refused before
  anything reaches the CPU (`blind-trust`, §3.5) unless it says
  `decisions.acceptBlindTrust: true`. The other fix is a TIA instance
  that trusts nothing: close every session (below), or restart the
  agent. The check runs again on
  the TIA thread just before TIA's download, so a trust a queued call
  gave in between is caught too; that refusal comes after the pin check
  and the CPU's claim, and says TIA's download was not called.
- a fingerprint read refused for a certificate question alone names the
  opt-in; one refused for credentials or a question tiad does not know
  does not, since the opt-in answers neither.

- **the instance is retired once no session is left on it** (#789 D1):
  when the last session's close ends (a `DELETE`, or the idle reaper)
  and the instance trusted blindly, tiad disposes TIA Portal and waits
  for its process to exit (up to 120 s). The next call that needs TIA
  starts a new instance (about 8 s on the bench machine, plus a cold
  project open), and only then is the record cleared. Both steps are
  audit lines (`audit blind-trust retired`, `audit blind-trust
  cleared`). While any session is open, still opening, still closing,
  or broken (a close that failed), the instance stays, and the download
  refusal above holds; a failed open leaves the retire to the next
  close. A retire that cannot show the process exited (no process id, a
  failed dispose, a process that outlives the wait) counts as TIA gone:
  the `tia` gate fails until the agent restarts, since that instance may
  still hold the trust. So does one still running after
  `TIAD_RETIRE_WATCH` (`3m`), since TIA's dispose has no bound of its
  own: the gate then names the stuck retire. An instance that never
  trusted blindly is never retired.

So a deploy reads the fingerprint after loading, then closes its
session: the next deploy starts in an instance that trusts nothing and
needs no `acceptBlindTrust`. A read before loading, in the same session,
still makes that session's download accept the blind trust; read it in
a session of its own and close that first.

**Bench-proven (#789 D1 probe, read-only):** a fresh instance asked the
TLS question; the opt-in read trusted it; a read without the opt-in in
the same instance then succeeded riding on it; closing the session
retired the instance (its process exited 5.6 s after the retire began);
in the new instance, started in the same agent process 6.6 s later, a
read without the opt-in was asked the TLS question again and refused.
So TIA's trust goes with the retired instance, not only tiad's record.
Nor is it kept in a saved project: a project saved after the trusting
read, reopened in a new instance, was asked the TLS question again.
The first create after a retire took 31 s (a warm one about 6 s): a
deploy after a trusting read pays about half a minute. TIA V21 opens
one project per instance ("Another project is already open"), and tiad
holds one session at a time (§3.2), so the last session is the only one.

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

### 3.9 Credentials going online (#789 stage 5 (A))

A station naut makes has access control on, with the project user
`nautilus` holding the runtime right Full access (`cpu.accessControl:
fullAccessUser`, its password `secrets.fullAccessPassword`). Once a
hardware load puts that on a CPU, TIA asks credentials every time it goes
online to it. Before this, tiad answered no, so a CPU it made locked it out
of its own compare, upload, download and verify: the lockout James's stage
5 go was conditional on.

**What tiad answers.** Each online call takes the password of the CPU's
user `nautilus`:

| Call | Where the password goes |
|---|---|
| `POST …/compare`, `…/upload`, `…/fingerprint`, `POST /v1/deploys/verify` | `secrets.fullAccessPassword` (the only secret they take) |
| `POST …/download` | its `secrets.fullAccessPassword`, also for the compare and fingerprint read after the load |

When TIA asks a project user's legitimation
(`OnlineAuthenticationConfiguration`), tiad logs on as the project user
`nautilus` with that password (`LoginUse` in core, the one rule both the
glue and tiad-fake run). It answers no, naming why, when:

- the request carries no password;
- the agent's TIA instance trusts a certificate blindly (§3.8), or this
  call trusted one: no password goes to a CPU tiad cannot verify. The
  refusal says "password not sent" and names the fix: closing every
  session retires the instance (#789 D1), and the next one logs on;
- this call answered any question no before (a certificate it does not
  trust, credentials, a question it does not know): TIA may go on after a
  no, and no password follows one;
- the connection is not secure communication, or tiad cannot read whether
  it is;
- the CPU offers no project-user legitimation, or tiad cannot read what it
  offers;
- this call answered credentials already, on any of its connections: TIA
  asking again may mean the CPU refused the password, and tiad never sends
  one twice, since a CPU may lock a user out after failed logons.

Legacy access-level passwords (`OnlinePasswordConfiguration`,
`OnlineReadAccessPassword`) and anything tiad does not know still cancel.
Every answer and refusal is an audit line (`audit online-login`) naming the
call, address and user, never the password; the glue logs whether the
connection is secure and which user types the CPU offers, never the
configuration itself. A refusal fails the call as any question answered no
does: `operation_failed` for a compare, upload, read or verify, and
`outcome_unknown` inside a download, where TIA's call had begun. A download
of a station whose spec is `fullAccessUser` without the password is refused
up front (`bad_request`, nothing sent), a dry run too: the compare after it
would ask. So is a hardware load of such a station while the instance trusts
blindly, checked again on the TIA thread before the deploy record: the load
turns access control on, and the compare after it could not log on. A
software load then is not refused, since whether its CPU asks is not known
before it goes online: if it asks, the logon is refused inside TIA's call
(`outcome_unknown`). A download TIA fails after tiad logged on says the CPU
may have refused the password.

**What a blind trust costs.** The refusal after a blind trust is the safe
side, and it has a price on a CPU that asks credentials. TIA's fingerprint
service asks TLS trust on the bench ("certificate not matching"), and an
upload may. A read logs on through the project's connection first, then
trusts the service: it goes through, if the service asks no credentials of
its own, but from then on the instance sends no password until it is
retired. An upload asked TLS before credentials cannot do both and is
refused. There is no opt-in to send a password after a blind trust, by
decision (naut-lead, 2026-11-22): a customer who needs one is a security
call for James.

**What the fakes prove, and what they do not.** Both fakes model a CPU
that asks credentials once a hardware load of a fullAccessUser station
turned its access control on, as that load left its password, and asks
again when the password is wrong. The contract suite (`Credentials`)
drives every online call through it with, without, and with a wrong
password, and with a blind trust. tiad-fake answers through the core rule,
so a mutant of it fails the suite. What only the bench can show: which
configuration type the real CPU asks, whether an upload asks through the
connection or through its own callback (`UploadPasswordConfiguration`,
which tiad still refuses), whether TIA's fingerprint service asks
credentials of its own after the project's connection logged on (tiad
answers once per call, so it would refuse that read), whether TIA keeps a
logon for later calls in the same instance, and how a refused password
shows (the fakes ask again;
TIA may fail going online instead, which tiad reports as a logon TIA
failed after). The glue's log lines answer these in the first window on a
CPU with access control on.

**The download's logon carries a reset.** A download's logon is made by
the agent itself, so its answer to TIA's user-management check can read
whether the download logged on (§3.5, the `logon` route). For that route
the bench window has to show five things before relying on it:
- that TIA asks credentials going online before it asks the download's
  checks;
- that TIA does not ask credentials again within the same download after
  a hardware load restarts the CPU, which tiad would refuse as
  `AskedAgain`;
- that the logon works when the project being loaded does not hold the
  user, as in the restore of the as-found project;
- that TIA reaching the download's checks after tiad's logon means the CPU
  took it (a refused password asks again or fails going online, on the
  fakes and, by TIA's dialogs, on a real CPU);
- whether TIA asks credentials in every download on an instance that
  logged on to the CPU earlier, and whether it asks them before the
  download's checks. If it does not, the `logon` route gives way to
  `instance-logon` (§3.5), which needs an equal compare that logged on
  in the same instance. The guard that refuses the off routes on a CPU
  that asked credentials in the download sees nothing then, so tiad also
  refuses them on any CPU it answered credentials for earlier in the
  instance. A logon kept from outside tiad's knowledge (another tool on
  the same TIA, which the agent does not share) stays out of reach. The
  window restarts the agent before every load (tiad retires an instance
  by itself only after a blind trust), so a load never rides on an
  earlier logon unless it falls back to `instance-logon` on purpose.

The window's stop criteria cover each: an `outcome_unknown` naming
credentials or `user-management` stops it.

**Not covered yet.** Changing the user's password (rotation): the
download that loads a new password needs the old one to go online and the
new one for the compare after it, and a request carries one. With the new
password, as the apply that set it was given, the download is refused going
online (`outcome_unknown`, nothing loaded by TIA's account); with the old
one it loads and then ends `outcome_unknown` at the compare, the CPU holding
the new password. A rotation needs a request that carries both.

## 4. Online plane

| | Web API | OPC UA | Classic S7 (PUT/GET) |
|---|---|---|---|
| Licence | none | per CPU | none |
| Optimized DBs, by symbol | yes | yes | no |
| Push | no; polling | subscriptions | no |
| Force | no | no | no |
| Bench FW 3.1 | Browse/Read/Write (≥ 2.8), operating mode (≥ 2.9). Per the 11/2023 manual: `DiagnosticBuffer.Browse` and `Alarms.Browse` from 3.1, of `Modules.*` only `DownloadServiceData`; `Api.GetMethods` on the bench settles it | with a licence | yes |
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

**The diagnostic buffer** (#789 stage 5 (A) step 3) is read with
`DiagnosticBuffer.Browse` (FW ≥ 3.1 per the manual, not on R/H CPUs),
which needs `read_diagnostics`, the spec's `diagnostics` right.
`naut siemens diag` prints it (`--status` for the entry count and last
change only, `--json`, `--long`), and logs its session out on every path.
A refusal for a missing right or an older firmware names the fix.
- The client sends `language` and `count` only. `count: 0` is sent as it
  is, since it means "status only" and leaving it out means 50. No
  `filters`: the manual's table names it `filters` and its example
  `entries`, so every read takes all three texts until the bench settles
  the key.
- Entries are kept in the CPU's order and time stamps as the CPU's text
  (`Time()` parses one), so one odd entry does not lose the read.
- Per manual, fake-verified only. The first access-control bench window
  checks: that `WebAllowDiagnostics` grants `read_diagnostics`, the
  `filters` key, the entry order, and the answer for a language the
  project does not hold.

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
| `cmd/naut/siemens*.go` | `naut siemens agent probe`, `deploy`, `serve`, `diag` |

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

**What the CPU runs: one archive per deploy (decided 2026-11-21, #789).**
TIA's compare cannot say that a CPU runs what is in git by rebuilding from
source. The offline probes on #789 showed this:
- Two builds of the same SCL compare *different*. The blocks differ only in
  their time stamps.
- A copy of the compiled block compares *identical*.
- An XML import gets new time stamps, so it compares *different*.

So:
- **Archive every deploy.** Each successful download archives the session
  project (`.zap`, content-addressed). It journals the git commit, the
  source hash, TIA's build and version, the archive's hash, and the
  `DownloadId` read after the load.
- **Checking a git commit.** To check that the CPU runs a commit, tiad
  follows the commit to its deploy record. It opens that archive and runs
  `CompareToOnline`, which must come back equal.
- **Changes made outside nautilus.** A compare against the last deploy's
  archive that comes back different means the CPU was changed outside
  nautilus. That raises an alarm.
- **Fallback.** Master copies of the compiled blocks are the fallback, only
  if the archive route hits a wall.

**Built (D1a):** the archive per deploy, the journal, the fingerprint
after the load and `GET /v1/deploys` (§3.5, "The deploy record").

**Built (D1b):** `POST /v1/deploys/verify` (§3.5, "Verifying a deploy").
It opens a copy of a deploy's archive and compares it online. An archive
whose sha256 differs from its journal line is *broken*, never equal. A
record whose archived CPU is at another address is refused before going
online. The latest loaded deploy that compares different is
`changedOutside`; a changed `DownloadId` or `Hardware` is reported beside
it.

**Proven (D7):** on the bench 1510SP, a deploy's archive, retrieved
later, compares equal to the CPU it was loaded into. The #789 D7 window
(2026-11-22 UTC) archived and loaded a deploy, retrieved the archive into
the work dir and compared it equal after a station PUT. A read-only run of
`POST /v1/deploys/verify` on that deploy, with tiad at #1191, then
answered `equal` from the deploys dir, with no station PUT and no compile:
`latest` was that deploy, `changedOutside` false, and with the fingerprint
read `downloadIdSame` and `hardwareSame` true. The blind trust of that read
was retired when the verify's session closed. Openness compares hardware
only in-project, so the hardware question on #789 still stands beside it:
the fingerprint's `Hardware` item is the only hardware evidence a verify
gives.

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
| D7 | (2026-11-21) What the CPU runs is proven against the archived project of the deploy, never against a rebuild from source (§7, #789) |
