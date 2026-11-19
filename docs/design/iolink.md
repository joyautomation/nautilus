# Design: IO-Link

**Context:** IO-Link (`iolink/iodd`, `iolink`), see the context map.
Downstream of the device library: a port device hangs off an IO-Link
master's `devices:` row. `iolink/iodd` is a leaf published language, like
`devprofile`; the IODD's names stop at `iolink`. Supporting work: build it
to the IO-Link standard and stop. Issue #705, part of the parity epic
#725. Status: **proposal**, §15.

## 0. Why

Nothing speaks IO-Link to the controller directly. The master speaks
IO-Link to the sensor, and the controller reaches the master over OPC UA,
Modbus TCP, a REST API or a fieldbus. Over OPC UA, as on the reference
project behind #725, each port's process data arrives as raw bytes, a hex
string or an integer, and the project decodes it with hand-written code
per maker. Nothing checks that the right sensor is on the port: a swapped
sensor's bytes are decoded as if they were the expected one's.

Every IO-Link device ships an **IODD**, an XML file that says what its
process data means (which bits, which type, what scaling) and what its
parameters are. This layer reads the IODD so that nobody writes a decode
by hand, and checks each port's identity so that a wrong sensor is a
named fault rather than wrong numbers.

**Done when** (from #705): a virtual master serving three makers' address
spaces is browsed, decoded from IODDs, identity-checked and
parameterized, with no hand-written decode.

## 1. Decisions

From #717 (James, 2026-11-17), which this layer follows:

1. **Derive at load, from a vendored copy.** `naut iolink import` copies
   an IODD into the project's `iodd/` folder. nautilus derives each port
   device's type, tags and decode from that copy when it loads the
   project. Nothing is generated into other files. The copy is the pin:
   a naut upgrade never changes how a deployed controller decodes a port.
2. **One struct tag per device, plus command tags** (device-library.md
   §1): a port device is a struct tag whose members read back the
   device, and process data out is written through command tags.

The shared format, agreed between #705 and #717 on 2026-11-18 (naut-virtdev
and naut-lead):

3. **`ports:` on the master's row** (§2): port number to a port device's
   name and IODD. `iodd:` is a plain file name in `iodd/`, as `profile:`
   is in `profiles/`.
4. **An `iolink:` block in the master's profile** (§3), beside
   `modbus:` and `opcua:`. It names the master's existing points by
   template; it never restates a node id. A new master is profile data,
   not code.
5. **Packages.** `iolink/iodd` (the IODD model, the parser, the process
   data codec) imports no other nautilus package. `devbind` may import
   it. The adapter that decodes and identity-checks a port is `iolink`'s
   and wraps the master's driver, as `devbind.ModbusDriver` wraps Modbus.

Default until James says otherwise:

6. **Test IODDs are synthetic.** The public repo commits only IODDs
   written for its tests, from a made-up "acme" vendor, to the IODD
   schema. Real vendor IODDs are never committed, because their licences
   vary; a user imports their own.

## 2. A master's ports

A master is an ordinary `devices:` row (device-library.md §2) with one
more key:

```yaml
devices:
  - name: IOLM1
    profile: pepperl-fuchs-ice3
    endpoint: 192.0.2.21
    ports:
      2: { name: BinLevel, iodd: acme-level100-20261118-IODD1.1.xml }
      5: { name: AirPress, iodd: acme-press200-20261118-IODD1.0.1.xml }
```

| Key | Meaning |
|---|---|
| `ports` | port number → port device; ports not listed are not IO-Link devices to this project (SIO, unused) |
| `ports.N.name` | the port device's tag name, an IEC identifier |
| `ports.N.iodd` | a plain file name in the project's `iodd/` folder |
| `ports.N.desc` | one line for the tag and the faceplate (optional) |
| `ports.N.pd-variant` | which of the IODD's conditional process data layouts is in use (§4.3); only for an IODD that has them |

