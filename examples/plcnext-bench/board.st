PROGRAM Board
(* sim.yaml only: the starter-kit board's analog side, in place of the AI4 U.
   The slider (SliderV, 0..10 V) reads on AI_00 in the element's standard
   format, 0..30000 counts; the other three channels are unwired, 0 V. With
   SupplyMissing every channel reads 0x8020, the element's "supply missing"
   code, as the bench's slot 4 does today. The pushbuttons need no model:
   DI_00..DI_03 are setpoints in tags/sim.yaml. On the controller
   (nautilus.yaml) this task does not run; the driver fills AI_00..03. *)
VAR_EXTERNAL
    SliderV       : REAL;
    SupplyMissing : BOOL;
    AI_00 : INT;  AI_01 : INT;  AI_02 : INT;  AI_03 : INT;
END_VAR
VAR CONSTANT
    SUPPLY_MISSING : INT := -32736;  (* 16#8020 read as INT *)
END_VAR

IF SupplyMissing THEN
    AI_00 := SUPPLY_MISSING;  AI_01 := SUPPLY_MISSING;
    AI_02 := SUPPLY_MISSING;  AI_03 := SUPPLY_MISSING;
ELSE
    AI_00 := REAL_TO_INT(LIMIT(0.0, SliderV, 10.0) * 3000.0);
    AI_01 := 0;  AI_02 := 0;  AI_03 := 0;
END_IF;
END_PROGRAM
