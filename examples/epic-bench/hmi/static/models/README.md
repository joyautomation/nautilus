# The bodies of the groov EPIC screen

Five models, each one mesh, that `src/lib/Epic.svelte` loads for the
unit's bodies. Everything live on the screen (the lit display, the
status lights, the channel lamps, the readings) is drawn by that
component over them.

| File | What it is | Triangles | Tolerance |
| --- | --- | --- | --- |
| `chassis.glb` | GRV-EPIC-CHS4, the four-slot chassis | 41,808 | 1.0 mm, 0.6 rad |
| `processor.glb` | GRV-EPIC-PR1, the processor with its touchscreen closed | 41,476 | 0.5 mm, 0.6 rad |
| `power-supply.glb` | GRV-EPIC-PSAC, the power supply, in its bay in the processor | 23,328 | 0.5 mm, 0.6 rad |
| `module.glb` | The generic groov I/O module with its cover closed, in slot 0 | 34,180 | 0.5 mm, 0.6 rad |
| `module-light.glb` | That module's status light: the light pipe along the wave of its cover | 107 | 0.5 mm, 0.6 rad |

They are tessellated from the CAD drawings Opto 22 publishes for the
groov EPIC system
(https://www.opto22.com/support/resources-tools/downloads/groov-epic-system-new),
fetched 2026-11-13. The product names are Opto 22's.

## How they were made

With `../../tools/tess.py` and `../../tools/vis.py`, from the STEP files
(GRV-EPIC-CHS4, GRV-EPIC-PR1, GRV-EPIC-PSAC and
GRV-EPIC-GenericModule_coverLow, the module with its cover down; its
header has the three commands):

1. Each STEP file is tessellated directly, at the tolerance in the
   table, so every triangle lies on a CAD face and straight edges stay
   straight. Nothing is decimated. Each vertex's normal is the CAD
   surface's own normal at that point: flat faces shade flat, fillets
   shade smooth.
2. What can never be seen is left out instead: the fasteners, the power
   button behind the closed screen, the terminal strip under the closed
   module cover, the loose construction geometry in the processor's
   file, and every CAD face from which no ray gets out of the assembled
   unit in any direction (circuit boards, connector pins, the faces
   between neighbours).
3. Each part is moved into one frame and scaled to **metres**: x from
   the chassis's left edge, y up from the chassis floor, z out from the
   chassis back. The processor stands on the floor against the back with
   the power supply in its bay, and the module is in slot 0, at the
   height that puts its hold-down screw over the hole in the chassis's
   slot tab; the other slots are the same model moved along x by the
   slot pitch, 21.35 mm (measured between the chassis's slot fingers).
   The three CAD faces of the module's light pipe are written as a model
   of their own, so the screen can light them in the module's colour.
4. Written as plain GLB: float positions, normals as normalised bytes
   (`KHR_mesh_quantization`), no materials, no textures, no compression.
   The screen loads them with three's own GLTFLoader and nothing else.

The assembled unit measures 173.0 x 157.8 x 141.8 mm (width, height from
the processor's foot to the modules' tops, depth).
