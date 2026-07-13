# nautilus IEC 61131-3

VS Code language support for **IEC 61131-3 Structured Text** (`.st`) and
**Function Block Diagram** (`.fbd`) as used by the
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

The extension spawns **`nautilus lsp`** — the nautilus CLI's language-server
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
go install github.com/joyautomation/nautilus/cmd/nautilus@latest
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
  live controller is running. Review a logic change the way you'd review
  the wiring, not the text.
- **Online edits speak `.fbd`** — a controller running an FBD program
  serves and accepts the `.fbd` source itself, so download / text diff /
  pull and the sync status bar work exactly as they do for `.st`.
- `.fbd` files get the same **diagnostics-as-you-type** as `.st` — the
  netlist compiles through the identical `lang/fbd` → `lang/st` pipeline the
  runtime uses, with errors mapped back to the exact `.fbd` line.

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
  github.com/joyautomation/nautilus/cmd/nautilus@latest`. Point
  `nautilus.cliPath` at it if it's installed elsewhere.
- **Inline live values** need a running nautilus controller exposing the tag
  API; set `nautilus.runtimeUrl` (default `http://localhost:8080`).

## Roadmap

**Graphical LD / SFC projection and FBD editing** — the FBD preview is the
first projection; next are Ladder / SFC views and bidirectional editing,
where diagram edits write back to the *same* text that lowers to the *same*
nautilus IR.

## Source & license

Part of the [nautilus](https://github.com/joyautomation/nautilus) monorepo
(`tools/vscode-iec`). Issues and contributions welcome there. Licensed under
the Apache License 2.0.
