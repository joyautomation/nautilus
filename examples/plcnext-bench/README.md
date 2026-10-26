# plcnext-bench — the nautilus runtime on the office PLCnext

A Phoenix Contact **AXC F 2152** (ARMv7, 2 cores, 512 MB, PREEMPT_RT
Linux, firmware 2023.0 LTS) with four Axioline F modules on its local
bus. The nautilus runtime runs **on the controller** and drives those
modules; Phoenix Contact's IEC engine is idle and PLCnext Engineer is
never opened. The image crosses through `rt/plcnext-io`'s in-process
shim, which touches the Axioline frames through the firmware's ANSI-C
API every millisecond and shares them with naut through a
shared-memory page (`transport: shm`); the firmware's gRPC data-access
service on `/run/plcnext/grpc.sock` carries the handshake, and can carry
the image too (`transport: grpc`). The bus is brought up by
[BusConductor](https://github.com/PLCnext/BusConductor), Phoenix Contact's
open component for configuring an Axioline bus at run time without
Engineer, which presents the whole process image as two 512-byte arrays.

## Status: running on the bench (2026-10-24)

`naut run .` on the controller, `transport: shm`: the bytes go through
[`rt/plcnext-io`](../../rt/plcnext-io/)'s `nautilus_io`, the
ACF-started shim that reads and writes the Axioline frames every
millisecond through the firmware's own ANSI-C API, by way of its
shared-memory page. BusConductor reports four modules, the bus runs,
and `bench.st`'s 1 Hz heartbeat on the DO16 module's output 15 shows in
the shim's own view of the output frame (`naut plcnext shm --watch`:
byte 2 toggles `00`/`80` every 500 ms). The image costs 0.03–0.05 ms a
scan instead of an ~11 ms gRPC round trip, the inputs are 0.1–1.1 ms
old when the program sees them, and naut uses 4 % of one core at the
50 ms scan (9 % on gRPC) and 20 MB, beside the shim's 3 % and 17 MB, on
a controller whose firmware already idles at load 4–5. A 5 ms scan
works too but costs naut 21.5 % and disturbs the best-effort shim's
timing (rt/plcnext-io/README.md has the numbers), so the project stays
at 50 ms.

Fail-safe, tried on the bench: kill naut and the shim drives the output
frame to zero 1 s later (`output-watchdog`); restart the plcnext runtime
and naut notices the shim's new start, runs the BusConductor handshake
again and carries on.

`transport: grpc` is still there (and is the default): it reads the
images through the GDS mirror ports below. With the shim deployed the
shim is the output frame's only writer, so on grpc the inputs still
read but the outputs no longer reach the bus.

