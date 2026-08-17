# Changelog

All notable changes to the **nautilus IEC 61131-3** extension are documented
here. The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [0.9.17] - 2026-08-16

### Added

- **Acceptance tests in the Test Explorer.** `*_test.yaml` suites appear in
  VS Code's Test Explorer and run through `nautilus test` — per-test results,
  failure messages on the failing step, and a gutter run button
  (`src/acceptanceTests.ts`).
- **JSON schemas** for `nautilus.yaml`, tag files, and `*_test.yaml` —
  completion and validation in any YAML editor that honors schema
  associations.
- **ST inside test expectations is highlighted** via an injection grammar,
  and the extension now wins the `.st` file association.
- Manifest-aware language features (hover shows a tag's unit and
  description, tag completion in expectations, unknown-tag squiggles in key
  position) ship with the `nautilus` CLI ≥ 0.5.0 — update it to light
  them up.

## [0.9.16] - 2026-07-27

### Changed

- Marketplace publisher is now **joyauto** (extension ID `joyauto.vscode-iec`).
  The previous `joyautomation` publisher record was defective on the
  Marketplace side; installs from the old ID were never possible.

## [0.9.15] - 2026-07-25

### Fixed
- **Shift-clicking a pipe vertex to multi-select could make an already
  highlighted node's dot visually disappear, with no data change.** Root
  cause confirmed in the gesture harness with `getComputedStyle`/
  `getBoundingClientRect` assertions (not just class-presence checks): a
  shift-click landing on a pipe's TERMINAL/anchor handle instead of an
  interior vertex — an easy miss, since the terminal handle is a larger
  target sitting right next to the interior ones being multi-selected — fell
  through `vtxDown`'s terminal branch, which (unlike every other selection
  handler 0.9.14 fixed) never got the "Shift-miss is a no-op" guard. It
  unconditionally overwrote the whole node multi-selection with a plain
  end-of-pipe selection, and since a terminal handle's own dot had no
  selected-state visual at all, every previously highlighted node's `.sel`
  style vanished in the same frame — no CSS bug, no data mutation, just a
  clobbered selection with nothing left to show it (`EditorCanvas.svelte`).
- **A pipe's terminal/anchor handle, once legitimately selected, never
  showed any visual of its own.** `kind:'end'` selections (arrow-key nudge,
  the always-live anchor click target) rendered identically whether selected
  or not — found while sweeping every other selected-state visual moved
  across 0.9.12–0.9.14's handle-layer refactors. Added the same `.sel`
  treatment interior vertices already have, for both a plain terminal point
  and the anchor ring + center dot.
- **Ports-edit dots never visibly reacted to being selected** (panel row ↔
  canvas dot, 0.9.13). Every dot's BASE style already used the selection
  colors, so the `sel` class toggle — correctly wired — painted no
  difference; the standalone `*.component.json` ports editor had the right
  pattern (a further, more opaque style on `.sel`) and this extension's own
  canvas editor didn't. Fixed to match.

### Added
- **Visual (not just class-presence) regression coverage for the mimic
  editor's selected-state handles.** The gesture harness's `Editor` gained
  `visualState(selector)` — computed style + rendered bounding box for every
  matching element — plus shared assertions (`assertVisuallyDistinctSelection`
  / `isVisiblyRendered`) that a `.sel` class must actually paint something
  visible and different from the unselected baseline, not just be present in
  the DOM. Covers pipe vertices, terminal/anchor handles, and ports-edit
  dots.

## [0.9.14] - 2026-07-25

### Fixed
- **Shift-clicking pipe vertices to multi-select, then Delete, could silently
  do nothing.** Reproduced in the gesture harness: `ed.selection` is only
  Shift-aware in the vtx handle's own pointerdown handler — every OTHER
  selection-setting pointerdown (a shift-click that misses a vertex handle —
  small targets — and lands on empty canvas, the pipe's own stroke, a
  midpoint square, an equipment box, or a label) unconditionally overwrote
  or nulled the selection, discarding an entire in-progress node
  multi-selection with no visual feedback. By the time Delete was pressed
  there was nothing left selected. Fixed: Shift is now a no-op everywhere
  except the vertex toggle it's meant for (`EditorCanvas.svelte`). Also
  hardened the vertex handle's double-click-to-delete gesture to ignore a
  held Shift, so a fast same-vertex re-click can't slip a point deletion in
  behind the multi-select's back.
- **A pipe node multi-selection had no mouse-only way to delete it.** The
  properties panel now shows a "Delete N points" button for a `nodes`
  selection instead of only documenting the Delete/Backspace shortcut.
- **A ports-edit dot sitting on its equipment's own edge could lose a
  dead-center click to the box underneath it.** Same z-order class as
  0.9.11's pipe-anchor fix: the dots moved into their own layer, painted
  after the equipment boxes, so they win hit-testing outright rather than
  needing a proximity workaround.

## [0.9.13] - 2026-07-25

### Added
- **Arrow-key nudge for the ports editor.** With ports-edit mode active and a
  port selected (a panel row or a canvas dot), arrow keys now nudge that
  port instead of hitting the generic equipment-nudge branch and dragging
  the whole instance — the surprise this fixes. Step: 1 canvas pixel
  converted to the equipment box's fraction space per press, Shift = one
  grid step, mirroring the equipment/pipe-end nudge conventions exactly
  (`nudgePort`, `webview-ui/src/mimic/portsGestures.ts`). Arrow keys are
  swallowed (not forwarded to the equipment) while ports-edit is active and
  no port is selected. The ports-mode hint bar now mentions the shortcut.

## [0.9.12] - 2026-07-25

### Fixed
- **Enter completed a pipe but dropped its connection to the port under the
  cursor.** The draw-completion rule only treated the live rubber-band
  segment as an implicit final click when the already-clicked draft *didn't*
  meet the point floor — so for any pipe with a corner (click a port, click a
  bend, then Enter while resting on the destination port) the hovered port
  was silently discarded and the pipe landed short of it, while a plain
  floating pipe finished fine. The rubber-band end is now committed onto a
  hovered destination port even when the clicked draft already meets the
  floor; pure cursor drift over empty canvas is still ignored
  (`resolveDraftFinish`, `webview-ui/src/mimic/pipeDraft.ts`).
- **The live pipe-drag preview didn't show what releasing would create.**
  While dragging a pipe's terminal onto a port, the preview drew a plain line
  to the cursor (keeping the dragged end's stale point and omitting the port
  stub / orthogonal corner), then snapped to a different, stubbed route on
  release. The draw-mode rubber band had the same split — it showed the
  segment to a hovered port that finishing then dropped. Both are gone: the
  drag preview now feeds the SAME `{points, from, to}` the drop commits
  through the SAME `routedPoints()` pipeline the finished pipe renders with
  (port-snap tracked live on the drag), and the draw preview renders the
  shared `draftSpec` the finish commits — one geometry path, no parallel
  preview math (`EditorCanvas.svelte`).

### Added
- **Browser gesture harness** (`webview-ui/gesture-harness/`,
  `npm run test:gestures`) — drives the production mimic bundle in headless
  Chrome with real pointer/keyboard input to catch focus, hit-testing and
  preview-vs-commit bugs the pure-logic tests can't see. Not part of CI (needs
  a browser). The two fixes above ship with regression cases in it.

## [0.9.11] - 2026-07-25

### Fixed
- **Enter wouldn't complete a pipe draw whose end was a port anchor.**
  The draw-mode completion guard was a raw `draft.length >= 2` — the
  number of EXPLICIT clicks placed — which happens to total 2 in every
  case except one: a port already shows its anchor preview before it's
  clicked, so "click port A, then Enter while resting on port B" (no
  second click) is the natural gesture for a zero-interior,
  both-ends-anchored pipe, and it silently did nothing. Completion
  (`resolveDraftFinish`, `webview-ui/src/mimic/pipeDraft.ts`) now treats
  the live rubber-band segment as an implicit final click ONLY when the
  clicked draft alone doesn't already meet the anchor-aware point floor —
  an ordinary multi-click floating pipe finishes exactly as clicked,
  unperturbed by wherever the cursor has since wandered. Covers Enter and
  double-click (both call the same completion path).
- **Clicking a pipe's anchored end selected the equipment underneath
  instead.** An anchored end renders exactly on its equipment's port,
  which sits on that equipment's own box — the box, later in paint order,
  won every hit-test there, and (a second, compounding bug) a
  not-yet-selected pipe had no vertex handle to click in the first place
  (handles only rendered for the ALREADY-selected pipe). Fixed two ways:
  an anchored end now has priority over equipment for the very first
  click, at the pointer-capture phase, using the same proximity check the
  pipe-drawing snap already uses (no dependency on paint order); and once
  a pipe IS selected, its vertex/midpoint handles render in their own
  layer painted after the equipment boxes, so an anchor handle is both
  visible and clickable on top of them, not hidden underneath. An
  anchored end's handle is now a **ring** (fill: none, thicker stroke,
  small center dot) rather than a filled dot, so it reads as "pinned to
  something" rather than "a point". Clicking it addresses that specific
  end (a new `Selection` kind, `'end'` — the properties panel treats it
  identically to a plain pipe selection); Arrow keys now nudge it,
  detaching it first if it's anchored (a nudge is never a silent dead
  end, the same convention drag-to-detach already uses).

