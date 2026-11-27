# device-library — field devices declared by profile

A transfer skid with two field devices, and no driver manifest in sight. Each
row under `devices:` in `nautilus.yaml` names a **profile**: a device's
register map, cited from its public documentation. nautilus derives
everything else from it when the project loads:

- **PUMP**: an ABB ACS355 drive (FMBT-21, ABB Drives profile over Modbus
  TCP). `PUMP` is one struct tag (`PUMP.StatusWord`, `PUMP.Actual1`,
  `PUMP.Tripped`), and each writable register has a command tag
  (`PUMP_ControlWord`, `PUMP_Reference1`).
- **VALVE**: a 16-channel relay board (Modbus TCP). `points:` keeps only
  `Relay1` and `Relay2`, so only those are polled; `VALVE_Relay1` closes
  relay 1 and `VALVE.Relay1` reads it back.

The drive's trip alarm comes from the profile too (`alarms:` in
`profiles/abb-acs355-fmbt21.yaml`); the project writes none.

```sh
naut check .                  # validate offline
naut test  .                  # the sequence against the profiles' simulators, virtual time
naut devices render .         # what the rows derive: types, tags, bindings, alarms
naut devices up .             # both devices on real sockets, until ctrl-c
```

## Files

| File | What it is |
|---|---|
| `nautilus.yaml`, `skid.st` | the controller: two `devices:` rows, three tags, the sequence |
| `profiles/` | the project's pinned copies of the profiles (`naut devices add`), with the drive's simulator |
| `skid_test.yaml` | three sequence tests: run, stop, a drive trip (and its alarm) |

## Working with profiles

```sh
naut devices profiles                   # the built-in library
naut devices add <id>                   # copy one into profiles/: your pin
naut devices update                     # compare the copies with the library; --yes replaces
naut devices lint profiles/<id>.yaml    # check a profile you wrote
```

The copy in `profiles/` is the pin: a new naut never changes it. `update`
shows what the project would derive differently first, never replaces a
copy you edited unless you pass `--yes --discard-edits`, and refuses while
a program here reads a member the new profile no longer has.

## What the tests prove, and what they cannot

The controller's bindings and the virtual device come from the same profile,
so `naut test` cannot catch a wrong register **in the profile**: both sides
agree on it. It proves the logic, the start-up sequence and the scaling
direction against a device that behaves. A profile's registers are trusted
because every row cites a document page and someone who did not write the
profile checked it against the document
([`docs/design/device-library.md`](../../docs/design/device-library.md) §4).
[`bin-fill`](../bin-fill/) keeps hand-written manifests for that reason.
