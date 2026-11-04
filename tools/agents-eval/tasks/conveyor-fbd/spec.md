Add the control for a packing-line conveyor, written in function block
diagram (FBD) in a new file `conveyor.fbd`, running as its own task every
100 ms.

Tags (add them to nautilus.yaml with exactly these names):

- `StartPB` (input, BOOL): start push button, TRUE while pressed.
- `StopPB` (input, BOOL): stop push button, wired normally closed: TRUE when
  NOT pressed, FALSE while pressed.
- `EStop_OK` (input, BOOL): the e-stop safety relay is healthy.
- `Guard_Closed` (input, BOOL): the guard door is closed.
- `PE101_Blocked` (input, BOOL): the photo-eye at the discharge is blocked.
- `ResetPB` (input, BOOL): jam reset push button.
- `M201_Run` (output, BOOL): the conveyor motor.
- `JamAlm` (output, BOOL): jam alarm.

How it should work:

1. Start runs the conveyor and it keeps running after start is released;
   stop stops it.
2. It may only run while `EStop_OK` and `Guard_Closed` are both TRUE. Losing
   either stops it, and it then needs a new start press.
3. If the photo-eye stays blocked for 3 seconds while the conveyor is
   running, that is a jam: `JamAlm` latches on and the conveyor stops.
4. `ResetPB` clears `JamAlm`, but only once the photo-eye is clear. While
   `JamAlm` is on the conveyor cannot be started.
