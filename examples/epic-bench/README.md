# epic-bench — the office groov EPIC as nautilus I/O

An Opto 22 **GRV-EPIC-PR1** on the bench, four modules in the chassis,
driven by the nautilus runtime over the processor's own Modbus/TCP
server — first from a laptop, then from the EPIC's own Linux. Nothing
vendor-side was installed on the EPIC: no PAC Control strategy, no
CODESYS. The chassis was *imported*, not typed, and `bench.st` is the
IEC program that drives it.

| slot | module | what it is | nautilus tags |
|---|---|---|---|
| 0 | GRV-IACDCTTL-24 | 24 digital inputs | `EPIC_M0_DI00` … `EPIC_M0_DI23` |
| 1 | GRV-ODCI-12 | 12 digital outputs | `EPIC_M1_DO00` … `EPIC_M1_DO11` (writable) |
| 2 | GRV-IICTD-12 | 12 ICTD temperature inputs, °C | `EPIC_M2_AI00` … `EPIC_M2_AI11` |
| 3 | GRV-IV-24 | 24 voltage inputs, ±160 V | `EPIC_M3_AI00` … `EPIC_M3_AI23` |

## How the files got here

```sh
naut optommp browse --host 192.168.82.243   # what the processor reports: modules, channel types, live values
naut optommp import --host 192.168.82.243   # → modbus_manifest.yaml + tags/optommp.yaml
```

`naut optommp` talks OptoMMP (TCP 2001), the memory-mapped protocol every
groov EPIC, groov RIO and SNAP PAC answers, and reads the chassis the
processor itself sees: module type and channel type per slot, the
channel-present mask, the current values. From that it writes a Modbus
manifest onto the processor's Modbus/TCP map (form 1678: digital state at
coil/discrete `512 + slot*64 + ch`, analog values as float32 at input
register `4608 + (slot*64 + ch)*2`) and the tag file to compose. The
runtime then needs nothing new: it is the `modbus` driver.

## Run it

```sh
naut check .     # validate offline — the block-read plan is built from the manifest, no EPIC needed
naut test  .     # the acceptance suite in virtual time (epic-bench_test.yaml)
naut run   .     # poll the EPIC for real: dashboard + tag API on http://localhost:8086
```

`bench.st` mirrors the first eleven inputs to the first eleven outputs,
puts a 1 Hz heartbeat on output 11 (`BlinkPeriodS` is an operator
setpoint), and copies the two wired analog channels onto `BenchTempC` and
`BenchVoltsV`.

Verified 2026-10-23 on the bench: with `naut run` up, the EPIC's own coil
587 (slot 1 channel 11) read straight off the processor toggled
`1100110011000110` over four seconds, 100 ms scan, four Modbus blocks per
cycle; the ICTD probe read 19.7 °C over Modbus and 20.2 °C over OptoMMP at
the same moment.

## On the EPIC itself

The same project runs *on* the processor. The EPIC is a quad-core ARMv7
(Cortex-A9 class, VFPv3/NEON) running Opto's Yocto Linux 4.1.3 on a
PREEMPT_RT 5.15 kernel with 1.8 GB RAM and 4 GB free on `/`, so the
static ARM build of `naut` runs unchanged:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -trimpath -ldflags='-s -w' -o naut-arm ./cmd/naut
scp naut-arm <shell-user>@192.168.82.243:nautilus/naut
scp -r examples/epic-bench <shell-user>@192.168.82.243:nautilus/
ssh <shell-user>@192.168.82.243 'cd nautilus/epic-bench && ../naut run .'
```

Shell access needs Opto 22's free **GROOV-LIC-SHELL** license (groov
Manage → System → License) and then a shell user created and the shell
server started on the Shell Access page — the shell user is its own
account, not the groov Manage admin. The shell user has sudo with a
password; nothing here needs it.

Verified 2026-10-24 on the bench: `naut run` on the EPIC, polling its own
Modbus server at `192.168.82.243:502` (the generated manifest's address;
`127.0.0.1` works the same), ran the heartbeat at 1 Hz on coil 587 as
read from a laptop, at 4 % of one core and 17 MB resident with the
EPIC's own stack (Ignition Edge, PAC Control engine) still running
beside it.

That run also found a runtime bug: the EPIC reports every **unwired
ICTD channel as NaN**, and one NaN tag made `/api/state` answer 200 with
an empty body and the SSE stream skip every frame — a blank dashboard
with no error anywhere. Fixed on this branch: a REAL that is not a
number renders as JSON `null`, the Modbus driver marks such a tag's
quality `bad` (the link is fine, the reading is not), and the state
endpoint answers 500 with the encoder's reason instead of silence if
anything else ever fails to render.

The processor's Ignition Edge on 8088 and PAC Control engine on 22001
are not touched.
