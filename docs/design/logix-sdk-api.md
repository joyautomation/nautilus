# The Studio 5000 Logix Designer SDK: a complete capability survey

Written 2026-09-20 against **Logix Designer SDK 2.02.00** — C# client
package `RockwellAutomation.LogixDesigner.CSClient` **2.2.1109** (net10.0),
Python client `logix_designer_sdk` **2.0.2** — as installed on **ECHO1**.

This is the companion to `logix-target.md`. That document argues about what
nautilus should build; this one is the reference underneath it: what the SDK
can actually do, what it cannot, and what it costs.

**It corrects a load-bearing conclusion in that brief.** See §9.

---

## 1. How this was produced, so every claim is traceable

Nothing here is recalled from memory or inferred from a blog post. Four
sources, each named at the point of use:

| Tag | Source |
|---|---|
| **[XML]** | The shipped XML documentation file inside the NuGet package — `lib/net10.0/RockwellAutomation.LogixDesigner.LogixProject.CSClient.xml`. 65 types, 361 methods, 191 enum fields, 52 properties. This is the compiler's own output, not prose. |
| **[DOX]** | The Doxygen reference installed with the SDK at `C:\Users\Public\Documents\Studio 5000\Logix Designer SDK\dotnet\Documentation` — the narrative topic pages (Tags, Partial Import and Export, Full Project Import and Export, release limitations). |
| **[RN]** | The SDK's own release notes, `…\Logix Designer SDK\ReleaseNotes\`. |
| **[OBS]** | Observed on ECHO1 this session — service state, listening sockets, config files, assembly contents. |

Where something has been **executed** rather than read, it says so. As of
this session exactly two operations have ever been run against this SDK:
`OpenLogixProjectAsync` and `SaveAsAsync` (`logix-target.md` §13.5). Treat
everything else as documented-but-unrun, and see §11 for the list.

---

## 2. The shape of the thing

**It is a local gRPC service, and that is the whole architecture.** [OBS]

```
your process  ──gRPC/HTTP2──▶  LdSdkService (LdSdkServer.exe, .NET 10)
  CSClient.dll                   127.0.0.1:53204  and  [::1]:53204
  or pythonnet                   ▲
                                 └── FTSP auth ── FlexNet ── CodeMeter
                                 └── Logix Designer COM services (per version)
```

- `LdSdkService` is a Windows service, **Running / Automatic**. [OBS]
- Port **53204**, from `appsettings.json` → `LDSDKService.APIPort`. [OBS]
- It binds **only** to `127.0.0.1` and `::1`. There is no remote protocol.
  Anything remote needs an agent co-resident with the licensed install —
  which is exactly why `logix-target.md` §4.2 proposes `logixd`. [OBS]
- The client is `Grpc.Net.Client` 2.76.0 + `Google.Protobuf` 3.33.4. [OBS]
- The **Python SDK is not a second implementation.** It is pythonnet
  (`pythonnet.load(runtime="coreclr")`) loading the same
  `RockwellAutomation.LogixDesigner.LogixProject.CSClient.dll` out of its own
  wheel. One API, two skins; the C# surface is the real surface. [OBS]
- **C++ clients were dropped in 2.02.00.** Python and C# only. [RN]

### The project lives on the server, not in your process

> "the project file is sent to the server side of the Logix Designer SDK and
> the client-side project file is left untouched until the `Save` operation
> is complete… Another user could be updating the project file on another
> workstation, and that work could be overwritten by a `Save`." [DOX]

There is **no file locking**. The SDK hands you the rope. Any real agent has
to own the "one writer per project file" rule itself — a `logixd`
requirement, not a nicety.

---

## 3. The complete API surface

`ILogixProject` is the curated facade: **75 instance methods**, plus 6 static
entry points on `LogixProject`. Grouped by the job they do.

### 3.1 Project lifecycle — and format conversion

