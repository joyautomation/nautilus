---
title: Siemens S7-1500 target (experimental)
description: Write the program in nautilus, compile and download it to an S7-1500 through TIA Portal Openness, and see what the Siemens writer translates today and what it does not yet.
---

:::caution[Experimental]
The Siemens target is under construction. The commands and the list of
what translates will change. Read [What does not translate yet](#what-does-not-translate-yet)
before you plan a project around it.
:::

With `target: siemens`, nautilus is the IDE and an S7-1500 is the runtime.
You write structured text, ladder or FBD in nautilus, in git. nautilus
writes it as SCL. **tiad**, an agent on a Windows machine with TIA Portal
V21, uses TIA Portal Openness to build the project, compile it and
download it to the CPU. Nobody opens TIA Portal to deploy.

```
nautilus project ──▶ naut check --target siemens
                 ──▶ naut siemens deploy ──▶ tiad (Windows, TIA V21) ──▶ S7-1500
```

`naut` runs on Linux or wherever you work. Only tiad needs the Windows
machine.

## Set up

### The manifest

Add a `target: siemens` section to `nautilus.yaml`. A project with this
section gets the Siemens rules in `naut check` without a flag.

```yaml
target:
  siemens:
    agent: http://192.0.2.50:8190   # tiad's URL
    block-prefix: naut_             # optional, this is the default
    station:
      cpu:
        name: PLC_1
        order: 6ES7 510-1DK03-0AB0
        firmware: V3.1
        address: 192.0.2.10
        subnet-mask: 255.255.255.0
        profinet-name: plc-1
      web-server:
        enabled: true
        users:
          - name: naut
            rights: [read, write, mode]
```

- `agent` is where tiad listens. `NAUTILUS_TIAD_URL` or `--agent` override
  it. Without any of them, the default is `http://127.0.0.1:8190`.
- `block-prefix` goes in front of every block, DB and tag table nautilus
  generates (`naut_Main`, `naut_Tags`). It keeps a generated block from
  colliding with TIA's own `Main` OB or with blocks you already have. It
  must start like an SCL identifier. An empty prefix is refused.
- `group` names the block group nautilus owns in the TIA project. The
  default is `nautilus`.
- `station` describes the CPU's whole hardware configuration. A download
  replaces the whole configuration, so a CPU has exactly one station
  spec. Two projects aimed at one CPU would erase each other.
- `cpu.configuration-protection` and `cpu.access-control` have defaults
  (a password, and access control on with one user, `nautilus`).
  `keep` leaves the setting of an adopted project as found. The default
  never turns on access control that an adopted project has off: that
  takes `naut siemens deploy --turn-on-access-control` (below).
- The station can also list local `modules` and a `profinet` section with
  devices.

Passwords and tokens are never in the manifest or on the command line.
They come from the environment, or from a file named by the same variable
with `_FILE` added:

| Variable | What |
|---|---|
| `NAUTILUS_TIAD_TOKEN` | tiad's token (or `NAUTILUS_TIAD_TOKEN_FILE`) |
| `NAUTILUS_SIEMENS_CONFIG_PASSWORD` | the CPU's configuration-protection password |
| `NAUTILUS_SIEMENS_FULL_ACCESS_PASSWORD` | the CPU user `nautilus` |
| `NAUTILUS_SIEMENS_WEB_PASSWORD_<USER>` | each web server user |
| `NAUTILUS_SIEMENS_PASSWORD` | the web user that `target.siemens.online.user` names; also what `naut siemens diag` and `serve` log in with |
| `NAUTILUS_SIEMENS_PROTECTION_PASSWORD` | a protected CPU's password |

A refusal names the variable it is missing.

### Install tiad

tiad is installed from source on the Windows machine, because the
Siemens Openness assemblies exist only where TIA Portal is installed.
From `tools/tiad/`, in an administrator PowerShell:

```powershell
.\install-tiad.ps1 -Targets 192.0.2.10
```

`-Targets` lists the only CPU addresses tiad will go online or download
to. The script builds tiad with the machine's own compiler, adds the
account to the "Siemens TIA Openness" group, writes the Openness
allowlist entry, writes the token to `%ProgramData%\tiad\tiad.token`,
registers a scheduled task that starts the agent at logon, and checks the
agent with its health and probe calls. Neither check starts TIA. Someone
must be logged in at the console for the agent to run.

Copy the token to the machine where you run `naut` and point
`NAUTILUS_TIAD_TOKEN_FILE` at it.

To see a block in TIA Portal's own window, install with `-Ui`. An agent
installed that way refuses every download. Run the installer again
without `-Ui` to deploy.

## The commands

### naut check

```
naut check --target siemens
```

It runs the same rules the writer uses. A construct the writer does not
translate is reported here, before anything reaches tiad.

### naut siemens deploy

```
naut siemens deploy [flags] [dir]
```

With no flag, it **builds and compiles only**. Nothing reaches the CPU
and the TIA project is not saved. It writes the SCL, has tiad build the
project from the station spec, imports the sources into the nautilus
block group, makes the cyclic interrupt OBs and compiles hardware and
software. Each TIA compile message is printed against the nautilus file
and line it came from.

`--download` also downloads to the CPU at `target.siemens.station.cpu.address`.
It reaches the CPU, so tell everyone who shares it first. In order:

1. The CPU is compared with its last deploy record. A CPU that was changed
   outside nautilus is refused unless you pass `--accept-outside-changes`.
2. The data layout check (below) says whether the download keeps the
   CPU's data. When it may reset data blocks and you did not pass
   `--reset-data`, the deploy stops here, naming why and the data blocks,
   and prints the command to rerun.
3. The plan is printed: the verdict, the station changes, whether
   hardware is loaded, what happens to each OB, the program blocks
   outside nautilus's group that the download loads too (a customer's,
   `Main` at the root), the data layout check, the CPU's mode when the
   project names a web user (`target.siemens.online`) and
   `NAUTILUS_SIEMENS_PASSWORD` is set, and every answer the download will
   give TIA, in words.
