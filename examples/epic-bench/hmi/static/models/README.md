# The bodies of the groov EPIC screen

Four models, each one mesh, that `src/lib/Epic.svelte` loads for the
unit's bodies. Everything live on the screen (the lit display, the
status lights, the channel lamps, the readings) is drawn by that
component over them.

| File | What it is | Triangles |
| --- | --- | --- |
| `chassis.glb` | GRV-EPIC-CHS4, the four-slot chassis | 14,434 |
| `processor.glb` | GRV-EPIC-PR1, the processor with its touchscreen closed | 20,000 |
| `power-supply.glb` | GRV-EPIC-PSAC, the power supply, in its bay in the processor | 6,000 |
| `module.glb` | The generic groov I/O module with its cover closed, in slot 0 | 11,000 |

They are simplified from the CAD drawings Opto 22 publishes for the
groov EPIC system
(https://www.opto22.com/support/resources-tools/downloads/groov-epic-system-new),
fetched 2026-11-13. The product names are Opto 22's.

## How they were made

1. The STEP files (GRV-EPIC-CHS4, GRV-EPIC-PR1, GRV-EPIC-PSAC and
   GRV-EPIC-GenericModule_coverLow, the module with its cover down) were
   tessellated to GLB, in millimetres.
2. In Blender, per part: the solids were separated and the ones the
   screen never shows were dropped (fasteners, sketch outlines, and in
   the module the terminal strip under the closed cover); the faces of
   each solid were welded along their seams; flat faces were merged
   (planar dissolve, 1.5 degrees) and the rest collapsed to the triangle
   count above; edges sharper than 32 degrees were kept sharp.
3. Each part was moved into one frame and scaled to **metres**: x from
   the chassis's left edge, y up from the chassis floor, z out from the
   chassis back. The processor stands on the floor against the back with
   the power supply in its bay, and the module is in slot 0; the other
   slots are the same model moved along x by the slot pitch, 21.35 mm
   (measured between the chassis's slot fingers).
4. Exported as plain GLB: positions and normals, no materials, no
   textures, no compression (the screen loads them with three's own
   GLTFLoader and nothing else). `chassis.glb` has no normals: it is
   flat sheet metal and is shaded flat.

The assembled unit measures 172.6 x 164.6 x 142.3 mm (width, height from
the cover's paddle to the processor's top, depth).

What the simplification loses: the wordmark raised on the processor's
skirt and the two rings moulded into each module's head do not survive
it, so the screen draws a small printed plate over each.
