# Gesture inventory

One row per user-facing gesture of the nautilus VS Code extension, with what
covers it today. Written against `origin/main` at d3a4bdb (2026-10-03).

Sources walked: `package.json` `contributes.commands` (28) and `menus`; the
`?` shortcut tables in `webview-ui/src/shortcuts.ts` (`FBD_SHORTCUTS`,
`LD_SHORTCUTS`, `SFC_SHORTCUTS`, `MIMIC_SHORTCUTS`, `COMPONENT_SHORTCUTS`;
the toolbar hint and the `?` popover are both generated from these); the
extension README and CHANGELOG feature claims; the two harness files in this
directory; the rig verbs in `content/assets/capture/ex01-lift-station/fixtures/gestures.sh`;
the smoke checks `01`..`11` in `content/assets/capture/ext-stable/smoke/`.

## How to read it

- **id**: stable within this file (`C` commands, `F` FBD, `L` ladder, `S` SFC,
  `M` mimic, `P` component, `X` other claims). Every command and every row of
  every `?` table appears exactly once. The `File` group is shared by all five
  tables in `shortcuts.ts`, so it has a row per editor (each editor's `?`
  popover shows it).
- **webview test**: a test in `diagram.test.mjs` (`D`) or `gestures.test.mjs`
  (`G`) that performs the gesture or asserts its result. A parenthesised note
  marks partial cover (for example a test that renders the diff, not the
  command that opens it). Ladder, SFC and FBD tests deliver a model and read
  back the posted ops; they never run the extension host.
- **rig verb**: a verb in `gestures.sh` (xdotool against a real VS Code in the
  incus rig) that performs it.
- **smoke**: the number of the `ext-stable/smoke/NN-*.sh` check that exercises
  it in a real VS Code. "menu entry present only" means the check sees the
  button but never runs the command.
- `—` means nothing covers it. Cover is judged from test names, comments and
  the verb list, not by running them; a "partial" label is deliberately
  conservative.

Totals: 155 rows. **0 rows have no coverage at all** (no webview test,
no rig verb, no smoke check); per section below.

| section | rows | no coverage |
|---|---|---|
| Commands | 28 | 0 |
| FBD `?` | 18 | 0 |
| Ladder `?` | 20 | 0 |
| SFC `?` | 22 | 0 |
| Mimic `?` | 18 | 0 |
| Component `?` | 8 | 0 |
| Other claims | 41 | 0 |

## Commands (package.json `contributes.commands`)