### Added
- **Route suggestion.** A pure, deterministic autorouter
  (`webview-ui/src/mimic/autoroute.ts`) that generates an orthogonal
  path's interior vertices between two points (each optionally exiting in
  a `PortDir`, honoring the same `PORT_STUB` stub the runtime draws) while
  avoiding a list of obstacle rectangles — equipment boxes, margin
  included by the caller. Tries the straight/L/Z canonical shapes first
  (fewest corners), then hugging each obstacle's four edges, then a
  coarse visibility-graph search minimizing corner count (0-1 BFS over
  the rails formed by the two endpoints' and every obstacle's edges), and
  finally the plain L regardless of collision as an absolute last resort
  — it never throws and never returns nothing. Wired into the
  **port-to-port pipe draw**: completing a draw with BOTH ends anchored
  and ZERO interior clicks (click port A, click port B — or A, then Enter
  over B) now auto-inserts a suggested route into the `addPipe` op and
  defaults that pipe's `routing` to `orthogonal`. It's a one-shot
  GENERATOR, not live state — the inserted vertices are ordinary,
  editable points from then on. A **Re-route** button in the props panel
  (shown for any pipe with at least one anchored end) replaces the
  interior points with a fresh suggestion via a normal, undoable
  `setPipePoints`.

## [0.9.10] - 2026-07-25

### Added
- **Pipe port anchors.** A `MimicPipe` end can now name a specific
  equipment port instead of a stored `[x, y]` — `from`/`to`:
  `{equip, port}`. The anchored end's position is DERIVED (the port's
  resolved absolute position, re-computed every render) rather than
  stored, so it can never go stale: moving the equipment moves the pipe
  end with it automatically, no extra edit needed. In the mimic editor,
  starting or ending a `+ Pipe` draw on a port dot records an anchor
  instead of a raw point (with hover feedback on the dot); dragging an
  existing pipe's terminal vertex onto a port attaches it, off a port
  detaches it (materializing a concrete point at the drop position);
  PropsPanel shows each end's state with a × to detach or
  equipment/port dropdowns to attach. Deleting equipment that has
  anchored pipes materializes those anchors into concrete points as
  part of the same delete — no dangling refs left behind by the editor.
  An anchor that doesn't resolve (unknown equipment/port, e.g. a
  hand-written doc) still renders — flagged, fixable in PropsPanel —
  never refused. `hmi/src/lib/mimic.ts` gained the shared resolution
  (`resolvePipeEndpoints`, `makeGetPort`, `portAbsolute`) both the
  mimic editor and the runtime `<Mimic>` call, so an anchored pipe
  renders identically in both.
- **Port exit directions.** A `MimicPort` can carry an explicit
  `dir: 'left' | 'right' | 'up' | 'down'` — the direction a pipe leaves
  it. Left absent, it's INFERRED from the port's edge position (a port
  sitting exactly on one edge infers that edge's direction; a corner or
  interior port infers none). An anchored pipe end honors its port's
  effective direction with a short straight stub (12 canvas units)
  before any corner, so a pump's discharge port on its right edge
  produces a horizontal exit even when routed `orthogonal`. PortsPanel
  (the mimic editor's ports-mode side list, and the standalone
  `*.component.json` editor) gained a per-port direction selector
  (auto/left/right/up/down — "auto" shows what inference resolves to,
  so you always know whether you're looking at a default or an
  override); port dots in both editors now draw a small direction tick.
