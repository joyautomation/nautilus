(* Shared PLC-Lab input mapping for current and previous scan images.
   Byte indices are absolute I bytes; masks identify the bit within each byte. *)
FUNCTION_BLOCK MapInputs
VAR_INPUT
    Bytes : ARRAY[0..3] OF BYTE;
END_VAR
VAR_OUTPUT
    S0_EStop : BOOL;
    S1_ControlOn : BOOL;
    S2_ControlOff : BOOL;
    S3_Start : BOOL;
    S4_Pause : BOOL;
    S5_ClearEStop : BOOL;
    Reserve1 : BOOL;
    Reserve2 : BOOL;
    B4_ItemOnEntry8ft : BOOL;
    B5_ItemOnEntry10ft : BOOL;
    B6_ItemOnEntry12ft : BOOL;
    B7_ItemOnEntry16ft : BOOL;
    B8_ItemInStorage8ft : BOOL;
    B9_ItemInStorage10ft : BOOL;
    B10_ItemInStorage12ft : BOOL;
    B11_ItemInStorage16ft : BOOL;
    ReserveByte : BOOL;
    B0_New8ft : BOOL;
    B1_New10ft : BOOL;
    B2_New12ft : BOOL;
    B3_New16ft : BOOL;
END_VAR
S0_EStop := (Bytes[0] AND 1) <> 0;
S1_ControlOn := (Bytes[0] AND 2) <> 0;
S2_ControlOff := (Bytes[0] AND 4) <> 0;
S3_Start := (Bytes[0] AND 8) <> 0;
S4_Pause := (Bytes[0] AND 16) <> 0;
S5_ClearEStop := (Bytes[0] AND 32) <> 0;
Reserve1 := (Bytes[0] AND 64) <> 0;
Reserve2 := (Bytes[0] AND 128) <> 0;
B4_ItemOnEntry8ft := (Bytes[1] AND 1) <> 0;
B5_ItemOnEntry10ft := (Bytes[1] AND 2) <> 0;
B6_ItemOnEntry12ft := (Bytes[1] AND 4) <> 0;
B7_ItemOnEntry16ft := (Bytes[1] AND 8) <> 0;
B8_ItemInStorage8ft := (Bytes[1] AND 16) <> 0;
B9_ItemInStorage10ft := (Bytes[1] AND 32) <> 0;
B10_ItemInStorage12ft := (Bytes[1] AND 64) <> 0;
B11_ItemInStorage16ft := (Bytes[1] AND 128) <> 0;
ReserveByte := (Bytes[2] AND 1) <> 0;
B0_New8ft := (Bytes[3] AND 1) <> 0;
B1_New10ft := (Bytes[3] AND 2) <> 0;
B2_New12ft := (Bytes[3] AND 4) <> 0;
B3_New16ft := (Bytes[3] AND 8) <> 0;
END_FUNCTION_BLOCK
(* The connector PLC-Lab reads and writes over OPC UA. PLC-Lab looks for a
   container named exactly PlcLabConnector holding these members, with these
   names and types: on CODESYS it is a global variable list; here it is one
   struct tag of this type (nautilus.yaml). See README.md.

   PLC-Lab writes Inputs (its sensors, as bytes) and the two area tables,
   which say which bytes it uses; it reads Outputs. The SIZE_ members tell it
   how big the arrays are. *)

TYPE
  PlcLabArea : STRUCT
    CntBytes : WORD;  (* how many bytes; 0 ends the table *)
    Addr     : WORD;  (* first byte in Inputs or Outputs *)
    IoOfs    : WORD;  (* first byte of the process image: 0 is IB0 / QB0 *)
  END_STRUCT;

  PlcLabConnectorData : STRUCT
    SIZE_AREA_I_ARRAY : WORD := 10;
    SIZE_AREA_Q_ARRAY : WORD := 10;
    SIZE_I_ARRAY      : WORD := 1024;
    SIZE_Q_ARRAY      : WORD := 1024;
    Inputs      : ARRAY[0..1023] OF BYTE;      (* written by PLC-Lab *)
    Outputs     : ARRAY[0..1023] OF BYTE;      (* read by PLC-Lab *)
    AreaInputs  : ARRAY[0..9] OF PlcLabArea;   (* written by PLC-Lab *)
    AreaOutputs : ARRAY[0..9] OF PlcLabArea;   (* written by PLC-Lab *)
  END_STRUCT;