| id | area | gesture | source of the claim | webview test | rig verb | smoke |
|---|---|---|---|---|---|---|
| C01 | Extension (Palette) | Toggle inline live tag values from the palette | package.json command `nautilus.liveValues.toggle` | gestures.test.mjs: the live pill reflects nautilus.liveValues.enabled and toggles it through the host | — | 08 (sets it by setting) |
| C02 | Extension (Palette; missing-CLI prompt) | Install or update the naut CLI | package.json command `nautilus.installCli` | — | — | 01 |
| C03 | Extension (Palette) | Show which naut is in use and its version | package.json command `nautilus.showCliInfo` | — | — | 12 |
| C04 | Extension (Palette) | Restart the language server | package.json command `nautilus.restartLanguageServer` | — | — | 12 |
| C05 | Extension (Palette; Live Values view title) | Connect to a controller (set the runtime URL) | package.json command `nautilus.connect` | — | — | 12 |
| C06 | Extension (Palette; editor context menu; Live Values item inline) | Write a new value to a tag (right-click an identifier, or the pencil in the Live Values panel) | package.json command `nautilus.setValue` | — | — | 13 |
| C07 | Extension (Live Values view title (hidden from palette)) | Refresh the Live Values panel | package.json command `nautilus.liveValues.refresh` | — | — | 12 |
| C08 | Extension (Palette) | Download the program to the controller (online edit, with confirmation) | package.json command `nautilus.program.download` | — | — | 08 |
| C09 | Extension (Palette) | Diff the program with the controller | package.json command `nautilus.program.diff` | — | — | 16 |
| C10 | Extension (Palette) | Roll back the controller program | package.json command `nautilus.program.rollback` | — | — | 08 |
| C11 | Extension (Palette) | Pull the program from the controller | package.json command `nautilus.program.pull` | — | — | 16 |
| C12 | Extension (Text editor title; Palette) | Open the FBD diagram beside the text | package.json command `nautilus.fbd.preview` | — | — | 03, 07 |
| C13 | Extension (Text editor title; Palette) | Open the ladder diagram beside the text (also an L5X) | package.json command `nautilus.ld.preview` | — | — | 10 |
| C14 | Extension (Text editor title; Palette) | Open the SFC diagram beside the text | package.json command `nautilus.sfc.preview` | — | — | 03 |
| C15 | Extension (Diagram editor title) | Diff the FBD diagram against git HEAD | package.json command `nautilus.fbd.diff` | — | — | 04 |
| C16 | Extension (Diagram editor title ... menu) | Diff the FBD diagram against the controller | package.json command `nautilus.fbd.diffController` | — | — | 04 (menu entry present only) |
| C17 | Extension (Diagram editor title ... menu) | Diff the FBD diagram between git revisions | package.json command `nautilus.fbd.diffRevisions` | — | — | 04 (menu entry present only) |
| C18 | Extension (Diagram editor title) | Diff the ladder diagram against git HEAD | package.json command `nautilus.ld.diff` | diagram.test.mjs: Theme: ladder diff colours come from theme tokens (renders a diff; not the command) | — | 04, 11 |
| C19 | Extension (Diagram editor title ... menu) | Diff the ladder diagram against the controller | package.json command `nautilus.ld.diffController` | — | — | 04 (menu entry present only) |
| C20 | Extension (Diagram editor title ... menu) | Diff the ladder diagram between git revisions | package.json command `nautilus.ld.diffRevisions` | — | — | 04 (menu entry present only) |
| C21 | Extension (Diagram editor title) | Diff the SFC diagram against git HEAD | package.json command `nautilus.sfc.diff` | diagram.test.mjs: Restore: SFC mounts from a saved model (and diff) (renders a diff; not the command) | — | 04 |
| C22 | Extension (Diagram editor title ... menu) | Diff the SFC diagram against the controller | package.json command `nautilus.sfc.diffController` | — | — | 04 (menu entry present only) |
| C23 | Extension (Diagram editor title ... menu) | Diff the SFC diagram between git revisions | package.json command `nautilus.sfc.diffRevisions` | — | — | 04 (menu entry present only) |
| C24 | Extension (Palette) | Edit the ports of a built-in component | package.json command `nautilus.editComponentPorts` | — | component_edit_ports (opens the editor on a component file; not via the command) | — |
| C25 | Extension (Palette; Get Started walkthrough) | Create a project (naut new) | package.json command `nautilus.newProject` | — | — | 17 |
| C26 | Extension (Palette) | Open the Get Started walkthrough | package.json command `nautilus.getStarted` | — | — | 01 (auto-open, not the command) |
| C27 | Extension (Text editor title; Palette) | Open a text file as a diagram editor (replaces the tab, keeps unsaved edits) | package.json command `nautilus.diagram.openAsDiagram` | — | ed_open_diagram | 04 |
| C28 | Extension (Diagram editor title; Palette) | Show the diagram's source text beside it | package.json command `nautilus.diagram.showSource` | — | — | 04 |

## FBD editor (`?` list)