- The **heated-tank demo** (`examples/hmi-demo`) now shows both
  features: the city-feed pipe's end is anchored to P-101's inlet, one
  E-101 coolant pipe end is anchored to its port (E-101, a custom
  component, gained an embedded instance-level `ports` override so the
  anchor resolves identically in the editor and at runtime — see
  `resolveRuntimePorts()`'s doc comment on why a *.component.json
  sidecar alone isn't enough for that), and P-101's discharge pipe
  exits **horizontally** before its 90° turn into the tank, driven by
  its `out` port's inferred `dir` — the effect is identical whether
  you're looking at the mimic editor or the shipped app.

## [0.9.9] - 2026-07-25

### Added
- **Full SFC comment parity with FBD/Ladder.** A "+ comment" toolbar
  button adds a note (placeholder text, dblclick to write it — the
  `addComment` op appends it just above `END_SFC`, the same placement
  `insertStatement` uses for FBD's `// text` comment fragment); Del now
  goes through a dedicated `deleteComment` op (`setComment` with empty
  text still deletes too, unchanged); and — new — a comment can be
  **dragged to pin its layout position**, exactly like a step, addressed
  as `cm:<index>` in the `(* @layout *)` block, the same `cm:N` scheme
  `lang/fbd` uses for its own diagram notes. `nautilus sfc edit` gained
  `addComment`/`deleteComment` alongside the existing `setComment`
  (`lang/sfc/edit.go`), with parity tests covering add/delete/roundtrip
  and untouched-region invariance.
- **Visual diff for `.sfc` charts** — the SFC equivalent of FBD/Ladder's
  diagram diff, same commands and UX: `nautilus: Diff SFC Diagram (vs
  git HEAD)` and `(vs Controller)`, plus the sync pill's "≠ controller"
  click-through. Added/removed/changed steps, transitions, action
  associations, and comments overlay with the shared
  added/removed/changed palette and legend; a removed element is spliced
  back into the chart (ghosted, dashed) so a reviewer sees what left as
  well as what arrived. The base/head merge (`diffSfc`, `sfc.ts`) is pure
  and unit-tested; rendering is read-only, matching FBD/Ladder's diff mode.

### Fixed
- **Stray blank line after add-then-delete.** Adding a step, transition,
  or comment inserts a leading blank separator line (matching the file's
  own hand-written style); deleting only ever removed the block's own
  lines, so an add immediately undone by a delete — or deleting one
  element out of a blank-separated list — left a stray or doubled blank
  line behind. Delete now consumes that same separator back
  (`lang/sfc/edit.go`'s `blockDeleteEdit`), so add+delete round-trips are
  byte-identical.

### Changed
- The extension's `npm test` now also runs the webview-ui pure-logic test
  suites (`sfc.test.ts`, the mimic `ports`/`portsGestures`/`routing`
  tests) via `node --experimental-strip-types --test`, previously only
  run by hand — CI now fails if one breaks.

## [0.9.8] - 2026-07-24

### Added
- **SFC drag-to-connect.** A small connect handle appears on hover at the
  bottom edge of a step box (distinct from a body-drag, which still pins
  layout, mirroring the FBD/mimic-editor split between wiring and moving);
  dragging it onto another step adds a transition between them (condition
  `TRUE`, selected as soon as the model round-trips so it's immediately
  editable) with a live rubber-band line and a green highlight on the step
  under the pointer. Esc cancels mid-drag.
- **Orphaned transitions are now visible and fixable from the diagram.**
  A transition whose FROM or TO references an unknown step name (typically
  left behind by deleting a step) used to be silently skipped by the
  diagram's layout pass — present in the model, invisible on the canvas,
  unselectable, undeletable graphically. It now renders as a red dashed
  "problem" chip — `FROM ? → TO ?` with the actual (possibly-unknown)
  names and condition — anchored beside its surviving endpoint when it has
  one, else collected into a strip along the bottom edge. The chip is
  selectable, Del-deletable, dblclick-editable (condition), and carries a
  ⚙ retarget control: a popover with a dropdown of existing steps (plus an
  "other…" free-text escape hatch) to re-point FROM and/or TO, backed by a
  new `setTransitionEnds` structural edit op (`nautilus sfc edit`/
  `lang/sfc`), never stricter than the text format itself.
- **Delete-step cascade offer.** Deleting a step with attached transitions
  now opens an in-canvas popover — "step only (flag N)" (the default,
  preserving the breadcrumb philosophy: nothing cascades silently) or
  "step + N transitions" — instead of either silently leaving dangling
  refs with no explicit choice, or a native `confirm()` dialog.
- **Target pickers.** The "+ transition" and "+ alt branch" popovers' "to
  step" field is now a dropdown of existing steps with an "other…"
  free-text fallback for naming a not-yet-existing step, instead of a bare
  text box.
- **Comments are now visible and editable in the SFC diagram.** A `//`
  comment run renders as a dashed note box in a strip along the top edge
  (steps shift down to make room); click to select, dblclick to edit
  (empty text deletes), Del to delete — the existing `setComment` op,
  simply never wired into the canvas before this release. (Creating a
  brand-new comment graphically isn't wired yet — still a text-only move.)

## [0.9.7] - 2026-07-24

### Added
- **SFC (Sequential Function Chart) graphical editor** — the fourth
  graphical language, alongside FBD, Ladder and the mimic editor, following
  the same architecture: `.sfc` text stays canonical, `nautilus sfc graph`
  projects the render model, and every gesture is a structural op resolved
  by `nautilus sfc edit` into minimal text edits.
  - New `iec-sfc` language (syntax highlighting, language-configuration),
    `nautilus.sfcDiagram` custom editor ("Open With → SFC Diagram"), and
    `nautilus: Open SFC Diagram Preview` command/editor-title button,
    alongside FBD/Ladder's.
  - Classic SFC visual grammar: steps as boxes (double border = initial
    step) with their action associations tabled beside the box; transitions
    as bars across the flow line (single = normal/alternative, double =
    simultaneous divergence/convergence) with the condition beside the bar;
    a transition that loops back to an earlier step (an abort, a return to
    an idle step) renders as a compact "↩" jump glyph instead of stretching
    a line back up the chart. Layout is a pure, deterministic function of
    chart topology — steps rank by BFS distance from the initial step
    (first discovery wins) and column by a post-order tree layout over that
    same discovery order — with FBD's `(* @layout *)` pin block reused
    verbatim as the optional per-step manual-position escape hatch.
  - Gestures: add a step (toolbar, optionally after the selection); add a
    transition from a selected step; branch an alternative or simultaneous
    path off a selected transition; rename a step; edit a transition's
    condition; add/edit/delete a step's action associations inline in its
    table; edit a body action's ST source (the associations that reference
    an `ACTION` block render underlined); drag a step to pin its layout,
    "auto layout" to clear every pin; select + Del to delete a step,
    transition, or association. Every gesture posts through the identical
    op-queue/WorkspaceEdit pipeline FBD and Ladder use, so undo is text
    undo.
  - Live step highlighting: the ACTIVE step outlines and its name
    highlights, reading the retained `_S_<Step>_X` slot the SFC transpiler
    already emits per step — no new controller API, the same live-values
    stream that drives inline `.st` pills.
  - Non-modal diagnostics: a "N problems" toolbar pill plus a badge on the
    offending step or transition, never blocking a save — a structurally
    incomplete chart (a new step with no transitions yet) is a diagnostic
    breadcrumb, exactly like typing the same hole directly into the text.
  - No visual diff yet for `.sfc` (FBD and Ladder both have vs-HEAD/
    vs-Controller diffing) — text diff and `nautilus sfc check` cover chart
    review in this release.

## [0.9.6] - 2026-07-24

### Added
- **Optional snap-to-grid.** A "Snap: On/Off" toggle in the mimic editor's
  toolbar controls whether dragging equipment, labels and pipe vertices
  snaps to the grid; off gives free-pixel placement. Persisted as
  `nautilus.mimic.snapToGrid` (workspace-scoped when a folder is open, same
  convention as the live-values toggle), so it survives editor reloads and
  is also settable from Settings directly. Shift-arrow nudging always steps
  by the grid regardless of the toggle.
- **Orthogonal pipe routing.** Each pipe can opt into `"routing":
  "orthogonal"` (a new `PropsPanel` control, direct/orthogonal) to route
  every segment through a single 90° corner instead of a straight diagonal
  — the corner continues the incoming leg's direction first so a chain of
  corners reads as one clean Z rather than zigzagging. `routing` is a
  document property (defaults to `"direct"`/absent, today's behavior), not
  an editor preference, so the VS Code editor and the runtime `<Mimic>`
  component always draw a pipe identically; flow-dash animation follows
  whichever path is actually drawn. Node dots/hit-targets stay at the
  pipe's real points either way.

### Fixed
- **Multi-node pipe-vertex delete.** Deleting a shift-click node
  multi-selection (or a double-clicked single vertex) down to fewer than 2
  remaining points now consistently removes the whole pipe instead of
  either silently refusing (vertex double-click at the 2-point floor used
  to no-op) or leaving an inconsistent gap between the two delete paths.
  The keyboard multi-node delete also now holds its post-delete shape via
  the same optimistic "settle" render every other pipe-mutating gesture
  uses (vertex drag, single-vertex delete, equipment move), instead of
  rendering the pipe's pre-delete points until the doc round-trips back —
  removing the one structural gap where a slow round-trip could make a
  just-deleted node look like it was still there.

## [0.9.5] - 2026-07-23

### Added
- **Override a built-in's ports.** Run **"Nautilus: Edit Component Ports…"**
  from the Command Palette to pick any component — a kit built-in (Tank,
  Pump, Valve, Gauge, Sparkline) or one of the project's own custom
  components — and edit its connection points graphically in the Component
  Editor. Picking a built-in for the first time creates its
  `{Name}.component.json` sidecar prefilled with that built-in's current
  default ports; the sidecar mechanism, precedence (instance → sidecar →
  built-in default), and live-updating watcher are unchanged — a built-in's
  name is just a component name like any other now.
- **Custom components in the mimic editor palette.** Any component the
  project defines — via a `*.component.json` sidecar and/or already placed
  somewhere in the open doc — now gets its own section in the equipment
  palette (below a divider, under the built-ins), previewed with its real
  compiled island where one's available (a dashed placeholder chip
  otherwise), clickable/draggable onto the canvas exactly like a built-in.

### Changed
- The Component Editor's preview already tried the real built-in
  renderer before falling back to a user island / placeholder chip
  (`ComponentApp.svelte`) — this release makes that resolution order
  reachable for built-in-named sidecars end-to-end (host-side discovery
  was already name-agnostic; the fix was making the palette and the new
  command actually offer that path).

### Fixed
- **Canvas clipping.** The mimic editor canvas didn't clip its own rect —
  pipe geometry sitting past the canvas bounds (a doc hand-edited or
  resized to be smaller than its content) could paint outside the visible
  frame instead of being cropped to it. Both the editor canvas and the
  shipped runtime `<Mimic>` component now clip to the canvas rect (with a
  few px of margin so a port dot or selection halo legitimately sitting
  right on the edge isn't sliced in half) — a render-only fix; out-of-bounds
  geometry is still never dropped from the document itself.

## [0.9.4] - 2026-07-23

### Changed
- **Ports are named.** A connection point is now `{ "name": "tubeIn", "x":
  0, "y": 0.5 }` instead of a bare `[x, y]` pair — everywhere ports appear:
  `{Component}.component.json` sidecars, an equipment instance's `ports`
  override, the built-in registry defaults (Tank: top/left/right/bottom;
  Pump/Valve: in/out), and both editors' gestures and rendering. Names are
  unique within a component's port list; a duplicate name is rejected the
  same way any other malformed sidecar entry is (strict parse throws).
  Hovering any port dot — in the Component Editor or the mimic editor's
  ports mode ('p') alike — now shows a tooltip with its name and fraction.
  A graphically added port auto-names itself `p1`, `p2`, … (first free).
  `examples/hmi-demo`'s `HeatExchanger.component.json` is migrated with
  meaningful names (`tubeIn`/`tubeOut`/`shellIn`/`shellOut`). The name
  exists so a pipe end can anchor to a specific port explicitly (e.g.
  `E101.tubeOut`) — not implemented yet.
- **Component Editor: a ports side panel**, styled after the mimic editor's
  properties panel — one row per port (name field, x/y readout, delete),
  plus an "Add port" button that places a new one at the first free
  quarter-position slot on the outline. Clicking a row selects the matching
  dot on the canvas and vice versa; renaming commits through the same
  whole-document op path as every other gesture, so it's undoable.
- **Fixed:** in the Component Editor, port dots rendered inset from the
  dashed placeholder outline on its bottom and right edges only (top/left
  were fine). Root cause: the placeholder's CSS sized itself at
  `width/height: 100%` of its measured box but then added its own padding
  and border on top (`box-sizing: content-box`, the default) — normal block
  layout anchors an element's top-left corner flush with its container, so
  that extra size could only show up by pushing the bottom and right edges
  outward, past where the dots (computed from the pre-inflation box) were
  placed. Switched the placeholder to `box-sizing: border-box` so it always
  matches its measured box exactly. A second, subtler mismatch affected
  REAL content (built-in and custom components alike, in both editors): a
  bare `<svg>` root is inline by default, reserving a few px of descender
  space below it inside its measuring wrapper, so bottom-edge ports floated
  slightly below the actual shape — fixed by rendering every component root
  as `display: block`.

## [0.9.3] - 2026-07-23

### Added
- **User-authored Svelte components now render for real** inside the mimic
  editor and the Component Editor — not just built-ins. Referencing a
  non-built-in component (`HeatExchanger`, say) finds its `{Name}.svelte` in
  the workspace and compiles it into a self-contained bundle using **your
  own project's toolchain** (its `svelte/compiler`, its `node_modules` —
  resolved per file, so bare imports like `@joyautomation/nautilus-hmi`
  come from wherever the component actually lives), with esbuild (now a
  bundled dependency) doing the bundling. The result mounts as an isolated
  island wherever the canvas would otherwise have shown the dashed
  "unknown component" chip — live tag bindings flow into it exactly like a
  built-in, and the Component Editor now measures the REAL component for
  its box instead of a fixed placeholder size (fixing wrong-aspect-ratio
  placeholders for custom components). Falls back to the chip, with the
  compile failure as its tooltip, if the source can't be found or doesn't
  compile — a broken custom component never breaks the editor.
- Compiling is workspace-trust-gated (it runs your project's own code) and
  declared as `limited` support in untrusted workspaces — built-ins and
  chips work fully either way, and trusting the workspace later builds
  whatever was already referenced.
- Editing a custom component's `.svelte` file live-rebuilds and reloads the
  mimic/Component Editor showing it, the same way editing the `.mimic.json`
  text does.

## [0.9.2] - 2026-07-23

### Changed
- **Component metadata moved to per-component sidecar files.** The single
  project-wide `mimic.components.json` manifest is gone; a component's
  metadata now lives in a file named `{ComponentName}.component.json` found
  ANYWHERE in the workspace (component names are already unique in the
  registry, so this is name-keyed, not path-keyed). Today the only key is
  `ports` (connection points) — the shape is deliberately general, a
  per-component metadata file rather than a ports-only one, so it's the
  growth point for default props/bind hints/palette metadata later. The
  mimic editor's ports mode ('p' on a selected equipment instance) is
  unchanged to use — it edits an existing sidecar if one exists, else
  creates one next to `{ComponentName}.svelte` if the workspace has one,
  else next to the `.mimic.json` being edited. Two sidecars for the same
  component name resolve deterministically (shortest path wins, with a
  console warning naming both). This is also now how you override a
  **built-in** kit component's ports project-locally (a `Tank.component.json`
  beats the registry default).

### Added
- **Component Editor for `*.component.json`**: opening a sidecar directly
  (not just via the mimic editor's ports mode) now shows the component —
  rendered for real if it's a kit built-in, a proportioned placeholder
  outline labeled with its name otherwise — centered with its ports as
  draggable dots. Drag a dot to move it, double-click a dot to remove it,
  double-click the outline to add one (snapped to the nearest edge and the
  quarter grid), with live fraction coordinates next to the dragged dot.
  Same gesture math as the mimic editor's ports mode (shared, not
  duplicated), same whole-document-WorkspaceEdit-per-edit shape, so text
  undo covers the session.

## [0.9.1] - 2026-07-22

### Added
- **Connection points**: components declare ports (Tank top/sides/bottom,
  Pump and Valve inlet/outlet). Port dots light up while drawing pipes or
  dragging vertices and endpoints snap to them — and a pipe end landing
  on a component now **rides along when the component moves**, with the
  neighboring vertex adjusted to keep the run orthogonal. Attachment is
  inferred from geometry, so `.mimic.json` keeps plain `[x, y]` points
  and the runtime `<Mimic>` needs nothing new. A move plus its attached
  pipe ends commits atomically as one undo step.

### Fixed
- Dragged equipment/vertices/labels no longer flash back to their old
  position for an instant on release: the editor holds the committed
  position until the document confirms it, and the host now pushes the
  updated document immediately after applying an op.
- The editor no longer sticks on "waiting for document…": the webview
  announces readiness and the host replies with the current document
  (the one-shot post could race the webview's mount and be dropped).
- 0.9.0's vsix was packaged without its runtime dependencies, which
  broke extension activation entirely (`Cannot find module
  'vscode-languageclient'`). Packaged with dependencies again.

## [0.9.0] - 2026-07-21

### Added
- **Graphical HMI mimic editor**: `*.mimic.json` P&ID documents (the
  `@joyautomation/nautilus-hmi` `<Mimic>` format) now open as a visual
  editor by default — the JSON stays canonical and is one
  "Open With → Text Editor" away. WYSIWYG canvas rendered with the real
  kit components (Tank, Pump, Valve, Gauge, Sparkline): drag equipment
  on a snap grid, draw orthogonal pipe runs (Shift for free angles),
  drag/insert/remove pipe vertices, place labels, and edit ids, sizes,
  static props, and tag bindings in the props panel. Every gesture is a
  structural op resolved in pure TypeScript against the current buffer
  and applied as one text edit — undo/redo is VS Code's text undo, and
  hand-editing the JSON side by side updates the canvas live.
- **Live mimic preview while editing**: when a controller is reachable
  (`nautilus.runtimeUrl`), the editor polls `GET /api/state` — bound
  equipment animates with the real process on the canvas, and tag names
  autocomplete in the binding editor ("!" prefix negates a boolean).

## [0.8.36] - 2026-07-16

### Added
- **File icons for the IEC languages**: `.st` (ST monogram, blue),
  `.fbd` (block with pins, violet), `.ld` (rung with contact and coil,
  green) — light and dark variants, shown in tabs, quick open, and the
  explorer whenever your file icon theme doesn't claim the extension
  (the built-in themes don't; a theme with its own mapping wins by
  design).

## [0.8.35] - 2026-07-16

### Added
- **`pin => target` output bindings** (with the updated `nautilus` CLI):
  the IEC standard's formal-call output capture now parses in FBD and
  ladder — `t2:TON(PT := T#5S, ET => Elapsed)` stores the elapsed time
  into a variable at the call site, the one way ladder writes a non-BOOL.
  ST always had it; the diagram shows the bound pin on the output side.
- **B wraps the selection in a branch** (ladder): select any series
  element, press B, and it becomes leg one of a parallel branch with an
  open `_` leg — the classic "add an OR path around this contact"
  gesture, nestable.
- `nautilus new --language st|fbd|ld` scaffolds a project whose program
  is written in that language, with a matching driver stub and acceptance
  test; the interactive prompt asks too.

## [0.8.34] - 2026-07-16

### Added
- **Function-name suggestions on function contacts.** The double-click
  call editor now offers the transpiler's function vocabulary (GT/GE/…,
  SEL/MUX, ABS/SQRT, string ops) as an intellisense dropdown that
  completes JUST the name and keeps your arguments — free text still
  wins (user-defined FUNCTIONs the panel can't know about type through),
  and a name the compiler doesn't recognize is a rung diagnostic, not a
  silent pass.

## [0.8.33] - 2026-07-16

### Fixed
- **Function contacts aren't stuck as GT.** The palette's FN( ) button
  inserts `GT(_, 0.0)` as a placeholder, but double-click previously only
  edited the arguments — the function name was untouchable graphically.
  Double-click now edits the whole call: type `LE(TempC, 10.0)`,
  `EQ(Step, 3)`, any function (with the updated `nautilus` CLI); bare
  args without a name keep the current function.

## [0.8.32] - 2026-07-16

### Changed
- **Ladder diff marks are unmissable now.** Every added/changed/removed
  element gets a dashed outline box; a changed element shows **was <old>**
  right under its operand (the retag's previous tag, a block's previous
  args), and a removed element is labeled struck-through. The ladder diff
  drops the git colors for its own palette — cyan added, amber changed,
  red removed — because green means POWER in a ladder and the theme's
  git-modified blue was invisible; the toolbar legend follows.

## [0.8.31] - 2026-07-16

### Changed
- **Diffs stay live while you edit.** Both diagram diffs (FBD and ladder)
  now re-overlay the current text onto the frozen base after every edit —
  keep working and watch the added/removed/changed marks track your
  changes (previously the ladder diff dropped back to the plain view on
  the first edit, and the FBD diff froze and ignored edits). A mid-edit
  parse error shows as a banner and the next edit re-diffs. The toolbar
  gains **✕ exit diff** to return to the live editable view; reopening
  the preview or switching files also exits.

## [0.8.30] - 2026-07-16

### Changed
- **Ladder diff goes element-level.** A rung marked "changed" now shows
  WHAT changed inside it: added contacts/coils/blocks in green, removed
  ones ghosted in place, and edits (a retag, new args, NO↔NC) as a
  single amber element whose tooltip says what it was before. Branches
  diff leg by leg. Live power colors stay out of diff views.

## [0.8.29] - 2026-07-16

### Added
- **Divergence from the live program is visible in the diagrams.** The
  status-bar poll's verdict now streams into every FBD/ladder surface: a
  toolbar pill shows **≠ controller** (or **online edit** when the
  controller matches the workspace but not what it booted with) — click
  it for a visual diff against live. The status bar item itself now also
  works when a diagram custom editor has focus (it previously hid unless
  a text editor was visible, and couldn't resolve the program file).
- **Ladder visual diff** — "nautilus: Diff Ladder Diagram (vs Controller)"
  and "(vs git HEAD)": rung-granular overlay in the ladder preview —
  added/changed rungs get status colors, removed rungs splice back in
  ghosted where they lived. Editing the file drops back to the live view.
- **Every language now diffs against live**: ST text diff (existing), FBD
  diagram diff (now POU-targeted on multi-task controllers instead of
  always fetching the main program), ladder diagram diff (new), and the
  text diff names `.ld` virtual documents correctly (ladder highlighting
  instead of ST; `pull` previews too).

## [0.8.28] - 2026-07-15

### Added
- **The ladder editor shares the FBD variables panel** (with the updated
  `nautilus` CLI): the toolbar's `vars` button now works on .ld files —
  every header declaration with live values, unused/no-tag badges, × to
  delete, and the footer row to declare (ext/local toggle, type
  suggestions). Backed by new `declareVar`/`deleteVar` ladder ops that
  edit the ST header (compact one-line headers refuse whole-line deletes).
- **Tag suggestions in ladder retag dropdowns**: double-clicking a
  contact/coil now offers the header's declared variables, same as FBD —
  the suggestion list is fed from the ladder header (it was empty
  before). Unused-variable badges reflect actual rung references.

## [0.8.27] - 2026-07-15

### Fixed
- **Right rail no longer clipped.** Full-pane width was measured off the
  document, which overshoots by the scroll container's own vertical
  scrollbar. The ladder now measures its actual scroll container (and
  re-lays out when the scrollbar comes and goes).

## [0.8.26] - 2026-07-15

### Changed
- **The ladder spans the full editor pane**, PLC-IDE style: rails
  stretch to the view width, coils hug the right rail, and conditions
  build from the left. Rungs re-lay out live as the pane resizes; a pane
  narrower than the widest rung falls back to horizontal scroll.

## [0.8.25] - 2026-07-15

### Fixed
- **Ladder Delete / Ctrl+X / N / M — the actual root cause.** The
  selection lives in Svelte 5 `$state`, so its `path` array is a
  reactive Proxy — and proxies can't cross `postMessage` (structured
  clone throws `DataCloneError`), killing the key handler between the
  gesture and the op with no visible error. Copy/paste dodged it by
  accident (`.slice()` returns a plain array), which is why they worked
  while delete didn't. Every ladder op is now snapshotted to plain JSON
  before posting, and a posting failure logs to the "nautilus ladder"
  output channel instead of dying silently.

## [0.8.24] - 2026-07-15

### Changed
- **Deleting a rung's last coil no longer refuses** (with the updated
  `nautilus` CLI). The rung grammar needs one coil, so the old behavior
  rejected the delete with a toast — easy to miss and it read as "delete
  is broken". Now the last coil becomes the `( _ )` placeholder (the
  never-block rule: gestures always land, diagnostics guide). Deleting
  the placeholder itself still points you at deleting the rung.
- **"nautilus ladder" output channel**: every ladder gesture, op, and
  CLI answer is logged (View → Output → nautilus ladder) so a
  does-nothing edit can be diagnosed from the log instead of guessed at.

## [0.8.23] - 2026-07-15

### Added
- **Ladder edit buttons**: ✂ cut, ⧉ copy, ⎘ paste, ✕ delete in the
  palette bar, live with the selection — every clipboard/delete action
  now has a mouse path that can't be intercepted by the editor shell.

### Fixed
- **Delete and Ctrl+X in the ladder.** The key handler skipped any event
  something else had already preventDefault-ed — and VS Code's webview
  plumbing does exactly that to some editing keys (Delete, Ctrl+X) while
  letting Ctrl+C/V through, which is why copy/paste worked but delete and
  cut didn't. Keys are now captured ahead of the host page (capture
  phase) and deduped internally instead of trusting `defaultPrevented`.

## [0.8.22] - 2026-07-15

### Added
- **Ladder copy / cut / paste** (with the updated `nautilus` CLI).
  Ctrl+C copies the selected element, Ctrl+X cuts it, Ctrl+V pastes after
  the selection (or at the end of the last rung) — coils paste into the
  coil zone. Branches paste with their legs intact, and a pasted block's
  instance name is uniquified against the whole program (a paste
  duplicates state, it never aliases the original timer/counter).

### Fixed
- **Ladder keyboard shortcuts actually fire.** Del/N/M listened on
  `window`, but a webview only delivers keys while something in its
  document holds focus — and clicking SVG focuses nothing. The ladder
  surface is now focusable, takes focus when you click an element, and
  listens for keys directly.

## [0.8.21] - 2026-07-15

### Fixed
- **Ladder selection now sticks.** Clicking an element selected it on
  pointerdown, but the follow-up click bubbled to the ladder background
  and immediately cleared the selection — so Delete/N/M never had
  anything to act on. Nodes now swallow that click.

## [0.8.20] - 2026-07-15

### Added
- **Comments in the ladder diagram**, matching the FBD editor (with the
  updated `nautilus` CLI). Full-line `//` comment runs render as dashed
  note blocks between rungs — double-click to edit (multiline, Ctrl+Enter
  saves), empty text deletes, and the palette's `//` button adds one after
  the selected rung. A rung header's `(* … *)` comment now renders beside
  the rung name; double-click it to edit, or double-click the faint
  `(* … *)` ghost on a rung that has none to add one.

## [0.8.19] - 2026-07-15

### Added
- **Ladder palette + drag & drop** (with the updated `nautilus` CLI).
  A sticky instruction palette above the ladder — NO/NC contacts,
  comparison, TON/CTU blocks, branch, plain/S/R coils, + rung. Click
  appends at the selection; **drag onto any ⊕ hotspot** to place
  exactly (hotspots light up during the drag, drops snap to the
  nearest target). **Existing elements drag to move** — reorder within
  a rung, into a branch leg, across rungs; coils reorder in the coil
  zone. Moves are a single structural op with same-series index fixup,
  and every touched rung is re-parse-gated.
- **Compile errors show in the ladder** — rungs with diagnostics get a
  red name and a "!" badge carrying the compiler's message (same text
  as the editor squiggle), and the toolbar problems pill counts them.

