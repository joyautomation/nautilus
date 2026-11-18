---
title: Toradex Verdin (experimental)
description: Run nautilus on a Toradex Verdin ARM64 module under Torizon OS — installed, updated and rolled back with naut deploy, with real-time and boot timings measured on the bench unit.
---

:::caution[Experimental]
Verified on one bench unit: a Verdin AM62 Dual 1GB (two Cortex-A53 cores
at 1.25 GHz) on a carrier board, running Torizon OS 7.8.0 (kernel 6.6,
`PREEMPT`, not `PREEMPT_RT`; systemd 255; Docker 25). Other ARM64 Verdin
modules may run the same `linux_arm64` binary; none has been tried. Nothing here makes nautilus a safety controller; read
[What it does not do yet](#what-it-does-not-do-yet) first.
:::

A Verdin is a system-on-module: on a carrier board it is an industrial
ARM64 PC, a common host for a soft PLC. nautilus needs nothing from it
beyond Linux. The release's `linux_arm64` naut is the runtime, a
controller built for it is one static file, and `naut deploy` installs it
as a systemd service, updates it and rolls it back from any machine.

The I/O is on the network: Modbus TCP, OPC UA and EtherNet/IP devices,
or Sparkplug over MQTT. The carrier's own GPIO, CAN and serial ports are not
nautilus drivers.

## What you need

- A Verdin on a carrier, on the network, running **Torizon OS** (or any
  Linux with systemd, `sh`, coreutils and `tar`).
- An SSH user that signs in with a key and is root or has `sudo` without
  a password. `naut deploy` runs its on-box half as root.
- `naut` on your own machine (Linux, macOS or Windows; any architecture).
  It builds the ARM64 controller there.

### A module that shipped with only the installer

A new module may boot the **Toradex Easy Installer** instead of an OS.
Its debug console (the carrier's USB debug port, 115200 baud) has a root
shell where `ip addr show` gives its address, and its interface is
reachable over VNC (port 5900) or a display. This was done once, by
hand, on the bench unit; two things are worth knowing:

- Its default feed offers **Torizon OS Easy Pairing**: Torizon OS plus a
  container that shows a code for pairing with Torizon Cloud. For a box
  that never talks to Torizon Cloud, install plain **Torizon OS** ("no
  containers pre-provisioned"). If the list does not show it, add a feed
  under **Feeds**: a URL serving an `image_list.json` of the form
  `{"config_format":1,"images":["<the plain image's image.json URL>"]}`,
  which Toradex publishes next to the Easy Pairing image
  (`…/verdin-am62/torizon/torizon-docker/teziimage/image.json` for the
  AM62).
- The install asks you to accept the SoC vendor's firmware licence.

On Torizon OS the user is `torizon`. Its first login forces a new
password. Then add your key to `~/.ssh/authorized_keys`, and give the
deploy passwordless `sudo` with a drop-in, checked before it goes in
place:

```sh
printf 'torizon ALL=(ALL) NOPASSWD: ALL\n' > /tmp/90-naut-deploy
sudo visudo -cf /tmp/90-naut-deploy && \
  sudo install -m 0440 /tmp/90-naut-deploy /etc/sudoers.d/90-naut-deploy
```

That rule is broad: `torizon` can then run anything as root without a
password. `naut deploy` needs root on the box, so anything narrower must
still let it run its on-box script; a scoped helper mode for exactly that
is being added (#771). Remove the drop-in when the box no longer takes
deploys.

## Install, update and roll back

From the project's directory on your machine:

```sh
naut deploy install torizon@verdin     # build for linux/arm64, install nautilus.service, start
naut deploy update torizon@verdin      # rolls itself back (exit 3) if the new release does not start
naut deploy rollback torizon@verdin
naut deploy status torizon@verdin
naut deploy backup torizon@verdin      # retained values
```

`naut deploy` asks the box its architecture and builds the controller on
naut's own `linux_arm64` release, checked against the release's
`checksums.txt`. The [deployment guide](/guides/deployment/#to-a-box-over-ssh)
has the whole of it: the release layout, retained values, the unit, and a
CD workflow for a deployment repository.

Torizon OS is an OSTree system: `/usr` is read-only and replaced by OS
updates, and nothing `naut deploy` writes is there. `/opt` is a link to
`/var/rootdirs/opt`, and `/var/lib` and `/etc/systemd/system` are
writable. OSTree keeps `/var` and merges `/etc` across an OS update, so
the releases, retained values and unit should survive one; an OS update
with nautilus installed has not been tried.

Measured on the bench unit with `tools/deploy/check.sh` (from an amd64
machine on the same network):

| step | time |
|---|---|
| build + install | 12.5 s |
| build + update | 7.8 s |
| rollback | 4.5 s |
| an update whose first scan hangs, rolled back | 73 s (the unit's 60 s start timeout, then the old release) |

The check also deploys a release that cannot start, a failed install and
a redeploy while the controller is down, and asserts after every step
which release answers and that a retained setpoint survived. All pass on
the bench unit.

## Real-time on the AM62

`priority:` and `cpu:` work under the unit `naut deploy` installs: it
runs as a dynamic user holding `CAP_SYS_NICE`. With
`priority: 80` and `cpu: 1` on a task, its thread was seen at
`SCHED_FIFO` 80 on CPU 1 (`ps -L -o cls,rtprio,psr`), with no refusal in
the journal.

Measured with `tools/jitter` on the bench unit (2026-11-18): each row is 5
minutes, about 300,000 slots of a 1 ms task. "Loaded" is both cores
saturated at normal priority: two busy loops and a loop writing to the
eMMC. Only the 1 ms task is given `SCHED_FIFO` and a core; the other
tasks stay at normal priority.

**The fast tasks alone** (`-slow 0 -alloc 0`, the mix behind the
[real-time guide's](/guides/real-time/) desktop numbers):

| 1 ms task | box | slots ran | p50 | p99 | p99.9 | max late |
|---|---|---:|---:|---:|---:|---:|
| normal, unpinned | quiet | 99.62 % | 84 µs | 408 µs | 1.41 ms | 19.4 ms |
| FIFO 80, CPU 1 | quiet | **99.97 %** | 26 µs | **172 µs** | **320 µs** | 11.6 ms |
| normal, unpinned | loaded | 76.8 % | 80 µs | 7.04 ms | 12.0 ms | 44.2 ms |
| FIFO 80, CPU 1 | loaded | 95.8 % | 54 µs | 528 µs | 9.98 ms | 54.8 ms |
| FIFO 80, CPU 1, `performance` governor | loaded | 99.1 % | 66 µs | 456 µs | 1.41 ms | 26.9 ms |

**The full mix** (adds a 100 ms task running about 20 ms of logic, and an
allocating 50 ms task):

| 1 ms task | box | slots ran | p99 | p99.9 | max late |
|---|---|---:|---:|---:|---:|
| normal, unpinned | quiet | 95.1 % | 2.11 ms | 8.96 ms | 24.3 ms |
| FIFO 80, CPU 1 | quiet | 96.3 % | 1.06 ms | 9.98 ms | 21.6 ms |
| normal, unpinned | loaded | 71.9 % | 9.47 ms | 22.0 ms | 79.3 ms |
| FIFO 80, CPU 1 | loaded | 93.7 % | 1.06 ms | 14.1 ms | 114 ms |
| FIFO 80, CPU 1, `performance` governor | loaded | 94.2 % | 912 µs | 15.4 ms | 381 ms |

What the numbers say about this module and kernel:

- **`priority:` with `cpu:` is what pays**, as on the desktop: on a
  quiet box the 1 ms task's p99 drops from 408 µs to 172 µs, and it
  misses 101 slots in 300,000. It is still a stock `PREEMPT` kernel on a
  1.25 GHz A53: the tail reaches milliseconds, against the desktop's
  20 µs p99 and 0.3 ms worst case. With `GOMAXPROCS=4` (below), a quiet
  box with the full mix reaches p99 96 µs and p99.9 188 µs, but its worst
  case, 11.6 ms, is far from the desktop's 0.3 ms. Plan on
  10 ms tasks or longer, or 1 ms only where an occasional late scan is
  acceptable.
- **The `performance` governor helps under load.** With both cores busy,
  the FIFO task's missed slots fell from 4.2 % to 0.9 % and its p99.9
  from 9.98 ms to 1.41 ms. It is set at runtime
  (`/sys/devices/system/cpu/cpufreq/policy0/scaling_governor`) and does
  not survive a reboot by itself.
- **Set `GOMAXPROCS=4` on a two-core module.** Go runs code on as many
  scheduler slots as there are cores, two here. A task's thread waking at
  `SCHED_FIFO` still needs a free slot before it runs, and a long scan in
  another task, or the garbage collector, can hold both. With the full
  mix, 1 ms task at FIFO 80 on CPU 1, 3 minutes a row:

  | `GOMAXPROCS` | box | slots ran | p99 | p99.9 | max late |
  |---|---|---:|---:|---:|---:|
  | 2 (the default) | quiet | 95.8 % | 1.02 ms | 9.22 ms | 61.5 ms |
  | **4** | quiet | **99.92 %** | **96 µs** | **188 µs** | 11.6 ms |
  | 8 | quiet | 99.81 % | 100 µs | 216 µs | 60.4 ms |
  | 2 (the default) | loaded | 93.8 % | 1.44 ms | 14.1 ms | 133 ms |
  | 4 | loaded | 97.5 % | 440 µs | 4.74 ms | 145 ms |

  Under the unit, the unit reads `/etc/nautilus/env`
  (`EnvironmentFile=-/etc/nautilus/env`; the `-` makes the file
  optional). Create it as root and restart:

  ```sh
  sudo mkdir -p /etc/nautilus
  printf 'GOMAXPROCS=4\n' | sudo tee /etc/nautilus/env >/dev/null
  sudo systemctl restart nautilus
  ```

  In a container, `-e GOMAXPROCS=4` should do the same; only the unit was
  measured. Under full load the worst case stays above 100 ms: the collector's pauses (up to 25 ms) and
  the runtime's own threads at normal priority are starved by the busy
  loops.
- **Two cores is little headroom.** Fully loaded, a task without
  priority missed a quarter of its slots. Keep the box's other work
  (containers, a historian, a browser on the display) light, or
  measure with it running.

## In a container

Torizon OS runs Docker, and a controller can run as a container instead
of the systemd service. The image is the controller on a distroless base,
with retained values on a volume:

```dockerfile
FROM gcr.io/distroless/static-debian12:nonroot
COPY plant /controller
# /state is the working directory (retain.json). Shipped owned by the
# image's user, so a named volume mounted there starts writable.
COPY --chown=nonroot:nonroot state/ /state/
WORKDIR /state
ENTRYPOINT ["/controller"]
```

Build the controller with `naut build -runtime <a linux_arm64 naut>`,
create an empty `state/` directory beside it, then on the box:

```sh
docker build -t plant:1 .
docker run -d --name nautilus --restart unless-stopped \
  --network host --ulimit rtprio=80 \
  -v nautilus-state:/state plant:1
```

- **Priority without a capability.** `--ulimit rtprio=80` lets the
  controller, a non-root user holding no capabilities, put its tasks at
  `SCHED_FIFO` up to 80: seen at 80 on CPU 1. `--cap-add SYS_NICE` alone
  does not do it for a non-root user (Docker adds the capability to the
  bounding set, not the ambient one); the runtime logs the refusal and
  runs the task at normal priority.
- **Update and roll back by tag.** Stop with `docker stop -t 15 nautilus
  && docker rm nautilus`, then run the `docker run` line with `plant:2`,
  or `plant:1` to go back. Retained values stay on the volume: a setpoint
  written under `:1` was there under `:2` and again under `:1`. `docker
  stop` sends `SIGTERM`, on which the controller makes its last retained
  save, while `docker rm -f` kills it without that save; that difference
  is expected from how the controller stops, not measured in a
  container.
- **It comes back after a reboot** through the restart policy, with its
  retained values.

What the container path gives up against the systemd path:

- **No hang detection (expected; not staged in a container).** Docker
  restarts a container that exits, not one whose scan hangs. The systemd
  unit's watchdog restarts a hung scan (seen under a test unit, not on
  the Verdin), and `naut deploy update` rolls back a release whose first
  scan never finishes (seen on the Verdin).
- **A slower start (measured).** Docker itself became active about 36 s
  after the kernel started, so the first good scan came 48–52 s after a
  reboot command, against 35–37 s for the service.

Use the service unless the box's other software is managed as containers
and nautilus should be too.

## From power-on to the first good scan

**Before nautilus runs**, nothing on the box writes an output: what the
field devices do is their own loss-of-communication setting (a Modbus
device's watchdog, an EtherNet/IP connection timeout, a PROFINET
device's substitute values). Set those for the time the box takes to
boot.

**When it starts**, before the first scan:

- Retained values load before the first scan and before the tag API
  answers.
- An output with an `init:` value is written on the first scan. A typed
  output without one starts at its type's zero but is not written to its
  driver until something writes it: the logic, or an operator.
- With no redundancy, the first scan does not wait for a driver's inputs
  to be live; it runs on what the drivers hold. (A redundant pair's
  takeover does wait, bounded; see the
  [redundancy guide](/guides/redundancy/).)
- Under the `Type=notify` unit, systemd counts the service as started
  when its first scan has completed.

**How long it takes.** Measured with `tools/deploy/boottime.py` and the
`tools/deploy/boot-probe` project, five software reboots of the bench
unit, the last two with `GOMAXPROCS=4`. These are **warm** boots: the
module stayed powered.

| | reboot command → first good scan (seen from another machine) | kernel start → naut started | kernel start → first scan |
|---|---|---|---|
| warm reboot 1 | 36.6 s | 20.0 s | 21.2 s |
| warm reboot 2 | 35.6 s | 21.0 s | 22.4 s |
| warm reboot 3 | 37.3 s | 22.2 s | 24.4 s |
| warm reboot 4 (`GOMAXPROCS=4`) | 35.1 s | 20.6 s | 22.0 s |
| warm reboot 5 (`GOMAXPROCS=4`) | 36.9 s | 21.6 s | 23.5 s |

- naut itself takes **1.2–2.2 s** from start to its first scan.
- Most of the wait is before it starts. The unit waits for
  `network-online.target`, which Torizon OS reaches about 17.7 s after the
  kernel starts (`NetworkManager-wait-online` alone takes 6.7 s); see
  `systemd-analyze critical-chain nautilus.service`.
- A **cold** power-on adds the power supply, the boot ROM and the
  bootloader before the kernel. It has not been measured yet.

## What it does not do yet

- **A cold power-on** has not been timed; the numbers above are warm
  reboots.
- **A `PREEMPT_RT` kernel** has not been tried on the Verdin, nor
  `isolcpus` to keep a core for the controller. Both change the real-time
  numbers above.
- **The carrier's I/O**: GPIO, CAN, the serial ports. nautilus reaches I/O
  over the network.
- **Two Verdins as a redundant pair** is its own piece of work (the
  redundancy issue), not covered here.
- **Torizon Cloud** (its OTA updates and fleet view) is not used:
  `naut deploy` and the container recipe work without it.
- **Support.** Toradex supports its modules and Torizon OS; a third-party
  runtime on them is yours (or your integrator's) to support.

## Where the evidence is

- `docs/design/verdin-bench.md` has every measurement on this page with
  its conditions, and the full jitter reports.
- `tools/deploy/check.sh <ssh-target>` is the install → update →
  rollback check, run on the bench unit.
- `tools/deploy/boottime.py` with `tools/deploy/boot-probe` times reboots
  to the first good scan.
- `tools/jitter` measured the real-time numbers: `-slow 0 -alloc 0` for
  the fast tasks alone, and the full mix by default.
- `go test ./cmd/naut -run 'TestDeploy'` runs the deploy logic, including
  the on-box script against a stand-in `systemctl`, in CI.
