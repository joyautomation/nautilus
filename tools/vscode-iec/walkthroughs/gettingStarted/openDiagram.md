[Open FBD Diagram Preview](command:nautilus.fbd.preview)

(Works once `program.fbd` is the active editor — open it first, then run
this or use the editor-title button.)

Every gesture on the diagram — renaming a block, rewiring a pin, retyping a
constant, inserting an instruction — is a structural edit resolved by the
Go compiler into minimal text changes, so `program.fbd` stays a plain,
git-diffable text file underneath.