Rules, all in `devbind` so there is one copy of each:

- The port number is between 1 and the profile's `iolink.ports`. A row
  with `ports:` whose profile has no `iolink:` block is an error.
- A port device's name joins the one name check devbind already runs:
  unique across devices, port devices and tags, and not a type or block
  name.
- `iodd:` passes `devprofile.IsFileName`: no directory, no `..`. A file
  that is not in `iodd/` is an error that says to run `naut iolink import`.
- The master's `points:` must select every point the `iolink:` block
  names for a listed port; leaving one out is an error naming the point.
  (Without it, the port could not be decoded.)

## 3. The master profile's `iolink:` block

```yaml
# pepperl-fuchs-ice3.yaml
iolink:
  ports: 8
  pd-in:  { point: "Port{n}_PdiBytes", form: bytes, doc: "pf §12.2 p. 171" }
  pd-out: { point: "Port{n}_PdoBytes", form: bytes, doc: "pf §12.2 p. 172" }
  pd-valid: "Port{n}_PdiValid"
  vendor-id: "Port{n}_VendorID"
  device-id: "Port{n}_DeviceID"
  status: "Port{n}_Status"
  # isdu: comes with PR 5
```

| Key | Meaning |
|---|---|
| `ports` | how many IO-Link ports the master has |
| `pd-in.point` | the point holding a port's raw process data in |
| `pd-in.form` | how that point carries the bytes: `hex` (text, two hex digits per byte, as on the ifm), `u32` (an unsigned 32-bit integer, as the P+F's PDI Data Unsigned32); `bytes` (octets on the wire, a ByteString or a Byte[] array: a STRING point with `wire: ByteString` or `wire: ByteArray`, holding the octets as lowercase hex, any length up to 232 octets) |
| `pd-in.doc`, `pd-in.unverified` | where the form is documented or was measured, as a point's `doc:` (a docs id, then where); or `unverified: true`. One is required: the form is a claim of its own (for `u32`, where a short image sits), and `naut devices show` prints it |
| `pd-out` | the same for process data out; absent means the master offers no PD out |
| `pd-out-valid` | a writable BOOL point that says the master's process data out is valid (the P+F's PDO Valid), with `pd-out` (optional): the adapter drives it false while the device on the port is not the expected one (§6.2) |
| `pd-valid` | a BOOL point that says the master holds valid process data in (optional) |
| `vendor-id`, `device-id` | integer points holding the attached device's identity (optional, see §6) |
| `status` | a point holding the port's mode or connection status (optional) |
| `isdu` | (PR 5) `methods` (vendor OPC UA methods, their node ids as templates under it), `opc-30120` (the IO-Link companion specification's objects), or `none`; until then the key is refused as unknown |

Every template is expanded for `n` = 1..`ports`, and `devprofile` checks
that each expansion names a point the profile has, with a type its key
can carry (`u32` an integer point, `hex` a STRING, `bytes` a STRING with a `wire:`, ids integers, `pd-valid` a BOOL). The keys are
kebab-case like the rest of the profile.

What the three masters offer (version 3 of the P+F and ifm profiles, 2 of
the SICK, PR 3b):

| Master | PD in | PD out | Ids | Status | ISDU | Notes |
|---|---|---|---|---|---|---|
| P+F ICE3 | `PdiBytes` (bytes, ByteString) | `PdoBytes` (bytes) | yes | `Status`, `Mode` | none yet | pf §12.2 pp. 171-172 document the Byte String nodes, 1 to 32 octets; the U32 points stay for projects that read them. The Byte Array twins are not served |
| ifm AL1590 | `Pdin` (hex, measured) | `Pdout` (hex, unverified) | `VendorID`, `DeviceID` (unverified) | `Status` (unverified) | none yet | only pdin was measured; the others follow its naming. The AL1427 manual spells them differently (ProcessDataOutput, VendorID, DeviceID) with no node ids |
| SICK SIG300 | `PdiBytes` (bytes, ByteArray) | `PdoBytes` | yes | `Status` | none yet | OPC 30120's variables (Byte[], UInt16/UInt32 ids, Byte status) named and typed as the NodeSet has them; every node id and the namespace index are stand-ins, `unverified` until a real SIG300's address space is exported |

OPC 30120 types process data and ISDU values as `Byte[]`, not as a
ByteString, so a profile says which it is with `wire:`; a read accepts
either, a write sends the declared one. The simulators serve each as
declared (a Byte[] node has ValueRank 1), and both refuse more than 232
octets, a read as Bad and a write as Bad_OutOfRange.

### 3.1 The `u32` form

A `u32` point carries at most four bytes. An IODD whose process data in
(or out) is longer than four bytes cannot be read through it, and that is
an error at load naming the port, the IODD's length and the form, not a
truncated value.

Where a process data image shorter than four bytes sits inside the U32
(in the low bytes or the high bytes) is not in the P+F manual's table.
PR 3 takes the low bytes, marked `unverified` in the profile, until a
real ICE3 is read: the U32 is written out as four big-endian octets and
the image is the LAST `n` of them (a 2-octet image of 0x0286 is the U32
0x00000286). The leading octets must be zero; if they are not, that is a
decode fault naming the form, so a wrong guess shows up as Bad values
rather than as plausible wrong ones. PR 3 tests both.

## 4. What an IODD gives a port device

`iolink/iodd` reads IODD 1.0.1 and 1.1 and keeps only what this layer
uses:

- **identity:** vendor id, device id, vendor and device name, and the
  device variants (`DeviceVariantCollection`);
- **process data in and out:** the record items, each with its subindex,
  bit offset, bit length, type and name; a `Condition` selecting between
  process data layouts (§4.3);
- **scaling and units:** a record item's gradient, offset and unit code
  from the user interface section (`ProcessDataRefCollection`, IODD 1.1
  only: a 1.0.1 IODD's members are unscaled);
- **value names:** `SingleValue` entries (shown on the faceplate, not
  decoded into the struct);
- **variables:** index, subindex, access rights, type, default and
  value range, for ISDU (§7). The standard variables an IODD references
  by id (`StdVariableRef`, e.g. the serial number) are resolved from a
  table in `iolink/iodd` of their spec index and type, so the IO-Link
  Community's standard definitions files are not needed at load;
- **texts:** the primary language's text for every name.

Anything else (menus, user roles, images, the communication
characteristics) is ignored, not refused.

### 4.1 The port device's type

The struct type's name comes from the IODD file name: the standard name
`<Vendor>-<Device>-<date>-IODD<version>.xml` loses its date and version
and is upper-cased with `-` as `_` (`ACME_LEVEL100`). Two port devices
with the same IODD share the type. Two IODDs that map to the same name
are an error naming both files. The name may not be a standard type or
block name, or a device's name, the check devbind already runs on a
profile's `udt:`.

Members, in this order:

1. one per process data in record item, named after its text (an IEC
   identifier made from it), or `Item<subindex>` when the text is empty
   or two items would collide;
2. `PdValid : BOOL` when the master profile has `pd-valid`.

| IODD type | Member type |
|---|---|
| `BooleanT` | `BOOL` |
| `UIntegerT`, up to 8 / 16 / 32 / 64 bits | `USINT` / `UINT` / `UDINT` / `ULINT` |
| `IntegerT`, up to 8 / 16 / 32 / 64 bits | `SINT` / `INT` / `DINT` / `LINT` |
| `Float32T` | `REAL` |
| `StringT` | `STRING` |
| `OctetStringT` | `ARRAY [0..n-1] OF BYTE` |
| an integer with a gradient or offset | `REAL`, the scaled value (raw × gradient + offset) |
| `TimeT`, `TimeSpanT`, `ArrayT` in process data | not in the first pass: refused at load, naming the item |

Process data that is a single value rather than a record is one member,
named after the process data's own text, or `Value`.

Each record item of process data out gets a command tag,
`<Port device>_<Item>`, of its member type (§5.2).

### 4.2 Unit and value names

The unit code becomes the member's `unit` (the IO-Link standard unit
table is a small Go table in `iolink/iodd`, keyed by code; an unknown
code leaves the unit empty), so the faceplate and the HMI show it. Value names go to the faceplate only.

### 4.3 Variant process data

Some devices change their process data layout with a parameter: the
IODD then has several `ProcessData` entries, each with a `Condition` on a
variable's value. The first pass needs the project to say which layout is
in use, so a port with conditional process data is an error at load
unless the row names the condition value (`ports.N.pd-variant: <value>`).
Reading that variable from the device to choose automatically comes with
ISDU (§7), and then the identity check covers it: a device whose
variable does not match the chosen layout is a wrong-device fault.

## 5. The process data codec

### 5.1 Decode

IO-Link process data is an octet string. A record item's `bitOffset`
counts from the least significant bit of the last octet: the whole
image is read as one big-endian unsigned integer, and an item occupies
bits `bitOffset` .. `bitOffset + bitLength - 1` of it. The IO-Link
Community's standard definitions show it: `V_DirectParameters_1`'s
subindex 1, the first octet, is at bitOffset 120 of 128, and subindex 16
at 0.

- **Integers:** `IntegerT` is two's complement over `bitLength` bits and
  sign-extended from its top bit.
- **`Float32T`:** IEEE 754 single, big-endian, 32 bits.
- **`StringT`, `OctetStringT`:** `fixedLength` octets; a string's
  encoding is the IODD's (US-ASCII or UTF-8), and trailing NUL octets
  are trimmed. An item of either whose bitOffset is not a multiple of 8
  is refused at load: the schema allows it, but no source we have says
  how its octets would be laid out.
- **Length:** the image the master delivers must be the IODD's length.
  A shorter image is a decode fault (the struct's quality is Bad), never
  zero-padded. The `u32` form always delivers four octets; a shorter
  image is their last octets, with the leading ones zero (§3.1).

`hex` text is decoded strictly: an even number of hex digits, nothing
else. `0286` is the bytes 0x02 0x86.

### 5.2 Encode (process data out)

A port device with process data out has a command tag per record item.
The adapter keeps the last value written to each, encodes the whole
image from them on every push, and writes it through the `pd-out` point.
An item no program has written encodes as zero. The image is not written
at all until a program has written at least one item, so a port's output
is never driven by a controller that never meant to (device-library.md
§3.2's rule for command tags). A value that does not fit its item (300
into an 8-bit item) is refused, not wrapped, and the push fails with the
tag's name.

## 6. Identity

A port device's identity is the pair (vendor id, device id) the IODD
declares. When the master profile has `vendor-id` and `device-id`, the
adapter compares what the master reports with the IODD's pair, at start
and whenever either value changes.

| State | When | The struct's quality |
|---|---|---|
| `Ok` | the ids match | from the process data (§6.1) |
| `NoDevice` | the port reports no device (ids 0, or `status` says so) | Bad |
| `WrongDevice` | the ids differ | Bad, and the bytes are not decoded |
| `AwaitingRearm` | the ids match but a port with process data out has not been re-armed (§6.2) | from the process data (§6.1); the fault stays active |
| `Unchecked` | the master profile has no id points | from the process data (§12) |

Every port device gets two companion tags, beside the drivers'
`__Online`:

- `<Port device>__Identity : INT`, the state (0 `Ok`, 1 `NoDevice`,
  2 `WrongDevice`, 3 `Unchecked`, 4 `AwaitingRearm`), always Good, so an alarm or a program
  can read it while the device's values are Bad;
- `<Port device>__IdentityFault : BOOL`, true in `NoDevice`,
  `WrongDevice` and `AwaitingRearm`, with a default alarm: "{name}: wrong,
  missing or unconfirmed device on {master} port {n}".

A `WrongDevice` is also logged once per change, naming the port, the
expected pair and the pair read, so the cause is in the log and not only
in an alarm's text.

### 6.2 Outputs, a wrong device and re-arming

A master keeps the last process data out it was given and sends it to
whatever device is on the port, including a wrong one. So not writing is
not safe, and a port with process data out is **armed explicitly**
(`iolink.OutGate`):

- a port that is **not armed** sends **nothing** (no image, not zeros
  either: off is not always safe) and, where the profile has
  `pd-out-valid`, drives it **false**, so the master stops sending the
  last image and the device falls back to its own fail-safe values;
- the **first Ok (or Unchecked) arms the port**: at a power-up, and at
  a takeover (a new leader builds new ports and checks again), an Ok
  identity is the device the program was written for, so neither needs a
  pulse. A `NoDevice` before the device has ever been seen is a power-up
  transient (an IO-Link port takes a moment to start) and does not use
  that up; a device hot-plugged into a port that was empty since the start
  also arms on its first Ok, being the device the program was written for.
  A `WrongDevice` at any time before the first Ok means the port is never
  armed by an Ok: the right device appearing later does not arm it;
- after the device has been seen Ok, **any** break while running (a
  swap, or `NoDevice` as much as `WrongDevice`) disarms the port, and only
  a **pulse on
  `<Port device>__Rearm`** arms it again (a writable BOOL tag, false then
  true): the "confirm after a device swap" an operator does, journalled by
  the audit journal like any tag write (who, when, from where), and logged
  by the adapter. A pulse while the device on the port is not the one
  expected is **refused**, naming the state, and changes nothing;
- an armed port sends the program's **current** value of every item (the
  image first and, where there is one, `pd-out-valid` true a push later,
  so the master holds the new image before it is told it is valid), and
  follows the program from then on. Nothing a program wrote before or
  during a break is replayed, and a program that holds constant values
  stalls nothing: the pulse takes the values it holds;
- until it is armed the identity reads **`AwaitingRearm`** (4) and
  `__IdentityFault` stays true, so the alarm stays active and the operator
  sees why the port is quiet;
- `Unchecked` (no id points) is armed by its first check and then writes
  as told, since nothing can be compared; `naut check` says so;
- the master's driver takes the first push after it starts as the state
  of the world and never sends it, so a port armed by its first check
  sends its image with the next push, a scan later, not with the first;
- a port with no process data out has no gate, no `__Rearm` and no
  `AwaitingRearm`.

The pulse is seen as a push in which `__Rearm` is true after one in which
it was false (the first push after a start is only the state of the
world); a pulse shorter than a scan is not seen. Under `naut test` the
in-process face sends a port's outputs through the same gate.

A master profile with no `pd-out-valid` cannot do the first step's second
half: a wrong device on that port is sent the master's last process data
out until the identity fault is seen (it is raised as an alarm) and the
program or an operator stops it. `naut devices show` says so for such a
profile (today the ifm AL1590 and the SICK SIG300). The identity is
checked when the ids arrive, so for up to one scan of the id points after
a swap the new device is sent the old image; IO-Link's own validation
and backup (a port setting on the master) is what closes that window.

### 6.1 Quality

The port device's struct is:

- **NotConnected** until the master's process data for the port has
  arrived once;
- **Bad** in `NoDevice` or `WrongDevice`, when `pd-valid` is false, or on
  a decode fault (§5.1);
- **Stale** when the master's points are Stale (the master stopped
  answering);