| Call | What it does |
|---|---|
| `OpenLogixProjectAsync(path, …)` | Opens an existing project. **Valid input types: ACD, L5K and L5X.** [DOX] |
| `CreateNewProjectAsync(path, majorRev, processorType, name, …)` | New project, default content. Will not overwrite. |
| `ConvertAsync(path, revision, …)` | Converts to a **higher** revision. Downgrade is an error. |
| `UploadToNewProjectAsync(controllerPath, newProjectPath, …)` | Uploads from a controller into a brand-new file. Leaves the controller Offline. |
| `GetProcessorTypesAsync(majorRev, …)` | The catalogue of controller types for a revision. |
| `SaveAsync()` | Overwrites the open file. |
| `SaveAsAsync(path, force, detailedL5x, …)` | **Saves as the format the file extension names: ACD, L5K or L5X.** [DOX] |
| `ChangeControllerTypeAsync(name)` | "Morph" to a compatible controller type. |

**This is the single most important entry in the whole document for
nautilus.** Open accepts L5X; SaveAs writes L5X. So

```
ACD ──OpenLogixProjectAsync──▶ (server) ──SaveAsAsync("x.L5X")──▶ L5X
L5X ──OpenLogixProjectAsync──▶ (server) ──SaveAsAsync("x.ACD")──▶ ACD
```

is a **two-call, whole-project round trip through the public, documented
API** — no GUI, no partial-import XPath gymnastics. It is also, in
retrospect, exactly the path the 52-file headless batch conversion took, and
the path `lang/l5x` now reads the output of.

`detailedL5x` is worth its own line. It "provides additional info when saving
as an L5X file: **References, Context, ProductDefinedTypes and IOTags**"
[XML] — which is character-for-character the `ExportOptions` attribute in the
committed fixtures:

```
ExportOptions="References NoRawData L5KData DecoratedData Context ProductDefinedTypes IOTags"
```

So §11's "the same effect is reachable upstream through `ExportOptions`" has
a concrete lever: `detailedL5x: false` produces the lean export, and
`lang/l5x`'s `--drop-l5k` is the downstream approximation of it.

Two constraints on the round trip: `SaveAs` "**doesn't upload tag data from
controller. Only offline values are saved**" [XML], and morph/convert refuse
to go backwards.

**Morph rules** [DOX]: compatible-family only (ICE1→ICE1, ICE2→ICE2),
ICE1→ICE2 allowed, **ICE2→ICE1 refused**, same-type is a no-op that returns
success.

### 3.2 Partial export — read a component out as L5X

`PartialExportToXmlFileAsync(xPath, filePath, …)`. **Works online or
offline.** Does not overwrite; an existing destination is an error. [DOX]

The XPath vocabulary is small and complete [DOX]:

| Target | XPath |
|---|---|
| All data types | `Controller/DataTypes/DataType` |
| One UDT | `Controller/DataTypes/DataType[@Name='MyUDT']` |
| Only user UDTs | `Controller/DataTypes/DataType[@Class='User']` (v33+) |
| All controller tags | `Controller/Tags/Tag` |
| One program | `Controller/Programs/Program[@Name='MainProgram']` |
| One routine | `Controller/Programs/Program[@Name='MainProgram']/Routines/Routine[@Name='Init']` |
| That routine in *every* program | `Controller/Programs/Program/Routines/Routine[@Name='Init']` |
| One AOI definition | `Controller/AddOnInstructionDefinitions/AddOnInstructionDefinition[@Name='BSEL']` |

`@Class` works **only** on `DataType`. XPath is **case-sensitive**; use
PascalCase.

**Collections have two spellings and they are not the same file.**
`Controller/Programs` exports a file whose *target* is a collection — the GUI
cannot import it, `PartialImportFromXmlFileAsync` can.
`Controller/Programs/Program` exports the same programs with target type
*Program* — importable by the GUI and by all three import methods. Prefer the
second unless you know why you want the first.

Safety caveat: exporting a collection from a safety project **drops
`SafetySignaturesHmac`**, so re-importing it removes every safety signature.

### 3.3 Partial import — three methods, and only two work online

| Method | Offline | **Online** | Imports |
|---|:---:|:---:|---|
| `PartialImportFromXmlFileAsync(xPath, file, collisionOption, continueOnErrors, …)` | ✅ | ❌ | Anything in the allowed-node table. Honours per-node `Use="Delete｜Create｜Update｜Overwrite｜Ignore"`, so a hand-built L5X is a scripted edit. |
| `PartialImportWithTargetFromXmlFileAsync(…)` | ✅ | **✅** | AOI definitions, DataTypes, Programs, Tags, SafetySignaturesHmac. **Rungs are not allowed here.** |
| `PartialImportRungsFromXmlFileAsync(xPath, insertPosition, replaceCount, file, onlineImportOption, …)` | ✅ | **✅** | Rungs into an RLL routine, at a position, replacing *n* existing rungs. |

