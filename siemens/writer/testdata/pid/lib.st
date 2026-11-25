FUNCTION_BLOCK Loop
VAR_INPUT
    Enable : BOOL;
    Meas : REAL;
    Target : REAL;
END_VAR
VAR_OUTPUT
    Out : REAL;
END_VAR
VAR
    ctl : PID;
END_VAR
ctl(AUTO := Enable, PV := Meas, SP := Target, KP := 2.0, KI := 0.5, CV_MIN := 0.0, CV_MAX := 100.0);
Out := ctl.CV;
END_FUNCTION_BLOCK