### Fixed
- **Selection, delete, and double-click gestures now register
  reliably** — node listeners are attached directly instead of through
  Svelte's delegated events, which drop click/dblclick on SVG inside
  webviews (the same fix the FBD editor needed).

## [0.8.18] - 2026-07-15

### Added
- **Ladder editing** (with the updated `nautilus` CLI) — the ladder view
  is now a graphical editor, FBD-style: gestures become structural ops
  (`nautilus ld edit`), which rewrite the affected rung's text
  canonically; the header line and its comment survive, and every edit
  is gated on the result re-parsing. Gestures: click selects · Del
  deletes (the last coil refuses — delete the rung) · N toggles NO/NC ·
  M cycles coil mode (plain → S → R) · double-click retags a
  contact/coil (tag picker), edits a function/block's arguments, or
  renames a rung · hover ⊕ hotspots insert an open `_` contact between
  elements, a coil in the coil zone, or a new branch leg · "+ rung"
  appends a rung. Works in both the preview panel and the "Open With →
  Ladder Diagram" editor; text remains the source of truth.

## [0.8.17] - 2026-07-15

### Added
- **"Open With → Ladder Diagram"** — the ladder as a real custom editor
  over the `.ld` document, matching the FBD editor: right-click the file
  (or the editor tab) → Open With → Ladder Diagram, side-by-side with
  the text if you like. The view is a faithful projection with live
  power flow; text remains the default editor and the source of truth.

