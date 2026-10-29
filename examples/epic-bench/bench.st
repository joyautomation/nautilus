PROGRAM Bench
(* The smallest program that proves the round trip: nautilus reads the
   EPIC's inputs and drives its outputs over OptoMMP. The first eleven
   pushbuttons mirror to the first eleven lamps, the last output carries a
   heartbeat, the two wired analog channels are copied onto named tags so
   the dashboard reads in engineering terms, and the temperature goes to
   scratchpad float 0, where a PAC Control strategy or groov View on the
   same EPIC can read it.

   After an outage — the EPIC unreachable, or its communication watchdog
   tripped — the lamps stay dark until an operator writes BenchAck: the
   driver's status tags into IoHold (lib/io_hold.st). The heartbeat keeps
   blinking, so the program is visibly running while it holds. *)
VAR_EXTERNAL
    EPIC_M0_DI00 : BOOL;   (* slot 0, GRV-IACDCTTL-24 *)
    EPIC_M0_DI01 : BOOL;
    EPIC_M0_DI02 : BOOL;
    EPIC_M0_DI03 : BOOL;
    EPIC_M0_DI04 : BOOL;
    EPIC_M0_DI05 : BOOL;
    EPIC_M0_DI06 : BOOL;
    EPIC_M0_DI07 : BOOL;
    EPIC_M0_DI08 : BOOL;
    EPIC_M0_DI09 : BOOL;
    EPIC_M0_DI10 : BOOL;
    EPIC_M1_DO00 : BOOL;   (* slot 1, GRV-ODCI-12 *)
    EPIC_M1_DO01 : BOOL;
    EPIC_M1_DO02 : BOOL;
    EPIC_M1_DO03 : BOOL;
    EPIC_M1_DO04 : BOOL;
    EPIC_M1_DO05 : BOOL;
    EPIC_M1_DO06 : BOOL;
    EPIC_M1_DO07 : BOOL;
    EPIC_M1_DO08 : BOOL;
    EPIC_M1_DO09 : BOOL;
    EPIC_M1_DO10 : BOOL;
    EPIC_M1_DO11 : BOOL;   (* channel 11 — heartbeat *)
    EPIC_M2_AI00 : REAL;   (* slot 2, GRV-IICTD-12, channel 0 — °C *)
    EPIC_M3_AI00 : REAL;   (* slot 3, GRV-IV-24, channel 0 — V *)
    EPIC_SP_F0   : REAL;   (* scratchpad float 0 *)
    EPIC__Online        : BOOL;   (* driver status *)
    EPIC__Outages       : DINT;
    EPIC__WatchdogTrips : DINT;
    BenchAck     : BOOL;
    BenchHeld    : BOOL;
    BenchDtS     : REAL;
    BlinkPeriodS : REAL;
    Blink        : BOOL;
    BenchTempC   : REAL;
    BenchVoltsV  : REAL;
END_VAR
VAR
    acc  : REAL;
    hold : IoHold;
END_VAR

(* Heartbeat: toggle every half period. *)
acc := acc + BenchDtS;
IF acc >= BlinkPeriodS / 2.0 THEN
    Blink := NOT Blink;
    acc := 0.0;
END_IF;
EPIC_M1_DO11 := Blink;

(* Held after an outage until acknowledged; BenchAck is a momentary
   button, cleared once read. *)
hold(Online := EPIC__Online, Outages := EPIC__Outages, Trips := EPIC__WatchdogTrips, Ack := BenchAck);
BenchHeld := hold.Held;
BenchAck := FALSE;

(* Pushbuttons to lamps. *)
EPIC_M1_DO00 := EPIC_M0_DI00 AND NOT BenchHeld;
EPIC_M1_DO01 := EPIC_M0_DI01 AND NOT BenchHeld;
EPIC_M1_DO02 := EPIC_M0_DI02 AND NOT BenchHeld;
EPIC_M1_DO03 := EPIC_M0_DI03 AND NOT BenchHeld;
EPIC_M1_DO04 := EPIC_M0_DI04 AND NOT BenchHeld;
EPIC_M1_DO05 := EPIC_M0_DI05 AND NOT BenchHeld;
EPIC_M1_DO06 := EPIC_M0_DI06 AND NOT BenchHeld;
EPIC_M1_DO07 := EPIC_M0_DI07 AND NOT BenchHeld;
EPIC_M1_DO08 := EPIC_M0_DI08 AND NOT BenchHeld;
EPIC_M1_DO09 := EPIC_M0_DI09 AND NOT BenchHeld;
EPIC_M1_DO10 := EPIC_M0_DI10 AND NOT BenchHeld;

(* Engineering names for the two wired analog channels. *)
BenchTempC := EPIC_M2_AI00;
BenchVoltsV := EPIC_M3_AI00;

(* Shared with whatever else runs on the EPIC. *)
EPIC_SP_F0 := BenchTempC;
END_PROGRAM
