# Changelog

All notable changes to the **nautilus IEC 61131-3** extension are documented
here. The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

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