- else **Good**.

## 7. ISDU (acyclic parameters)

ISDU reads and writes a device variable by index and subindex, through
the master. Each master does it its own way, so the profile's `isdu:`
says which:

- `opc-30120`: OPC 30120, the OPC UA companion specification for
  IO-Link devices and masters (release 1.0, namespace
  `http://opcfoundation.org/UA/IOLink/`). A port's device is an
  `IOLinkDeviceType` object, and ISDU is its `ReadISDU(Index UInt16,
  SubIndex Byte) → (Result ByteString, ErrorType UInt16, Status Int32)`
  and `WriteISDU(Index, SubIndex, Data ByteString) → (ErrorType,
  Status)` methods. Status 0 is success; -1 another ISDU is running, -2
  the device is not active, -3 a device error with the IO-Link error in
  ErrorType. The device object's node id is a template under `isdu:`.
- `methods`: a master's own OPC UA methods, when it does not follow OPC
  30120; their node ids and arguments are under `isdu:`, cited from the
  maker's manual like every other point. PR 5 takes the P+F and ifm
  methods from their manuals; until it does, both profiles say `none`.
- `none`: no parameters from this master.

The OPC UA client gains method calls (it reads, writes and subscribes
today). `iolink` encodes a write and decodes a read with the IODD
variable's type, by the same codec as process data. Every write goes to
the audit journal (#709): who, which device, which variable, old and new
value.

The CLI: `naut iolink param get <device> <variable>` and `set`, by the
variable's name or index. The faceplate lists a port device's variables
with their values, value names and access rights. Keeping parameter
values in step with git is #718's job, built on these reads and writes.

## 8. Commands

| Command | Does | PR |
|---|---|---|
| `naut iolink import <iodd.xml> [project-dir]` | parse and check the IODD, then copy it into `iodd/` | 2 |
| `naut iolink show <iodd.xml>` | print what an IODD derives: identity, the type, members, command tags, variables | 2 |
| `naut iolink param get/set` | §7 | 5 |

`import` reuses `naut devices add`'s writer: `iodd/` must be the
project's own folder (not a symlink), files are created with `O_EXCL`,
and an existing copy is never replaced (it is the pin). It refuses an
IODD that does not parse, or none of whose process data layouts the
codec can decode (§4.1); a layout it cannot decode is named in a note,
since a device with variants may run in one it can (§4.3). Load
refuses a port whose chosen layout the codec cannot decode. Import does
not follow `xi:include` or fetch anything.

`naut build` ships `iodd/*.xml`, since load needs them, and the virtual
device fixtures never list them as fixtures (the trap #944 fixed for
`profiles/`).

## 9. Virtual devices

A virtual master serves raw process data and identity from its
simulator, as #712's masters do now. A port device has no simulator of
its own in the first pass. The process model writes the port's raw
image (bytes, a U32 or hex) through the master's simulator. ISDU answers
(PR 5) come from the IODD's variables and defaults, served by the
virtual master's method handlers (virtdev's code, naut-virtdev's review).

## 10. Not a tautology

If the simulator encoded process data with the codec under test, a
sequence test could not catch a codec bug: both sides would agree on it.
So:

1. **The codec's tests use byte vectors worked out by hand**, from the
   IO-Link rules in §5.1, never from the codec's output: a bit field that
   straddles two bytes, sign extension of a 12-bit integer, a big-endian
   float, a string with trailing NULs, a short image.
2. **The simulators serve literal bytes.** A virtual master's process
   model writes a fixed image (`16#0286`), and the test expects the
   decoded value worked out by hand from the synthetic IODD.
3. **Encode is checked against decode and against hand vectors.** A
   property test encodes random in-range values and decodes them back;
   two hand vectors pin the bit order, so encode and decode cannot be
   wrong the same way.
4. **The IODD parser is tested against IODDs written for the test**,
   one per feature, in both schema versions, plus a malformed one per
   refusal.

## 11. Packages

| Package | Holds | May import |
|---|---|---|
| `iolink/iodd` | the IODD model, the parser, the codec, the standard variable and unit tables | nothing in nautilus (a leaf, like `devprofile`) |
| `iolink` | the port device: its type and tags from an IODD and a master profile, the adapter that decodes and identity-checks, ISDU over a master | `iolink/iodd`, `devprofile`, `io`, `lang/ir` |
| `devbind` | `ports:` on a row, the name and port checks | may import `iolink` and `iolink/iodd` |

The context map gains an IO-Link row, and `internal/contextmap` a rule:
`iolink/iodd` imports no nautilus package; `iolink` keeps devbind's
distance (no `runtime`, `server`, `alarm`, `internal/project`, `virtdev`
or the CLI); field drivers import neither.

## 12. Open

- **`Unchecked`.** A master that reports no ids (the ifm today) cannot
  have its sensor checked. Proposal: such a port device's values read
  Good, its `__Identity` reads `Unchecked`, and `naut check` warns once per
  port. The alternative is Stale until ids exist, which would grey a
  working sensor on every ifm master. PR 3 adds ids to the ifm profile,
  so this is mostly a question for future masters.
- **`pd-valid` and `pd-variant`** are additions to the agreed format;
  they need naut-virtdev's agreement before PR 3.
- **The writer helper.** `existingCopy` and `createInDir` live in
  `cmd/naut/devicelib.go` and name `profiles/` in their messages.
  Making them take the folder is a small change in naut-virtdev's file.
- **`ports:` in devbind.** #717 PR 4a parses and validates it (the name
  check, the `iodd/` file, the port number against
  `(*devprofile.Profile).IOLinkPorts()`, which PR 3 here fills from
  `iolink.ports`); this layer's PR 4 consumes it.