## [0.8.16] - 2026-07-15

### Changed
- **Ladder rendering rebuilt on a real layout engine** (ported from the
  tentacle-plc ladder editor's RSLogix-style auto-layout). A pure layout
  pass computes absolute SVG geometry — no more CSS-flexbox drift:
  uniform contact/coil widths on a strict grid, branch legs stacked from
  the rung centerline with explicit branch rails and short legs
  right-padded to meet them, coils right-aligned against the right power
  rail in every rung, continuous rails down the diagram, operand labels
  and live values in reserved space below each symbol (no more
  collisions), and broken through-wires so contacts read as real
  interruptions. Power flow paints per-segment, including branch rails
  and the stretch wire to the coil zone.

## [0.8.15] - 2026-07-15

### Added
- **Ladder Diagram (LD) — the third IEC language** (with the updated
  `nautilus` CLI). `.ld` files hold nautilus's textual rung format —
  series contacts AND, `[ a | b ]` branches OR, `/x` NC, `( x )` /
  `( S x )` / `( R x )` coils, `FN(args)` comparison contacts,
  `inst:TON(…)` blocks in-rung — compiling through the FBD netlist, so
  the whole function vocabulary, user FUNCTION_BLOCKs, and arrays come
  along. The extension registers the language with highlighting;
  compile diagnostics land on the offending RUNG; live values, online
  edits (download/diff/pull/rollback by POU name), and multi-task
  projects all treat `.ld` as a first-class program.
- **Ladder preview with live power flow** ("nautilus: Open Ladder
  Diagram Preview" on any `.ld` file). Rungs render IEC-style between
  the power rails — layout is canonical, straight from the source —
  and with live values on, power paints the rung: energized wires and
  closed contacts green, open contacts dimmed, in-rung blocks lit by
  their streamed Q pin, comparisons evaluated against live tags.
  Read-only for now: edit the text, the ladder follows.

## [0.8.14] - 2026-07-15

### Added
- **Function-block instance monitoring** — the traditional PLC-IDE
  "open the instance" experience, in both surfaces:
  - **In the text editor**, a FUNCTION_BLOCK body's live pills now read
    from a *monitored called instance*: bare `prev` shows `r1.prev`. A
    CodeLens over each block header shows which instance is monitored
    and switches between declared instances (found by scanning the
    project's sources); a type with exactly one instance monitors it
    automatically — zero clicks for the common case.
  - **In the diagram**, double-click a function block's body to open
    the instance inspector: inputs, outputs, and internal state for
    THAT instance, ticking live, with "open source" jumping to the
    `FUNCTION_BLOCK` in the project's `.st` libraries. Double-click
    the instance NAME to rename, as before.

## [0.8.13] - 2026-07-15

### Added
- **Live-value pills on indexed chips.** `TempHist[2]`, `m[1][2]`, and
  `tbl[i].val` chips now show live values: the diagram parses each
  array's declared lower bound from the header (`ARRAY[1..4]` stores
  element `[1]` at position 0) and resolves the element from the
  streamed value — variable indexes follow their own live value. When
  bounds are unknown the pill stays hidden rather than risk showing the
  wrong element. With the updated runtime, the live stream now carries
  every task's retained locals (not just the main program's), so a
  reports-task shift register ticks in its diagram.

## [0.8.12] - 2026-07-15

### Added
- **Arrays in FBD** (in the `nautilus` CLI — update it alongside the
  extension). Element reads (`Levels[2]`, `m[1][2]`, `tbl[i].vals`)
  render as chips and element/member writes (`Levels[4] := …`,
  `M.Cmd := …`) as coils; everything transpiles verbatim to ST, which
  already had arrays. Double-click retarget accepts accessor text
  (validated by the real parser, so an edit can never break the next
  parse); reading the exact element a coil writes draws feedback like
  a plain seal-in. Computed indexes go through a named wire (the
  netlist keeps arithmetic in blocks). Copying an element-target coil
  is skipped — a `_copy` rename of `Levels[2]` wouldn't parse.
- **Online edits for every task program** (with the updated `nautilus`
  runtime). A multi-task controller's programs are addressed by POU
  name: Download routes by the active file's `PROGRAM <Name>`, and
  Diff / Pull / Rollback / the sync status bar all target the program
  the active file names instead of assuming the main task. A workspace
  can now hold one program file per task — open the one you mean.
- The status-bar sync poll no longer raises error toasts in workspaces
  it can't compose (e.g. a library file active among several program
  files); commands still explain when invoked directly.