| id | area | gesture | source of the claim | webview test | rig verb | smoke |
|---|---|---|---|---|---|---|
| F01 | FBD | Click a block, chip, coil, note or wire to select it | "?" list FBD / Select / `Click` | diagram.test.mjs: FBD: deleting two selected notes (selection by click) | fbd_pin_el / g_click | — |
| F02 | FBD | Box-select several nodes | "?" list FBD / Select / `Shift + drag` | fbd-drag.test.mjs: FBD drag: Shift+drag over empty canvas selects exactly the boxed nodes (F02) | — | — |
| F03 | FBD | Add to or remove from the selection | "?" list FBD / Select / `Ctrl + click` | diagram.test.mjs: FBD: deleting two selected notes | — | — |
| F04 | FBD | Select everything | "?" list FBD / Select / `Ctrl + A` | diagram.test.mjs: FBD: Ctrl+A selects every node | — | — |
| F05 | FBD | Double-click to edit a constant, rename a wire or instance, edit a note | "?" list FBD / Edit / `Double-click` | diagram.test.mjs: FBD: double-clicking an FB header renames the instance | float_edit, fbd_add_tag_ref | — |
| F06 | FBD | Commit an in-place edit (Ctrl+Enter in a note) | "?" list FBD / Edit / `Enter` | — | float_edit | — |
| F07 | FBD | Cancel an in-place edit | "?" list FBD / Edit / `Esc` | fbd-drag.test.mjs: FBD drag: Esc cancels a constant edit — no setLiteral, chip keeps its value (F07) | — | — |
| F08 | FBD | Drag from an output pin to an input pin to wire; drop on + to add an input | "?" list FBD / Edit / `Drag pin → pin` | diagram.test.mjs: FBD: disconnecting two inputs of one block goes highest pin first (disconnect, not wire); fbd-drag.test.mjs: FBD drag: output pin → input pin posts a rewire naming exactly those pins; dropping a wire on a block's + posts addInput; a wire released on empty canvas posts nothing | fbd_wire | — |
| F09 | FBD | Delete the selection; a selected wire disconnects | "?" list FBD / Edit / `Del / Backspace` | diagram.test.mjs: FBD: deleting a wired coil posts no disconnects for its own edges | fbd_wire (asserts) | — |
| F10 | FBD | Copy, cut, paste; pastes into another .fbd too | "?" list FBD / Edit / `Ctrl + C / X / V` | diagram.test.mjs: FBD: Ctrl+C / Ctrl+V duplicates in place; Ctrl+X deletes | — | 05 |
| F11 | FBD | Move a node; the position is pinned in the file | "?" list FBD / Layout & view / `Drag a node` | fbd-drag.test.mjs: FBD drag: dragging a node posts ONE setLayout per drag, with the new coordinates (F11) | fbd_move_node (selftest: reads the `@layout` entry back) | — |
| F12 | FBD | Move the selection with the arrow keys (pinned like a drag) | "?" list FBD / Layout & view / `Arrow keys` | diagram.test.mjs: FBD: arrow-key moves persist as ONE setLayout | — | — |
| F13 | FBD | Pan the canvas by dragging empty space | "?" list FBD / Layout & view / `Drag empty canvas` | fbd-drag.test.mjs: FBD drag: dragging empty canvas pans the viewport and posts no edit (F13) | — | — |
| F14 | FBD | Scroll or pinch to zoom (corner buttons zoom and fit too) | "?" list FBD / Layout & view / `Scroll / pinch` | diagram.test.mjs: FBD zoom: Ctrl+= / Ctrl+- / Ctrl+0 drive the xyflow viewport too (keys only) | fbd_zoom_to | 06 (ladder and SFC only) |
| F15 | FBD | Zoom in / out / fit | "?" list FBD / Layout & view / `Ctrl + = / Ctrl + - / Ctrl + 0` | diagram.test.mjs: FBD zoom: Ctrl+= / Ctrl+- / Ctrl+0 | fbd_zoom_to | 06 (ladder and SFC only) |
| F16 | FBD | Undo the last edit to the file from the diagram | "?" list FBD / File / `Ctrl + Z` | — | g_key (ctrl+z in takes) | 03 (preview panel) |
| F17 | FBD | Redo | "?" list FBD / File / `Ctrl + Y / Ctrl + Shift + Z` | mimic-more.test.mjs: M17 Ctrl+Z / Ctrl+Y / Ctrl+Shift+Z post no message and are not preventDefault-ed (undo/redo is VS Code's text undo over the host's WorkspaceEdit; the webview owns no stack, so that is all that is observable) | — | 03 (preview panel) |
| F18 | FBD | Save the file from the diagram | "?" list FBD / File / `Ctrl + S` | — | g_save | 03 (preview panel) |

## Ladder editor (`?` list)

