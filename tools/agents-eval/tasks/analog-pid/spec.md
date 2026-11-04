Add temperature control for a heated tank, written in function block
diagram (FBD) in a new file `heater.fbd`, running as its own task every
100 ms.

Tags (add them to nautilus.yaml with exactly these names):

- `TT101_mA` (input, REAL): the temperature transmitter's 4-20 mA signal.
  4 mA is 0 °C and 20 mA is 150 °C.
- `HeatEnable` (input, BOOL): the operator's heater enable switch.
- `TempSP` (setpoint, REAL, 60.0): temperature setpoint, °C.
- `TicKp` (setpoint, REAL, 2.0) and `TicKi` (setpoint, REAL, 0.1): PI gain
  and integral gain (repeats per second).
- `TempHiSP` (setpoint, REAL, 90.0): high temperature alarm limit, °C.
- `TT101_Temp` (state, REAL): the scaled temperature, °C.
- `E101_Out` (output, REAL): heater power, 0-100 %.
- `TempHiAlm` (output, BOOL): high temperature alarm.

How it should work:

1. Scale the 4-20 mA signal linearly into `TT101_Temp` in °C.
2. A PI loop drives `E101_Out` to hold `TT101_Temp` at `TempSP`. More heat
   raises the temperature. The output stays within 0-100 %.
3. `TempHiAlm` comes on when the temperature has been at or above `TempHiSP`
   for 5 seconds, and goes off when it drops below.
4. The heater is off (`E101_Out` = 0) whenever `HeatEnable` is off or
   `TempHiAlm` is on.
