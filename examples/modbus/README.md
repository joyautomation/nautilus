# modbus — a generic plant polled over Modbus TCP

A manifest project on the classic field bus: two PID temperature loops
behind one gateway (same host, unit-ids 1 and 2 — the Anybus/Omron shape),
a VFD, and a four-channel gas analyser. Everything the driver polls is
generated from **one hand-written file**, `devices.yaml` — the device map:
register maps, formats and scaling once per device *type*, instances with
host/unit-id once per physical device.

```sh
naut modbus import --map devices.yaml    # regenerate the committed files
naut test -m memory.yaml .               # logic-only acceptance tests
naut modbus serve --manifest modbus_manifest.yaml --values seed.json
naut run .                               # poll the "devices" for real
```

## The workflow

**1. Import — offline.** `naut modbus import --map devices.yaml` emits
two committed, never-hand-edited files, byte-identical on every re-run:

- `modbus_manifest.yaml` — sources (host, unit-id, word order, timeouts,
  `enable:` tag) and tag bindings (table, address, format, scale,
  writable/rewrite, scan class). Decoded strictly by the driver core; a
  typo is an error, not a silently dropped binding.
- `tags/modbus.yaml` — the ordinary tag file (`role: input|output`, unit,
  desc, init), composed via `tag-files:`. Re-derive it from the manifest
  alone with `naut modbus tags modbus_manifest.yaml --map devices.yaml`.

Add `--plan` to see the block-read plan: the FTIR-style analyser's four
floats coalesce into one FC3 request instead of four.

**2. Test — no socket.** `plant_test.yaml` runs in virtual time against the
memory driver (`naut test -m memory.yaml .`): `given:` writes the
driver's input image exactly the way a block read would, so the same tests
pass unchanged once the real driver polls.

**3. Serve — a bench without hardware.** The in-repo slave stands in for
the whole plant on one listener, multi-unit like the real gateway:

```sh
naut modbus serve --manifest modbus_manifest.yaml --values seed.json --ramp
```

`seed.json` is `{tag: value}` in engineering units — serve inverts each
binding's scaling and word order on the way into the registers, `--ramp`
makes the numeric inputs drift so trends look alive. Poke it with the
commissioning tool:

```sh
naut modbus browse --host 127.0.0.1 --port 5020 --unit 3 --from 2000 --count 4
naut modbus browse --host 127.0.0.1 --port 5020 --unit 4 --word-order little --format float32 --count 8
```

(The map keeps unit ids distinct across the whole plant — TC_A=1, TC_B=2,
FAN=3, GAS=4 — because serve keys its one listener by unit id and refuses
two sources that share both a unit id and a table.)

**4. Run.** Point the manifest's hosts at the bench (or the real devices)
and `naut run .` — the driver builds the same plan `--plan` printed and
polls it. A source whose `enable:` tag (`CFG_HeatersOn`) is false is
parked, its tags held, never zeroed.