> "Import online is possible only using `PartialImportWithTargetFromXmlFileAsync`
> and `PartialImportRungsFromXmlFileAsync`. Both functions can use
> `PartialImportOption` which is ignored during offline import." [DOX]

`ImportCollisionOptions` (offline import): `OverwriteOnColl`,
`DiscardOnColl`, `CancelOnColl`. [XML]

`PartialImportOption` — **online only**, and read these three carefully:

| Value | Documented meaning [XML] |
|---|---|
| `LeaveEdits` | "Imported logic changes will be left as offline pending edits." |
| `AcceptEdits` | "Imported logic changes will be **accepted and sent down to the Logix Controller**" |
| `FinalizeEdits` | "…accepted and sent down… In addition **if the Logix Controller is in run mode, accepted edits will be assembled**." |

That is test / accept / assemble. See §9.

**Other import rules** [DOX]:
- Tags import only under the same kind of object they were exported from —
  controller tags cannot land in a program or an AOI.
- **An AOI is a unit.** You may not reach inside one through import.
- Importing FBD/ST content **appends** sheets/lines after the existing ones;
  it does not replace them. And an "empty" FBD/ST routine is impossible — one
  empty SHEET/LINE is always added.
- Per-version gates on import targets: Equipment Sequence v35+, SFC v32+,
  program tags as a target node v34+.

### 3.4 Controller lifecycle

| Call | Notes |
|---|---|
| `GetCommunicationsPathAsync()` / `SetCommunicationsPathAsync(path)` | Set is **Offline-only**. |
| `GoOnlineAsync()` / `GoOfflineAsync()` | |
| `ReadConnectedStateAsync()` | `Unknown｜Offline｜Connected｜Online` |
| `ReadControllerModeAsync()` | `Run｜Program｜Faulted｜Test` |
| `ChangeControllerModeAsync(mode)` | Requests `Run｜Program｜Test`. |
| `BuildAsync(target)` | Compiles user subroutines and **caches the binaries in the .ACD**, so a later download doesn't recompile. Target: `DefaultTarget｜PhysicalController｜EchoController`. **v37+.** |
| `DownloadAsync()` | **Offline-only**, and the controller must already be in Program mode. |
| `UploadAsync()` | **Offline-only**. Merges controller → open project file; throws if the merge fails. Leaves the controller Offline. |

The download warning is sharp and worth quoting verbatim:

> "the Logix Designer SDK does not check the controller state, so the
> operation fails if the controller is not in the required state. **In
> contrast to downloads initiated in the Logix Designer GUI, a download
> initiated by the Logix Designer SDK does not contain operations to change
> the controller mode to Program and to save changes after the download.** You
> have to execute these operations separately if they are required." [XML]

So the GUI's download is a *macro*. The SDK gives you the primitive and the
mode transitions are yours to sequence — which is good for a gated pipeline
(`logix-target.md` §15.2) and dangerous for a naive one.

`BuildAsync`'s target matters more than it looks: "Build target has to match
connected controller type (emulated or physical) **to avoid recompile**"
[XML]. CI that builds against Echo and then downloads to iron pays the
compile twice.

### 3.5 Tag values — and the number that settles the two-plane argument

`GetTagValue<TYPE>Async(tagPath, mode)` / `SetTagValue<TYPE>Async(tagPath,
mode, value)` for BOOL, SINT, INT, DINT, LINT, USINT, UINT, UDINT, ULINT,
REAL, LREAL, STRING. Plus `GetTagValueAsync` / `SetTagValueAsync` operating
on **raw bytes for any type, UDTs included**.

`tagPath` is XPath, not a tag name [DOX]:

```
Controller/Tags/Tag[@Name='foo']
Controller/Programs/Program[@Name='MainProgram']/Tags/Tag[@Name='foo']
```

`OperationMode.Offline` reads/writes **values in the project file**;
`OperationMode.Online` goes to the controller. The interaction with connected
state is a table, not a guess [DOX]:

| Connected state | Requested mode | |
|---|---|---|
| Offline | Offline | allowed |
| Offline | Online | allowed — the SDK **temporarily goes online for you** |
| Online | Offline | **forbidden**, throws |
| Online | Online | allowed |

