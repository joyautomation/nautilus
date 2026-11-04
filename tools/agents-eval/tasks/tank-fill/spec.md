Add the fill control for a water storage tank, written in ladder logic (LD)
in a new file `tank.ld`, running as its own task every 100 ms.

Tags (add them to nautilus.yaml with exactly these names):

- `LSL101` (input, BOOL): low level switch, TRUE while the water is above it.
- `LSH101` (input, BOOL): high level switch, TRUE while the water is at or above it.
- `LSHH101` (input, BOOL): high-high level switch, TRUE while the water is at or above it.
- `AutoSel` (input, BOOL): the Hand-Off-Auto selector is in Auto.
- `HornAck` (input, BOOL): horn silence push button.
- `AlmReset` (input, BOOL): alarm reset push button.
- `P101_Run` (output, BOOL): the fill pump.
- `HHAlarm` (output, BOOL): high-high level alarm lamp.
- `Horn` (output, BOOL): the alarm horn.

How it should work:

1. In Auto, the pump starts when the level drops below the low switch and
   runs until the water reaches the high switch. It then stays off until the
   level drops below the low switch again. Out of Auto the pump is off.
2. The high-high switch stops the pump at once, whatever the mode.
3. If the high-high switch stays on for 2 seconds, `HHAlarm` comes on and
   stays on (latched) until `AlmReset` is pressed while the high-high switch
   is clear. While `HHAlarm` is on the pump must not run.
4. The horn sounds when `HHAlarm` comes on. `HornAck` silences it, and it
   stays silent until the next alarm. Resetting the alarm also stops the horn.