END_TYPE
PROGRAM Main
VAR_EXTERNAL
    S0_EStop : BOOL;
    S1_ControlOn : BOOL;
    S2_ControlOff : BOOL;
    S3_Start : BOOL;
    S4_Pause : BOOL;
    S5_ClearEStop : BOOL;
    Reserve1 : BOOL;
    Reserve2 : BOOL;
    B4_ItemOnEntry8ft : BOOL;
    B5_ItemOnEntry10ft : BOOL;
    B6_ItemOnEntry12ft : BOOL;
    B7_ItemOnEntry16ft : BOOL;
    B8_ItemInStorage8ft : BOOL;
    B9_ItemInStorage10ft : BOOL;
    B10_ItemInStorage12ft : BOOL;
    B11_ItemInStorage16ft : BOOL;
    ReserveByte : BOOL;
    B0_New8ft : BOOL;
    B1_New10ft : BOOL;
    B2_New12ft : BOOL;
    B3_New16ft : BOOL;
    P0_ControlPL : BOOL;
    P1_MachineIsRunningPL : BOOL;
    P2_EStopPL : BOOL;
    M0_TransportCross : BOOL;
    M1_MainTR : BOOL;
    M2_TR_8ft : BOOL;
    M3_TR_10ft : BOOL;
    M4_TR_12ft : BOOL;
    M5_TR_16ft : BOOL;
    IsControlOn : BOOL;
    IsRunning : BOOL;
    CntAfterCross : INT;
    PlcLabConnector : PlcLabConnectorData;
    PlcLabMapped : BOOL;
    RawInputs : ARRAY[0..3] OF BYTE;
    ControlStep : INT;
    RunStep : INT;
    SortStep : INT;
    LengthHistory : ARRAY[0..4] OF INT;
END_VAR
VAR
    Previous : ARRAY[0..3] OF BYTE;
    Inputs, PreviousInputs : MapInputs;
    NewItem, EntryRise, StartRise, ClearRise, OffFall, PauseRise : BOOL;
    NewLength, i : INT;
    ib, qb : ARRAY[0..15] OF BYTE;
    a, j, n, src, dst : DINT;
END_VAR
(* PLC-Lab connector in: the area tables say which Inputs bytes are which I bytes. *)
FOR a := 0 TO 9 DO
    n := PlcLabConnector.AreaInputs[a].CntBytes;
    IF n = 0 THEN EXIT; END_IF;
    FOR j := 0 TO n - 1 DO
        src := PlcLabConnector.AreaInputs[a].Addr + j;
        dst := PlcLabConnector.AreaInputs[a].IoOfs + j;
        IF src <= 1023 AND dst <= 15 THEN ib[dst] := PlcLabConnector.Inputs[src]; END_IF;
    END_FOR;
END_FOR;
PlcLabMapped := PlcLabConnector.AreaInputs[0].CntBytes > 0 OR PlcLabConnector.AreaOutputs[0].CntBytes > 0;
RawInputs[0] := ib[0];
RawInputs[1] := ib[1];
RawInputs[2] := ib[2];
RawInputs[3] := ib[3];
(* Decode both images through the same physical I/O mapping. *)
Inputs(Bytes := RawInputs);
PreviousInputs(Bytes := Previous);
S0_EStop := Inputs.S0_EStop;
S1_ControlOn := Inputs.S1_ControlOn;
S2_ControlOff := Inputs.S2_ControlOff;
S3_Start := Inputs.S3_Start;
S4_Pause := Inputs.S4_Pause;
S5_ClearEStop := Inputs.S5_ClearEStop;
Reserve1 := Inputs.Reserve1;
Reserve2 := Inputs.Reserve2;
B4_ItemOnEntry8ft := Inputs.B4_ItemOnEntry8ft;
B5_ItemOnEntry10ft := Inputs.B5_ItemOnEntry10ft;
B6_ItemOnEntry12ft := Inputs.B6_ItemOnEntry12ft;
B7_ItemOnEntry16ft := Inputs.B7_ItemOnEntry16ft;
B8_ItemInStorage8ft := Inputs.B8_ItemInStorage8ft;
B9_ItemInStorage10ft := Inputs.B9_ItemInStorage10ft;
B10_ItemInStorage12ft := Inputs.B10_ItemInStorage12ft;
B11_ItemInStorage16ft := Inputs.B11_ItemInStorage16ft;
ReserveByte := Inputs.ReserveByte;
B0_New8ft := Inputs.B0_New8ft;
B1_New10ft := Inputs.B1_New10ft;
B2_New12ft := Inputs.B2_New12ft;
B3_New16ft := Inputs.B3_New16ft;
NewItem := (B0_New8ft AND NOT PreviousInputs.B0_New8ft)
    OR (B1_New10ft AND NOT PreviousInputs.B1_New10ft)
    OR (B2_New12ft AND NOT PreviousInputs.B2_New12ft)
    OR (B3_New16ft AND NOT PreviousInputs.B3_New16ft);
EntryRise := B4_ItemOnEntry8ft AND NOT PreviousInputs.B4_ItemOnEntry8ft;
StartRise := S3_Start AND NOT PreviousInputs.S3_Start;
ClearRise := S5_ClearEStop AND NOT PreviousInputs.S5_ClearEStop;
OffFall := NOT S2_ControlOff AND PreviousInputs.S2_ControlOff;
(* Source chart explicitly uses a RISING edge for S4_Pause despite its NC label. *)
PauseRise := S4_Pause AND NOT PreviousInputs.S4_Pause;
CASE ControlStep OF
    4: IF NOT S0_EStop THEN ControlStep := 1;
       ELSIF S1_ControlOn AND NOT PreviousInputs.S1_ControlOn THEN ControlStep := 2; END_IF;
    1: IF S0_EStop AND ClearRise THEN ControlStep := 4; END_IF;
    2: IF NOT S0_EStop THEN ControlStep := 1;
       ELSIF OffFall THEN ControlStep := 4; END_IF;
