# nautilus IEC 61131-3 (Structured Text)

VS Code language support for **IEC 61131-3 Structured Text** (`.st`) as used by
the [nautilus](https://github.com/joyautomation/nautilus) Go + SvelteKit SCADA
framework. The goal is to let you *develop SCADA in VS Code like a real software
developer* — starting with syntax highlighting and building toward compiler
diagnostics, live tag values, and graphical language projection.

## What works today

**Syntax highlighting**, immediately, with no build step. This is a purely
declarative extension right now — the grammar and language configuration are
contributed straight from `package.json`, so opening any `.st` file lights up:

- Block `(* … *)` and line `//` comments
- Single-quoted strings with `$`-escapes
- Numeric literals: decimal integers, reals, exponents, based integers
  (`16#FF`, `2#1010`, `8#777`), typed literals (`INT#42`, `REAL#3.14`,
  `BOOL#TRUE`), and time literals (`T#5s`, `TIME#1h30m`)
- Boolean constants `TRUE` / `FALSE`
- Control-flow keywords, `VAR…END_VAR` sections, storage modifiers
- Elementary data types (`BOOL`, `INT`, `DINT`, `REAL`, `LREAL`, `TIME`,
  `STRING`, and the rest of the IEC elementary set)
- Logical operators (`AND`, `OR`, `XOR`, `NOT`, `MOD`) and symbolic operators
  (`:=`, `=>`, `<>`, `<=`, `>=`, `+ - * /`)
- Built-in functions (`ABS`, `MIN`, `MAX`, `LIMIT`, `SQRT`, …) and standard
  function blocks (`TON`, `TOF`, `TP`, `R_TRIG`, `F_TRIG`, `CTU`, `CTD`,
  `CTUD`, `SR`, `RS`)

All highlighting keyword/type/built-in lists are derived **directly from the
nautilus Go compiler** (`lang/st/token.go`, `lang/st/lexer.go`,
`lang/ir/builtins.go`, `lang/ir/builtins_fb.go`) so they match what the
compiler actually accepts.

### Install / try it

```sh
# From this directory, with vsce installed:
npx @vscode/vsce package
code --install-extension vscode-iec-0.1.0.vsix
```

Or, for a dev loop, open this folder in VS Code and press **F5** to launch an
Extension Development Host, then open a `.st` file.

> ST is case-insensitive (the nautilus lexer upper-cases identifiers before
> keyword lookup), and the grammar matches keywords case-insensitively to
> match.

## Roadmap

This skeleton is deliberately declarative. The interesting work is next, and
each step reuses the existing nautilus Go compiler and runtime rather than
re-implementing anything in TypeScript.

### (a) Language server backed by the nautilus `lang/st` compiler

Wrap the existing Go `lang/st` parser + `lang/ir` lowering pass as a
long-running **`st-lsp`** binary speaking LSP over stdio. Because the compiler
already produces precise tokens, an AST, and typed lowering diagnostics, the
server can offer *real* feedback rather than regex heuristics:

- **Diagnostics** — surface parse and type/lowering errors inline as you type
  (dangling `ELSE`, unknown FB fields, non-numeric `LIMIT` args, etc.).
- **Completion** — keywords, elementary types, built-in functions/FBs, and
  in-scope `VAR` / `VAR_EXTERNAL` tags.
- **Go-to-tag / hover** — jump from an identifier to its `VAR` declaration, and
  hover a `VAR_EXTERNAL` tag to see its runtime binding and datatype.

The client side lives here: uncomment `main` / `activationEvents` in
`package.json`, compile `src/extension.ts` (`npm run compile`), and start a
`vscode-languageclient` `LanguageClient` with `documentSelector`
`[{ language: "iec-st" }]` that spawns the `st-lsp` binary.

### (b) Inline live-value decorations

Port the concept already proven in mini-scada's CodeMirror editor
(`hmi/src/lib/editor/inline-values.ts`) to VS Code. There, an editor extension
scans the document for tag accesses and renders the current runtime value as an
inline widget, refreshed from a live value map.

In VS Code this becomes a `TextEditorDecorationType` with `after` content
attached to each identifier that resolves to a running tag. The value stream is
fed by a connected nautilus runtime's **SSE / tag API**, so while a program is
running you see `LevelPct` annotated with its live value right in the source —
the "watch window, inline" experience.

### (c) Graphical LD / FBD / SFC projection

A round-tripping graphical editor: render Ladder Diagram / Function Block
Diagram / Sequential Function Chart views that edit the *same* program and lower
to the *same* nautilus **IR** as the text. Text ↔ diagram stays in sync because
both are just projections of one intermediate representation — edit either side,
regenerate the other. This is the payoff of building on a real compiler with a
shared IR instead of treating each language as a separate island.

## Files

| Path | Purpose |
| --- | --- |
| `package.json` | Extension manifest — language + grammar contributions |
| `language-configuration.json` | Comments, brackets, auto-closing, indentation |
| `syntaxes/iec-st.tmLanguage.json` | TextMate grammar (`source.iec-st`) |
| `src/extension.ts` | Entry-point scaffold for the future language client |
| `tsconfig.json` | TypeScript config for compiling `src/` to `out/` |

## License

See the nautilus repository root.
