# Mimic editor gesture harness

The browser-gesture test layer for the mimic editor webview. It loads the
**production** `mimic-editor.js` bundle in headless Chrome, performs the host
side of the webview message protocol (`src/mimicEditor.ts`) with a test doc,
and drives **real** pointer/keyboard input over the Chrome DevTools Protocol.

## Why it exists

The mimic editor's pure-logic modules (`pipeDraft.ts`, `routing.ts`,
`autoroute.ts`, …) have fast `node --test` unit tests. But whole classes of
bug only appear in a real browser:

- **focus / keyboard delivery** — does `Enter` reach the finish handler when a
  toolbar button has focus?
- **pointer capture & z-order hit-testing** — does a click on a port dot that
  sits on an equipment box register the way the user sees it?
- **preview vs. commit geometry** — does the live rubber-band / drag preview
  render the SAME shape (stubs, orthogonal corners, anchors) that pointer-up /
  Enter actually commits?

Those need real input dispatched at real coordinates against the real render
tree — which is exactly what this harness does (`Input.dispatchMouseEvent` /
`dispatchKeyEvent` via CDP, **not** synthetic DOM `dispatchEvent`).

## Run

```sh
# from tools/vscode-iec/webview-ui
npm run build          # produce ../media/dist/mimic-editor.js first
npm run test:gestures  # runs gesture-harness/gestures.test.mjs
```

Requirements: a `google-chrome`/`chromium` on `PATH` and Node ≥ 21 (uses the
built-in global `WebSocket` and `fetch` — **no npm dependencies**). This is why
it is a separate script and **not** part of `npm test` / CI, which must stay
browser-free.

Options (env):

- `MIMIC_BUNDLE=/path/to/media/dist` — test a specific build (e.g. an installed
  VSIX's `media/dist`) instead of the repo's `../media/dist`. Handy for A/B:
  point it at an old bundle and watch the regression tests fail.
- `HEADED=1` — run with a visible window (debugging).

## Files

| file | role |
|------|------|
| `cdp.mjs` | Minimal CDP client over WebSocket: launch Chrome, dispatch real input, evaluate page JS. Zero deps. |
| `host.html` | Stand-in for the webview shell: stamps `data-mimic-mode`, mounts `#app`, loads the bundle, and exposes `window.__*` helpers that only READ the rendered DOM (ports, handles, draft/pipe paths, canvas↔viewport mapping). |
| `harness.mjs` | `Editor` — opens a bundle with a test doc, drives the host protocol, and offers a gesture layer (enter pipe mode, click/hover a named port, drag a handle mid-flight, press Enter). Plus `applyOpToDoc`, a tiny reducer so a committed op can be reflected back and its "materialized" render compared. |
| `gestures.test.mjs` | The regression suite (`node:test`). |
| `themes.mjs` | What VS Code injects per theme (body class + `--vscode-*` variables, Dark Modern / Light Modern / High Contrast values): `applyThemeJs('light')` reproduces a theme so colours can be asserted — or screenshotted — against light, dark and HC. |
| `diagram-host.html` / `diagram.test.mjs` | The same approach for the FBD / Ladder / SFC diagram bundle (`fbd-flow.js`): models delivered by `postMessage` as the extension host does, real input, assertions on the posted `edit`/`ldEdit`/`sfcEdit` ops. `DIAGRAM_BUNDLE=…` points it at another build. |

## Adding a regression case

Reproduce the user's gesture with the `Editor` helpers, then assert on either
the ops posted (`ed.ops()`) or the rendered geometry (`ed.pipePaths()`,
`ed.draft()`). For a preview-vs-commit case: capture the mid-drag/pre-Enter
render, commit, reflect the op with `applyOpToDoc` + `ed.setDoc`, and assert the
two paths are equal. When you fix a bug, first confirm the new test FAILS
against the shipped bundle (`MIMIC_BUNDLE=…`), then passes against your build.