Not yet seen with eyes: the DO16 LEDs. The output frame toggles, but a
channel only switches when the module's UP supply is wired, and the bus
reports "Supply missing (Up)" on slot 4 (so the analog module reads
`0x8020`, Axioline's supply-missing marker, on both channels). Look at
slot 2 while it runs; wire UP to slot 4 for the analogs.

## How the image reaches the service

Three facts about this firmware, each found by trying it (the design
brief has the evidence):

- The data-access service (and the OPC UA server) resolve **MainProcess
  ports only**: eCLR variables, ESM, PLM components. The I/O components'
  own ports — `Arp.Io.AxlC/0.DI4096`, even the standard PROFINET
  diagnostics — answer `DAE_NotExists`. I/O data reaches a service only
  through a **GDS connector** into a MainProcess port.
- It reads and writes **array elements by name** (`Port[17]`) and
  refuses an array as a whole (`DAE_NotSupported`). The driver reads the
  bytes its bindings reach as one multi-item `Read` per scan and flushes
  the output span as one `Write`.
- BusConductor's `CONFIG_REQ` / `START_IO_REQ` act on a **rising edge
  sampled every 100 ms**; the driver holds each low for 300 ms, raises
  it, leaves it high, and skips a bus that is already configured and
  running, so a reconnect never restarts good I/O.

The connector, with no Engineer project and no SDK: a bare controller's
eCLR exposes the PROFINET-device system variables
`Arp.Plc.Eclr/PND_S1_INPUTS` and `PND_S1_OUTPUTS`, 512 bytes each, which
`PCWE.gds.config` normally wires to the (unused) PROFINET device. The
copy in `controller/` re-points those two connectors at
`Arp.Io.AxlC/0.DI4096` and `DO4096`, and the driver's default `inputs` /
`outputs` are the mirrored ports. The tidy versions — a 20-line C++
component with two array ports (needs the PLCnext SDK), or an empty
Engineer program with two array ports (needs Engineer once) — are the
same shape and can replace it without touching the driver.

| GDS port | what |
|---|---|
| `Arp.Plc.Eclr/PND_S1_INPUTS` | mirror of the input image, every module's frame in bus order |
| `Arp.Plc.Eclr/PND_S1_OUTPUTS` | mirror of the output image, written by the driver |
| `Arp.Plc.Eclr/AXIO_DIAG_STATUS_REG_RUN` | the bus is exchanging process data; false makes every input Bad |
| `BcComponent1/BusConductor.{CONFIG_REQ,CONFIGURED,START_IO_REQ,NUM_MODULES}` | the handshake |

**Layout, measured:** in BusConductor's generic mode every module takes
its frame length in BOTH images — DI16 2 bytes, DO16 2 bytes, the 0-bit
device nothing, AI2/AO2 4 bytes — so inputs are DI16 @0, AI @4..7 and
outputs are DO16 @2..3, AO @4..7. `plcnext_manifest.yaml` says so;
`naut plcnext tags` derives `tags/plcnext.yaml` from it.

## Deploy, from nothing

```sh
# 1. BusConductor as its own project, from upstream, with this example's three edited files on top
git clone https://github.com/PLCnext/BusConductor /tmp/bc
mkdir -p /tmp/bcp && cp -r /tmp/bc/generic_axioline/. /tmp/bc/no_engineer/. /tmp/bcp/
cp controller/PCWE.gds.config   /tmp/bcp/Plc/Gds/        # the two Axioline connectors
cp controller/pcwe.plm.config   /tmp/bcp/Plc/Plm/        # library path inside this project dir
cp controller/PCWE.opcua.config /tmp/bcp/Services/OpcUA/ # GdsPortsToProvide=All (optional: lets an OPC UA client see the GDS)
tar czf BusConductor-project.tgz -C /tmp/bcp . && scp BusConductor-project.tgz admin@192.168.82.216:/opt/plcnext/

# 2. on the controller: install it, point `current` at it, restart the runtime (~1 min)
ssh admin@192.168.82.216
mkdir -p /opt/plcnext/projects/BusConductor
tar xzf /opt/plcnext/BusConductor-project.tgz -C /opt/plcnext/projects/BusConductor
cd /opt/plcnext/projects && rm current && ln -s BusConductor current
sudo /etc/init.d/plcnext restart

# 3. nautilus
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -trimpath -ldflags='-s -w' -o naut-arm ./cmd/naut
scp naut-arm admin@192.168.82.216:/opt/plcnext/apps/nautilus/naut
scp -r examples/plcnext-bench admin@192.168.82.216:/opt/plcnext/apps/nautilus/
ssh admin@192.168.82.216 'cd /opt/plcnext/apps/nautilus && ./naut plcnext browse'   # CONFIGURED=true NUM_MODULES=4 RUN=true + both images
# 4. the shim (transport: shm) — build, deploy and recovery in rt/plcnext-io/README.md;
#    before its restart, pin the shim to CPU 1 and naut to CPU 0 (its README's lateness table says why)
ssh admin@192.168.82.216 'cd /opt/plcnext/apps/nautilus && echo 1 > io/nautilus_io.cpu && echo 0 > naut.cpu'
ssh admin@192.168.82.216 /opt/plcnext/apps/nautilus/naut plcnext shm            # status 0x0f: PLC started, frames, RUN
ssh admin@192.168.82.216 /opt/plcnext/apps/nautilus/plcnext-bench/controller/naut-keepalive.sh   # dashboard + tag API on :8080
```

**Surviving a reboot.** The controller has no systemd and `admin`
cannot write `/etc/init.d`, but it may keep a crontab (cronie 1.6.1,
`S90crond`, which starts before `S99plcnext`). `controller/naut-keepalive.sh`
starts `naut run` for this project unless it is already running (pid in
`naut.pid`, log in `naut-run.log`, one 1 MB generation kept), so

```sh
ssh admin@192.168.82.216 'printf "@reboot %s\n* * * * * %s\n" \
  /opt/plcnext/apps/nautilus/plcnext-bench/controller/naut-keepalive.sh \
  /opt/plcnext/apps/nautilus/plcnext-bench/controller/naut-keepalive.sh | crontab -'
```

starts it at boot and restarts it within a minute of a crash; `crontab
-r` takes it out. Installed on the bench: a killed naut was back at the
next minute, and after a `sudo reboot` naut, the shim and the heartbeat
were all back about a minute after the controller answered ssh, with no
hand on it. naut need not wait for the runtime: the driver retries
the socket and the page with backoff. It is deliberately not an ACF
`<Process>` like the shim — a declared ACF process that exits during
start-up takes the whole runtime down, and naut can exit (a bad project,
a busy port).

`admin`'s sudo on a PLCnext is an allowlist; `/etc/init.d/plcnext` is on
it, `rm`/`ln` are not, and the projects directory is admin-owned, so the
symlink needs no sudo. Rollback: point `current` back at `PCWE` and
restart. The controller's `Output.log` shows the result: `Component
'BcComponent1' from library 'BusConductor' created`, `Found
Auto-Config-Device … skip Axioline Bus Config`, `GDS data connections
successfully created`, `PD enabled`.

Commissioning tools, all run on the controller:

```sh
./naut plcnext read  Arp.Plc.Eclr/AXIO_DIAG_STATUS_REG_PF "Arp.Plc.Eclr/PND_S1_INPUTS[4]"   # any GDS name, element or scalar
./naut plcnext write "Arp.Plc.Eclr/PND_S1_OUTPUTS[2]" 128                                   # one byte, as its own type
./naut plcnext browse --no-handshake --bytes 16                                              # just look
```

## Run it

```sh
naut check .     # on the laptop: validates the manifest offline, no controller needed
naut test  .     # the acceptance suite in virtual time
```

`bench.st` mirrors the sixteen inputs to fifteen outputs, puts a 1 Hz
heartbeat on output 15, and echoes analog input 0 to analog output 0.

## Timing, honestly

The firmware refreshes the Axioline frames every 500 µs. On `transport:
shm` the shim copies them every 1 ms and naut's scan reads the latest
copy; the program's own scan (50 ms here) is the rate that matters. On
`transport: grpc` each poll is one round trip of ~11 ms for this
manifest's eight bytes, remote-I/O grade.

The shim's own timing depends on its scheduling. Out of the box it is
best-effort (`admin` cannot grant SCHED_FIFO): pinned to CPU 1 with
naut on CPU 0, about 1 cycle in 800 later than 200 µs on a quiet
controller and a third of them under a stream into its Ethernet. After
the owner's one-time `setcap` step (rt/plcnext-io/README.md, Real-time
priority) it runs FIFO 80: no cycle later than 200 µs quiet or beside
two busy loops, 3 % under the stream and none by more than 1 ms. On the
bench it is set, and the deployed `nautilus.yaml` gives the scan task
`priority: 40`; this copy does not, because on a controller without the
capability the task would report it as an error.
