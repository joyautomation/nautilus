## Structured Text (`.st`)

```
PROGRAM Scaling
(* Level in percent from the raw 4-20 mA signal, held to 0-100. *)
VAR
  pct : REAL;
END_VAR
pct := (LT101_Raw - 4.0) / 16.0 * 100.0;
IF pct < 0.0 THEN
  pct := 0.0;
END_IF;
IF pct > 100.0 THEN
  pct := 100.0;
END_IF;
LT101_Pct := pct;
END_PROGRAM
```

ST is standard IEC: a `FUNCTION_BLOCK` per piece of equipment in `lib/`,
`T#5S` time literals, single-quoted strings, IEC names only. `VAR` holds
scratch values and block instances only; tags come from the manifest.
Keep `IF`/`CASE` nesting shallow, comment each block of logic, and split a
long program into function blocks.
