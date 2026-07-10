# nautilus IEC 61131-3 (Structured Text)

VS Code language support for **IEC 61131-3 Structured Text** (`.st`) as used by
the [nautilus](https://github.com/joyautomation/nautilus) Go + SvelteKit SCADA
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

**Graphical LD / FBD / SFC projection** — render Ladder / FBD / SFC views
that edit the *same* program and lower to the *same* nautilus IR as the text;
text ↔ diagram stay in sync as projections of one IR.

## Source & license

Part of the [nautilus](https://github.com/joyautomation/nautilus) monorepo
(`tools/vscode-iec`). Issues and contributions welcome there. Licensed under
the Apache License 2.0.
