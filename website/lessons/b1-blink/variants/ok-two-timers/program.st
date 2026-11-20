PROGRAM Blink
VAR
    offT : TON;
    onT  : TON;
END_VAR
offT(IN := NOT Lamp, PT := T#500ms);
onT(IN := Lamp, PT := T#500ms);
IF offT.Q THEN Lamp := TRUE; END_IF;
IF onT.Q THEN Lamp := FALSE; END_IF;
END_PROGRAM