4. You type `yes`, or pass `--yes`, which answers exactly what was printed.
5. The download runs. It counts as done only when an online compare right
   after it is equal. The deploy record is printed and the TIA project is
   saved for the next deploy.

Answers are never implied. Each acting answer has its own flag:

| Flag | Answer |
|---|---|
| `--reset-data` | you accept that **the load may return data blocks to their start values, retained ones included**. The data layout check says when a download needs it, and the plan names the data blocks. With `--allow-stop` too, TIA's reinitialize question, which stops the CPU, is answered yes |
| `--allow-stop` | TIA may stop the CPU to load. Needs `--reset-data`. Without it, a load that needs STOP is cancelled and loads nothing; a CPU already in STOP is loaded without that question |
| `--start` or `--no-start` | one of them is required: whether TIA starts the CPU after the load |
| `--hardware` | load the hardware configuration too. Needs `--attest-hardware "<statement>"`; `--cpu-access-control disabled\|enabled` is your observation of the CPU's access control now |
| `--turn-on-access-control` | turn on the access control of a CPU whose project has it off (below). With `--download`, needs `--hardware` and `--cpu-access-control disabled` |
| `--trust-certificate SHA256` | the CPU certificate TIA may trust |

**First deploy to a CPU that already runs a program.** A new project built
from the station spec cannot load software only onto such a CPU, because
its hardware, users and devices differ. `--adopt` says where the CPU's own
project comes from. It is never guessed:

- `--adopt FILE.zap21`: a project archive (TIA's Project > Archive). This is
  usually what you have.
- `--adopt last-deploy`: the archive of the CPU's last deploy record.
- `--adopt upload`: an upload of the CPU's program. It is read-only, but it
  reaches the CPU even without `--download`. The first upload from a CPU
  pins its identity (order number and firmware), and every later online
  call to it is checked against that pin. tiad cannot read a CPU's
  identity online yet, so until it can, those calls (`--download`,
  `naut siemens verify`, `show --online`) need
  `--attest-order "<order number>" --attest-firmware <version>`, both read
  off the CPU itself (its label or display). tiad compares them with the
  pin, refuses any difference, and audits every use; the deploy record
  keeps it.

`--adopt` is for a first deploy. Once a download took the adopted
project, later deploys build on it and `--adopt` is refused. Blocks,
users and devices that are not nautilus's stay as found.

The first download keeps the CPU's data. Before anything is imported, the
deploy compares the adopted project with the CPU. When they are equal,
tiad records the data layout of the project as found (the deploy prints
`data layout baseline: the adopted project as the CPU runs it`), and the
first download compares with that. Nautilus only adds its own blocks, so
that download needs no `--reset-data`: it loads only what changed, the
CPU stays in RUN, and the plan lists every block it adds (see **Keeping
the CPU's data** below). A block of the adopted project that tiad cannot
read (know-how protected, or not compiled when it was archived) counts as
changed, and then that first download needs `--reset-data`. So does every
download to a PLC that holds a technology object or a software unit (see
below). With `--adopt upload` the first download needs `--reset-data`
too, for now: a CPU last loaded by an older TIA version may hold blocks
that this TIA rebuilds when nautilus compiles, and tiad cannot see that.

**Turning access control on for an adopted CPU.** If the adopted
project's access control is off, the default `access-control` is refused,
and the refusal names the flag. `--turn-on-access-control` turns it on:
the user `nautilus` gets Full access, with its password from the secret
store. Turning it on also gives these a say:

- the project's other local users: they can log on with their own rights.
  The plan lists them.
- the project's anonymous user: once access control is on, anyone who
  reaches the CPU gets this user's rights without a password. If it is
  active and holds anything beyond reading (write, mode change, HMI or
  read access, Full access, or rights tiad cannot read), the deploy is
  refused: remove those rights in the project, or leave access control
  off. If it holds read rights only, the plan prints a warning.

Without `--download` it only builds, and saves nothing. With `--download`
it needs `--hardware` and `--cpu-access-control disabled`: the project
loads only by a hardware load that resets the CPU's user management,
resting on your observation that the CPU's access control is off now. It
does not go with `--dry-run`. Run it once. Later deploys find access
control on and need no flag. tiad journals the turn-on, and refuses to
save the project before that load. If that download ends in doubt, the
project stays marked "turn-on pending" (the deploy prints a NOTE) and
loads only with `--hardware`: pass `--cpu-access-control disabled` if the
CPU still asks no password, or `enabled` if the load went through.

`--keep DIR` keeps the generated SCL sources.

**Keeping the CPU's data.** A download loads only what changed. That
keeps the values in the CPU's data blocks, retained or not, as long as no
data block's layout changed. A layout is what TIA keeps of a block or a
data type besides its code: its variables, their types and start values,
its number, its retain settings. When a layout changed, TIA returns the
data blocks it reaches to their start values. With the CPU in RUN it asks
first. With the CPU already in STOP it does not ask (measured on an
S7-1500 with TIA V21, #1341).

So before every download tiad reads the layout of every block and data
type in the PLC, a customer's too, and compares it with the layout it
recorded when it loaded the deploy the CPU still holds. Step 1 proves the
CPU holds that deploy. On the first deploy after `--adopt` it compares
with the adopted project instead, which the deploy compared equal to the
CPU before the import. The plan says what it found:

- `data layout: unchanged since deploy d-...` (or `since the adopted
  project (adopted:...)`): the download needs no `--reset-data`, and the
  CPU keeps its data. A change to code only, such as a new statement, is
  this case.
- the same line, then `adds to the CPU: OB naut_OB_main (OB30, cyclic
  interrupt, 100 ms), FB naut_main, ...` and `nothing already on the CPU
  changes its data layout, so its data is kept`: the download only adds
  blocks and data types (OBs, FBs, FCs, data blocks, PLC data types). It
  needs no `--reset-data`: a new block holds no data the CPU has, and
  nothing already on the CPU uses it yet. Check the list: it is exactly
  what goes onto the CPU besides the code changes.
- `data layout: N change(s) since deploy d-...`, then each change
  (`ADDED`, `CHANGED`, `REMOVED`, `UNREADABLE`) and `data blocks whose
  values may be lost: ...`: the download needs `--reset-data`, and
  `--allow-stop` when the CPU is in RUN, since TIA stops it to
  reinitialize. A new variable in a
  program, a new tag, a new member in a data type, a changed start value
  and a block removed are all such changes. An added block is listed with
  them when something else changed too. A data type's change names every
  data block that holds it.
- `data layout: no baseline (...)`: the download needs `--reset-data`,
  and the whole program loads, so every data block returns to its start
  values. This is the case for the first nautilus download to a CPU
  without `--adopt`, for an adopted project the compare took no layout
  of (the deploy prints why), for a CPU changed outside nautilus, after a
  TIA upgrade, and for any deploy recorded before this check existed.

A hardware load (`--hardware`) always needs `--reset-data`. So does every
download to a PLC that holds a technology object or a software unit: tiad
does not read their layout yet, so it cannot say they are unchanged.

`--reset-data` without `--allow-stop` lets TIA reset the named data blocks
only when the CPU is already in STOP. With the CPU in RUN, TIA stops it to
reinitialize them, so the download is cancelled there and loads nothing.
Pass `--allow-stop` only when a stop is acceptable, not by habit.

### naut siemens verify

```
naut siemens verify [--deploy ID] [dir]
```

Asks whether the CPU still runs a deploy. tiad runs TIA Portal's own
online compare on a copy of that deploy's archive. It is read-only, but it
goes online to the CPU. By default it compares with the CPU's newest
record. It exits 0 only when the CPU is equal, 1 when it is not or the
compare could not say, and 2 on a usage or configuration error. tiad holds
one project at a time, so close any open session first.

### naut siemens show

```
naut siemens show [--online] <block> [dir]
naut siemens show --close [dir]
```

Opens the project the last deploy saved and shows a block in TIA Portal's
own editor, on the agent's screen. `<block>` is the TIA block name, such
as `naut_Main` for a program (with the default prefix) or `naut_Tags`
for the tags. It needs an agent installed with `-Ui`; any other agent is
refused before anything opens.

`--online` also takes the project online to the CPU in TIA's window. TIA
then shows its own online/offline compare and the CPU's state. It loads
and writes nothing. The project stays open, so another `show` reuses it.
`--close` takes it offline and closes it. Close it before `verify` or
`deploy`.

For a demo, the order is deploy, `naut siemens verify`, then
`naut siemens show --online <block>`.

## What translates today

nautilus's own lowering runs first, so what nautilus cannot run is refused
before anything else. The writer then follows one rule: write what
nautilus computes exactly, or refuse. A difference is either matched or
refused at check time, with one exception, the table below.

| nautilus | Siemens |
|---|---|
| Program | an FB (`naut_Main`) with an instance DB, called from the task's OB |
| Each task | a cyclic interrupt OB at its cycle, 1 ms to 60 s: OB30 to OB38, then from OB123 |
| A tag two or more tasks use | a copy per task, as nautilus keeps one: the task's OB copies the tag in before its program runs and writes back only what the scan assigned (see [More than one task](#more-than-one-task)) |
| Manifest tags | one global DB, `naut_Tags`, with each tag's `init:` as its start value (I/O tags as a PLC tag table come later) |
| STRUCT types, in a library file or the program | a PLC data type each (`naut_Motor`), in the PLC data types group named like the block group, written only when something uses it (declared but unused types are not written). The type carries the TYPE's initial values; a tag's `init:` map is written as start values in `naut_Tags`. Your blocks can take a STRUCT as an input, output or in-out, and a function can return one. A whole struct is copied whole |
| Enumerations (`TYPE Mode : (Idle, Run := 10);`) | TIA has no enumerated type, so an enumeration is the integer nautilus carries for it: a `DInt`, the Int32 nautilus's OPC UA server serves every enumeration as, whatever base type you declare (`(A, B) INT`), or an `LInt` when a value needs 64 bits. Each member a block names is a `VAR CONSTANT` of that block, `<Enum>_<Member>` (`#Mode_Run`), so the SCL still reads by name, CASE labels included. A variable, member or tag starts at the type's initial member, or at the member its initial value or `init:` names. `TO_INT` and the other integer conversions of an enumeration convert its integer; `TO_Mode(n)` keeps n's value, so n must fit the enumeration's integer |
| TON, TOF, TP, R_TRIG, F_TRIG, CTU, CTD, CTUD | the IEC instructions as multi-instances; counters are 64-bit. A timer `t` has a static beside it, `naut_t_last`, that holds its `Q` and `ET` as its last call left them: the CPU's timer updates `Q` and `ET` whenever the program reads them, while nautilus returns the last call's values, so every read of `t.Q` or `t.ET` reads the static. A program that writes a timer's `Q` or `ET` is refused. The first deploy of a program with timers after this change needs `--reset-data` (the block gained the statics) |
| SR, RS | written inline on a static |
| SFC | the state machine nautilus itself runs, written into the program's FB: each step's flag (`_S_Fill_X`) and timer are statics of the FB, named as in nautilus. On the CPU, every scan of every conformance chart matched nautilus's, at a task interval that puts no timer preset on a scan boundary (a timer that ends exactly on a scan may end one scan apart, as on any two clocks), and a STOP to RUN starts the chart over at its initial step, as in nautilus. A change that leaves the FB's statics as they were, such as a named transition's condition or an action's statements, loads in RUN and keeps the active step. A new step, qualifier or timed association, a first `Step.T` read, or a line added above an unnamed transition (its hidden name carries its line number) changes them: the deploy asks for `--reset-data`, which starts the chart over. Name your transitions to keep edits live |
| PID | nautilus's own PID, as an FB `naut_PID` written line for line from nautilus's (on a CPU, fed the same inputs with DT set, every output matched nautilus's exactly). Its REAL pins are LREAL, since a nautilus REAL holds 64 bits; a REAL tag that reads CV gets TIA's "accuracy may be lost" warning. A PID declared `VAR RETAIN` starts over after a restart, as in nautilus |
| bitwise AND/OR/XOR/NOT, bit access `.3`, shifts and rotates | written on the bit string of the operand's width |
| integer arithmetic | 64-bit, wrapped at the store, as nautilus does |
| division and MOD by anything but a non-zero constant | through a generated function that answers 0 for a zero divisor, as nautilus does |
| TRUNC, `TO_<type>`, MIN, MAX, MUX | written as the explicit conversion or instruction. TIA has no REAL or LREAL conversion to or from TIME, so those go through DINT (`DINT_TO_TIME(REAL_TO_DINT(x))`), TIA's TIME being a DINT of milliseconds; a REAL into TIME gets a warning, since the CPU's TIME ends at about 24.8 days, and a constant beyond that is refused |
| STRING, LEN, LEFT, RIGHT, MID, CONCAT, INSERT, DELETE, REPLACE, FIND | TIA's `String` and string instructions. A String holds 254 bytes, so a literal longer than that is refused, and a result that may grow longer is warned (see below). Where the CPU would return an empty string for a position below 1 (or a negative length), the writer clamps the argument first, as nautilus does |
| integer and BOOL conversions to STRING, `STRING_TO_` an integer, REAL or LREAL | the same text as nautilus (`42`, not TIA's `+42`; `TRUE`/`FALSE`), through a generated function `naut_STR_LINT` where needed |

Field drivers (Modbus, OPC UA client, EtherNet/IP, MQTT, IT) do not run
on a Siemens target. They run in `naut`.

## More than one task

nautilus runs each task's scan on its own copy of the tags it shares with
other tasks. At the end of the scan only what the scan assigned is written
back, member by member and bit by bit. A scan never sees another task's
half-finished scan, and a tag a scan did not assign keeps what another
task or an operator wrote meanwhile.

The Siemens program does the same. A tag that only one task uses stays in
`naut_Tags`, and its program reads and writes it there. For a tag that two
or more tasks use, each of those tasks gets a DB, `naut_Scan_<task>`,
which holds:

- the task's copy of each such tag;
- a flag for each part of the tag the program can write, set by the
  statement that writes it.

The task's OB copies the tags in, calls the program, writes back what
the flags mark, and clears them.

All the task OBs run at the priority TIA gives a new cyclic interrupt OB
(13 on TIA V21), so none interrupts another. That keeps the copy-in and
the write-back in one piece. It also means a fast task can wait behind a
slow one, which nautilus's tasks never do. Giving faster tasks a higher
priority is a later step. Until then, don't change the priority of the
`naut_` OBs in TIA: the copies stay whole only while no task OB can
interrupt another. A task's `priority:` in `nautilus.yaml` (a Linux
real-time priority) has no effect on a Siemens target.

## Your own blocks and functions

Each `FUNCTION_BLOCK` your project declares (in a library file, in a
program's file, or from a `dialect:` such as the Logix `TONR`) becomes an
FB named `naut_<Name>`. Each instance lives inside the block that declares
it, as a multi-instance, so in TIA `m101` reads as `#m101` inside
`naut_Main`, the way it reads in nautilus. Each `FUNCTION` becomes an FC
that returns its result. `VAR CONSTANT` stays `VAR CONSTANT`, and a
library's global constant is copied, with its name and value, into each
block that uses it.

Only the blocks your programs use are written. A library block nothing
calls is left out, and it refuses nothing.

A block called from a program with several tasks can't read a manifest
tag inside its body: each task works on its own copy of the tags it
shares. Pass the tag to the block as a pin instead.

## What does not translate yet

These are gaps in the writer, not rules for you. The diagnostic reads
"the Siemens writer doesn't translate X yet (#N)", and N is the issue
that closes it.

| Issue | Not translated yet |
|---|---|
| #1299 | arrays, a function writing its own STRUCT input (TIA hands a function a STRUCT input by reference, so the write would reach the caller's variable) |
| #1303 | WSTRING, and a STRING literal with characters outside ASCII |
| #1375 | the conversions to and from STRING TIA has no match for: REAL and LREAL to STRING (TIA writes `+2.150000E+1` where nautilus writes `21.5`), TIME, BYTE, WORD, DWORD, LWORD, and STRING_TO_BOOL |
| #1304 | TIME arithmetic, LTIME, dates |
| #1305 | the rest: type aliases, converting an enumeration to anything but an integer (such as `TO_STRING`), ULINT and LWORD arithmetic, ATAN2, integer ABS, `VAR RETAIN` inside a block, an instance of your own block in a program's `VAR RETAIN`, reading or writing an instance's in-out from outside it, a manifest tag named inside a block when the project has several tasks, and more |

Real limits keep their own words, such as a task interval over 60 s or a
block name collision.

## Where the CPU behaves differently

In a few cases nautilus and the CPU part ways: nautilus faults the scan
where the CPU carries on (it sets ENO to FALSE, or uses 0), or a string
grows past the 254 bytes the CPU holds. These are not refused. `naut check
--target siemens` and `naut siemens deploy` warn about them:

- A MUX or a shift is warned at its own line with `[siemens/divergence]`.
- REAL to integer conversions and TRUNC are common and seldom provably in
  range, so they are one summary line per program, `[siemens/conversion-range]`:
  "N REAL → integer conversions can differ on Siemens if a value exceeds
  its range". `naut check -v` and `naut siemens deploy -v` list the sites.
- A CONCAT, INSERT or REPLACE whose result may grow past 254 bytes is one
  summary line per program too, `[siemens/string-length]`: TIA keeps the
  first 254 bytes, nautilus keeps them all.
- A `STRING_TO_` number is warned at its line with `[siemens/string-parse]`.

A constant that is provably in range never warns.

| Construct | No warning when | nautilus | S7-1500 |
|---|---|---|---|
| `MUX(K, IN0, ...)` | K is a constant in range | K out of range faults the scan | MUX sets ENO FALSE, the scan carries on |
| `SHL`, `SHR`, `ROL`, `ROR` | the count is unsigned or a constant | a negative count faults the scan | the count is converted to TIA's unsigned count, beyond the width |
| `TRUNC`, `TRUNC_<int>` (summary line) | the argument is a constant in range | a value beyond the integer (or NaN) faults the scan | the conversion sets ENO FALSE, the scan carries on |
| `REAL_TO_<int>`, `TO_<int>` of a REAL (summary line) | the same | the same | the same |
| `CONCAT`, `INSERT`, `REPLACE` (summary line) | the operands are literals of 254 bytes or fewer in all | the result keeps every byte | the result keeps the first 254 bytes |
| `STRING_TO_INT` and the other `STRING_TO_` numbers | never | text that is not a number, or is out of range, stops the program (the scan faults) | the PLC silently uses 0 or a partial number (`'4x'` reads as 4, `'2,5'` as 25.0) and carries on |

For the conversions, only the out-of-range case differs. The rounding matches: a REAL converts
to an integer half to even, and TRUNC goes toward zero, both as TIA's
documentation describes. This comes from the documentation and has not
been measured on a CPU yet.

## When a deploy stops

**`this download needs --reset-data`.** The data layout check found a
change that may reset data blocks, or no baseline to compare with. The
message names the data blocks. If losing their values is acceptable, run
the command it prints. If not, take the change out, or move the values
somewhere safe first.

**`no layout baseline`.** tiad has nothing it trusts to compare with. The
reason follows in brackets:

| Reason | What to do |
|---|---|
| no verify by this agent found the CPU equal to its last deploy | tiad was restarted since the deploy compared the CPU. Run the deploy again: it compares the CPU first |
| the verify is more than an hour old | the plan waited too long. Run the deploy again |
| the CPU's newest load is another deploy | a download ran since the compare, from another session or machine. Run the deploy again |
| the CPU's newest load loaded another project | another project loaded this CPU last. Deploy once with `--reset-data`. (The first deploy after `--adopt` compares with the adopted project instead: see the next line) |
| the adopt compare took no baseline | the deploy's compare of the adopted project took no layout; the reason follows (a call changed the project first, an agent with TIA's window, a deploy record line tiad cannot read). Fix that and run the deploy again, or deploy once with `--reset-data` |
| a download reached the CPU since the compare that took the session's adopt baseline, or the CPU's deploy journal has a newer line | something loaded the CPU after the deploy compared it. Run the deploy again |
| recorded no data layout | the CPU's last deploy was made by an older tiad. Deploy once with `--reset-data` |
| a TIA upgrade, or a change of the scheme | tiad reads layouts differently now, so no recorded layout counts. Deploy once with `--reset-data` |
| lines the agent cannot read | the deploy record is damaged. Find out why before you deploy |

**`GATE MISS`.** The check found no layout change, so the download went
ahead without `--reset-data`, and then TIA asked to stop the CPU,
reinitialize data blocks or load every block anyway. tiad never answers
those without your consent: it cancels the download at that question.
When TIA asks before it loads anything, which is how it has behaved so
far, nothing was loaded and the CPU kept its data. This catch works only
with the CPU in RUN: with the CPU in STOP, TIA asks nothing and resets the
changed data blocks without a word, so there the layout check is the only
protection. Then:

1. Report it on #1341 with the deploy's output. The check missed a change
   TIA sees, and that is a bug.
2. If the reset is acceptable, deploy again with `--reset-data`.

The agent's log has one line per miss, starting `audit layout-gate-miss`,
with the question TIA asked and the deploy the check compared with. The
line before each download, `audit layout-gate`, records what the check
found. If the deploy says `OUTCOME UNKNOWN` instead, tiad had already
answered another question yes, so TIA may have loaded: compare first
(`naut siemens verify`), as the deploy says.