## [0.8.11] - 2026-07-14

### Added
- **The rest of the IEC 61131-3 standard-function library** (in the
  `nautilus` CLI/runtime — update it alongside the extension). SEL and
  MUX now execute (previously they rendered in the diagram but failed to
  compile); new: SHL/SHR/ROL/ROR, EXPT, TRUNC, ASIN/ACOS/ATAN/ATAN2,
  and the character-string functions LEN, CONCAT, LEFT, RIGHT, MID,
  FIND, INSERT, DELETE, REPLACE, plus BOOL↔REAL and *_TO_STRING /
  STRING_TO_* conversions. The diagram knows them all: standard formal
  pin names (SHL's IN/N, MID's IN/L/P, …), arity-aware disconnect,
  CONCAT gets the extensible "+" pin, and the function field completes
  against the full set.

## [0.8.10] - 2026-07-14

### Added
- **"No tag on controller" warnings.** With live values streaming, a
  declared `VAR_EXTERNAL` that doesn't exist in the controller's tag
  store is flagged where it matters: an amber **no tag** badge in the
  vars panel, and an amber border on any chip that READS it — because
  a read of a never-written tag faults the scan at runtime, while a
  write (coil) creates the tag on its own. The tooltip says how to fix
  it: seed it in the runtime, drive it from a driver or the HMI, or
  write it from logic first. Warnings disappear when live values are
  off or stale (no controller to compare against).

## [0.8.9] - 2026-07-14

### Added
- **Declare variables straight from the vars panel.** A footer row in
  the panel takes section (ext/local toggle), name, and type (with the
  type dropdown) — Enter or "+" declares without a round-trip through
  the "+ add" palette. Only the variable list scrolls now, so the
  footer stays put and its dropdown isn't clipped.

## [0.8.8] - 2026-07-14

### Added
- **Retarget a variable chip in place** (needs the updated `nautilus`
  CLI). Double-clicking any variable input chip — including the `_`
  open pins a paste or disconnect leaves — opens the tag picker: choose
  a declared tag (searchable dropdown) or type a new name, and every
  read that chip feeds rewrites in place, negation bubbles intact.
  Typing `Name : TYPE` declares the new tag and wires it in one
  gesture, which replaces the old dblclick-to-declare flow on
  undeclared chips (it now lives in the same editor instead of a
  separate type-only prompt).

## [0.8.7] - 2026-07-14

### Changed
- **Copy/paste severs wiring, keeps configuration** (in the `nautilus`
  CLI — update it alongside the extension). Pasting a copied element no
  longer clones its tag/wire connections: references outside the copied
  selection open as `_` pins with the usual diagnostic breadcrumbs,
  so a pasted block can't silently read the original's tags (two alarms
  quietly watching one sensor). Literals stay — `LT(TempC, 62)` pastes
  as `LT(_, 62)`, a TON keeps its `PT := T#10S`. References BETWEEN
  copied statements still follow the `_copy` renames, so a multi-select
  copy of a seal-in latch remains a self-consistent loop.

### Fixed
- **No more double scrollbars under palette dropdowns.** The palette
  card no longer scrolls (its own overflow was capturing the suggest
  dropdown, nesting a second scrollbar inside the card's); dropdowns
  now overflow the card freely. The vars panel keeps its scrollbar —
  it's the one with genuinely long content.

## [0.8.6] - 2026-07-14

### Changed
- **Suggest fields are now browsable comboboxes.** Focusing or clicking
  a completing field (palette function/type/tag fields, on-canvas
  editors) opens the full vocabulary in a scrollable list — previously
  the list only appeared once typed text matched, so a field prefilled
  with a valid value (e.g. function "AND") showed nothing and the
  options were undiscoverable. Typing switches the list to filtering,
  ArrowDown reopens a dismissed list, and the highlighted row scrolls
  into view. Free text still always wins.

## [0.8.5] - 2026-07-14

### Fixed
- **Toolbar buttons no longer wrap in narrow panes.** "live", "vars",
  and "+ add" keep their labels on one line; the gesture hint truncates
  with an ellipsis instead of squeezing the controls.

## [0.8.4] - 2026-07-14

### Changed
- **Unwiring a coil reverts to floating references.** Deleting the wire
  into an output no longer leaves `X := _` with error chips — the
  statement is removed and both endpoints become ghost (dashed) chips at
  their existing positions, the exact inverse of wiring a ghost output.
  A bare variable source becomes a ghost input the same way; named wires
  and FB instances keep their own statements untouched. The webview now
  sends the endpoints' live positions with the disconnect so even
  never-dragged nodes stay where they were.

### Fixed
- **Deleting or editing a trailing comment no longer eats `END_FBD`.**
  A comment run that touched `END_FBD` (exactly where "+ add" inserts
  new notes) was scanned with an end-of-file span, so deleting or
  rewriting it removed `END_FBD`/`END_PROGRAM` and broke the program.
  Regression-tested.

## [0.8.3] - 2026-07-13

### Changed
- **Webview style system consolidated** (no intended visual changes).
  A single `theme.css` now defines every VS Code theme variable we use as
  a semantic `--nx-*` token — components no longer restate raw
  `--vscode-*` vars, so fallback values can't drift (they already had:
  three different border alphas, two input border radii). Shared chrome
  became shared code: `.nx-input` (all text fields), `.nx-pill` (live
  value pills, previously duplicated in the diagram and vars panel),
  a `Popover` component (palette + vars panel card), and a `FloatEditor`
  component extracted from App that owns all in-place edit mechanics.
  The comment line-height now has one source of truth shared by layout
  math, note rendering, and the comment editor.

