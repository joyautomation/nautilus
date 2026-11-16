PROGRAM Main
VAR_EXTERNAL
    StartPB : BOOL;
    StopPB  : BOOL;
    Lamp    : BOOL;
END_VAR
VAR
    tank : TankCycle;
END_VAR
tank(Start := StartPB, Stop := StopPB);
Lamp := tank.Running;
END_PROGRAM
