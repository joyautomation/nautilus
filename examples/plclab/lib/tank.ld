(* The demo plant's logic: a tank that fills, holds, drains and fills
   again, started and stopped from two pushbuttons. Written as rungs; a .ld
   file with no PROGRAM is a library, called once from plclab.st. Open it
   with "Open With -> Ladder Diagram". *)

FUNCTION_BLOCK TankCycle
VAR_INPUT
    Start : BOOL;  (* pushbutton, normally open *)
    Stop  : BOOL;  (* pushbutton, normally closed: FALSE when pressed, or when nothing is connected *)
    Low   : BOOL;  (* level switch: TRUE while the level is above it *)
    High  : BOOL;  (* level switch: TRUE while the level is above it *)
END_VAR
VAR_OUTPUT
    Running : BOOL;
    Fill    : BOOL;  (* fill valve *)
    Drain   : BOOL;  (* drain valve *)
    Full    : BOOL;  (* lamp *)
END_VAR
VAR
    dwell : TON;  (* how long the tank stays full before it drains *)
END_VAR
LD
  RUNG run (* start/stop seal-in *)
    [ Start | Running ] Stop ( Running )

  RUNG fill (* below the low switch, fill until the high switch; never while draining *)
    Running /Drain [ /Low | Fill ] /High ( Fill )

  RUNG hold (* full for 3 s: drain *)
    Running High /Fill dwell:TON(PT := T#3S) ( S Drain )

  RUNG drained (* down to the low switch, or stopped: close the drain *)
    [ /Low | /Running ] ( R Drain )

  RUNG full
    High ( Full )
END_LD
END_FUNCTION_BLOCK
