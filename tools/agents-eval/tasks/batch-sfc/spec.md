Add a batch sequence for a mixing tank, written as a sequential function
chart (SFC) in a new file `batch.sfc`, running as its own task every 100 ms.

Tags (add them to nautilus.yaml with exactly these names):

- `StartPB` (input, BOOL): start push button, TRUE while pressed.
- `AbortPB` (input, BOOL): abort push button, TRUE while pressed.
- `LT101_Level` (input, REAL): tank level, 0-100 %.
- `FullSP` (setpoint, REAL, 80.0): level at which filling stops, %.
- `EmptySP` (setpoint, REAL, 5.0): level at which draining stops, %.
- `XV101_Open` (output, BOOL): fill valve.
- `M101_Run` (output, BOOL): agitator.
- `XV102_Open` (output, BOOL): drain valve.
- `FillTimeout` (output, BOOL): alarm, the fill took too long.
- `BatchCount` (state, DINT): number of completed batches.

How it should work:

1. Idle: everything off. Pressing start begins a batch.
2. Fill: open the fill valve until the level reaches `FullSP`.
3. Mix: run the agitator for 30 seconds.
4. Drain: open the drain valve until the level is at or below `EmptySP`, then
   count the batch in `BatchCount` and go back to Idle.
5. If filling takes longer than 60 seconds, set `FillTimeout` and go straight
   to Drain. `FillTimeout` stays on until the next batch is started.
6. Pressing abort during Fill or Mix goes straight to Drain. An aborted or
   timed-out batch is not counted.
