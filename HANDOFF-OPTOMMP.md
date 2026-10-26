# Handoff: the OptoMMP driver (branch `optommp-driver`)

Goal: the groov EPIC's I/O becomes a first-class `driver: {type: optommp}`
target, replacing the Modbus-projection path for Opto hardware, exposing
everything the processor knows about its channels (quality included).

## Read first

- `docs/design/open-controllers.md` §2 (EPIC) — what is verified and why
  Modbus was the first step.
- `optommp/` on this branch: `wire.go` (pure-stdlib OptoMMP block client,
  `MaxBlock = 0x600`), `chassis.go` (`Scan`), `modules.go` (form-1465
  module/channel tables), `modbusmap.go` (the current projection),
  `optommp_test.go` (in-process fake device over `net.Pipe`).
- House shape for a driver: `plcnext/driver.go` (small, recent) and
  `modbus/` (`io.Driver`, `io.BatchReader`, `io.QualityReporter`,
  `Health` → status row in `internal/project/drivers.go`, loader case in
  `internal/project/project.go`, schema enum in
  `tools/vscode-iec/schemas/nautilus.schema.json`).
- Opto form 1465 (OptoMMP protocol guide, memory map). The PDF and a text
  dump were in the earlier session's scratchpad `downloads/`; fetch from
  opto22.com if gone. Form 1678 is the Modbus map, no longer needed.
- `github.com/joyja/node-optommp` (James's npm port of Opto's python
  toolkit): an address reference for analog min/max areas (0xF0D8xxxx)
  and the scratchpad; not a dependency.

## Bench

- GRV-EPIC-PR1 at `192.168.82.243`, OptoMMP on `:2001`, no credential.
  Chassis: slot 0 GRV-IACDCTTL-24 (24 DI), 1 GRV-ODCI-12 (12 DO, heartbeat
  on 11), 2 GRV-IICTD-12 (ch 0 wired ≈ 19 °C; unwired channels read NaN),
  3 GRV-IV-24 (ch 0 wired ≈ 8.4 V).
- Shell: this worktree's git-ignored `.env` (`EPIC_HOST`, `EPIC_USERNAME`,
  `EPIC_PASSWORD`). The password contains `$` and `#`: read it with
  `grep '^EPIC_PASSWORD=' .env | cut -d= -f2-`, never `source`. The shell
  user is not the groov Manage admin. `naut` (ARM build) and
  `~/nautilus/epic-bench` are already on the EPIC. The PAC engine,
  groov View and Node-RED run beside it; leave them alone. (No Ignition
  Edge: checked 2026-10-25.)
- Measured: one OptoMMP request ≈ 0.75 ms p50 / 1.5 ms p99 **regardless of
  size and the same on loopback as over the LAN** — the cost is inside
  Opto's server. So: two block reads per scan (packed digital state at
  `F1000B00`, packed analog values at `F1000300`), one write when outputs
  change, and a 5 ms scan is the honest floor. The processor's own Modbus
  server lives behind the same server; no faster path exists for third
  parties: the backplane to the modules is ARCNET (a COM20020 as `arc0`,
  driven through Opto's own kernel modules), its module protocol lives
  inside the closed `mmpServer`, and Opto documents only OptoMMP for
  on-device programs. (`/dev/fpgaDirectIo` is a char driver for the
  processor's direct I/O, not the module path. Brief §2.4 on
  `plcnext-groov` has the write-up.)

## Deliver, committing after each piece

1. `optommp.Driver`: manifest `optommp_manifest.yaml` (host, port,
   scan-rate, modules, bindings), packed-area reads, digital and analog
   writes, reconnect with backoff, `Health`. Quality per channel: absent
   (channel-present mask) or faulted → Bad, NaN analog → Bad, dead socket
   → Stale, never delivered → NotConnected. Expose module part numbers,
   channel types, units, analog ranges, and (optional tags) scratchpad
   and whatever counters/latches the modules offer. Vendor spellings stop
   inside `optommp/`.
2. `naut optommp import` emits the optommp manifest + tag file (keep
   `--modbus` for the old projection); loader case, status row, schema,
   tests against the fake device.
3. `examples/epic-bench` on the new driver, verified live **on the EPIC**
   (coil/heartbeat read back off the device), README with CPU/RSS/per-scan
   numbers; then brief §2.

## Rules of the house that bit before

- CI treats Go 1.24 as a floor (`GOTOOLCHAIN=local`); keep `go.mod` at
  `go 1.24.0`, no new deps for this driver (stdlib only).
- In auto mode the classifier refuses `sed` in Bash and new `tailscale
  serve` ports; use Read/Edit, and leave port forwards to James.
- A NaN tag used to blank the dashboard (PR #429 fixed it); the driver
  still has to mark it Bad.
- Commit after each logical piece; do not open the PR until James says.
- The base PR for `plcnext-groov` may merge meanwhile; rebase onto main
  then.
