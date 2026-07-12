# Changelog

All notable changes to the **nautilus IEC 61131-3** extension are documented
here. The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

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
