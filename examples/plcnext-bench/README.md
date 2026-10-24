# plcnext-bench — the nautilus runtime on the office PLCnext

A Phoenix Contact **AXC F 2152** (ARMv7, 2 cores, 512 MB, PREEMPT_RT
Linux, firmware 2023.0.0 LTS) with four Axioline F modules on its local
bus. The nautilus runtime runs **on the controller** and drives those
modules; Phoenix Contact's IEC engine is idle and PLCnext Engineer is
never opened. The seam is the firmware's own gRPC data-access service on
`/run/plcnext/grpc.sock`, and the bus is brought up by
[BusConductor](https://github.com/PLCnext/BusConductor), Phoenix Contact's
open component for configuring an Axioline bus at run time without
Engineer, which presents the whole process image as two byte arrays:

| GDS port | what |
|---|---|
| `Arp.Io.AxlC/0.DI4096` | the input image, every module's input process data in bus order |
| `Arp.Io.AxlC/0.DO4096` | the output image, written whole |

`plcnext_manifest.yaml` binds nautilus tags to byte and bit offsets in
those arrays; `naut plcnext tags` derives `tags/plcnext.yaml` from it.

## Status: built and tested against a fake firmware, not yet run on the bench

What is verified (2026-10-23):

- `naut` cross-compiles to a 22 MB static ARMv7 binary with the `plcnext`
  driver in it (`CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7`).
- The driver's handshake, image decode, bit-merge writes and quality are
  pinned by `go test ./plcnext/` against an in-process gRPC server that
  answers like the firmware's `IDataAccessService` with BusConductor
  behind it.
- SSH to the controller works (user `admin`), the socket exists, the
  firmware's gRPC library registers `IDataAccessService` and
  `ISubscriptionService` (the `IAxioMasterService` methods are present
  but answer `Unimplemented` on this firmware, which is why the bus goes
  through BusConductor and not through DDI commands).

What the controller needs before the first run, and why it was not done
in the session that built this: the controller currently carries a
hand-written per-module TIC configuration from an earlier experiment
(`/opt/plcnext/projects/PCWE/Io/Arp.Io.AxlC/{1..4}.tic`) that the
firmware **rejects at boot** (`Output.log`: `AXIO conf with neg. result:
cmd 0x0711, errCode 0x0c13 … Abort download`), so no `Arp.Io.AxlC` port
exists today. The fix is the documented BusConductor deployment, which
is a copy of files plus **one restart of the plcnext runtime**, an action
the build session was not permitted to take on its own:

```sh
# on the laptop, with github.com/PLCnext/BusConductor cloned beside this repo
B=../BusConductor
ssh admin@192.168.82.216 'cd /opt/plcnext/projects && tar czf /opt/plcnext/PCWE.bak.tgz PCWE && rm -f PCWE/Io/Arp.Io.AxlC/*.tic PCWE/Io/Arp.Io.AxlC/links.xml'
scp -r $B/generic_axioline/Io/Arp.Io.AxlC/.    admin@192.168.82.216:/opt/plcnext/projects/PCWE/Io/Arp.Io.AxlC/
scp    $B/generic_axioline/Plc/Gds/PCWE.gds.config admin@192.168.82.216:/opt/plcnext/projects/PCWE/Plc/Gds/
scp    $B/generic_axioline/Plc/Eclr/eCLR.img    admin@192.168.82.216:/opt/plcnext/projects/PCWE/Plc/Eclr/
scp    $B/no_engineer/Plc/Esm/PCWE.esm.config   admin@192.168.82.216:/opt/plcnext/projects/PCWE/Plc/Esm/
scp    $B/no_engineer/Plc/Plm/pcwe.plm.config   admin@192.168.82.216:/opt/plcnext/projects/PCWE/Plc/Plm/
scp    $B/no_engineer/Plc/Meta/PCWE.meta.config admin@192.168.82.216:/opt/plcnext/projects/PCWE/Plc/Meta/
# Libs/BusConductor (libBusConductor.so, same md5 as upstream) is already on the controller
ssh admin@192.168.82.216 'sudo /etc/init.d/plcnext restart'
```

(`Plc/Gds`, `Plc/Eclr` and `Services/OpcUA` directories may need creating
first; the controller's `Output.log` from 2025-04-05 shows exactly this
BusConductor component running on this firmware, so the library is
known-good here.)

Then, on the controller:

```sh
./naut-arm plcnext browse          # CONFIGURED=true NUM_MODULES=4, then the first bytes of both images
```

and the offsets in `plcnext_manifest.yaml` can be checked against what
the modules actually put there — the module list in the manifest is what
the earlier experiment's TIC files recorded (device types `0x00400002`,
`0x00800002`, `0x00000100`, `0x00200008`: 16, 16, 0 and 32 bits of
process data), not a scan this driver has made. Analog scaling is left
raw (`int16` counts) until a known voltage has been read back.

## Run it

```sh
naut check .     # on the laptop: validates the manifest offline, no controller needed
naut test  .     # the acceptance suite in virtual time

CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -o naut-arm ./cmd/naut
./naut-arm build -o controller .                       # this project embedded on the ARM binary
scp controller admin@192.168.82.216:/opt/plcnext/apps/nautilus/
ssh admin@192.168.82.216 /opt/plcnext/apps/nautilus/controller   # dashboard + tag API on :8080
```

`bench.st` mirrors the sixteen inputs to fifteen outputs, puts a 1 Hz
heartbeat on output 15, and echoes analog input 0 to analog output 0.

## Timing, honestly

The firmware refreshes the images from the Axioline bus every 500 µs;
this driver polls them over the local socket at `scan-rate` (50 ms here;
the gRPC round trip for a 512-byte array is on the order of a
millisecond on this CPU). That is remote-I/O-grade, not motion-grade, and
it is the same tier as a PLCnext Engineer program reading the GDS from a
non-real-time thread. The deterministic tier — an Arp C-API program
exchanging the GDS buffer inside the ESM cycle, the shape of Phoenix
Contact's own `SampleRuntime` — is the next step and is described in
`docs/design/open-controllers.md`.
