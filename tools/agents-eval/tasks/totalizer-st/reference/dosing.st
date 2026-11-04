PROGRAM Dosing
(* FT-201 pulse totalizer and the XV-201 dose. *)
VAR
    pulseEdge : R_TRIG;
    startEdge : R_TRIG;
END_VAR

pulseEdge(CLK := FT201_Pulse);
startEdge(CLK := DoseStart);

IF pulseEdge.Q THEN
    Dose_L := Dose_L + 0.5;
    Total_L := Total_L + 0.5;
END_IF;

IF TotReset THEN
    Total_L := 0.0;
END_IF;

IF startEdge.Q THEN
    Dose_L := 0.0;
    DoseDone := FALSE;
    XV201_Open := TRUE;
END_IF;

IF XV201_Open AND Dose_L >= DoseSP THEN
    XV201_Open := FALSE;
    DoseDone := TRUE;
END_IF;
END_PROGRAM
