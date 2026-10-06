PROGRAM EnumSt
VAR_EXTERNAL
    Start    : BOOL;
    Full     : BOOL;
    st_State : WashState;
    st_Code  : INT;
    st_Valve : Valve;
END_VAR
CASE st_State OF
    Idle:
        IF Start THEN st_State := WashState#Fill; END_IF;
    WashState#Fill:
        IF Full THEN st_State := Wash; END_IF;
    Wash:
        st_State := Drain;
    Drain:
        IF NOT Start THEN st_State := Idle; END_IF;
END_CASE;
IF st_State = Fill THEN
    st_Valve := Open;
ELSE
    st_Valve := Closed;
END_IF;
st_Code := TO_INT(st_State);
END_PROGRAM