And then:

> "**Getting tag data in online mode requires around 0.5 seconds** because
> the SDK service has to wait for the data from the controller." [DOX]

Half a second. Per tag. From the vendor's own documentation. That is the
whole of `logix-target.md` §4's two-plane argument, conceded in the manual:
**the SDK is a commissioning tool, not a transport.** Anything at scan rate
belongs on raw EtherNet/IP.

Byte semantics for the binary calls [DOX]: short data is zero-padded to the
tag length; **long data throws**. A bare `STRING`/`STRING_16`/`STRING_32` tag
returns just the characters, no length prefix — but a string *inside* a UDT
is fixed binary.

The offline binary path is quietly useful: it reads and writes UDT tag values
**in an .ACD with no controller anywhere**.

### 3.6 Introspection — thinner than you want

| Call | Returns |
|---|---|
| `GetAllExecutablesAsync()` | **An array of XPath strings for every executable (routine and AOI) in the project.** v32+. |
| `GetProcessorTypesAsync(majorRev)` | Available controller types. |
| `GetCommunicationsPathAsync()` | The current comm path. |

`GetAllExecutables` is the *only* browse-shaped call in the API. There is no
"list programs", no "list tags", no "list UDTs". **Everything else you want
to know about a project's contents, you learn by exporting L5X and reading
it** — which is precisely the gap `lang/l5x` fills, and it makes the reader a
structural complement to the SDK rather than a nice-to-have.

### 3.7 SD card, safety, content protection

- **SD card** (v34+): `StoreImageOnSDCardAsync(loadEvent, loadMode,
  firmwareUpdate, …)` and `LoadImageFromSDCardAsync()`. Both need the
  controller in Program mode with the card attached and unlocked; load is
  blocking and "could take several minutes". `RequestedLoadEvent` =
  `OnDemandOnly｜OnCorruptRAM｜OnPowerUp`; `RequestedLoadMode` = `Run｜Program`;
  `AutomaticFirmwareUpdate` = `Enabled｜Disabled`.
- **Safety** (v37+ except where noted): `IsSafetyLockedAsync`,
  `SafetyLockAsync(pw)`, `SafetyUnlockAsync(pw)`,
  `SetSafetyLockPasswordAsync`, `SetSafetyUnlockPasswordAsync`,
  `GenerateSafetySignatureAsync` (**online only**; will not regenerate over an
  existing one), `DeleteSafetySignatureAsync`, `GetSafetySignatureAsync`,
  `GetSafetyNetworkNumberAsync(module)` (backplane SNN only).
- **Content protection** (v32+, and the headline feature of 2.02.00 [RN]):
  `ProtectWithPasswordAsync` / `ProtectWithLicenseAsync` (single or batch),
  `ProtectAllWithPasswordAsync` / `ProtectAllWithLicenseAsync`,
  `UnprotectAsync`, `UnprotectAllAsync`, `LockExecutableAsync` /
  `UnlockExecutableAsync`, `LockAllAsync` / `UnlockAllAsync`,
  `IsExecutableProtectedAsync`, `IsExecutableLockedAsync`.
  License-based protection needs **a Wibu CmDongle physically present**. The
  `…AllAsync` variants fan out over `GetAllExecutables` **in parallel** and
  log rather than throw on the ones they can't touch (safety routines, for
  instance) — convenient, and a silent-partial-success hazard worth
  asserting against.

### 3.8 Logging and progress

`AddEventHandler(IOperationEvent)`, `RemoveEventHandler`,
`ClearEventHandlers`; multiple handlers per project. Shipped implementations:
`StdOutEventLogger`, `LogFileEventLogger`.

The docs are blunt that this is the debugging path: "In case of confusions it
is good to check operation events. Using that logs Logix Designer give more
detailed explanation what happened during operations." [DOX] For `logixd`,
attach a handler always and keep the stream — a failed partial import says
almost nothing through the exception and a great deal through the events.

---

## 4. What the wire protocol has that the facade does not

`ILogixProject` is a curated surface over a wider gRPC service. The generated
client (`LogixDesignerSDK.LogixSDK.LogixSDKClient`) is **public but
undocumented**, and enumerating its RPC methods from the assembly gives
52 [OBS]:

```
Build ChangeControllerMode Close Convert ConvertFromL5K ConvertFromL5X
CreateNewProject DeleteSafetySignature Download ExportToL5K ExportToL5X
GenerateSafetySignature GetAllExecutables GetCommPath GetEvents
GetLockedStatus GetProcessorTypes GetProtectedStatus
GetSafetyLockPasswordArgs GetSafetyNetworkNumber GetSafetySignature
GetSafetyUnlockPasswordArgs GetTagValue GoOffline GoOnline IsSafetyLocked
KeepAlive LoadImageFromSDCard LockExecutable MorphController Open
OpenFromL5K OpenFromL5X PartialExportToXmlFile PartialImportFromXmlFile
PartialImportRungsFromXmlFile PartialImportWithTargetFromXmlFile
ProtectExecutable ReadConnectedState ReadControllerMode SafetyLock
SafetyUnlock Save SetCommPath SetSafetyLockPassword SetSafetyUnlockPassword
SetTagValue StoreImageOnSDCard StoreImageOnSDCardWithLoadOption
UnlockExecutable Unprotect Upload UploadToNewProject
```

Three things fall out:

1. **`ExportToL5X` / `OpenFromL5X` / `ConvertFromL5X` are first-class RPCs.**
   The facade reaches them by file extension on Open/SaveAs (§3.1) — the
   format round trip is not a side effect, it is a designed operation.
2. **`KeepAlive` and `GetEvents` are session mechanics.** A long-lived agent
   holding an open project has to heartbeat; that is a `logixd` design input,
   not an optimization.
3. **The file-carrying requests are chunked** — the message set includes
   `PartialImportFromXmlFileRequest.ChunkOneofCase`,
   `ProjectOpenRequest.ChunkOneofCase` and four more [OBS]. Which bears
   directly on the 30 kB limit; see §5.

**Could a Go client speak this directly?** Mechanically, probably — it is
ordinary gRPC on a known port. Practically, three things are in the way: the
`.proto` is not shipped, the FTSP login is a separate handshake through
`FtspAdapterLDSDK.exe` (bundled in the NuGet package under `FtspAdapter/`),
and an unsupported client is a maintenance liability on a vendor API that
already dropped C++ in a minor release. **Not recommended** — but worth
knowing the door exists, because it means `logixd` could one day be a single
Go binary rather than a Go process shelling to .NET.

---

## 5. Hard limits

| Limit | Value | Source |
|---|---|---|
| Maximum project size | **500 MB** | [RN] |
| Maximum **single operation data file** | **30 kB** | [RN] |
| Minimum project version | **Logix Designer v31**; earlier is unsupported | [RN] |
| v31 projects | **Only one open at a time, across every application on the machine** | [RN] |
| Operating system | **64-bit only** | [RN] |
| Communications | **FactoryTalk Linx only. RSLinx is not supported.** The controller must already be visible in the FT Linx Network Browser. | [RN] |
| Simultaneous project *opening* | **Prohibited** — open sequentially, then operate concurrently | [DOX] |

### The 30 kB limit, and why it is probably not the problem it looks like

`logix-target.md` §14 lists spike **S4** as "find whether the documented
30 kB per-operation limit bites" for generated routines. The wire message set
answers most of it: the file-carrying requests are **chunked** [OBS], so the
client streams a large L5X as a sequence of messages rather than one. The
30 kB almost certainly bounds a *message*, not a *file*.

**That is an inference from the message shapes, not a measurement.** S4 stays
open — but it should now be cheap to close, and the expected answer is "no,
it doesn't bite."

### Per-version feature gates [DOX]

Everything below is v31 unless listed: open, convert, create, upload,
upload-to-new, change controller type, download, save, save-as, go
online/offline, read connected state, read/change controller mode, get/set
comm path, safety network number, safety signature read, is-safety-locked.

| Operation | Minimum |
|---|---|
| Content protection, lock/unlock, is-protected, is-locked, `GetAllExecutables` | **v32** |
| SFC as a partial-import target | **v32** |
| `UploadToNewProject` | **v33** |
| `DataType[@Class='*']` export | **v33** |
| SD card load/store; program tags as a partial-import target | **v34** |
| Equipment Sequence as a partial-import target | **v35** |
| **`Build`**, safety lock/unlock, safety passwords, generate/delete safety signature | **v37** |
| USINT/UINT/UDINT/ULINT/LREAL tag accessors | **v32** |

