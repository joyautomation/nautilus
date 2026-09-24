# nautilus IEC 61131-3

VS Code language support for **IEC 61131-3 Structured Text** (`.st`),
**Function Block Diagram** (`.fbd`), **Ladder Diagram** (`.ld`) and
**Sequential Function Chart** (`.sfc`) as used by the
[nautilus](https://github.com/joyautomation/nautilus) Go + SvelteKit SCADA
framework: develop SCADA in VS Code like a real software developer.

![Live tag values rendered as pills next to identifiers in a .st file, with the nautilus file tree alongside](https://raw.githubusercontent.com/joyautomation/nautilus/main/tools/vscode-iec/images/live-values.png)

## Features

### Syntax highlighting (no setup)

Purely declarative — opening any `.st` file lights up comments, strings,
numeric/typed/time literals, keywords, `VAR…END_VAR` sections, elementary
types, operators, built-in functions, and standard FBs. All keyword lists are
derived **directly from the nautilus Go compiler** (`lang/st`, `lang/ir`), so
they match what the compiler actually accepts.

### Language intelligence (needs the nautilus CLI)

The extension spawns **`naut lsp`** — the nautilus CLI's language-server
subcommand, which runs the *real* `lang/st` compiler over stdio:

- **Diagnostics as you type** — parse errors and typed lowering errors
  (undeclared identifiers, unknown FB fields, type mismatches) with precise
  line/column squiggles.
- **Go-to-definition** — jump from an identifier to its `VAR` /
  `VAR_EXTERNAL` declaration; POU-scoped (FB locals resolve before globals).
- **Hover** — declared type and var-section for any identifier.
- **Completion** — in-scope variables, keywords, elementary types, and the
  compiler's actual builtin function/FB registries.

Install the CLI once:

```sh
go install github.com/joyautomation/nautilus/cmd/naut@latest
```

`nautilus.cliPath` points elsewhere if it's not on PATH.

### FBD diagram preview & visual diff (needs the nautilus CLI)

nautilus's Function Block Diagram source (`.fbd`) is a git-diffable text
netlist; the extension projects it into the diagram a controls engineer
expects:

- **Live diagram preview** — `nautilus: Open FBD Diagram Preview` (or the
  editor-title button) renders blocks, pins, variable chips, wire fan-out
  with signal names, IEC negation circles, and seal-in feedback wires. It
  re-renders as you type; the text stays the source of truth, and layout is
  computed from topology so no coordinates pollute your diffs. Edit from
  the diagram: double-click constants to retype them and blocks to rename
  them, click an input pin to toggle `NOT`, drag an output onto a pin to
  rewire, and insert instruction templates from the "+ add" palette — every
  gesture is a structural operation resolved by the Go compiler into
  minimal text edits. Or right-click a `.fbd` file → "Open With → FBD
  Diagram" to use the diagram as the editor itself.
- **Visual diff** — `nautilus: Diff FBD Diagram (vs git HEAD)` overlays the
  committed and working-tree diagrams, coloring added / removed / changed
  blocks and wires; `(vs Controller)` does the same against the program a
  live controller is running; `(between git revisions…)` picks any two
  commits from the file's history (or one commit and the working tree)
  and overlays those. Review a logic change the way you'd review the
  wiring, not the text.
- **Online edits speak `.fbd`** — a controller running an FBD program
  serves and accepts the `.fbd` source itself, so download / text diff /
  pull and the sync status bar work exactly as they do for `.st`.
- `.fbd` files get the same **diagnostics-as-you-type** as `.st` — the
  netlist compiles through the identical `lang/fbd` → `lang/st` pipeline the
  runtime uses, with errors mapped back to the exact `.fbd` line.

### SFC (Sequential Function Chart) diagram preview & editor (needs the nautilus CLI)

nautilus's SFC source (`.sfc`) is a git-diffable ST POU with an
`SFC…END_SFC` body — steps, transitions and actions in the IEC standard's
own textual grammar; the extension projects it into the classic chart view:

- **Live chart preview** — `nautilus: Open SFC Diagram Preview` (or
  right-click a `.sfc` file → "Open With → SFC Diagram" to use the chart as
  the editor itself) renders steps as boxes (double border = the initial
  step) with their action associations as a table beside the box,
  transitions as bars across the flow line — a single bar for a normal or
  alternative transition, a double bar for a simultaneous divergence or
  convergence — with the condition text beside the bar. A transition that
  loops back to an earlier step (an abort, a return to Idle) draws as a
  compact "↩" jump glyph instead of a long line back up the chart. Layout is
  computed from the chart's topology (steps flow top-to-bottom, parallel
  branches fan into columns); drag a step to pin its position, "auto
  layout" clears every pin.
- **Edit from the diagram** — every gesture is a structural operation
  resolved by the Go compiler into minimal text edits, exactly like FBD:
  add a step or transition from the toolbar, branch an alternative or
  simultaneous path off a selected transition, double-click a transition's
  condition or a step's name to edit it, add/edit/delete action
  associations directly in a step's table, double-click a body action
  (one backed by an `ACTION` block, shown underlined) to edit its ST body
  in place, and add/edit/delete/drag a `//` comment note (rendered in a
  strip along the top edge, same as FBD/Ladder): "+ comment" on the
  toolbar, dblclick to write it, drag to pin its position, Del to remove.
- **Drag-to-connect** — hover a step to reveal a small connect handle on
  its bottom edge (a body-drag still pins layout, exactly like dragging a
  step anywhere else); drag the handle onto another step to add a
  transition between them, live rubber-band line and drop-target highlight
  included, Esc to cancel mid-drag.
- **Orphaned transitions stay visible and fixable** — a transition whose
  FROM or TO no longer resolves (typically left by deleting a step) renders
  as a red "problem" chip instead of vanishing from the canvas: selectable,
  Del-deletable, condition-editable, and retargetable via a ⚙ popover
  (dropdowns of existing steps, plus an "other…" escape hatch for a
  not-yet-existing name) backed by the `setTransitionEnds` edit op.
  Deleting a step that still has attached transitions offers an in-canvas
  choice — flag them (the default) or cascade-delete them too — never a
  silent cascade and never a native confirm dialog.
- **Live step highlighting** — with a controller reachable, the ACTIVE step
  outlines and its name highlights, reading the same retained `_S_<Step>_X`
  slot the runtime already exposes for every step (no new controller API).
- `.sfc` files get the same **diagnostics-as-you-type** as `.st`/`.fbd` —
  errors surface as a non-modal "N problems" pill plus a badge on the
  offending step or transition, never blocking a save; a structurally
  incomplete chart (a step with no transitions yet) is a diagnostic
  breadcrumb, not a rejected edit.
- **Visual diff** — `nautilus: Diff SFC Diagram (vs git HEAD)` / `(vs
  Controller)` / `(between git revisions…)`, the same commands and
  added/removed/changed visual language FBD/Ladder use: added/removed/changed steps, transitions,
  action associations and comments overlay on the chart, a removed
  element spliced back in ghosted, read-only until "exit diff".

### HMI mimic editor (`*.mimic.json`)

P&ID mimic documents for the `@joyautomation/nautilus-hmi` `<Mimic>`
component open as a **graphical editor** by default (the JSON stays the
canonical, diffable artifact — "Open With → Text Editor" any time):

- **WYSIWYG canvas**: equipment renders with the real kit components
  (Tank, Pump, Valve, Gauge, Sparkline), pipes with the kit's animated
  flow — what you see is what the HMI ships.
- **Drag-and-drop authoring**: place equipment from the palette, drag on
  a snap grid (toggle **Snap: On/Off** in the toolbar for free-pixel
  dragging instead — persisted as `nautilus.mimic.snapToGrid`; Shift-arrow
  nudging always snaps), draw pipe runs orthogonal-by-default (Shift for
  free angles), drag / insert / double-click-remove pipe vertices, shift-click
  to multi-select vertices for a batch delete, drop free labels.
- **Props panel**: ids, component type, position/size, static props,
  **tag bindings** (prop ← tag, `!` negates a boolean) with prop
  suggestions per component, and — for a selected pipe — **routing**
  (direct straight segments, or orthogonal 90°-corner segments; a document
  property, so the editor and the shipped `<Mimic>` always draw it the
  same way).
- **Live while you edit**: with a controller reachable at
  `nautilus.runtimeUrl`, bound equipment animates with the real process
  on the editor canvas and tag names autocomplete in the binding editor.
- Every gesture becomes one text edit — **undo/redo is text undo**, and
  editing the JSON side by side updates the canvas live.
- **Custom components**: your own `*.svelte` equipment (referenced by
  `component` in the doc) compiles as a live "island" with your project's
  own Svelte/dependencies and renders for real on the canvas and in the
  palette, right alongside the kit's built-ins — no code change to the
  extension needed.
- **Connection points (ports)**: press `p` on a selected piece of equipment
  to drag, add, or remove its pipe-snap ports graphically. An edit writes a
  `{Component}.component.json` sidecar shared by every instance of that
  component in the project (or just the one instance, if you toggle the
  edit's target) — the same file a custom component's own ports live in.
  **Run "Nautilus: Edit Component Ports…" from the Command Palette** to
  override a **built-in's** ports (Tank, Pump, Valve, Gauge, Sparkline) the
  same way: pick one, and it opens (creating if needed) that sidecar in the
  graphical Component Editor, prefilled with the built-in's current
  defaults. Each port also has an **exit direction** (the side a pipe
  leaves it from) — inferred automatically from its edge position, or set
  explicitly in the ports panel (auto/left/right/up/down) when a port sits
  on a corner or you want to override the inferred side.
- **Pipe port anchors**: start or end a pipe draw ON a port dot to attach
  that end to it (instead of a raw point) — the pipe end then tracks the
  port for as long as it's anchored, so moving the equipment moves the
  pipe with it. Drag an existing pipe's end onto a port to attach it, off
  a port to detach it (dropping a concrete point where you let go); the
  props panel shows each end's anchor with a × to detach or dropdowns to
  attach. An anchored end honors its port's exit direction with a short
  straight stub before any corner. Deleting equipment with anchored pipes
  materializes those ends as concrete points in the same edit — nothing
  is ever left pointing at equipment that no longer exists. An anchored
  end's handle renders as a **ring** (distinct from a plain vertex dot) and
  is clickable/draggable even sitting right on top of its equipment;
  clicking it selects that specific end (Arrow keys nudge it — detaching
  it first if it's anchored, so a nudge is never a dead end) and Enter
  completes a draw as soon as it's resting on the second port, with no
  second click required.
- **Route suggestion**: drawing a pipe port-to-port with no interior
  clicks (click port A, then port B — or A, then Enter while resting on
  B) auto-generates an orthogonal route around any equipment in the way,
  defaulting that pipe's `routing` to `orthogonal`; the suggested vertices
  are ordinary, editable points from then on — routing is a one-shot
  generator, never live state. A **Re-route** button in the props panel
  (any pipe with at least one anchored end) replaces the interior points
  with a fresh suggestion at any time — a normal, undoable edit.

### Inline live tag values (needs a running controller)

When a nautilus controller is running (any program using the `server`
package — the scaffold wires it by default), the extension subscribes to its
tag stream (`GET /api/stream`, SSE) and renders **live values next to every
identifier** in your `.st` source — the watch window, inline. Values gray out
when the stream goes stale; the status-bar item shows connection state and
toggles the feature. Set `nautilus.runtimeUrl` (default
`http://localhost:8080`) to point at your controller.

## Requirements

- **Syntax highlighting** works with no setup.
- **Language features** (diagnostics, go-to-definition, hover, completion)
  need the nautilus CLI on your PATH — `go install
  github.com/joyautomation/nautilus/cmd/naut@latest`. Point
  `nautilus.cliPath` at it if it's installed elsewhere.
- **Inline live values** need a running nautilus controller exposing the tag
  API; set `nautilus.runtimeUrl` (default `http://localhost:8080`).

## Roadmap

**Graphical editors are complete for all four IEC languages** — FBD, Ladder,
and SFC diagrams all write back to the *same* text that lowers to the *same*
nautilus IR, and all three now share visual diff and comment gesture parity.
Remaining diagram-side work: continued gesture parity as each language
editor matures.

## Development

Tests: `npm test` (extension host + webview pure logic — browser-free, CI-safe).
The mimic editor also has a **browser gesture harness**
(`webview-ui/gesture-harness/`, run with `npm --prefix webview-ui run
test:gestures`) that drives the real webview bundle in headless Chrome with
real pointer/keyboard input — it covers focus, hit-testing and
preview-vs-commit geometry the pure-logic tests can't reach. It needs a
Chrome/Chromium on `PATH` and is deliberately kept out of `npm test`/CI. See
that folder's README.

## Source & license

Part of the [nautilus](https://github.com/joyautomation/nautilus) monorepo
(`tools/vscode-iec`). Issues and contributions welcome there. Licensed under
the Apache License 2.0.

## Packaging a VSIX locally

```sh
npm ci
npx @vscode/vsce package -o /tmp/vscode-iec.vsix
```

**Do not pass `--no-dependencies`.** It produces a VSIX that installs
cleanly and then fails at activation with

```
Activating extension joyauto.vscode-iec failed due to an error:
Error: Cannot find module 'vscode-languageclient/node'
```

Nothing surfaces in the UI: the commands still appear in the palette,
because the palette is built from `package.json` and needs no activation.
Selecting one silently does nothing. The only evidence is the Extension Host
log under the user-data-dir.

`publish.yml` runs `npm ci` and packages with dependencies, so releases are
unaffected -- this only bites a local build.