## [0.8.2] - 2026-07-13

### Added
- **Tag completion (intellisense-style) in the diagram.** Name-like text
  fields now suggest as you type — free text with suggestions, never a
  gate: an unknown name still lands and becomes a diagnostic to follow.
  - Palette fields complete against declared variables (with their types
    shown), the function field against the known operator/function set,
    the timer type against standard FB types, and variable declarations
    against the elementary types. The `inputs` field completes each
    comma-separated argument independently.
  - On-canvas editors join in: retyping a constant suggests declared
    tags (swap a literal for a tag in place), and declare-in-place
    suggests types.
  - Keys: ↑/↓ highlight, Tab accepts (first item if none highlighted),
    Enter accepts only a highlighted item — otherwise it saves as
    before; Escape dismisses the list first, then cancels the edit.

## [0.8.1] - 2026-07-13

### Fixed
- **Multi-line comment editing.** Double-clicking a comment now opens a
  real multi-line textarea — Enter makes a new line like anywhere else,
  Ctrl+Enter (or clicking away) saves, Escape cancels — instead of a
  one-line input that crammed every line into a single `\n`-escaped
  string, where in practice only the first line was visible/editable.
  Clearing the text and pressing Ctrl+Enter now actually deletes the
  comment, as the tooltip always promised.

## [0.8.0] - 2026-07-12

### Added
- **Comments in the diagram.** Full-line `//` comment runs render as
  italic notes above the network they precede. Double-click edits (use
  `\n` for multi-line; empty text deletes), Del deletes, and the "+ add"
  palette inserts new ones.
- **Delete variables.** Each row in the vars panel gets an × — the
  declaration is removed even if still referenced (the references become
  ordinary diagnostics that lead you to the fix).
- **Copy & paste.** Ctrl+C captures the selected blocks/coils/instances,
  Ctrl+V pastes copies with fresh `_copy` names; references between the
  copied statements follow the renames (a copied seal-in latch stays a
  self-consistent loop).
- **Bare input/output references.** The palette can drop a lone variable
  chip (dashed "ghost") onto the canvas — it lives in the `(* @layout *)`
  block until you wire it, at which point it becomes real netlist text
  (an output reference writes its coil statement on first connection).

### Changed
- **Edits are never blocked by semantics.** Validation only guards
  parseability now: disconnecting a fixed-arity/min-arity pin or a coil
  source leaves a `_` placeholder (position preserved, undeclared-`_`
  diagnostic marks the open pin); deleting a wire or FB instance that is
  still referenced is allowed — the dangling reads become diagnostics,
  exactly as if you had typed it in text. Compilation/push to the runtime
  remains the gate.

## [0.7.5] - 2026-07-12

### Added
- **Variables panel.** A "vars" toolbar button lists every header
  declaration — name, type, initializer, section badge (ext/local/in/out)
  — including ones the logic doesn't reference yet (which the diagram
  itself intentionally doesn't draw); those are marked *unused*. Live
  values appear next to each variable, on the same stream/toggle as the
  node pills. Backed by a new `vars` list in the `nautilus fbd graph`
  render model.

## [0.7.4] - 2026-07-12

### Added
- **Live-values toggle in the diagram.** The diagram toolbar shows the
  stream state as a pill — green "live", amber "offline" (enabled but no
  frames), grey "live off" — and clicking it runs the same
  `nautilus.liveValues.toggle` command as the status bar item. The status
  bar item itself now stays visible while a diagram is open, not just a
  text editor.

### Fixed
- 0.7.3 was packaged without its runtime dependencies
  (`vscode-languageclient`), which crashed the whole extension on
  activation.

## [0.7.3] - 2026-07-12

### Added
- **Live values in the diagram.** The FBD editor and preview now show the
  same live controller values as the text editor's inline pills: variable
  chips and coils get a green value pill, and FB instances show each
  output pin's value (a1.Q, a1.ET) as an inline tag on its wire. Fed by
  the same SSE stream as the text decorations, so it honors
  `nautilus.liveValues.enabled` (and the status-bar toggle), greys out
  when frames stop arriving, and hides in diff mode. A diagram-only
  session keeps the stream alive — no text editor needs to be visible.

## [0.7.2] - 2026-07-12

### Added
- **Declare variables from the diagram.** The "+ add" palette gains
  "variable (external tag)" and "local variable (retained)" — a
  declareVar op inserts `name : TYPE;` into the right VAR_EXTERNAL/VAR
  section (creating the section above FBD if missing), with duplicate and
  collision checks. And the quick path: an UNDECLARED variable chip (it
  already wears the error badge) can be double-clicked — type its type,
  Enter, and the declaration lands in VAR_EXTERNAL. Add a block from the
  palette, double-click its red inputs, and the program compiles.

## [0.7.1] - 2026-07-12

### Added
- **Diagnostics in the diagram.** Anything that squiggles in the text now
  marks the diagram too: the offending element gets a red border, glow,
  and "!" badge, its tooltip carries the compiler's exact message, and
  the toolbar shows an "N problems" pill (hover for the full list).
  Every model node carries its source line, so the join uses the same
  line-mapped diagnostics the language server produces.

## [0.7.0] - 2026-07-12

### Added
- **Disconnect wires**: click a wire to select it (it highlights), press
  Delete. Semantics follow the text form — an FB pin drops its named
  argument, an extensible operator input (AND/OR/ADD/MUL/…) is removed as
  long as the block keeps its minimum inputs, and fixed-arity inputs (SUB,
  GT, LIMIT…) or a coil's source refuse with guidance, since the text form
  can't leave them dangling.
- **Add inputs to extensible blocks**: AND/OR/XOR/ADD/MUL/MIN/MAX/MUX show
  a dashed "+" pin below their inputs — drop any wire on it to append an
  input (`OR(a, b)` → `OR(a, b, c)`). The pin exists because it's wired:
  no dangling placeholder state, ever. Fixed-arity blocks refuse
  ("GT takes exactly 2 inputs").

## [0.6.5] - 2026-07-12

### Fixed
- Multi-select drags persist reliably: phantom selection-group entries are
  now dropped at every layer (webview, extension, and the Go op itself
  skips unknown ids within a batch instead of rejecting it wholesale), so
  the real nodes always pin. Requires the matching nautilus CLI build.

## [0.6.4] - 2026-07-12

### Fixed
- Multi-select drags no longer fail with "unknown node" — selection drags
  hand the handler synthetic group entries alongside the real nodes; pins
  now apply only to nodes that exist in the model.
- Selection is visible on every cue at once: the selected element itself
  recolors (focus-colored border + background tint) in addition to the
  wrapper outline/glow, and the toolbar shows an "N selected" pill.

## [0.6.3] - 2026-07-12

### Fixed
- **Multi-select drag moves the whole selection.** Drag-stop posted one
  setLayout op per node; each op resolved against the text before the
  previous one landed, so they overwrote each other's layout entries and
  everything but one node snapped back. A multi-node drag is now ONE
  batched op (one text edit), and the extension serializes all ops so
  rapid gestures can never race each other again.
- **Selection is unmissable** — selected nodes get a bright focus outline
  and glow, single node included.

## [0.6.2] - 2026-07-12

### Fixed
- Feedback lanes route around nodes instead of through them: the lane's
  horizontal run drops just below whatever it would actually cross —
  consulting live node positions, so it reroutes as you drag — while
  staying tight to its own logic rather than clearing the whole sheet.

## [0.6.1] - 2026-07-12

### Fixed
- Feedback lanes (the dashed seal-in wires) now follow node drags. Their
  paths were precomputed at layout time; they now derive from the live
  endpoints like every other wire, tracking during the drag and settling
  wherever the nodes land — an edge that becomes forward or backward after
  rearranging switches shape automatically.

## [0.6.0] - 2026-07-12

### Changed
- **The FBD diagram is rebuilt on Svelte Flow** (xyflow) — the hand-rolled
  SVG renderer is gone. Same banded auto-layout and Go-owned edit ops, now
  with a real editor's interaction model: node selection (click, shift for
  multi, marquee), a minimap, zoom controls, keyboard navigation, and
  native drag-to-connect with a live connection preview.

### Added
- **Wire by dragging pin → pin**: drag from any output handle to an input
  handle to rewire it — including pins that aren't wired yet (dropping on
  an unwired FB input adds the named argument to the call).
- **Delete from the diagram**: select nodes and press Delete/Backspace.
  Reference-protected as always — deleting a wire that still feeds inputs
  explains itself instead of breaking the program.
