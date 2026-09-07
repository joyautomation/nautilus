PROGRAM Sim
(* The plant, so `nautilus run` shows alarms coming and going without any
   field hardware. Acceptance tests suspend this task and drive the
   measurements themselves — freezing the thing that would otherwise fight
   the test is what turns a closed loop into an open-loop assertion. *)
VAR_EXTERNAL
    RTU9_WEL15_FIT_001 : AnalogInput;
    RTU9_TNK01_LIT_001 : AnalogInput;
    RTU9_WEL15_INT_001_YA : BOOL;
    SimDtS : REAL;
END_VAR
VAR
    t : REAL := 0.0;
END_VAR

t := t + SimDtS;
IF t > 120.0 THEN t := 0.0; END_IF;

RTU9_WEL15_FIT_001.PV := t;           (* ramps up through the flow HH limit *)
RTU9_TNK01_LIT_001.PV := 120.0 - t;   (* and the tank drains through its L  *)
RTU9_WEL15_INT_001_YA := t > 110.0;   (* a standalone contact, briefly made  *)

END_PROGRAM
