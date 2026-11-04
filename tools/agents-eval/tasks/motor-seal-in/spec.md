Add a motor starter to this project, written in ladder logic (LD) in a new
file `motor.ld`, running as its own task every 100 ms.

Tags (add them to nautilus.yaml with exactly these names):

- `StartPB` (input, BOOL): start push button, normally open, TRUE while pressed.
- `StopPB` (input, BOOL): stop push button, wired normally closed. It reads
  TRUE when it is NOT pressed and FALSE while pressed (or if its wire breaks).
- `OL_Trip` (input, BOOL): the motor overload relay, TRUE when it has tripped.
- `ResetPB` (input, BOOL): fault reset push button, TRUE while pressed.
- `Motor_Run` (output, BOOL): the motor contactor.
- `Motor_Fault` (output, BOOL): the fault lamp on the panel.

How it should work:

1. Pressing start runs the motor, and it keeps running after start is released.
2. Pressing stop stops it. Stop wins: holding start and stop together leaves
   the motor stopped.
3. An overload trip stops the motor at once and latches `Motor_Fault`. While
   the fault is latched the motor cannot be started.
4. Reset clears the fault, but only once the overload is no longer tripped.
