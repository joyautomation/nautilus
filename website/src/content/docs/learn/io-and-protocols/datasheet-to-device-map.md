---
title: From a datasheet to a device map
description: "Turn a Modbus datasheet's words (coil, holding register, scaled, low word first) into device map keys, and use the browse tool to settle word order."
sidebar:
  order: 2
---

A Modbus datasheet is a table of registers in the vendor's words. This lesson
translates each word into the device map you wrote in the [last
lesson](/learn/io-and-protocols/modbus-first-read/), including the one that
catches everybody: "low word first". It takes about ten minutes, with no
hardware, using the bench device that ships inside `naut`.

## The datasheet

Here is the register table of a flow meter, as a vendor might print it:

| Address | Type | Meaning | Notes |
| --- | --- | --- | --- |
| 40001 | Holding | Flow rate, m³/h | 32-bit float, **low word first** |
| 40003 | Holding | Totaliser | 32-bit unsigned, low word first |

## Words to keys

| The datasheet says | The device map says |
| --- | --- |
| 40001, 40003 (a "4xxxx" number) | `address: 0`, `address: 2`, and `table: holding` (the default). A number starting 4 is a holding register, 3 an input register, 1 a discrete input, 0 a coil. The register number minus 40001 is the address: **40001 is address 0**. |
| 32-bit float | `format: float32` |
| 32-bit unsigned | `format: uint32` |
| "low word first", "little-endian words", "CDAB" | `word-order: little` on the device type |
| "scaled x10", "in tenths" | `scale: 0.1` |
| "write", "setpoint" | `writable: true` (a holding register or a coil only) |

So the map is:

```yaml
devices:
  flow-meter:
    word-order: little
    registers:
      - {tag: Flow, address: 0, format: float32, unit: "m3/h", desc: flow rate}
      - {tag: Total, address: 2, format: uint32, desc: totaliser}
instances:
  - {id: FM1, type: flow-meter, host: 127.0.0.1, port: 15021, unit-id: 1}
```

```sh
naut modbus import --map devices.yaml
```

The generated tags are `FM1_Flow` and `FM1_Total`: the instance id, an
underscore, the register's tag. Two meters of this type would be `FM1_…` and
`FM2_…`.

## Checking word order with the bench

Never trust the word "low word first" alone; a datasheet can be wrong or read
the other way. Start the bench device with a value you know, 1234.5:

```sh
echo '{"FM1_Flow": 1234.5, "FM1_Total": 70000}' > seed.json
naut modbus serve --manifest modbus_manifest.yaml --values seed.json --listen 127.0.0.1:15021
```

In another terminal, read the two registers of the float as a `float32`,
first the default way (high word first), then low word first:

```sh
naut modbus browse --host 127.0.0.1 --port 15021 --unit 1 --from 0 --count 2 --format float32
naut modbus browse --host 127.0.0.1 --port 15021 --unit 1 --from 0 --count 2 --format float32 --word-order little
```

```text
addr  raw          float32
0     0x5000 449A  8.60791808e+09

addr  raw          float32
0     0x5000 449A  1234.5
```

The same two registers, `0x5000` then `0x449A`, are nonsense one way and
exactly `1234.5` the other. The float 1234.5 is `0x449A5000`; the device
sends the low half, `0x5000`, in register 0, which is what "low word first"
means. With a real device you do not know the answer in advance, but the
rule is the same: whichever reading gives a believable value is the word
order. If a value is garbage whose size still looks plausible, try
`word-order: little` first; if it is still wrong, the bytes inside each
register are swapped too (`byte-order: little`).

Run `naut modbus browse` without `--format` to see every format at once,
which finds a register's real format in one look.

## Next

[Run the controller against it](/learn/io-and-protocols/modbus-first-read/#5-run-the-controller-against-it),
or read the [Modbus guide](/guides/modbus/) for writes and per-tag quality.
