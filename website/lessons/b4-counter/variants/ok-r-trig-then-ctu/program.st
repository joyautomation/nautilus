PROGRAM Counter
VAR edge : R_TRIG; parcels : CTU; END_VAR
edge(CLK := Eye);
parcels(CU := edge.Q, R := Reset, PV := 5);
Count := parcels.CV;
Full := parcels.Q;
END_PROGRAM
