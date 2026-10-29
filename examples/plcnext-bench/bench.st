PROGRAM Bench
(* The round trip on the controller's own bus: sixteen inputs mirrored to
   fifteen outputs and a heartbeat on the sixteenth. The analog element
   has inputs only (AI_00 … AI_03, read through the tag API).

   With HoldAfterOutage on, after an outage the fifteen mirrored outputs
   stay off until an operator writes BenchAck: the driver's status tags into IoHold (lib/io_hold.st).
   On this controller that includes a naut restarted after the shim's
   output watchdog dropped the frame — the driver starts with
   AXC__WatchdogTrips = 1. The heartbeat keeps blinking meanwhile. *)
VAR_EXTERNAL
    DI_00 : BOOL;  DI_01 : BOOL;  DI_02 : BOOL;  DI_03 : BOOL;
    DI_04 : BOOL;  DI_05 : BOOL;  DI_06 : BOOL;  DI_07 : BOOL;
    DI_08 : BOOL;  DI_09 : BOOL;  DI_10 : BOOL;  DI_11 : BOOL;
    DI_12 : BOOL;  DI_13 : BOOL;  DI_14 : BOOL;  DI_15 : BOOL;
    DO_00 : BOOL;  DO_01 : BOOL;  DO_02 : BOOL;  DO_03 : BOOL;
    DO_04 : BOOL;  DO_05 : BOOL;  DO_06 : BOOL;  DO_07 : BOOL;
    DO_08 : BOOL;  DO_09 : BOOL;  DO_10 : BOOL;  DO_11 : BOOL;
    DO_12 : BOOL;  DO_13 : BOOL;  DO_14 : BOOL;  DO_15 : BOOL;
    AXC__Online        : BOOL;   (* driver status *)
    AXC__Outages       : DINT;
    AXC__WatchdogTrips : DINT;
    HoldAfterOutage : BOOL;
    BenchAck     : BOOL;
    BenchHeld    : BOOL;
    BenchDtS     : REAL;
    BlinkPeriodS : REAL;
    Blink        : BOOL;
END_VAR
VAR
    acc  : REAL;
    hold : IoHold;
END_VAR

acc := acc + BenchDtS;
IF acc >= BlinkPeriodS / 2.0 THEN
    Blink := NOT Blink;
    acc := 0.0;
END_IF;
DO_15 := Blink;

(* Held after an outage until acknowledged; BenchAck is a momentary
   button, cleared once read. *)
hold(Enable := HoldAfterOutage, Online := AXC__Online, Outages := AXC__Outages, Trips := AXC__WatchdogTrips, Ack := BenchAck);
BenchHeld := hold.Held;
BenchAck := FALSE;

DO_00 := DI_00 AND NOT BenchHeld;  DO_01 := DI_01 AND NOT BenchHeld;
DO_02 := DI_02 AND NOT BenchHeld;  DO_03 := DI_03 AND NOT BenchHeld;
DO_04 := DI_04 AND NOT BenchHeld;  DO_05 := DI_05 AND NOT BenchHeld;
DO_06 := DI_06 AND NOT BenchHeld;  DO_07 := DI_07 AND NOT BenchHeld;
DO_08 := DI_08 AND NOT BenchHeld;  DO_09 := DI_09 AND NOT BenchHeld;
DO_10 := DI_10 AND NOT BenchHeld;  DO_11 := DI_11 AND NOT BenchHeld;
DO_12 := DI_12 AND NOT BenchHeld;  DO_13 := DI_13 AND NOT BenchHeld;
DO_14 := DI_14 AND NOT BenchHeld;
END_PROGRAM
