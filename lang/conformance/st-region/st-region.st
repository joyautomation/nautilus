PROGRAM RegionSt
VAR_EXTERNAL
    In      : INT;
    st_Out  : INT;
    st_Copy : INT;
END_VAR
VAR
    Region : INT;   (* REGION is not reserved *)
END_VAR
REGION Scale the input
    st_Out := In * 2;
    REGION Clamp
        IF st_Out > 100 THEN
            st_Out := 100;
        END_IF;
    END_REGION
END_REGION
REGION
    Region := st_Out;
    st_Copy := Region + 1;
END_REGION;
END_PROGRAM