- **`opc-30120`, not `opc-30100`.** The agreed format spelled the value
  after OPC 30100, which is not the IO-Link companion specification;
  OPC 30120 is. The spelling here follows the specification.

## 13. Sources

- IODD schema versions: the root element's namespace,
  `http://www.io-link.com/IODD/2009/11` for 1.0.1 and
  `http://www.io-link.com/IODD/2010/10` for 1.1.
- Standard variable indexes (`V_VendorName` 16, `V_ProductName` 18,
  `V_SerialNumber` 21, `V_DeviceStatus` 36, `V_SystemCommand` 2, …) and
  the bitOffset example: the IO-Link Community's
  IODD-StandardDefinitions1.1.xml. `iolink/iodd` keeps the indexes and
  types it needs as a Go table; the file itself is not vendored, since
  its terms are the Community's.
- OPC 30120: reference.opcfoundation.org/IOLink/v100/docs/.
- Not yet confirmed from a primary source, so PR 2's tests pin them by
  the hand rules in §5.1 and say so: sign extension of `IntegerT` from
  its bit length, and the scaled value being raw × gradient + offset.

## 14. Not here

- The ifm IoT Core JSON (REST) transport: it needs an HTTP transport, not
  just a profile. A follow-up once OPC UA works end to end.
- IO-Link over Modbus or PROFINET masters: the `iolink:` block allows it
  (the points are Modbus registers), but no master profile does it yet.
- IODD menus and user roles, port configuration (setting a port's mode
  from nautilus), data storage, firmware update.
- Keeping parameters in git: #718.

## 15. Plan

1. This brief, and the context map's IO-Link row.
2. `iolink/iodd`: the parser, the codec, and `naut iolink import` and
   `show`, with synthetic acme IODDs. The contextmap rule. Independent
   of the shared format's code.
3. The `iolink:` block in `devprofile` (struct, validation, JSON schema)
   and in the P+F, ifm and SICK profiles and their simulators, plus a
   profile for a master that follows OPC 30120. Reviewed by naut-virtdev through naut-lead.
4. Runtime: `ports:` in devbind, the port device's type and tags, the
   decode and identity adapter, PD out. **Waits on #717 PR 4.**
5. ISDU: OPC UA method calls, each master's mechanism, virtual masters
   answering from the IODD, `naut iolink param`, the faceplate list,
   audit.
6. The done-when test (§0).
