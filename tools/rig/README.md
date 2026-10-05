# tools/rig: the whole-VS-Code rig

This rig runs a real desktop VS Code with the nautilus extension installed and
drives it the way a person does, with pointer and keyboard. Everything happens
in a throwaway Incus container with its own virtual display (Xvfb `:99` and
openbox), so it never touches the display of the machine it runs on. It has
two jobs:

- **asserting** (the product's tests). `smoke/run.sh` runs every `NN-*.sh` extension
  smoke check. `selftest.sh` runs every gesture verb once and reads each
  edit back from disk.
- **filming** (the content repo). The episode beats in
  `joyautomation/content/assets/capture` source this rig through `RIG_DIR`
  and use the same verbs, profile and frame.

The rig used to live in the content repo, filed as a filming aid. It moved here
(test plan §4.2, decision 1) because the verbs break whenever the extension
changes, so they have to be fixed in the same PR as the change.

```
tools/rig/
  lib/container.sh   the container: rig_up / rig_run / rig_push(_tree) / rig_down
  lib/lib.sh         capture plumbing: vscode_profile, launch_vscode, vs_cmd,
                     snap, rec_start, clip_start, start_controller,
                     cleanup_capture, ...
  lib/clips-index.sh the per-run review index (clips.html / clips.md), host side
  lib/manifest.sh    the run's manifest.json (schema 1), host side
  verbs/prep.sh      per-run setup inside the container (frame, ext_scaffold,
                     ext_open, diagram, the CDP `code` wrapper)
  verbs/gestures.sh  the verbs: semantic targets ("step Fill", "pin IN1 of t1")
                     resolved through cdp.js, input through xdotool
  verbs/cdp.js       read-only DevTools queries into the webviews and workbench
  selftest.sh        every verb, once, on a fresh scaffold
  demo.sh            selftest.sh filmed at episode pace and frame → out/demo/
  smoke/             the extension smoke suite: run.sh, build.sh, lib.sh,
                     NN-*.sh checks, and fixtures/ (their projects)
  builds/            build-from-scratch dogfood (test plan §5): one folder per
                     build — reference/ project, build.sh, PLAN.md, FINDINGS.md
  out/               results (gitignored): build/, smoke/, selftest/,
                     manifest.json, demo/
```

## Running it

You need incus, and your user in the `incus` group. The first run builds the
`nautilus-vscode-rig` image (apt, VS Code, fonts), which takes about 5 minutes
and is published for reuse. Every run after that starts in seconds.

```sh
tools/rig/smoke/build.sh                   # naut + VSIX from THIS checkout → out/build/
tools/rig/smoke/run.sh                     # build, then every NN-*.sh check (about 60 min)
tools/rig/smoke/run.sh 03-preview-undo     # one check, merged into the last results
tools/rig/selftest.sh                      # build, then every verb (human pace)
G_PACE=fast tools/rig/selftest.sh          # pointer teleports; what the nightly runs
tools/rig/demo.sh                          # the demo clips: human pace, 2560x1440 (~10 min)

NAUT=… VSIX=… tools/rig/smoke/run.sh       # builds you already have
NAUTILUS_REF=v0.12.0-rc1 tools/rig/smoke/run.sh   # build a ref, in ../nautilus-smoke
RIG_KEEP=1 …                               # leave the container up afterwards
RIG_CLIPS=0 …                              # no review clips (a quicker rehearsal)
RIG_NAME=nautilus-smoke-$(hostname -s) …   # one container name per concurrent run
```

`build.sh` builds the checkout in place: `go build ./cmd/naut`, `npm ci && npm
run package` in `hmi/` (the webview needs it), then `npm ci` in
`tools/vscode-iec` and `webview-ui`, then `vsce package`. It prints `NAUT=`,
`VSIX=` and `NAUTILUS_SHA=` lines, which `run.sh` and `selftest.sh` evaluate.
Never use `vsce package --no-dependencies`, because the VSIX must carry the
runtime dependencies that the extension loads at activation. Never package from
`/tmp` or a scratch directory either, because vsce fails there.

**Exit status** is the number of FAIL rows (capped at 100), and 2 when a run
never got as far as a table. Results go to `out/smoke/results.tsv` (check,
verdict, what, png) and `out/selftest/verbs-selftest.tsv` (n, verb,
PASS|FAIL|XFAIL|XPASS, detail), each beside its PNG evidence. **Read the PNGs.**
A verb can pass on the text and still show the wrong picture.

**Clips.** Every smoke check is recorded whole (`out/smoke/<check>.mp4`) and
every self-test verb on its own (`out/selftest/verbs-NN-<verb>.mp4`, beside
its PNG), so a run can be watched back for review or a demo. Each run also
writes `clips.html` (open it: every clip inline, with its verdict and
evidence PNGs) and `clips.md` into its folder. The nightly's `rig-out-*`
artifact carries all of it. `lib.sh`'s `clip_start`/`clip_stop` do the
recording: the root window's capture-frame corner, where `launch_vscode`
parks VS Code, not the window itself (VS Code relaunches mid-check, and
context menus and tooltips are separate X windows), 15 fps, pointer drawn,
fragmented mp4 so a clip survives a check killed by its timeout.
`RIG_CLIPS=0` turns them off.

**Manifest.** Each run also writes `out/manifest.json` (`lib/manifest.sh`;
schema 1, the contract in `randd/handoffs/TEST-PLAN-PROOF.md`), which the
docs site's proof pages read from the run's artifact. `run` says what was
tested and how it was filmed: nautilus sha, `naut version`, the VSIX's
version, the container's VS Code, UTC date, pace, frame (`CAP_W`×`CAP_H`,
`REC_ZOOM`, `REC_FONT_SIZE`), `runId` (`$GITHUB_RUN_ID` or `local`) and host.
`items` has one entry per smoke check (`kind` smoke: verdict = worst row,
FAIL > WARN > PASS, the rows, clip, PNGs, `durationS`) and one per self-test
verb (`kind` verb: `n`, verdict, detail, clip, PNG, `durationS`, `editor`
from the verb's prefix). Paths are relative to the manifest. Smoke and
selftest into the same `RIG_OUT` (the nightly) share one manifest; each run
rebuilds it from `smoke/` and `selftest/` on disk and leaves out a part
from another build (a different sha) or another set. `RIG_SET=nightly|demo`
names the set (default `nightly`); the frame and pace are what the container
actually used (each part writes `frame.env`, then `run.json`).

### Demo clips

`demo.sh` is the verb self-test filmed for people: every verb at
`G_PACE=human` in the series frame (2560×1440, `REC_ZOOM=2`,
`REC_FONT_SIZE=14`, the frame the ex01 beats are shot in), one clip each.
These are the clips the docs site's proof pages play; the nightly's
fast-pace clips are the fallback. It runs `selftest.sh` with those settings
and `RIG_SET=demo`, into `out/demo/` (`RIG_OUT` moves `out/`, so
`RIG_OUT=/x` writes `/x/demo/`): `demo/selftest/` as the self-test writes
it, and `demo/manifest.json` (set `demo`, pace `human`). The container's
display is sized from `CAP_W`/`CAP_H` (Xvfb is the frame plus 40 px each
way, `lib/container.sh`), so the big frame needs nothing else. Takes about 10
minutes. Watch a few clips before trusting a new build's set: a verb that
passes can still look wrong.

```sh
tools/rig/demo.sh                                   # build THIS checkout, film
NAUT=… VSIX=… RIG_NAME=nautilus-demo-$(hostname -s) tools/rig/demo.sh
```

### What goes into the container

Each entry point (`smoke/run.sh`, `selftest.sh`, and the content repo's
`record-vscode.sh`) pushes the same pieces:

| in the container | from |
|---|---|
| `~/lib.sh` | `lib/lib.sh` |
| `~/fixtures/` | `verbs/` first, then the run's own data laid over it: `smoke/fixtures/`, or an episode's `fixtures/` |
| `~/smoke/` | `smoke/` (smoke runs only) |
| `naut` | `/usr/local/bin` (selftest, record-vscode), or `/opt/smoke-bin`, off PATH, for smoke |
| the VSIX | installed with `code --install-extension` |

Beats and checks start with `source "$HOME/fixtures/prep.sh"`, followed by
`source "$HOME/fixtures/gestures.sh"` if they gesture. An episode that carries
its own copy of a verb file (content's `wk06`) keeps that copy, because the
episode's files are pushed last.

## The verbs

`verbs/gestures.sh` is the layer to build on. A verb takes semantic arguments
and resolves them to window pixels at the moment of the gesture, by asking the
webview's DOM where the element is: `cdp.js` reads `getBoundingClientRect` over
VS Code's `--remote-debugging-port`, which `prep.sh`'s `code` wrapper adds. All
input is still xdotool on the real window. Every verb saves the file and reads
it back before it returns 0. The verb list is at the top of the file.
`selftest.sh` exercises the core set. The verbs folded in from content's
`gestures-c.sh` and `beats-lib-b.sh` (series and branch ladder edits,
`mimic_bind_at`, workbench dialogs) are proven by ex01's build beats, but they
are not all in the self-test yet.

`G_PACE=human` (the default) moves the pointer with `human_move`, which is what
a take wants. `G_PACE=fast` teleports the pointer, for rehearsals and the
nightly.

XFAIL rows in the self-test exercise gestures the extension does not support
yet. They are listed in the content repo's
`ex01-lift-station/GESTURE-FINDINGS.md`. An XPASS means the gesture started
working, so go and look.

## The smoke suite

The smoke checks use the same rig, but they assert instead of shooting. Each
`NN-*.sh` drives a real VS Code 1.139 on its own fresh profile, reads files
back after gestures, queries the controller's `/api`, asks X for the window
title, and counts pixels where nothing else can answer (the live pill's green,
a modal's primary button). Run it before every stable promotion. The checks
still use hard-coded pixel boxes in places. Moving them onto the verb layer is
test plan §4.2 step 2. Traps learned while building it:

- **naut goes in at `/opt/smoke-bin`, never `/usr/local/bin`.** The extension
  searches `/usr/local/bin` itself, and check 01 needs a machine with no naut.
- **Nothing in a `rig_run` command line may contain `vscode-rec`.**
  `launch_vscode` pkills by that string and takes your shell with it.
- **Startup toasts auto-hide (~15 s)** before `launch_vscode` finishes
  settling. Read startup notifications in the notification centre.
- **Editor-title buttons move** when git sees the file modified (the git
  extension's "Open Changes" button joins them). Click them while the edit is
  only in the buffer, or use the palette.
- `window.dialogStyle: custom` puts modals inside the window, so the grab sees
  them. `window.zoomPerWindow: false` makes a window zoom write
  `window.zoomLevel`, which is how the zoom-key check proves that the diagram
  kept Ctrl+= to itself.
- **One run per `RIG_NAME` at a time.** Every entry point's EXIT trap is
  `rig_down`. A second run on the same name (or a timed-out one) deletes the
  container under the first, whose checks then all die with "Failed to
  retrieve PID of executing child process" (2026-09-25). A run that hangs at
  `incus launch` still holds the name, so kill it before you start another.
  Machines that share an incus remote need distinct names, so derive the name
  from the hostname.
- **Pixel boxes assume ONE editor group.** A palette command that is not
  offered for the active editor fuzzy-matches another one. "Open as Diagram
  Editor" on a file already showing as a diagram runs "Open … Diagram Preview"
  and splits the area. Reload Window does not restore a preview panel, so the
  split survives as an empty group. Check 07 closes the other groups before its
  reload and NOTEs any green outside the pill box.
- **Nothing version-shaped is hard-coded.** Check 02 reads
  `nautilusCli.minVersion` out of the VSIX. It moved 0.11.0 → 0.12.0 in 5f045a0,
  and a hard-coded check went red on a correct extension.
- **A pasted FBD copy is not meant to check clean.** Copy severs tag reads to
  `_`, and a whole-program paste carries undeclared coils (by design, see
  `lang/fbd/editparity.go` opDuplicate). Check 05 asserts that the paste is
  well-formed instead: every statement arrives once, and with the source's
  declarations spliced in, `naut check` gets past parsing and reports only `_`.

The checks' projects (`smoke/fixtures/tank-batch`,
`heated-tank.mimic.json`) are copies of the content repo's `ext-stable`
fixtures. Smoke owns its copies so that a nautilus checkout is all a run needs.

## The container

`lib/container.sh` starts from `images:ubuntu/noble` and installs Xvfb,
openbox, xdotool, ffmpeg, VS Code, the Night Owl theme and Red Hat YAML. It
then publishes the result as the `nautilus-vscode-rig` image (`RIG_IMAGE`).
Pass `--fresh` to `rig_up`, or `--rebuild-image` to the content repo's
`record-vscode.sh`, to re-provision it. The Xvfb screen is deliberately larger
than any capture frame (`CAP_W`/`CAP_H` + 40) so that VS Code's GTK window-frame
margin never forces lib.sh into xrandr panning. Everything runs as the user
`dev`, because VS Code refuses to run as root without `--no-sandbox`.
`incus launch` gets `</dev/null`, because with a non-terminal stdin it waits to
read instance YAML from it forever.

Frames: the ext-stable stills and the self-test use 1600x1000 at
`REC_ZOOM=2.5` (the `prep.sh` default), the smoke checks use 1920x1200 at zoom
1 (`smoke/lib.sh`), and the filming builds use 2560x1440 at `REC_ZOOM=2` (set
by the beats).

## Driving a real VS Code: traps

These traps date from the first desktop beats, shot on mira1's own display
(`:1`). They still apply in the container.

- **Capture the local X session, never an SSH-forwarded display.**
  `localhost:10.0` renders on the laptop you ssh'd from, so there is no
  framebuffer to read. `x11grab -window_id` reads the compositor's offscreen
  pixmap, so a window records even while it is obscured or the screen is
  locked. Input is the opposite: xdotool goes through XTEST, which a lock
  screen's keyboard grab swallows, so a desk session must be unlocked to drive
  anything. `require_unlocked` in `lib/lib.sh` fails fast. The container has no
  lock screen.
- **Context-menu items need a real click.** An XTEST click highlights the item
  but does not fire it. `yd_click` (ydotool, uinput) does fire it. See
  `lib/lib.sh`.
- **The workspace pins the runtime URL.** `naut new` writes
  `"nautilus.runtimeUrl": "http://localhost:8080"` into
  `.vscode/settings.json`, and workspace settings beat user settings. If you
  leave it, the extension connects to whatever else is on 8080: the status bar
  says `nautilus: live`, then `program differs`, and no live values appear. It
  looks like a broken extension. `point_extension_at` rewrites the setting.
- **`.fbd`/`.ld`/`.sfc` open as TEXT.** The diagram editors register with
  `priority: "option"`, so a plain open (or Ctrl+P) gives you the text view.
  Live values paint there too, so a shot can look fine while containing no
  diagram at all. Use `nautilus: Open … Diagram Preview` (`diagram` in
  `prep.sh`) or `ed_open_diagram` (gestures.sh).
- **Controllers outlive their run** and hold their port, so the next run dies
  on "none of these ports are free". `cleanup_capture` kills by port as well as
  by PID.
- **First-run furniture.** A profile that has never been used shows things a
  developer's own machine never does: a Copilot sign-in modal over the whole
  window, a window that opens at the wrong size, a recommendation toast for
  somebody else's extension (hence YAML is preinstalled in the image).
  `launch_vscode()` handles the known ones, and a new one will not be handled.
  Read the frames.

## VS Code in a browser was investigated and rejected. Don't redo this.

`code serve-web` (built into VS Code 1.127) serves the editor over HTTP, and
Playwright can drive and record it. That looked like a way to automate the VS
Code beats, and it isn't, for specific reasons:

- **The extension does not load.** Copying it into the server's `extensions/`
  directory is not enough: `extensions.json` there stays `[]`, and the editor
  opens `program.st` as **Plain Text**, with no highlighting, no diagnostics
  and no live values.
- **User settings don't come from the server profile.** The web client ignores
  a `settings.json` written to `data/User/` and keeps its own settings in
  browser storage, so theme, font size and workspace trust all have to be
  driven through the UI on every run.
- **Restricted Mode persists**, which disables part of what the mimic editor
  does anyway.

Each of these is probably solvable with enough UI automation. The result would
still be *web* VS Code, though, and there is no reason to accept that when real
desktop VS Code turned out to be drivable. This rig is what replaced that dead
end. For the same reason, `@vscode/test-electron` is a complement to the rig
(extension-host API tests), not a replacement: it reaches neither the webviews
nor real input (test plan §4.3).

## From the content repo

The content repo keeps episodes, beats, frame settings and fixture data, and it
sources this rig from a nautilus checkout:

```sh
NAUTILUS_REPO=~/Development/joyautomation/nautilus     # the default
RIG_DIR=${RIG_DIR:-$NAUTILUS_REPO/tools/rig}            # what content's lib/rig.sh uses
EP=ex01-lift-station NAUT=… VSIX=… ./record-vscode.sh 06-ld
```

Pin the checkout (a tag, or the commit the VSIX was built from) when an episode
has to re-render the way it was shot. The verbs follow the extension, so a beat
filmed against 0.11.x drives 0.11.x's editors.

## Nightly

`.github/workflows/rig-nightly.yml` runs the smoke suite and the self-test
(`G_PACE=fast`) every night at 06:00 UTC on a self-hosted runner and keeps a
rolling "rig nightly red" issue. Registering a box: [RUNNER.md](RUNNER.md).
