# heated-tank-fbd — the FBD toolchain, end to end

The same simulated heated surge tank as [`examples/heated-tank`](../heated-tank),
but the control logic is an IEC 61131-3 **Function Block Diagram** (`program.fbd`):
the pump hysteresis is a seal-in latch, the temperature PI feeds its integral
back through a retained variable, and a `TON` delays the low-temperature
alarm. The netlist transpiles through `lang/fbd` at startup and runs on the
same runtime — one compiler, two source languages.

The project is deliberately shaped like a real multi-program controller:

- **`program.fbd`** — the `Main` task, 10 Hz, owns field I/O. Includes a
  `SEL` block choosing between the normal and eco setpoints.
- **`reports.fbd`** — a second program on its own 1 Hz task (`Tasks` in
  `main.go`), computing operator-facing values on the shared tag store:
  an **array** shift register (`TempHist[1..4]`), a rolling average
  through an extensible `ADD`, a **user function block** call, and a
  status **string** built with `CONCAT` / `TRUNC` / `INT_TO_STRING` /
  `SEL`.
- **`blocks.st`** — ST-authored `FUNCTION_BLOCK RateOfChange`, composed
  via `Libraries` and instantiated from the FBD diagram like a built-in
  `TON`. Author blocks once, call them from any IEC language.

## Run it

```sh
go run ./examples/heated-tank-fbd
# port 8080 taken? pick another and point the extension at it:
#   NAUTILUS_API=localhost:8081 go run ./examples/heated-tank-fbd
#   (VS Code setting: nautilus.runtimeUrl = http://localhost:8081)
```

You should see the heater PI ramp toward the 65 °C setpoint and, after 10 s
below 62 °C, `tempLowAlm ON` — that's the TON firing.

Open <http://localhost:8080> for the built-in dashboard: PLC-style scan
diagnostics (scan time / period / jitter, phase breakdown, distribution) and
a live tag table with the descriptions and units this example registers via
`runtime.Options.Meta`. The same data feeds any HMI through `/api/stream`
(every frame carries `scan`) and `/api/meta`; the
`@joyautomation/nautilus-hmi` kit ships a ready-made `<ScanDiagnostics>`
component for it.

## Try the FBD tooling (VS Code + nautilus CLI ≥ 0.3.7)

1. **Open `program.fbd`** — syntax highlighting; the language server compiles
   the netlist as you type and maps errors to the exact `.fbd` line (try
   misspelling `LevelPct` inside the `FBD` block).
2. **Diagram preview** — click the preview icon in the editor title (or run
   "nautilus: Open FBD Diagram Preview"). Edit the text; the diagram follows.
   Note the seal-in feedback wire from the `PumpRun` coil, the `e` wire
   fanning out to both PI paths, and the negation circle on the latch.
3. **Inline live values** — with the controller running, values stream onto
   the identifiers in the text (`TempC`, `Heater`, `PumpRun`, …) and into
   both `.st` and `.fbd` files. Toggle via the status-bar item.
4. **Visual diff** — change some logic (e.g. make the alarm
   `LT(TempC, 55.0)`, or add a high-level alarm coil), then run
   "nautilus: Diff FBD Diagram (vs git HEAD)": added blocks/wires green,
   removed red, changed amber.
5. **CLI** — the same everywhere CI runs:

   ```sh
   naut check examples/heated-tank-fbd     # compile diagnostics
   naut fbd graph examples/heated-tank-fbd/program.fbd | jq .  # render model
   ```

## Try the newer features

1. **Tasks** — the dashboard's *Scan diagnostics* section now shows a
   **tasks table**: `main` at 100 ms beside `reports` at 1000 ms, each
   with its own scan count and fault column. `GET /api/program` lists
   both programs.
2. **Arrays in the diagram** — open `reports.fbd` and its preview: the
   shift register renders as `TempHist[n]` chips and coils, and reading
   the element its own coil wrote draws a feedback wire. Double-click an
   element chip to retarget it (`TempHist[2]` → `TempHist[3]` — the
   picker accepts accessor text).
3. **User FBs from FBD** — the `r1 : RateOfChange(...)` block in the
   diagram is authored in `blocks.st`. Change its math (say, per-second
   instead of per-minute), download, and the instance's retained `prev`
   carries across the swap.
4. **Strings + selection** — watch the `Status` tag in the dashboard tag
   table: `tank 59 degC — ok`, flipping to `LOW TEMP` with the alarm.
5. **Per-task online edits** — with `reports.fbd` open, change the
   average window math and run "nautilus: Download Program to
   Controller": the edit routes to the `reports` task by its `PROGRAM`
   name; `Main` keeps running untouched. `naut pull` reconciles both
   programs back into these files.
6. **Eco mode (SEL)** — flip it live and watch the PI retarget:

   ```sh
   curl -X POST localhost:8080/api/tags -d '{"name": "EcoMode", "value": true}'
   ```

## Poke the plant

Setpoints are plain tags — change them while it runs and watch the live
values react:

```sh
curl -X POST localhost:8080/api/tags -d '{"name": "TempSP", "value": 75}'
curl -X POST localhost:8080/api/tags -d '{"name": "PumpStartLevel", "value": 65}'  # seal-in latches
```