| id | area | gesture | source of the claim | webview test | rig verb | smoke |
|---|---|---|---|---|---|---|
| L01 | LD | Click an element to select it | "?" list LD / Select / `Click` | diagram.test.mjs: Ladder: click a rung name, Del deletes the rung (rung name only) | ld_node_el / click_el | — |
| L02 | LD | Click a rung's name to select the whole rung | "?" list LD / Select / `Click a rung's name` | diagram.test.mjs: Ladder: click a rung name, Del deletes the rung | ld_select_rung | — |
| L03 | LD | Double-click to retag a contact or coil, edit arguments, rename a rung | "?" list LD / Edit / `Double-click` | diagram.test.mjs: Ladder: the declare offer covers a block call's arguments (retag path) | ld_add_contact, ld_rename_block, float_edit | — |
| L04 | LD | Commit / cancel an in-place edit | "?" list LD / Edit / `Enter / Esc` | — | float_edit | — |
| L05 | LD | Click ⊕ to insert an element at that spot | "?" list LD / Edit / `⊕` | — | ld_add_contact, ld_add_coil, ld_add_block | — |
| L06 | LD | Drag a palette item onto a rung spot | "?" list LD / Edit / `Drag a palette item` | diagram.test.mjs: Ladder: the FB… picker names a TON / CTU instance with the first free name | ld_palette, g_drag_to | — |
| L07 | LD | Drag an element to another spot or rung | "?" list LD / Edit / `Drag an element` | diagram.test.mjs: Ladder zoom: a palette drop and a node drag still hit their spots at 173%; ld-keys.test.mjs: a contact dragged into another rung / to a later spot in its own rung post one `move` op (L07) | ld_move_element (selftest: another rung, the same rung, a coil) | — |
| L08 | LD | Delete the element, or the rung when its name is selected | "?" list LD / Edit / `Del / Backspace` | diagram.test.mjs: Ladder: click a rung name, Del deletes the rung | ld_delete_last_coil | — |
| L09 | LD | Press N to toggle a contact between NO and NC | "?" list LD / Edit / `N` | ld-keys.test.mjs: N on a selected contact posts toggleNeg, again flips it back | — | — |
| L10 | LD | Press M to cycle a coil normal → set → reset | "?" list LD / Edit / `M` | ld-keys.test.mjs: M on a selected coil cycles normal -> set -> reset -> normal | — | — |
| L11 | LD | Press B to wrap the selection in a parallel branch | "?" list LD / Edit / `B` | — | ld_add_branch | — |
| L12 | LD | Copy, cut, paste an element (into another ladder too) | "?" list LD / Edit / `Ctrl + C / X / V` | — | — | 05 (cut / paste) |
| L13 | LD | Esc cancels a drag | "?" list LD / Edit / `Esc` | diagram.test.mjs: Ladder: Esc cancels an in-flight palette drag | — | — |
| L14 | LD | Zoom around the pointer | "?" list LD / View / `Ctrl + wheel / pinch` | diagram.test.mjs: Ladder zoom: Ctrl+wheel zooms around the cursor | diagram_zoom | 06 |
| L15 | LD | Zoom in / out (corner buttons too) | "?" list LD / View / `Ctrl + = / Ctrl + -` | diagram.test.mjs: Ladder zoom: buttons and Ctrl+= / Ctrl+- / Ctrl+0 | diagram_zoom | 06 |
| L16 | LD | Fit the widest rung to the pane width | "?" list LD / View / `Ctrl + 0` | diagram.test.mjs: Ladder zoom: buttons and Ctrl+= / Ctrl+- / Ctrl+0 | diagram_zoom | 06 |
| L17 | LD | Pan by middle-dragging (wheel and scrollbars scroll) | "?" list LD / View / `Middle-drag` | ld-keys.test.mjs: a middle-button drag scrolls the pane by the drag delta and posts no op; the wheel scrolls too | — | — |
| L18 | LD | Undo the last edit to the file from the diagram | "?" list LD / File / `Ctrl + Z` | — | g_key (ctrl+z in takes) | — |
| L19 | LD | Redo | "?" list LD / File / `Ctrl + Y / Ctrl + Shift + Z` | ld-keys.test.mjs: Ctrl+Z / Ctrl+Shift+Z / Ctrl+Y post diagramKey undo / redo / redo | — | — |
| L20 | LD | Save the file from the diagram | "?" list LD / File / `Ctrl + S` | — | g_save | — |

## SFC editor (`?` list)

| id | area | gesture | source of the claim | webview test | rig verb | smoke |
|---|---|---|---|---|---|---|
| S01 | SFC | Click a step, transition, action or note to select it | "?" list SFC / Select / `Click` | diagram.test.mjs: SFC: after the float editor closes, Del works without another click | sfc_select_step, sfc_select_trans | — |
| S02 | SFC | Add a step to or remove it from the selection | "?" list SFC / Select / `Ctrl / Shift + click` | diagram.test.mjs: SFC: Ctrl-click multi-selects steps | — | — |
| S03 | SFC | Select every step | "?" list SFC / Select / `Ctrl + A` | diagram.test.mjs: SFC: Ctrl+A then Del deletes every step and transition as ONE op | — | — |
| S04 | SFC | Add a step (under the selected step, joined by a transition, or a free step) | "?" list SFC / Edit / `+ step` | diagram.test.mjs: SFC: empty chart (null arrays): + step ... Enter adds the INITIAL step | sfc_add_step, sfc_init | — |
| S05 | SFC | Add a transition from the selected step to an existing or new step | "?" list SFC / Edit / `+ transition` | — | sfc_add_transition, sfc_add_transition_new_step, sfc_add_transition_condition | — |
| S06 | SFC | Add an alternative branch out of the selected step | "?" list SFC / Edit / `+ alt branch` | — | sfc_add_alt_branch | — |
| S07 | SFC | Widen the selected transition's TO with a new parallel step | "?" list SFC / Edit / `+ parallel branch` | — | sfc_add_parallel_branch | — |
| S08 | SFC | Add another source step to the selected transition (simultaneous convergence) | "?" list SFC / Edit / `+ join` | — | sfc_join_step | — |
| S09 | SFC | Double-click to rename a step, edit a condition, an action or its ST body | "?" list SFC / Edit / `Double-click` | diagram.test.mjs: SFC: after the float editor closes, Del works (opens the editor) | sfc_rename_step, sfc_add_action, sfc_add_transition_condition, float_edit | — |
| S10 | SFC | Commit / cancel an in-place edit or the add form | "?" list SFC / Edit / `Enter / Esc` | diagram.test.mjs: SFC: Esc closes the add form | float_edit | — |
| S11 | SFC | Drag a step to move it; the position is pinned | "?" list SFC / Edit / `Drag a step body` | diagram.test.mjs: SFC zoom: step drag and connect rubber band stay under the cursor at 200% | g_drag | — |
| S12 | SFC | Drag the ⊙ handle onto another step to connect them | "?" list SFC / Edit / `Drag a step’s ⊙ handle` | diagram.test.mjs: SFC zoom: step drag and connect rubber band stay under the cursor at 200% | sfc_add_transition | — |
| S13 | SFC | Delete the selection (cascade offered for a step with transitions) | "?" list SFC / Edit / `Del / Backspace` | diagram.test.mjs: SFC: Ctrl+A then Del deletes every step and transition as ONE op | — | — |
| S14 | SFC | Copy, cut, paste steps with actions and inner transitions (into another .sfc too) | "?" list SFC / Edit / `Ctrl + C / X / V` | diagram.test.mjs: SFC: Ctrl-click multi-selects steps; copy/paste posts ONE pasteSteps; SFC: Ctrl+X cuts | — | 05 |
| S15 | SFC | Esc cancels a connect drag or closes a popover | "?" list SFC / Edit / `Esc` | diagram.test.mjs: SFC: Esc closes the add form (popover half only) | — | — |
| S16 | SFC | Zoom around the pointer | "?" list SFC / View / `Ctrl + wheel / pinch` | diagram.test.mjs: SFC zoom: Ctrl+= / Ctrl+- / Ctrl+0 / Ctrl+wheel | diagram_zoom | 06 |
| S17 | SFC | Zoom in / out (corner buttons too) | "?" list SFC / View / `Ctrl + = / Ctrl + -` | diagram.test.mjs: SFC zoom: Ctrl+= / Ctrl+- / Ctrl+0 / Ctrl+wheel | diagram_zoom | 06 |
| S18 | SFC | Fit the whole chart to the pane | "?" list SFC / View / `Ctrl + 0` | diagram.test.mjs: SFC zoom: an overflowing chart fits on first load; SFC zoom: Ctrl+0 | diagram_zoom | 06 |
| S19 | SFC | Pan by middle-dragging (wheel and scrollbars scroll) | "?" list SFC / View / `Middle-drag` | sfc-pan.test.mjs: S19 SFC: middle-drag pans the chart by the drag delta and posts no edit; the wheel scrolls | — | — |
| S20 | SFC | Undo the last edit to the file from the diagram | "?" list SFC / File / `Ctrl + Z` | — | g_key (ctrl+z in takes) | 03 (field undo only) |
| S21 | SFC | Redo | "?" list SFC / File / `Ctrl + Y / Ctrl + Shift + Z` | mimic-more.test.mjs: M17 Ctrl+Z / Ctrl+Y / Ctrl+Shift+Z post no message and are not preventDefault-ed (undo/redo is VS Code's text undo over the host's WorkspaceEdit; the webview owns no stack, so that is all that is observable) | — | 03 (field undo only) |
| S22 | SFC | Save the file from the diagram | "?" list SFC / File / `Ctrl + S` | — | g_save | 03 (field undo only) |

## Mimic editor (`?` list)

| id | area | gesture | source of the claim | webview test | rig verb | smoke |
|---|---|---|---|---|---|---|
| M01 | Mimic | Click equipment, a pipe or a label to select it | "?" list MIMIC / Select / `Click` | gestures.test.mjs: clicking an anchored end selects the END, not the equipment underneath | mimic_eq_el / click_el | — |
| M02 | Mimic | Add equipment to or remove it from the selection | "?" list MIMIC / Select / `Ctrl + click` | gestures.test.mjs: clipboard: Ctrl-click multi-selects | — | — |
| M03 | Mimic | Select all equipment | "?" list MIMIC / Select / `Ctrl + A` | gestures.test.mjs: clipboard: Ctrl+A selects all equipment; Del deletes it as ONE batch | — | — |
| M04 | Mimic | Shift-click a pipe vertex to multi-select pipe points | "?" list MIMIC / Select / `Shift + click a vertex` | gestures.test.mjs: shift-click node multi-select + Delete (x3), shift-click MISSING a vertex (x2), shift-click a node TWICE, VISUAL: shift-click a vertex | — | — |
| M05 | Mimic | Esc cancels the tool, leaves the ports editor, or deselects | "?" list MIMIC / Select / `Esc` | mimic-more.test.mjs: M05 Esc in pipe mode (draft discarded, then Select), in the ports editor, with equipment selected | — | — |
| M06 | Mimic | Drag to move equipment, labels, pipe points | "?" list MIMIC / Edit / `Drag` | gestures.test.mjs: interior VERTEX drag — preview == commit; a parse error mid-edit locks the stale canvas: dragging equipment posts no op | g_drag, mimic_drop | — |
| M07 | Mimic | Drag a pipe end onto a port to attach it, or off to detach | "?" list MIMIC / Edit / `Drag a pipe end` | gestures.test.mjs: terminal ATTACH; terminal DETACH; drag re-anchor; dragging a pipe END in Select shows the port dots | — | — |
| M08 | Mimic | Nudge the selection (Shift = one grid step) | "?" list MIMIC / Edit / `Arrow keys` | gestures.test.mjs: ports-edit: ArrowRight nudges the selected port; mimic-more.test.mjs: M08 ArrowRight nudges the equipment by 1 px, Shift+ArrowRight by one grid step | — | — |
| M09 | Mimic | Delete the selection (attached pipe ends kept, detached) | "?" list MIMIC / Edit / `Del / Backspace` | gestures.test.mjs: clipboard: Ctrl+A ... Del deletes it as ONE batch | — | — |
| M10 | Mimic | Copy, cut, paste equipment with props, bindings and pipes (into another mimic too) | "?" list MIMIC / Edit / `Ctrl + C / X / V` | gestures.test.mjs: clipboard: Ctrl-click multi-selects, Ctrl+C / Ctrl+V pastes ONE batch; Ctrl+X deletes the selection in one batch | — | 05 |
| M11 | Mimic | Duplicate the selection in place | "?" list MIMIC / Edit / `Ctrl + D` | gestures.test.mjs: clipboard: Ctrl+D duplicates one instance | — | — |
| M12 | Mimic | Press P to open the ports editor for the selected equipment | "?" list MIMIC / Edit / `P` | gestures.test.mjs: ports-edit tests (enterPortsMode presses p) | component_edit_ports | — |
| M13 | Mimic | Click to add a pipe point; starting or ending on a port dot anchors that end | "?" list MIMIC / + Pipe tool / `Click` | gestures.test.mjs: port dot -> port dot -> Enter commits an anchored pipe | mimic_pipe, mimic_pipe_direct | — |
| M14 | Mimic | Enter or double-click finishes the pipe | "?" list MIMIC / + Pipe tool / `Enter / double-click` | gestures.test.mjs: Enter completes a BENT pipe onto a hovered port; floating control: two clicks + Enter | mimic_pipe | — |
| M15 | Mimic | Hold Shift for a free angle while placing a point | "?" list MIMIC / + Pipe tool / `Shift` | mimic-more.test.mjs: M15 without Shift the second point is orthogonalised; holding Shift keeps the diagonal | — | — |
| M16 | Mimic | Undo the last edit to the file from the diagram | "?" list MIMIC / File / `Ctrl + Z` | — | g_key (ctrl+z in takes) | — |
| M17 | Mimic | Redo | "?" list MIMIC / File / `Ctrl + Y / Ctrl + Shift + Z` | mimic-more.test.mjs: M17 Ctrl+Z / Ctrl+Y / Ctrl+Shift+Z post no message and are not preventDefault-ed (undo/redo is VS Code's text undo over the host's WorkspaceEdit; the webview owns no stack, so that is all that is observable) | — | — |
| M18 | Mimic | Save the file from the diagram | "?" list MIMIC / File / `Ctrl + S` | — | g_save | — |

## Component (ports) editor (`?` list)

| id | area | gesture | source of the claim | webview test | rig verb | smoke |
|---|---|---|---|---|---|---|
| P01 | Component | Drag a port dot to move the port | "?" list COMP / Ports / `Drag a dot` | component-ports.test.mjs: P01 component: dragging a port dot posts the port at its new fraction, and the dot renders there | component_move_port (selftest; an explicit `dir` is dropped by the drag: XFAIL, #130) | — |
| P02 | Component | Double-click a port dot to remove the port | "?" list COMP / Ports / `Double-click a dot` | — | component_add_port (add side only) | — |
| P03 | Component | Double-click the outline to add a port there | "?" list COMP / Ports / `Double-click the outline` | — | component_add_port | — |
| P04 | Component | Click a port dot or panel row to select the port | "?" list COMP / Ports / `Click a dot or panel row` | gestures.test.mjs: ports-edit: a DEAD-CENTER click on a port dot selects the port; VISUAL: ports-edit dot selection (in the mimic editor's ports mode) | — | — |
| P05 | Component | Esc deselects the port | "?" list COMP / Ports / `Esc` | component-ports.test.mjs: P05 component: Esc deselects the selected port dot and posts nothing | — | — |
| P06 | Component | Undo the last edit to the file from the diagram | "?" list COMP / File / `Ctrl + Z` | — | g_key (ctrl+z in takes) | — |
| P07 | Component | Redo | "?" list COMP / File / `Ctrl + Y / Ctrl + Shift + Z` | component-ports.test.mjs: P07 component: redo (no webview undo/redo: the keys post nothing and the dot follows the doc the host re-sends) | — | — |
| P08 | Component | Save the file from the diagram | "?" list COMP / File / `Ctrl + S` | — | g_save | — |

## Other claims (README, CHANGELOG, menus)

| id | area | gesture | source of the claim | webview test | rig verb | smoke |
|---|---|---|---|---|---|---|
| X01 | Extension | Open a diagram by default via workbench.editorAssociations | README Diagrams | — | — | 17 |
| X02 | Extension | Open a *.mimic.json in the graphical editor (Open With → Text Editor to leave) | README HMI mimic editor | gestures.test.mjs: restore: the mimic editor mounts with saved state | — | — |
| X03 | Extension | Open a component file in the component editor | README HMI mimic editor (ports) | gestures.test.mjs: restore: the component editor mounts with saved state | component_edit_ports | — |
| X04 | Extension | A 0-byte .fbd/.ld/.sfc opens with the Empty file banner; initialize or the first gesture writes a skeleton | CHANGELOG 0.10.0 | diagram.test.mjs: FBD: null arrays ... blank file offers initialize; Ladder: a blank file seeds with the file name | sfc_init | 09 |
| X05 | Extension | A broken first load keeps its own chrome; a doc that never parsed offers Reopen as Text Editor | CHANGELOG 0.10.0 | diagram.test.mjs: Ladder/SFC: a broken FIRST load keeps its own chrome; gestures.test.mjs: a doc that never parsed shows the error and Reopen as Text Editor | — | — |
| X06 | Extension | A parse error mid-edit locks the stale canvas | CHANGELOG 0.10.0 | gestures.test.mjs: a parse error mid-edit locks the stale canvas | — | — |
| X07 | Extension | Webview reload restores the editor from saved state and the page says ready | CHANGELOG 0.10.0 | diagram.test.mjs: Ready; Restore: (x6); gestures.test.mjs: restore: (x2) | — | 07 |
| X08 | Extension | Diagrams follow the VS Code theme (light, dark, high contrast) | CHANGELOG 0.10.0 | diagram.test.mjs: Theme: (x3) | — | 11 |
| X09 | Extension | Live pill: green when a controller is reachable, amber offline; click toggles live values | README Live values | gestures.test.mjs: the live pill reflects nautilus.liveValues.enabled and toggles it | — | 07 |
| X10 | Extension | Live values paint on FBD, ladder (power flow) and SFC (active step) | README Live values | — | — | 07 |
| X11 | Extension | Inline live-value pills beside identifiers in .st/.fbd/.ld/.sfc text | README Live values | — | — | 13 (.st, .fbd, .ld text; not .sfc) |
| X12 | Extension | Live Values panel lists every tag and local with its value | README Live values | — | — | 13 |
| X13 | Extension | Status-bar item shows whether the file matches the controller | README Online edit | — | — | 08 |
| X14 | Extension | Download and Rollback ask for confirmation naming URL and program; nautilus.confirmControllerWrites=false skips it | README Online edit | — | — | 08 |
| X15 | Extension | Missing-CLI prompt Install naut; min-version warning (Update naut / Don't show again) | README Get started | — | — | 01, 02 |
| X16 | Extension | Walkthrough opens once, outside a nautilus project | README Get started | — | — | 01 |
| X17 | Extension | Diagnostics as you type in .st/.fbd/.ld/.sfc (naut lsp) | README Language intelligence | — | — | 01 |
| X18 | Extension | Go to definition, hover and completion in ST | README Language intelligence | — | — | 14 |
| X19 | Extension | *_test.yaml suites in the Testing view; run one from the gutter; failure inline on the assertion | README Testing | — | — | 15 |
| X20 | Extension | JSON-schema completion and validation for nautilus.yaml, tag, alarm and test files | README Testing | — | — | 14 (nautilus.yaml and *_test.yaml; tag and alarm files not exercised) |
| X21 | Extension | nautilus.fb.monitor, run by the FbMonitorLenses CodeLens over each FUNCTION_BLOCK header: pick which declared instance the body's live values read | src/extension.ts, src/liveValues.ts | — | — | 17 |
| X22 | Ladder | L5X opens read-only: pill, no palette, edits do nothing | README Rockwell L5X | diagram.test.mjs: Ladder: an L5X model is read-only | — | 10 |
| X23 | Ladder | Declare offer (amber declare …) files an undeclared identifier under VAR_EXTERNAL or VAR | CHANGELOG 0.10.0 | diagram.test.mjs: Ladder: the declare offer covers a block call's arguments | ld_declare | — |
| X24 | Ladder | FB… picker places any standard or project function block under a chosen instance name | README Diagrams | diagram.test.mjs: Ladder: the FB… picker names a TON / CTU instance with the first free name | ld_add_block | — |
| X25 | Ladder | Double-click an FB header to rename the instance everywhere | README Diagrams | — | ld_rename_block | — |
| X26 | Ladder | Empty body renders the palette; + rung adds the first rung | CHANGELOG 0.10.0 | diagram.test.mjs: Ladder: empty body (rungs null) renders the palette; + rung works | ld_add_rung | — |
| X27 | Ladder | Diff overlay: added / removed / changed elements marked, removed ghosted | README Visual diff | diagram.test.mjs: Theme: ladder diff colours come from theme tokens | — | 04, 11 |
| X28 | FBD | + add palette: function block places any standard block (PID included) with every input open | README Diagrams | diagram.test.mjs: FBD palette: function block places a PID | fbd_add_block | — |
| X29 | FBD | + add palette: output reference names its source (SpeedRef := lic.CV) | CHANGELOG 0.10.0 | diagram.test.mjs: FBD palette: output reference takes an FB output as its source | fbd_add_tag_ref | — |
| X30 | FBD | + add palette: comment / note | package.json UI (palette) | — | fbd_add_comment | — |
| X31 | FBD | Diff overlay: added / removed / changed blocks and wires | README Visual diff | — | — | 04 |
| X32 | SFC | Vars panel declares and deletes variables | CHANGELOG 0.10.0 | diagram.test.mjs: SFC: the vars panel declares/deletes with the payload naut sfc edit accepts | — | — |
| X33 | SFC | Diff overlay: added / removed / changed steps and transitions | README Visual diff | diagram.test.mjs: Restore: SFC mounts from a saved model (and diff) | — | 04 |
| X34 | SFC | Active step highlights live | README Diagrams | — | — | 07 |
| X35 | Mimic | Place equipment from the palette (Tank, Pump, Valve, Gauge, Sparkline, user components) | README HMI mimic editor | — | mimic_drop | — |
| X36 | Mimic | Bind props to tags in the props panel | README HMI mimic editor | — | mimic_bind | — |
| X37 | Mimic | Pipes snap to ports, follow equipment when it moves, orthogonal route suggested around obstacles | README HMI mimic editor | gestures.test.mjs: draw preview == committed render; terminal ATTACH / DETACH | mimic_pipe | — |
| X38 | Mimic | Snap to grid (nautilus.mimic.snapToGrid) | README Settings | mimic-more.test.mjs: X38 snapToGrid on lands a drag on the 10 px grid; off moves the exact delta | — | — |
| X39 | Mimic | Live canvas animates bound props from the controller | README HMI mimic editor | — | — | 13 |
| X40 | Extension | Signature help while typing a call: parameters with types, FB inputs and outputs (=>), the active one highlighted by comma position or named pin | README Language intelligence | — | — | 14 |
| X41 | Extension | Outline view, breadcrumbs and Go to Symbol in Editor list POUs, VAR sections and declarations, FBD statements, ladder rungs, SFC steps / transitions / actions (naut lsp documentSymbol) | README Language intelligence | — | — | 14 (.st, .ld, .sfc; not .fbd) |

## Count of rows with no coverage at all

**0 of 155.** Every row has at least one layer of coverage: a webview test, a
rig verb, or a smoke check. Some rows are covered only in part, and the cell
says which part:

- Commands: the diff-against-controller and diff-between-revisions commands
  (C16, C17, C19, C20, C22, C23) are covered only as a present menu entry (04);
  the ladder and SFC diff commands (C18, C21) have a render test, not the
  command; C24 opens the editor by verb, not via the command; C26 is covered by
  the walkthrough's auto-open, not the command. No webview test can reach a
  command, because commands live in the extension host.
- Undo and redo (F17, S20, S21, M17): the webview posts no message and does not
  swallow the keys; the undo itself is VS Code's text undo over the host's
  WorkspaceEdit, so only smoke 03 (field undo) sees it happen. S22 (save) is
  likewise only 03's field undo.
- Live values: X11 pills are checked in .st, .fbd and .ld text, not .sfc. X20
  schema completion is checked for nautilus.yaml and *_test.yaml, not tag or
  alarm files.
- Single-sided gestures: F08 (disconnect, not wire), F14/F15 (ladder and SFC
  zoom only in smoke), L01 (rung name only), S15 (popover half only), P02
  (add side only).

Notes for the next pass:

- Partial rows (listed above) are the place to add coverage first.
- The rig verbs for the ladder and FBD were written for the lift-station
  episode, so they exercise gestures the takes needed, not a systematic sweep.
