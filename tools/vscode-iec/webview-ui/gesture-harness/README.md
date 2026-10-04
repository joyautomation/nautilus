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
it is a separate script and **not** part of `npm test`, which must stay
browser-free; CI runs it in its own job (`vscode-iec-gestures`).

Options (env):

- `MIMIC_BUNDLE=/path/to/media/dist` — test a specific build (e.g. an installed
  VSIX's `media/dist`) instead of the repo's `../media/dist`. Handy for A/B:
  point it at an old bundle and watch the regression tests fail.
- `HEADED=1` — run with a visible window (debugging).
- `GESTURE_CLIPS=<dir>` — record every test's run as a clip (below).

## Clips

```sh
GESTURE_CLIPS=/tmp/clips npm run test:gestures
xdg-open /tmp/clips/index.html     # every clip, inline, with PASS/FAIL
```

Each test's run becomes `<dir>/<suite>/<NN>-<slug>.mp4` — `gestures` for the
mimic suite, `diagram` for FBD/LD/SFC, `NN` the test's order in its file —
and `<dir>/index.html` / `index.md` list them all with the test's verdict.
Use them to demonstrate a gesture, or to see what a red test actually did
without re-running it. `clips.mjs` does it: each `Browser` the test launches
gets a CDP screencast (`Page.startScreencast`, JPEG frames kept in memory,
plus one final screenshot at close), and when the test ends the frames are
resampled onto a 25 fps timeline and piped to `ffmpeg` (libx264, yuv420p).
A screencast only sends a frame when the page repaints, so the clip keeps
real timing for everything that moves but shortens a still stretch longer
than 1 s (the mount wait, a settle sleep) to 1 s. Nothing is overlaid.

It needs `ffmpeg` on `PATH` (a run with `GESTURE_CLIPS` set and no ffmpeg
fails up front). The directory's suite folders are emptied at the start of a
run. Unset, `recordClips()` registers nothing: no hooks, no screencast, no
extra time. CI's `vscode-iec-gestures` job always records and uploads the
directory as the **`gesture-clips`** artifact (kept 14 days): download it
from the run's summary page and open `index.html`.

A new test file gets clips by calling `recordClips('<suite>')` once at top
level; tests themselves need nothing.

## Files

| file | role |
|------|------|
| `cdp.mjs` | Minimal CDP client over WebSocket: launch Chrome, dispatch real input, evaluate page JS. Zero deps. |
| `host.html` | Stand-in for the webview shell: stamps `data-mimic-mode`, mounts `#app`, loads the bundle, and exposes `window.__*` helpers that only READ the rendered DOM (ports, handles, draft/pipe paths, canvas↔viewport mapping). |
| `harness.mjs` | `Editor` — opens a bundle with a test doc, drives the host protocol, and offers a gesture layer (enter pipe mode, click/hover a named port, drag a handle mid-flight, press Enter). Plus `applyOpToDoc`, a tiny reducer so a committed op can be reflected back and its "materialized" render compared. |
| `gestures.test.mjs` | The regression suite (`node:test`). |
| `clips.mjs` | `recordClips(suite)`: with `GESTURE_CLIPS` set, records each test's run to an mp4 and writes the index. |
| `themes.mjs` | What VS Code injects per theme (body class + `--vscode-*` variables, Dark Modern / Light Modern / High Contrast values): `applyThemeJs('light')` reproduces a theme so colours can be asserted — or screenshotted — against light, dark and HC. |
| `diagram-host.html` / `diagram.test.mjs` | The same approach for the FBD / Ladder / SFC diagram bundle (`fbd-flow.js`): models delivered by `postMessage` as the extension host does, real input, assertions on the posted `edit`/`ldEdit`/`sfcEdit` ops. `DIAGRAM_BUNDLE=…` points it at another build. |

## Adding a regression case

Reproduce the user's gesture with the `Editor` helpers, then assert on either
the ops posted (`ed.ops()`) or the rendered geometry (`ed.pipePaths()`,
`ed.draft()`). For a preview-vs-commit case: capture the mid-drag/pre-Enter
render, commit, reflect the op with `applyOpToDoc` + `ed.setDoc`, and assert the
two paths are equal. When you fix a bug, first confirm the new test FAILS
against the shipped bundle (`MIMIC_BUNDLE=…`), then passes against your build.

## Adding a suite

`npm run test:gestures` runs `gesture-harness/*.test.mjs` by glob, so a new
suite is just a new file there — no `package.json` change.

1. Name it `<topic>.test.mjs` (e.g. `fbd-drag.test.mjs`).
2. Import helpers from the shared module, **never** from another `*.test.mjs`
   (importing a test file re-registers its tests):
   - FBD / Ladder / SFC: `./diagram-helpers.mjs` — `open`, `withPage`,
     `deliver`, `posted`, `clickAt`, `key`, the `FBD` / `LD` / `SFC` fixture
     models, `fbdOps` / `ldOps` / `sfcOps`, and so on.
   - Mimic editor: `./mimic-helpers.mjs` — `withEditor`, the `doc*` fixtures,
     plus re-exports of `Editor`, `applyOpToDoc`, `twoTankDoc`.
   Both also export `BUNDLE`, `HEADLESS` and `recordClips`.
3. Call `recordClips('<suite>')` once at the top of the file, after the
   imports. With `GESTURE_CLIPS=<dir>` each of the file's tests is recorded to
   `<dir>/<suite>/NN-<slug>.mp4` and listed in `index.html`; use a unique suite
   name per file so clip folders do not collide.
4. Put only `test(...)` calls in the file; shared helpers and fixtures belong
   in the helpers module.
