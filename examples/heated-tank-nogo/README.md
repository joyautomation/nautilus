# heated-tank-nogo — the whole product, no Go

The heated surge tank as a **manifest project**: every nautilus feature in
one folder, and not a line of Go — even the plant physics is an IEC task.

```sh
nautilus run       # scan loop + dashboard + tag API on http://localhost:8080
nautilus check .   # compile (the CI gate)
nautilus test      # acceptance tests in virtual time (heated-tank_test.yaml)
nautilus build     # emit ./heated-tank-nogo — one deployable binary, no toolchain
```

## What it demonstrates

- **`nautilus.yaml`** — the no-Go wiring: tasks, tags by role with HMI
  units/descriptions, server options, driver selection. The Go library
  form of this same plant is `examples/heated-tank-fbd`.
- **Four tasks, three languages, one tag store** (the IEC resource/task
  model):
  - `program.fbd` — control at 10 Hz: pump seal-in latch, `SEL` eco
    setpoint, PI with retained integral, `TON` alarm delay.
  - `sim.st` — the plant, simulated in Structured Text.
  - `reports.fbd` — 1 Hz: an `ARRAY` shift register, the ST-authored
    `RateOfChange` block from `blocks.st`, string building with
    `CONCAT`/`TRUNC`/`INT_TO_STRING`/`SEL`.
  - `interlocks.ld` — the annunciator in ladder: comparison contact,
    in-rung `TON` with the standard's `ET =>` output binding, branch,
    NC contacts, set/reset coils, comment notes.
- **The editors**: right-click `program.fbd` → *Open With → FBD Diagram*,
  `interlocks.ld` → *Ladder Diagram*. Live values paint power flow;
  every gesture is a structural edit to the text underneath.
- **Online edits per task** (`online-edits: true`): edit any program file
  and *Download Program to Controller* — it routes by POU name; the
  divergence pill and visual diffs track you.
- **`heated-tank_test.yaml`** — acceptance tests with no Go and no
  toolchain. The runtime drives them on a **virtual clock**, so the 10 s
  `TON` alarm delay and the PI loop's ~32 s step response are asserted
  exactly, deterministically, in milliseconds. The fixture is
  `nautilus.yaml` itself, so a retuned gain can't drift away from what the
  tests verify. Assertions are tag matchers (`{near: 72.0, tol: 0.5}`) or
  ST expressions (`ABS(TempC - TempSP) < 0.5`) compiled by the same
  compiler as the logic.
- **`nautilus build`** appends this folder to the runner and emits a
  single self-contained controller binary — ships like any compiled
  program (`NAUTILUS_ADDR` / `NAUTILUS_TOKEN` apply; `NAUTILUS_CLI=1`
  recovers the CLI from a built binary). Test files are left out of the
  archive: they gate the deploy, they don't ride along on it.

Watch it live on the dashboard: the pump seals in at 40 %, drops out at
75 %; the PI settles the temperature at the setpoint; flip `EcoMode` or
`TempSP` from the tag table and watch the loop — and the `Status` line —
follow.