- **Manual layout, optional and per-node**: drag a node and its position
  pins into a `(* @layout *)` comment in the `.fbd` source, keyed by the
  node's stable id — invisible to the compiler, versioned like any text.
  Everything without a pinned position keeps auto-layout; renames carry
  pins along, deletes clean them up; an "auto layout" button clears all
  pins. Hand-placed where it matters, automatic everywhere else.

## [0.5.1] - 2026-07-12

### Changed
- **"+ add" stays in the diagram.** The palette no longer drops a snippet
  into the text editor (which yanked focus out of the diagram flow):
  template fields are filled in the palette itself, and Insert posts an
  `insertStatement` op — Go validates the fragment (it must parse, and new
  names must not collide) before anything touches the file, then the new
  block appears in the diagram. Works identically in the "Open With → FBD
  Diagram" editor, where no text editor need exist at all.

## [0.5.0] - 2026-07-12

### Changed
- **All diagram edits are structural operations now.** A gesture no longer
  computes text spans in the webview: it posts an op addressed by stable
  render-model ids (`setLiteral`, `toggleNot`, `rewire`, `rename`,
  `deleteNode`) to `nautilus fbd edit`, which resolves it against a fresh
  parse of the current buffer and returns minimal text edits. Rejected ops
  explain themselves ("wire seal feeds 2 inputs — rewire them first",
  "the name hot is already in use"). This is the foundation for
  full-editor parity — new edits are one AST operation in Go, not span
  plumbing across three layers.

### Added
- **Rename from the diagram**: double-click a function-block instance or a
  named wire's block to rename it — every reference updates (declaration,
  calls, pin reads, wire fan-out), with identifier validation and collision
  checks.
- **"Open With → FBD Diagram"** (CustomTextEditor): the diagram as a real
  editor over the `.fbd` document, tied to its lifecycle — undo, dirty
  state, and revert belong to the text document. Plain text remains the
  default editor; right-click a `.fbd` file → Open With to choose.
- `nautilus fbd edit` CLI: `{"source", "op"}` in, `{"edits"}` out — the
  same op service, scriptable.

## [0.4.5] - 2026-07-12

### Fixed
- **Rewire drag works under a real mouse.** The drag depended on pointer
  capture delivering moves to an invisible 7 px circle; it now tracks the
  pointer at the window level (no capture at all) and drops snap to the
  nearest input pin geometrically, so releasing near a pin is enough.
- Draggable outputs are visible now: every referenceable output pin shows
  a small blue dot — the drag starts there (the circle at the input end of
  a wire is the NOT toggle, not a drag handle).

## [0.4.4] - 2026-07-12

### Fixed
- The "+ add" palette opened the program in a NEW tab inside the preview's
  editor group, covering the diagram. The snippet now inserts into the
  editor group where the file is already open (or the first group), so the
  preview stays visible beside the text while the tabstops are filled.

## [0.4.3] - 2026-07-12

### Fixed
- **Diagram edit gestures actually work now.** The pan handler captured the
  pointer on every press, which retargeted the derived double-click/click
  events to the canvas — so "double-click to edit" and pin clicks never
  reached their targets in a real session. Panning now captures only after
  the pointer moves, and interactive elements opt out entirely.

### Added
- **Rewire connections by dragging.** Drag any referenceable output — a
  variable or constant chip, a named wire's block output, an FB output pin
  (`a1.Q`), or a coil — onto an input pin: the target argument's text is
  replaced with a reference to the source (span-verified before applying,
  like every diagram edit). Blocks without a wire name aren't draggable —
  name the wire first.
- **Insert instructions from the diagram.** The preview toolbar's "+ add"
  palette drops a template — block→wire, coil, TON timer, CTU counter —
  just above `END_FBD` as a snippet with live tabstops: focus lands in the
  text editor on the placeholders while the diagram re-renders as you type.

## [0.4.2] - 2026-07-11

### Added
- **Inline live values for program locals.** Retained `VAR` variables — a PI
  integral, latches, and FB instances — now stream in every frame alongside
  the tags, so `integral` gets a value pill just like `TempC`, and FB pins
  resolve through member access: hovering `a1.Q` or `a1.ET` shows the live
  timer state. (Requires a controller built from this commit; locals ride
  the frame's new `locals` field.)

## [0.4.1] - 2026-07-11

### Added
- **First graphical edits in the FBD preview.** The diagram is no longer
  read-only: double-click a constant chip to retype its value (setpoints,
  timer presets, thresholds), and click an input pin to toggle its `NOT`.
  Every gesture becomes a span-anchored text edit in the `.fbd` buffer —
  verified against the source before applying, round-tripped through the
  normal re-render — so the text stays the single source of truth and undo
  is just the editor's undo. Editing is disabled in diff views.

## [0.4.0] - 2026-07-11

### Added
- **Online edits for `.fbd` programs.** The nautilus runtime now accepts and
  serves Function Block Diagram source as the program of record, so the
  whole online-edit loop speaks `.fbd` end to end: "Download Program to
  Controller" composes a `.fbd` program file with its `.st` libraries,
  "Diff Program with Controller" shows a syntax-highlighted `.fbd` text
  diff, "Pull Program from Controller" writes a field edit back to the
  `.fbd` file, and the sync status bar watches `.fbd` editors too.
- **`nautilus: Diff FBD Diagram (vs Controller)`** — the graphical diff
  against what the controller is *running*: added / removed / changed
  blocks and wires between the live program and your working tree. Pairs
  with the git-HEAD diagram diff for the full review story: text or
  wiring, against git or against the plant floor.

## [0.3.9] - 2026-07-11

### Changed
- **FBD preview layout is now network-banded**, the way FBD editors draw
  sheets: each connected logic cone renders as its own horizontal band,
  variable boxes repeat per network instead of one far-left column, input
  chips sit adjacent to their consumers, reading another network's coil
  shows a variable box (only an in-network seal-in draws as a feedback
  wire, routed in lanes under its own band), coils right-align per band,
  and row ordering uses iterated pin-aware barycenter sweeps (chips align
  to the exact pin they feed) to cut wire crossings.

## [0.3.8] - 2026-07-11

### Added
- **Inline live tag values in `.fbd` files** — the identifier scanner is
  syntax-agnostic and FBD netlists reference the same runtime tags, so
  `.fbd` editors now get the same live value pills as `.st`.

## [0.3.7] - 2026-07-11

### Added
- **Function Block Diagram (`.fbd`) language support**: syntax highlighting
  (reusing the ST grammar plus `FBD`/`END_FBD`), and live LSP diagnostics —
  the netlist is transpiled to ST by the same `lang/fbd` compiler the runtime
  uses, and error positions map back to the exact `.fbd` source line.
- **FBD Diagram Preview** (`nautilus: Open FBD Diagram Preview`, editor-title
  button): a live, read-only diagram of the open `.fbd` file — operator and
  FB blocks with pins, input/coil variable chips, wire fan-out with signal
  labels, IEC negation circles, and seal-in feedback routed below the logic.
  Layout is derived from topology (no coordinates in the file); the panel
  re-renders as you type (150 ms debounce), pans/zooms with mouse or
  keyboard, follows the active `.fbd` editor, and matches the editor theme.
  Rendering consumes `nautilus fbd graph` JSON, so the FBD parser exists only
  in Go.
- **FBD visual diff** (`nautilus: Diff FBD Diagram (vs git HEAD)`): overlays
  the committed and working-tree diagrams and colors nodes and wires
  added / removed / changed using the git decoration theme colors. Matching
  uses stable structural node ids, so renaming a signal or reordering
  statements diffs precisely.

## [0.3.0] - 2026-07-09

First public release. (0.1.x–0.2.x were internal and never published.)

### Added
- **Syntax highlighting** for IEC 61131-3 Structured Text (`.st`): comments,
  strings, numeric / based / time / typed literals, control-flow and `VAR`
  keywords, elementary types, operators, and the nautilus built-in functions
  and function blocks. Keyword and type lists are derived directly from the
  nautilus Go compiler, so they match what it actually accepts.
- **Language server** (`nautilus lsp`) reusing the real `lang/st` compiler:
  - Diagnostics as you type — parse and typed lowering errors with precise
    line/column squiggles.
  - Go-to-definition (POU-scoped), hover with declared types, and completion
    of in-scope variables, keywords, elementary types, and builtins.
- **Inline live tag values** — when a nautilus controller is running, live
  values render as pills next to the matching identifiers, streamed over SSE
  and greying out when the stream goes stale. A status-bar item shows the
  connection state and toggles the feature.
- **Settings**: `nautilus.cliPath`, `nautilus.runtimeUrl`,
  `nautilus.liveValues.enabled`.
- **Commands**: "Toggle Inline Live Tag Values" and "Restart Language Server".