`BuildAsync` at v37 is the one that shapes the roadmap: **CI-verifies-the-logic
requires a v37+ project**, which the corpus already is.

---

## 6. Concurrency, and what it means for `logixd`

From [DOX], verbatim on the important part:

- "The Client Library supports concurrent execution… the Client Application
  can carry out LogixProject operations on multiple projects concurrently."
- "**Simultaneous projects opening is prohibited**, it is why open is done
  sequentially." — a Logix Designer restriction, not an SDK one.
- "When the Client Application operates on multiple projects simultaneously,
  an error in one project can cause **all the projects to fail**."

So the agent shape is forced, and it is a good shape:

1. A **global open mutex**. One `Open`/`UploadToNewProject` at a time,
   process-wide and ideally machine-wide.
2. A **per-project worker** afterwards; operations on distinct projects run in
   parallel.
3. **Exception isolation at the project boundary**, or one bad project takes
   the fleet down.
4. **`logixd` owns the "one writer per file" rule**, because the SDK does not
   (§2).
5. **`KeepAlive`** for any project held open across requests (§4).

---

## 7. Error model

All SDK errors are exceptions [DOX]:

```
Exception
└── LogixSdkException
    ├── ProjectException
    │   ├── OperationFailedException        the operation ran and failed
    │   ├── OperationNotPerformedException  it could not be attempted; also internal errors
    │   └── LoggerFailedException           out of space while logging
    └── EventLoggerRuntimeException         logging failed (bad path, no access)
```

The distinction that matters: **`OperationFailed` = your parameters or the
controller's state; `OperationNotPerformed` = the SDK or the channel.** The
first is a user error to report; the second is a reason to tear down the
session.

Every project-related exception carries the project path in parentheses at
the end of the message:

```
File: C:\tmp\proj\nested_export_main.xml already exist (C:\tmp\proj\nested_data_type.ACD).
```

And the licensing trap from `logix-target.md` §14 bears repeating here,
because it is the same class of problem: **three independent gates — FTSP
auth, the FlexNet `LDSDK.EXE` feature, and a CodeMeter entitlement — each
fail with a different uninformative message, and `flexsvr` reports *absent*
and *expired* features identically.** `logixd` must probe all three and name
which one failed. That is a product requirement.

---

## 8. What is genuinely absent

After a complete enumeration, these do not exist anywhere in the API — facade
or wire:

- **Any project browse.** No list-programs, list-routines, list-tags,
  list-UDTs. `GetAllExecutables` returns executable XPaths and nothing else.
  Read the project by exporting L5X.
- **Any editing primitive above import.** No "add a rung", "rename a tag",
  "create a UDT" as calls. Every structural change is an L5X document you
  construct and import — which is fine, and is what makes a *generator* the
  right shape for nautilus.
- **Any diff, compare or merge.** (Rockwell ships a separate Logix Designer
  Compare Tool; it is not in the SDK.)
- **Tag *value* preservation across a download.** `SaveAs` explicitly does not
  upload values from the controller, and download resets tags to project
  values. `logix-target.md` §15.2's open question stands: the answer, if
  there is one, is `UploadAsync` into a scratch project before the download
  and a `SetTagValue` sweep after — mechanical, not supported.
- **Anything remote.** §2.
- **Forced project unlock / steal.** No lock exists to break.

---

## 9. The correction: the SDK *does* support online import

`logix-target.md` says, in §15.1 and again in §6.2, that "the SDK exposes no
online-edit (test/assemble/accept) API" and builds §6.2's "no warm
per-program download" and §8's recommendation partly on it.

**That is wrong.** The documentation is explicit [DOX]:

> "Import online is possible only using `PartialImportWithTargetFromXmlFileAsync`
> and `PartialImportRungsFromXmlFileAsync`."

and `PartialImportOption` — the parameter those two take, which is "ignored
during offline import" — is exactly the three-state online-edit workflow
[XML]:

| | |
|---|---|
| `LeaveEdits` | leave the change as pending offline edits — **test** |
| `AcceptEdits` | accept it and send it down to the controller — **accept** |
| `FinalizeEdits` | accept, send down, and **if the controller is in Run mode, assemble** |

So a rung-level change **can** be pushed into a running controller, and so
can a whole Program, AOI definition, DataType or Tag through
`PartialImportWithTarget`.

### What this changes, stated carefully

