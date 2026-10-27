PROGRAM Bench
(* The round trip on the controller's own bus: sixteen inputs mirrored to
   fifteen outputs and a heartbeat on the sixteenth. The analog element
   has inputs only (AI_00 … AI_03, read through the tag API). *)
VAR_EXTERNAL
    DI_00 : BOOL;  DI_01 : BOOL;  DI_02 : BOOL;  DI_03 : BOOL;
    DI_04 : BOOL;  DI_05 : BOOL;  DI_06 : BOOL;  DI_07 : BOOL;
    DI_08 : BOOL;  DI_09 : BOOL;  DI_10 : BOOL;  DI_11 : BOOL;
    DI_12 : BOOL;  DI_13 : BOOL;  DI_14 : BOOL;  DI_15 : BOOL;
    DO_00 : BOOL;  DO_01 : BOOL;  DO_02 : BOOL;  DO_03 : BOOL;
    DO_04 : BOOL;  DO_05 : BOOL;  DO_06 : BOOL;  DO_07 : BOOL;
    DO_08 : BOOL;  DO_09 : BOOL;  DO_10 : BOOL;  DO_11 : BOOL;
    DO_12 : BOOL;  DO_13 : BOOL;  DO_14 : BOOL;  DO_15 : BOOL;
    BenchDtS     : REAL;
    BlinkPeriodS : REAL;
    Blink        : BOOL;
END_VAR
VAR
    acc : REAL;
END_VAR

acc := acc + BenchDtS;
IF acc >= BlinkPeriodS / 2.0 THEN
    Blink := NOT Blink;
    acc := 0.0;
END_IF;
DO_15 := Blink;

DO_00 := DI_00;  DO_01 := DI_01;  DO_02 := DI_02;  DO_03 := DI_03;
DO_04 := DI_04;  DO_05 := DI_05;  DO_06 := DI_06;  DO_07 := DI_07;
DO_08 := DI_08;  DO_09 := DI_09;  DO_10 := DI_10;  DO_11 := DI_11;
DO_12 := DI_12;  DO_13 := DI_13;  DO_14 := DI_14;
END_PROGRAM
