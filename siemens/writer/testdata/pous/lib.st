(* The library of the project's-blocks fixture (#1301): global constants,
   two FUNCTIONs and two FUNCTION_BLOCKs, one inside the other. *)
VAR_GLOBAL CONSTANT
    HI_LIMIT : REAL := 80.0;
    T_DELAY  : TIME := T#30ms;
    STEPS    : INT := 3;
    LOW      : INT := -2;
END_VAR

FUNCTION Scale : REAL
VAR_INPUT
    Raw : REAL;
    Lo  : REAL;
    Hi  : REAL;
END_VAR
VAR
    span : REAL;
END_VAR
span := Hi - Lo;
IF span <= 0.0 THEN
    Scale := Lo;
    RETURN;
END_IF;
Scale := Lo + Raw * span / 100.0;
END_FUNCTION

FUNCTION Bump : INT
VAR_INPUT
    X : INT;
END_VAR
Bump := X + 1;
END_FUNCTION

FUNCTION_BLOCK Debounce
VAR_INPUT
    IN : BOOL;
    PT : TIME;
END_VAR
VAR_OUTPUT
    Q : BOOL;
END_VAR
VAR
    t : TON;
END_VAR
t(IN := IN, PT := PT);
Q := t.Q;
END_FUNCTION_BLOCK

FUNCTION_BLOCK Station
VAR_INPUT
    Run   : BOOL;
    Level : REAL;
END_VAR
VAR_OUTPUT
    High  : BOOL;
    Count : DINT;
    Total : INT;
END_VAR
VAR_IN_OUT
    Starts : INT;
END_VAR
VAR
    db   : Debounce;
    edge : R_TRIG;
    i    : INT;
    hold : TON;
END_VAR
VAR_TEMP
    scratch : INT := 10;
END_VAR
VAR CONSTANT
    GAIN : REAL := 2.0;
    TOP  : INT := 6;
END_VAR
db(IN := Run, PT := T_DELAY);
edge(CLK := db.Q);
IF edge.Q THEN
    Starts := Starts + 1;
    Count := Count + 1;
END_IF;
High := Level * GAIN > HI_LIMIT;
Total := 0;
FOR i := 1 TO STEPS DO
    Total := Total + i;
END_FOR;
CASE Total OF
    TOP: Total := LOW;
END_CASE;
scratch := scratch + Total;
hold(IN := High);
END_FUNCTION_BLOCK
