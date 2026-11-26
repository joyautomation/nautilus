# Writer goldens: structured types (coverage B, #1299)

`Main.st` declares two STRUCTs (one nested in the other) and uses them
the ways the examples do: manifest tags of the type with an `init:` map
(`structGlobals`), member arithmetic that wraps as nautilus stores it, a
whole struct copied, a struct temp, and members named `Static` and `Name`.
`golden/` holds the output.

Each struct is written inline (`Struct … END_STRUCT`) where it is used,
not as a PLC data type: tiad does not import data types yet. Every member
carries its start value: the TYPE's own initial value, or what the tag's
`init:` map gives it. A whole struct is copied member by member, and a
struct temp is reset member by member at the start of every scan.

Regenerate with `go test ./siemens/writer/ -update` and review the diff.

Compiled 0 errors, 0 warnings on TIA V21 through tiad (a build-only
`naut siemens deploy --adopt last-deploy` on tia1, no CPU contact,
2026-11-23 UTC), together with the well-site example's control program
cut down to one task (its library's nested ChemFeed/Tank/Motor structs,
partial nested start values). The same fixture with the names unquoted
failed the import at `Name : DINT` ("The specified value "Name" is
invalid"), which is why `sclName` quotes it. Compile only: start values
reaching the CPU are not yet measured on a download.
