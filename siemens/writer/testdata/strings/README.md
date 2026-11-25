# Writer goldens: STRING

`main.st` uses every STRING construct the writer writes (#1303): each
string function with arguments that need no clamp, arguments clamped where
TIA differs from nautilus, constants the writer clamps, lengths and
positions wider than INT (computed at 64 bits, clamped, then narrowed),
CONCAT chains flattened into one call, string functions inside other
expressions, SEL and MUX of strings, `=` and `<>`, the conversions the
writer writes (integers through `naut_STR_LINT`/`naut_STR_ULINT`, BOOL
through SEL, `STRING_TO_` numbers), escapes, and a STRING start value that
reads like a TIME. `golden/` holds what the writer emits, with the tags
`Msg` (STRING, init `'hi'`), `Go` (BOOL) and `N` (DINT), one 100 ms task and
the block prefix `naut_`.

These exact files (prefix renamed) compiled 0/0 on TIA V21 through tiad
(offline, imported into a throwaway project), 2026-11-25.

What TIA does at the edges was measured on the bench 1510SP the same day
(`strings.go` says where it differs from nautilus and what the writer does
about it). Change a spelling here only with a new TIA compile of this
golden.
