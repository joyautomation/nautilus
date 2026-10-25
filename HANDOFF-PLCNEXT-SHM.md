# Handoff: PLCnext in-process tier, stage 2 (branch `plcnext-shm`)

**Status 2026-10-25: pieces 1–4 done and running on the bench** (commits
dc441b4, 65ed843, 4de35bc, cc8d11b). admin's crontab runs
`naut-keepalive.sh` at boot and every minute (installed 2026-10-24 with
James's OK; a killed naut came back at the next minute). Piece 4: James ran the `passwd`/`su`/`setcap` step
(root locked again after), and the shim runs FIFO 80 with naut's task
at FIFO 40 (the bench's deployed `nautilus.yaml` has `priority: 40`; the
repo's does not, since without the capability it is an error on the
card). Redeploying either binary drops its capability. Reboot proven:
a `sudo reboot` on 2026-10-25 came back with no hand on it: cron's
`@reboot` started naut on CPU 0 11 s into boot, the driver retried the
data-access socket until the runtime was up and was connected over shm
~35 s later (handshake included); the shim came up FIFO 80 on CPU 1, the
capabilities survived (they live in the overlay's upper layer), and the
heartbeat was back on the frame. Open: eyes on the DO16 LEDs. The bench pins the shim to CPU 1 and naut to CPU 0
(`io/nautilus_io.cpu`, `naut.cpu`; rt/plcnext-io/README.md has the
best-effort numbers that chose it). On the controller: `naut.prev`,
`io/nautilus_io.stage1`, `io/nautilus_io.stage2a` (before the lateness
histogram) and `io/nautilus_io.stage2b` (before the CPU pin) are the
previous binaries.

**When this branch rebases onto plcnext-groov** (asked by that branch's
session, 2026-10-24): its user guide
`website/src/content/docs/guides/plcnext.md` calls the shm tier a design
preview and lists start-on-boot as not done, and `docs/claims/plcnext.yaml`
has no rows for this work. Update both in this branch's PR: `transport:
shm`, `output-watchdog`, `naut plcnext shm --watch` (and `bench`); 0.03 ms
vs 11 ms and naut 4 % vs 9 %; the keepalive crontab recipe; claims rows
naming this branch's tests (the layout drift guard, TestDriverOverShm …).
The best-effort vs SCHED_FIFO benchmark numbers go in the same PR, in the
guide and the brief with their conditions stated; their claims rows can
stay `tests: []` with a note until a bench nightly records them.
Not yet: `naut-keepalive.sh` honouring a pause file
(`/tmp/nautilus-bench.lease`, docs/design/bench-testing.md §4) waits on
James's call on a bench nightly (§9).

Goal: the C shim `rt/plcnext-io/nautilus_io` becomes the I/O side of the
`plcnext` driver — the Axioline frames cross to the Go runtime through a
shared-memory seqlock page instead of the firmware's gRPC service — so the
EPIC-style "nautilus runs the PLC" claim holds at the bus rate on the
PLCnext. The manifest, bindings and tag file do not change; only the
bytes' route does.

## Read first

- `rt/plcnext-io/README.md` — stage 1 (running on the bench), the build
  line, deploy recipe, and the **three hazards** (never exit during ACF
  start-up; never call the ANSI-C surface before the PLC's first `Start*`
  callback; never guess a signature). Each cost a runtime restart; one
  rebooted the controller.
- `rt/plcnext-io/nautilus_io.c`, `NautilusIo.acf.config`, `.acf.settings`.
- `rt/fastloop/` — the seqlock page (`src/main.rs` layout) and the Go
  supervisor `tools/jitter/shmpeer` (stdlib `syscall.Mmap`). Reuse the
  shape: `in` written by the supervisor, `out` by the loop, two atomic
  bumps around the stores, bounded retries, never block the loop.
- `plcnext/driver.go` + `gds.go` — the gRPC transport (`dataAccess`
  interface). Stage 2 adds `transport: shm` beside it; `conductBus` (the
  BusConductor handshake) stays on gRPC, it runs once at start.
- `docs/design/open-controllers.md` §3 — architecture, the three firmware
  facts, tiers.

## Bench state when this was written

- AXC F 2152 at `192.168.82.216`; creds in
  `kraken/tentacle/tentacle-plcnext/.env` (`PLCNEXT_HOST/USERNAME/PASSSWORD`,
  note the spelling); `admin` sudo is an allowlist (`/etc/init.d/plcnext`
  yes; `rm`, `ln`, `setcap` no). Helpers from the earlier session may
  still be in its scratchpad (`plc.sh ssh|scp`, `plc-sudo.sh`).
- Current project is `/opt/plcnext/projects/BusConductor` (`current`
  symlink); `PCWE` untouched for rollback. `NautilusIo.acf.config` is in
  that directory, so the shim starts with the runtime, as
  `plcnext_firmware`, logs to `/opt/plcnext/apps/nautilus/io/logs/`.
- The shim is the **only writer** of `Arp.Io.AxlC/0.DO4096` (the output
  connector was removed from `Plc/Gds/PCWE.gds.config`); the input mirror
  `Arp.Plc.Eclr/PND_S1_INPUTS` remains for diagnostics (`naut plcnext read
  "Arp.Plc.Eclr/PND_S1_INPUTS[4]"`). Consequence: `naut run` over gRPC can
  read but its outputs no longer reach the bus — stage 2 is what restores
  outputs.
- After any restart the bus needs BusConductor's handshake once
  (`./naut plcnext browse` from `/opt/plcnext/apps/nautilus` does it);
  slot 4's module has no UP supply (analogs read `0x8020`).
- Shim numbers: 1 ms cycle, ~2 % of a core, 17 MB; best-effort scheduling
  (SCHED_FIFO refused: needs `setcap cap_sys_nice+ep` as root, which
  `admin` cannot do — ask James whether a root path exists).
- Cross-compile: zig (`downloads/zig/` in the old scratchpad, or any
  recent release), `-target arm-linux-gnueabihf.2.35 -mcpu=cortex_a9`.

## Deliver, committing after each piece

1. The exchange: a page layout shared by `nautilus_io` and Go (inputs
   out, outputs in, scan counter, monotonic stamp, bus RUN bit from
   `DiagVars`), a POSIX shm name both sides agree on, permissions that
   work for `plcnext_firmware` (shim) and `admin` (naut). Shim side in C.
2. Go side: `plcnext` manifest gains `transport: grpc | shm`; the shm
   transport implements `dataAccess`-equivalent reads/writes from the
   page; quality from the page's age and RUN bit; Health reports the
   transport and the shim's cycle stats. Fake-shim test in Go.
3. Bench: `examples/plcnext-bench` on `transport: shm`, heartbeat verified
   through the shim's own view of the frame, numbers (input age, CPU),
   README and brief §3 updated; systemd/ACF start of `naut` itself so it
   survives a reboot.
4. If James finds a root path: `setcap` the shim and record the FIFO
   lateness numbers next to the best-effort ones.

## Rules

- Never let the shim exit during start-up. Test changes by watching
  `logs/nautilus_io.log`; if the runtime FATALs, move the `.acf.config`
  aside and `sudo /etc/init.d/plcnext restart` to recover.
- Go 1.24 floor in CI; auto-mode classifier refuses `sed` and new
  `tailscale serve` ports.
- Commit per piece; no PR until James says. Rebase onto main once the
  `plcnext-groov` PR merges.
