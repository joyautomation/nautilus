PROGRAM PlcLab
(* One scan: PLC-Lab's input bytes into the process image, the image's
   bits onto named tags, the tank logic, and the outputs back into the bytes
   PLC-Lab reads. The connector half is what PlcLabSync does on CODESYS,
   written without pointers: the process image is two local byte arrays,
   IB0..IB15 and QB0..QB15, and a sensor PLC-Lab calls I0.2 is bit 2 of
   ib[0].

   The area tables say where things are. Area a copies CntBytes bytes:
   Inputs[Addr + i] into ib[IoOfs + i], and qb[IoOfs + i] into
   Outputs[Addr + i]. The first area with CntBytes = 0 ends the table. A
   byte outside the connector or the image is skipped, never a fault. *)
VAR_EXTERNAL
    PlcLabConnector : PlcLabConnectorData;
    PlcLabMapped : BOOL;

    StartPB   : BOOL;
    StopPB    : BOOL;
    LevelLow  : BOOL;
    LevelHigh : BOOL;

    FillValve  : BOOL;
    DrainValve : BOOL;
    RunLamp    : BOOL;
    FullLamp   : BOOL;
END_VAR
VAR
    ib : ARRAY[0..15] OF BYTE;  (* inputs as PLC-Lab numbers them: ib[0] is IB0 *)
    qb : ARRAY[0..15] OF BYTE;  (* outputs: qb[0] is QB0 *)
    a, i, n, src, dst : DINT;
    tank : TankCycle;
END_VAR

(* PLC-Lab's input bytes into the image *)
FOR a := 0 TO 9 DO
    n := PlcLabConnector.AreaInputs[a].CntBytes;
    IF n = 0 THEN
        EXIT;
    END_IF;
    FOR i := 0 TO n - 1 DO
        src := PlcLabConnector.AreaInputs[a].Addr + i;
        dst := PlcLabConnector.AreaInputs[a].IoOfs + i;
        IF src <= 1023 AND dst <= 15 THEN
            ib[dst] := PlcLabConnector.Inputs[src];
        END_IF;
    END_FOR;
END_FOR;
PlcLabMapped := PlcLabConnector.AreaInputs[0].CntBytes > 0 OR PlcLabConnector.AreaOutputs[0].CntBytes > 0;

(* the image's bits onto named tags *)
StartPB   := ib[0].%X0;  (* I0.0 *)
StopPB    := ib[0].%X1;  (* I0.1, normally closed *)
LevelLow  := ib[0].%X2;  (* I0.2 *)
LevelHigh := ib[0].%X3;  (* I0.3 *)

tank(Start := StartPB, Stop := StopPB, Low := LevelLow, High := LevelHigh);
FillValve  := tank.Fill;
DrainValve := tank.Drain;
RunLamp    := tank.Running;
FullLamp   := tank.Full;

qb[0].%X0 := FillValve;   (* Q0.0 *)
qb[0].%X1 := DrainValve;  (* Q0.1 *)
qb[0].%X2 := RunLamp;     (* Q0.2 *)
qb[0].%X3 := FullLamp;    (* Q0.3 *)

(* the image into the bytes PLC-Lab reads. Only a byte that changed is
   written: a scan that writes nothing to PlcLabConnector leaves it alone,
   so it cannot put back an older copy of an input byte PLC-Lab wrote
   while the scan ran. *)
FOR a := 0 TO 9 DO
    n := PlcLabConnector.AreaOutputs[a].CntBytes;
    IF n = 0 THEN
        EXIT;
    END_IF;
    FOR i := 0 TO n - 1 DO
        src := PlcLabConnector.AreaOutputs[a].IoOfs + i;
        dst := PlcLabConnector.AreaOutputs[a].Addr + i;
        IF src <= 15 AND dst <= 1023 THEN
            IF PlcLabConnector.Outputs[dst] <> qb[src] THEN
                PlcLabConnector.Outputs[dst] := qb[src];
            END_IF;
        END_IF;
    END_FOR;
END_FOR;
END_PROGRAM
