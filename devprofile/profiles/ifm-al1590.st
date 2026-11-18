FUNCTION HEX4 : STRING
(* A word as the four upper-case hex digits an ifm master's pdin carries. *)
VAR_INPUT
  V : UINT;
END_VAR
VAR
  i : INT;
  d : UINT;
  w : UINT;
END_VAR
HEX4 := '';
w := V;
FOR i := 1 TO 4 DO
  d := w MOD 16;
  w := w / 16;
  CASE d OF
    0: HEX4 := CONCAT('0', HEX4);
    1: HEX4 := CONCAT('1', HEX4);
    2: HEX4 := CONCAT('2', HEX4);
    3: HEX4 := CONCAT('3', HEX4);
    4: HEX4 := CONCAT('4', HEX4);
    5: HEX4 := CONCAT('5', HEX4);
    6: HEX4 := CONCAT('6', HEX4);
    7: HEX4 := CONCAT('7', HEX4);
    8: HEX4 := CONCAT('8', HEX4);
    9: HEX4 := CONCAT('9', HEX4);
    10: HEX4 := CONCAT('A', HEX4);
    11: HEX4 := CONCAT('B', HEX4);
    12: HEX4 := CONCAT('C', HEX4);
    13: HEX4 := CONCAT('D', HEX4);
    14: HEX4 := CONCAT('E', HEX4);
  ELSE
    HEX4 := CONCAT('F', HEX4);
  END_CASE;
END_FOR;
END_FUNCTION

FUNCTION_BLOCK IFM_AL1590_SIM
(* Each port's pdin, as hex text, from a word the process model sets. *)
VAR_INPUT
  Port1_Word : UINT; Port2_Word : UINT; Port3_Word : UINT; Port4_Word : UINT;
  Port5_Word : UINT; Port6_Word : UINT; Port7_Word : UINT; Port8_Word : UINT;
END_VAR
VAR_OUTPUT
  Port1_Pdin : STRING; Port2_Pdin : STRING; Port3_Pdin : STRING; Port4_Pdin : STRING;
  Port5_Pdin : STRING; Port6_Pdin : STRING; Port7_Pdin : STRING; Port8_Pdin : STRING;
END_VAR
Port1_Pdin := HEX4(Port1_Word); Port2_Pdin := HEX4(Port2_Word);
Port3_Pdin := HEX4(Port3_Word); Port4_Pdin := HEX4(Port4_Word);
Port5_Pdin := HEX4(Port5_Word); Port6_Pdin := HEX4(Port6_Word);
Port7_Pdin := HEX4(Port7_Word); Port8_Pdin := HEX4(Port8_Word);
END_FUNCTION_BLOCK
