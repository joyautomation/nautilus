Add a dosing totalizer to this project, written in structured text (ST) in
a new file `dosing.st`, running as its own task every 50 ms.

Tags (add them to nautilus.yaml with exactly these names):

- `FT201_Pulse` (input, BOOL): the flow meter's pulse output. Each pulse
  (FALSE to TRUE) is 0.5 litres.
- `DoseStart` (input, BOOL): start a dose, push button.
- `TotReset` (input, BOOL): reset the grand total, push button.
- `DoseSP` (setpoint, REAL, 100.0): the dose size in litres.
- `XV201_Open` (output, BOOL): the dosing valve.
- `Dose_L` (state, REAL): litres delivered in the current dose.
- `Total_L` (state, REAL): grand total litres, all doses.
- `DoseDone` (output, BOOL): the last dose is complete.

How it should work:

1. Count every rising edge of `FT201_Pulse` as 0.5 L. A pulse held on counts
   once.
2. Pressing `DoseStart` zeroes `Dose_L`, clears `DoseDone` and opens the valve.
3. When `Dose_L` reaches `DoseSP`, close the valve and set `DoseDone`, which
   stays on until the next start.
4. `Total_L` adds up every litre counted, across doses, and only `TotReset`
   zeroes it.
