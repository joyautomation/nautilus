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
2. The plan is printed: the verdict, the station changes, whether
   hardware is loaded, what happens to each OB, the program blocks
   outside nautilus's group that the download loads too (a customer's,
   `Main` at the root), and every answer the download will give TIA, in
   words.
3. You type `yes`, or pass `--yes`, which answers exactly what was printed.
4. The download runs. It counts as done only when an online compare right
   after it is equal. The deploy record is printed and the TIA project is
   saved for the next deploy.

Answers are never implied. Each acting answer has its own flag:

| Flag | Answer |
|---|---|
| `--allow-stop` | TIA may stop the CPU to load. Without it, a load that needs STOP is cancelled and loads nothing |
| `--reinit DB,...` | the DBs TIA may reinitialize. Their values go back to start values, retained ones too |
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
| STRUCT types, in a library file or the program | written inline where they are used, with the TYPE's initial values and a tag's `init:` map as start values; a whole struct is copied member by member |
| TON, TOF, TP, R_TRIG, F_TRIG, CTU, CTD, CTUD | the IEC instructions as multi-instances; counters are 64-bit |
| SR, RS | written inline on a static |
| bitwise AND/OR/XOR/NOT, bit access `.3`, shifts and rotates | written on the bit string of the operand's width |
| integer arithmetic | 64-bit, wrapped at the store, as nautilus does |
| division and MOD by anything but a non-zero constant | through a generated function that answers 0 for a zero divisor, as nautilus does |
| TRUNC, `TO_<type>`, MIN, MAX, MUX | written as the explicit conversion or instruction |

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

## What does not translate yet

These are gaps in the writer, not rules for you. The diagnostic reads
"the Siemens writer doesn't translate X yet (#N)", and N is the issue
that closes it.

| Issue | Not translated yet |
|---|---|
| #1299 | enums, arrays |
| #1301 | user FUNCTIONs and FUNCTION_BLOCKs, library FBs, `VAR CONSTANT` |
| #1302 | SFC. It will be written as an SCL state machine first |
| #1303 | STRING and the string functions |
| #1304 | TIME arithmetic, LTIME, dates |
| #1305 | the rest: ULINT and LWORD arithmetic, ATAN2, integer ABS, and more |
| #1310 | a task's `dt-tag`: nothing on the CPU writes it yet, so integrators and PI loops built on it would read 0 |

Real limits keep their own words, such as a task interval over 60 s or a
block name collision.

## Where the CPU behaves differently

In a few cases nautilus faults the scan and the CPU does not. The CPU
sets ENO to FALSE and carries on. These are not refused. `naut check
--target siemens` and `naut siemens deploy` warn about them, in two ways:

- A MUX or a shift is warned at its own line with `[siemens/divergence]`.
- REAL to integer conversions and TRUNC are common and seldom provably in
  range, so they are one summary line per program, `[siemens/conversion-range]`:
  "N REAL → integer conversions can differ on Siemens if a value exceeds
  its range". `naut check -v` and `naut siemens deploy -v` list the sites.

A constant that is provably in range never warns.

| Construct | No warning when | nautilus | S7-1500 |
|---|---|---|---|
| `MUX(K, IN0, ...)` | K is a constant in range | K out of range faults the scan | MUX sets ENO FALSE, the scan carries on |
| `SHL`, `SHR`, `ROL`, `ROR` | the count is unsigned or a constant | a negative count faults the scan | the count is converted to TIA's unsigned count, beyond the width |
| `TRUNC`, `TRUNC_<int>` (summary line) | the argument is a constant in range | a value beyond the integer (or NaN) faults the scan | the conversion sets ENO FALSE, the scan carries on |
| `REAL_TO_<int>`, `TO_<int>` of a REAL (summary line) | the same | the same | the same |

Only the out-of-range case differs. The rounding matches: a REAL converts
to an integer half to even, and TRUNC goes toward zero, both as TIA's
documentation describes. This comes from the documentation and has not
been measured on a CPU yet.
