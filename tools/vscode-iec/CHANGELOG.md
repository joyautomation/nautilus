# Changelog

All notable changes to the **nautilus IEC 61131-3** extension are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [0.1.0] - 2026-07-06

### Added
- Initial skeleton: language registration for IEC 61131-3 Structured Text
  (`iec-st`, `.st` files, alias "Structured Text").
- TextMate grammar (`source.iec-st`) with syntax highlighting for block
  `(* *)` and line `//` comments, single-quoted strings, numeric / based /
  time / typed literals, control-flow and `VAR` keywords, elementary data
  types, logical operators, `:=` / `=>` / comparison / arithmetic operators,
  and the nautilus built-in functions and function blocks.
- `language-configuration.json` for comment toggling, bracket matching,
  auto-closing pairs, and indentation.
- TypeScript entry-point scaffold (`src/extension.ts`) for the forthcoming
  language client (not yet wired into the manifest).

### Notes
- Keyword, type, built-in-function, and function-block lists are derived
  directly from the nautilus Go compiler (`lang/st`, `lang/ir`).
