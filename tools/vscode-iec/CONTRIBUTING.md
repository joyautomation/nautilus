# Contributing to the nautilus VS Code extension

The extension lives in `tools/vscode-iec` of the
[nautilus](https://github.com/joyautomation/nautilus) monorepo. Issues and
pull requests go there. Release mechanics (channels, tags, registries) are in
the repo's [RELEASING.md](../../RELEASING.md).

## Layout

- `src/` — the extension host (TypeScript, compiled to `out/`).
- `webview-ui/` — the diagram, mimic and component editors (Svelte + Vite,
  built to `media/dist/`). A separate npm package with its own lockfile. It
  links the HMI kit locally (`file:../../../hmi`), so `hmi/` must be built
  first.
- `syntaxes/`, `schemas/`, `walkthroughs/` — grammars, the YAML JSON
  schemas, and the Get Started walkthrough.

## Build

```sh
(cd ../../hmi && npm ci && npm run package)   # webview-ui's local dependency
npm ci --prefix webview-ui
npm ci
npm run compile          # extension host
npm run build:webview    # webviews
```

Press F5 in VS Code with this folder open to run an Extension Development
Host.

## Test

- `npm test` — the extension host and webview pure-logic tests
  (`node --test`, no browser). This is what CI runs, together with
  `npm --prefix webview-ui run check` (svelte-check).
- `npm run test:e2e` — a real VS Code with nothing useful on PATH, the way a
  desktop launcher starts it: the extension must find a `go install`ed
  `naut` and, without one, still activate and say how to fix it
  (`src/e2e/run.ts`). CI runs it on Linux (under `xvfb-run`), macOS and
  Windows.
- **Gesture harness** — `npm --prefix webview-ui run test:gestures` drives
  the real webview bundles in headless Chrome with real pointer and
  keyboard input over the DevTools protocol. It covers focus, hit-testing
  and preview-vs-commit geometry the pure-logic tests can't reach. It needs
  Chrome or Chromium on PATH and a fresh `npm run build:webview`, and is
  kept out of `npm test` and CI. See `webview-ui/gesture-harness/README.md`.

Gestures and keys for each diagram editor are declared once in
`webview-ui/src/shortcuts.ts`; the toolbar hint and the **?** legend are
both generated from it, so add a new gesture there.

## Packaging a VSIX locally

```sh
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

The release workflows package with dependencies
(`.github/actions/vscode-package`), so published builds are unaffected;
this only bites a local build.

## The README is the Marketplace page

`README.md` is what the Marketplace and Open VSX show. Images there must be
absolute `https://raw.githubusercontent.com/joyautomation/nautilus/main/...`
URLs (relative links break on the listing), and every command and setting it
names must exist in `package.json`.

## CHANGELOG

Add a line under `## [Unreleased]` in `CHANGELOG.md` for every user-visible
change, in the voice of the existing entries: what changed and why it
matters to someone using the extension.
