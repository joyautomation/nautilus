# heated-tank-fbd — the FBD toolchain, end to end

The same simulated heated surge tank as [`examples/heated-tank`](../heated-tank),
but the control logic is an IEC 61131-3 **Function Block Diagram** (`program.fbd`):
the pump hysteresis is a seal-in latch, the temperature PI feeds its integral
back through a retained variable, and a `TON` delays the low-temperature
alarm. The netlist transpiles through `lang/fbd` at startup and runs on the
same runtime — one compiler, two source languages.

## Run it

```sh
go run ./examples/heated-tank-fbd
# port 8080 taken? pick another and point the extension at it:
#   NAUTILUS_API=localhost:8081 go run ./examples/heated-tank-fbd
#   (VS Code setting: nautilus.runtimeUrl = http://localhost:8081)
```

You should see the heater PI ramp toward the 65 °C setpoint and, after 10 s
below 62 °C, `tempLowAlm ON` — that's the TON firing.

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
   nautilus check examples/heated-tank-fbd     # compile diagnostics
   nautilus fbd graph examples/heated-tank-fbd/program.fbd | jq .  # render model
   ```

## Poke the plant

Setpoints are plain tags — change them while it runs and watch the live
values react:

```sh
curl -X POST localhost:8080/api/tags -d '{"name": "TempSP", "value": 75}'
curl -X POST localhost:8080/api/tags -d '{"name": "PumpStartLevel", "value": 65}'  # seal-in latches
```