**It rehabilitates the online path.** A ladder change is, concretely, some
rungs in a routine; `PartialImportRungsFromXmlFile` takes an insert position
and a replace count, so "replace rungs 4–6 of this routine, accept, assemble"
is one call. That is a far better story than "your only path to the
controller is a download that stops it."

**It partly rehabilitates §6.3 too.** An online edit modifies a running
program in place; it does not reset tags, because nothing is being replaced
wholesale. The "retained state cannot be migrated" claim was reasoning from
the download path, which remains true *of downloads*.

**What has NOT changed:**

- A **download** still stops the controller and resets tags to project values
  (§3.4). Nothing here softens §15.2's gating argument.
- This is **not** nautilus's warm swap. Nautilus replaces a program and
  carries retained FB state across by instance name and type, with one-step
  rollback. Logix online-edits in place under its own rules. They solve
  overlapping problems differently, and claiming parity would be exactly the
  leaky impersonation §8 warns against.
- **None of this has been executed.** It is read from the vendor's
  documentation and from the enum's own doc comments. It needs a controller,
  and Echo's activation lapsed 2026-09-06.

### Consequences to act on

1. **Promote S2b.** "Online partial import of rungs with `AcceptEdits` /
   `FinalizeEdits` against a controller in Run" is now the highest-value
   unrun spike in the plan. It gates a materially better product story.
2. **Rewrite §6.2 and §15.1** once S2b resolves — and keep the wrong version
   visible, the way §13.x keeps its three corrections, because "I read the
   overview table and not the enum doc comments" is a trap the next person
   falls into identically.
3. **N-40's premise needs revisiting.** The content idea "What the vendor
   can't do, said out loud" rests on "no online-edit API." The honest version
   of that piece is now narrower and, arguably, better: *the vendor can do
   more than its overview suggests, and the thing it actually can't do is
   different from what everyone assumes.*

---

## 10. What this means for nautilus, in one table

| Nautilus need | SDK answer | Cost |
|---|---|---|
| ACD → L5X for git | `Open` + `SaveAs("*.L5X")` | 2 calls, ~10–20 s for a 500 kB project (measured, §13.5) |
| L5X → ACD to deploy | `Open("*.L5X")` + `SaveAs("*.ACD")` | 2 calls |
| Lean, diff-friendly export | `SaveAsAsync(…, detailedL5x: false)` | free |
| Read the project's contents | **Nothing** — export L5X and use `lang/l5x` | the reader is load-bearing, not optional |
| CI: does it compile? | `PartialImport` into a working copy + `BuildAsync` | needs v37+, no controller, no risk |
| Drift: does the controller match the repo? | `UploadToNewProject` + `SaveAs(L5X)` + `l5x.Equivalent` | needs a controller, read-only |
| Push a logic change, warm | `PartialImportRungsFromXmlFile(… FinalizeEdits)` | **unverified — S2b** |
| Push a whole project | `Download` | stops the controller; gate it (§15.2) |
| Live values at scan rate | **Do not use the SDK.** ~0.5 s per tag online | raw EtherNet/IP, `eip/` |
| Set a setpoint offline, in the file | `SetTagValue*(…, Offline)` + `Save` | no controller needed — genuinely useful |

---

## 11. Verification status — read this before quoting anything above

| Status | What |
|---|---|
| **Executed** (this project, ECHO1, §13.5) | `OpenLogixProjectAsync`, `SaveAsAsync` |
| **Observed** (this session, ECHO1) | service running, port 53204 bound to loopback only, assembly contents, RPC method list, Python-is-pythonnet, package versions |
| **Documented, unrun** | everything else — every capability in §3, the online-import finding in §9, the version gates in §5 |
| **Implemented and test-covered, still unrun** | 2026-09-21: `tools/logixd` exposes the whole surface above, and `logix/logixd/integration_test.go` drives it. The SDK tests skip rather than fail, because ECHO1 has **no FactoryTalk activation** — see `logix-target.md` §18. |
| **Inferred, labelled** | that the 30 kB limit bounds a gRPC message rather than a file (§5) |

The single highest-value thing this survey did **not** do is run anything.
The gap between "the manual says" and "we have seen it" is exactly where
§13.x's three recorded corrections came from. Close it with S2a and S2b
before the capability table in §10 is quoted to anyone outside the project.
