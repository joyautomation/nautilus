PROGRAM Counter
VAR
    parcels : CTU;   (* counts, and says when it has reached its preset *)
END_VAR

parcels(CU := Eye, R := Reset, PV := 5);   (* counts each rising edge of Eye *)

Count := parcels.CV;
Full  := parcels.Q;
END_PROGRAM