END_CASE;
IsControlOn := ControlStep = 2;
P0_ControlPL := IsControlOn;
P2_EStopPL := ControlStep = 1;
IF ControlStep = 1 THEN RunStep := 50; SortStep := 100;
ELSE
    CASE RunStep OF
        50: IF IsControlOn THEN RunStep := 51; END_IF;
        51: IF StartRise THEN RunStep := 52; END_IF;
        52: IF PauseRise OR NOT IsControlOn THEN RunStep := 51; END_IF;
    END_CASE;
END_IF;
IsRunning := RunStep = 52;
P1_MachineIsRunningPL := IsRunning;
(* Four parallel five-stage shift chains, represented by one length array.
   Every new-piece edge advances all chains, including during a pause. *)
IF NOT S0_EStop THEN
    FOR i := 0 TO 4 DO LengthHistory[i] := 0; END_FOR;
ELSIF IsControlOn AND NewItem THEN
    FOR i := 4 TO 1 BY -1 DO LengthHistory[i] := LengthHistory[i-1]; END_FOR;
    NewLength := 0;
    IF B0_New8ft AND NOT PreviousInputs.B0_New8ft THEN NewLength := 8;
    ELSIF B1_New10ft AND NOT PreviousInputs.B1_New10ft THEN NewLength := 10;
    ELSIF B2_New12ft AND NOT PreviousInputs.B2_New12ft THEN NewLength := 12;
    ELSIF B3_New16ft AND NOT PreviousInputs.B3_New16ft THEN NewLength := 16; END_IF;
    LengthHistory[0] := NewLength;
END_IF;
IF ControlStep <> 1 THEN
    CASE SortStep OF
        100: IF EntryRise THEN SortStep := 101; END_IF;
        101: CASE LengthHistory[4] OF
            8: SortStep := 102; 10: SortStep := 103;
            12: SortStep := 104; 16: SortStep := 105;
        END_CASE;
        102: IF B8_ItemInStorage8ft AND NOT PreviousInputs.B8_ItemInStorage8ft THEN SortStep := 100; END_IF;
        103: IF B5_ItemOnEntry10ft AND NOT PreviousInputs.B5_ItemOnEntry10ft THEN SortStep := 107; END_IF;
        104: IF B6_ItemOnEntry12ft AND NOT PreviousInputs.B6_ItemOnEntry12ft THEN SortStep := 108; END_IF;
        105: IF B7_ItemOnEntry16ft AND NOT PreviousInputs.B7_ItemOnEntry16ft THEN SortStep := 109; END_IF;
        107: IF B9_ItemInStorage10ft AND NOT PreviousInputs.B9_ItemInStorage10ft THEN SortStep := 100; END_IF;
        108: IF B10_ItemInStorage12ft AND NOT PreviousInputs.B10_ItemInStorage12ft THEN SortStep := 100; END_IF;
        109: IF B11_ItemInStorage16ft AND NOT PreviousInputs.B11_ItemInStorage16ft THEN SortStep := 100; END_IF;
    END_CASE;
END_IF;
M1_MainTR := (SortStep = 103) OR (SortStep = 104) OR (SortStep = 105) OR (SortStep = 107) OR (SortStep = 108) OR (SortStep = 109);
M2_TR_8ft := SortStep = 102;
M3_TR_10ft := SortStep = 107;
M4_TR_12ft := SortStep = 108;
M5_TR_16ft := SortStep = 109;
IF NewItem THEN CntAfterCross := CntAfterCross + 1; END_IF;
IF EntryRise AND NOT M1_MainTR THEN CntAfterCross := CntAfterCross - 1; END_IF;
IF P2_EStopPL THEN CntAfterCross := 0; END_IF;
M0_TransportCross := IsRunning AND IsControlOn AND (CntAfterCross < 5);
(* Save only after all edge checks, including when control is off or stopped. *)
Previous := RawInputs;
(* PLC-Lab connector out: Q bits into the image, the image into Outputs. *)
qb[0].%X0 := P0_ControlPL;
qb[0].%X1 := P1_MachineIsRunningPL;
qb[0].%X2 := P2_EStopPL;
qb[0].%X3 := M0_TransportCross;
qb[0].%X4 := M1_MainTR;
qb[0].%X5 := M2_TR_8ft;
qb[0].%X6 := M3_TR_10ft;
qb[0].%X7 := M4_TR_12ft;
qb[1].%X0 := M5_TR_16ft;
FOR a := 0 TO 9 DO
    n := PlcLabConnector.AreaOutputs[a].CntBytes;
    IF n = 0 THEN EXIT; END_IF;
    FOR j := 0 TO n - 1 DO
        src := PlcLabConnector.AreaOutputs[a].IoOfs + j;
        dst := PlcLabConnector.AreaOutputs[a].Addr + j;
        IF src <= 15 AND dst <= 1023 THEN PlcLabConnector.Outputs[dst] := qb[src]; END_IF;
    END_FOR;
END_FOR;
END_PROGRAM
