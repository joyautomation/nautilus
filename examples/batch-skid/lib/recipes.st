(* Recipe and batch-status UDTs, the three ship-with recipes, and the
   validity check the sequence gates Start on. A PROGRAM-less .st file is a
   project library, exactly like lib/physics.st — its TYPEs, FUNCTION, and
   FUNCTION_BLOCK join the whole compile, so any program can use them. *)

TYPE
  Recipe : STRUCT
    Name      : STRING; (* operator-facing label *)
    AmountA_L : REAL;    (* dosing-line A target, liters *)
    AmountB_L : REAL;    (* dosing-line B target, liters *)
    TempSP_C  : REAL;    (* jacket temperature setpoint *)
    HoldSec   : REAL;    (* hold-at-temperature dwell *)
    Agitate   : BOOL;    (* run AG-201 during charge/heat/hold *)
  END_STRUCT;

  BatchStatus : STRUCT
    Phase      : STRING; (* mirrors PhaseName, see status.st *)
    Count      : INT;    (* batches run since the controller last started *)
    ChargedA_L : REAL;   (* running total, dosing-line A *)
    ChargedB_L : REAL;   (* running total, dosing-line B *)
    ElapsedSec : REAL;   (* time since the current batch left Idle *)
  END_STRUCT;
END_TYPE

FUNCTION_BLOCK RecipeTable
(* The three recipes this project ships with, as a VAR_OUTPUT array pin.
   There is no struct/array literal in this language (see
   docs/languages/structured-text.md: "An initial value after := must be a
   single literal constant") and no way to declare a global outside a POU,
   so the table is built by plain field assignment on its own first call
   instead of a VAR CONSTANT initializer — the one place this project
   departs from lift-station's flat, tag-only data model. Call once, from
   phases.sfc; every call after the first is a no-op. *)
VAR_OUTPUT
    Recipes : ARRAY[1..3] OF Recipe;
END_VAR
VAR
    loaded : BOOL;
END_VAR
IF NOT loaded THEN
    Recipes[1].Name      := 'A-STD';
    Recipes[1].AmountA_L := 20.0;
    Recipes[1].AmountB_L := 15.0;
    Recipes[1].TempSP_C  := 45.0;
    Recipes[1].HoldSec   := 30.0;
    Recipes[1].Agitate   := TRUE;

    Recipes[2].Name      := 'B-NOMIX';
    Recipes[2].AmountA_L := 15.0;
    Recipes[2].AmountB_L := 20.0;
    Recipes[2].TempSP_C  := 40.0;
    Recipes[2].HoldSec   := 20.0;
    Recipes[2].Agitate   := FALSE; (* no agitation this recipe — see test 8 *)

    Recipes[3].Name      := 'C-HOT';
    Recipes[3].AmountA_L := 25.0;
    Recipes[3].AmountB_L := 25.0;
    Recipes[3].TempSP_C  := 55.0;
    Recipes[3].HoldSec   := 45.0;
    Recipes[3].Agitate   := TRUE;

    loaded := TRUE;
END_IF;
END_FUNCTION_BLOCK

FUNCTION RecipeValid : BOOL
(* Bounds check only — RecipeSel must select one of the three ship-with
   recipes. Gates Start in phases.sfc (Idle -> Prep). *)
VAR_INPUT
    Idx : INT;
END_VAR
RecipeValid := (Idx >= 1) AND (Idx <= 3);
END_FUNCTION
