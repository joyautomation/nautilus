# prep.sh — the rig's per-run setup, SOURCED inside the container before
# anything drives VS Code. Every entry point pushes tools/rig/verbs/ into the
# container as ~/fixtures (next to ~/lib.sh), then lays the run's own data
# over it — the smoke suite's smoke/fixtures/, or a content episode's
# fixtures/ — so a beat or a check starts with
#
#     source "$HOME/fixtures/prep.sh"
#     source "$HOME/fixtures/gestures.sh"     # if it gestures
#
# Merged from the two copies the content repo carried (ext-stable's, and
# ex01-lift-station's, which was ext-stable's plus the CDP `code` wrapper at
# the end). The wrapper is now on for every run: it only adds a DevTools port,
# and gestures.sh's cdp.js needs it to find anything in a diagram.
#
# THE FRAME. The default is the ext-stable stills frame: a 1600x1000 window at
# window.zoomLevel 2.5 (x1.58), editor.fontSize 14 — a ~1015x635 CSS-px VS
# Code, which still reads like VS Code at its normal size when the Marketplace
# shows it ~900 px wide. The series films 2560x1440 (beats set CAP_W/CAP_H and
# REC_ZOOM before sourcing this); the smoke suite uses 1920x1200 at zoom 1
# (smoke/lib.sh). Same theme (Night Owl) and the same furniture-free profile
# everywhere, from lib.sh's vscode_profile().

export OUT_DIR=${OUT_DIR:-$HOME/out}
export PROFILE=${PROFILE:-$HOME/.vscode-rec}
export CAPTURE_DISPLAY=${CAPTURE_DISPLAY:-${DISPLAY:-:99}}
export CAP_W=${CAP_W:-1600} CAP_H=${CAP_H:-1000}
export REC_ZOOM=${REC_ZOOM:-2.5} REC_FONT_SIZE=${REC_FONT_SIZE:-14}
source "$HOME/lib.sh"

FIX=$HOME/fixtures
NAUTILUS=$(command -v naut) || die "no naut on PATH in the container — the entry point (smoke/run.sh, selftest.sh, record-vscode.sh) pushes it"
export NAUTILUS
PORT=8080   # the container runs nothing else; the scaffold's own port

code --list-extensions 2>/dev/null | grep -qi '^joyauto.vscode-iec$' \
  || die "joyauto.vscode-iec is not installed — run with VSIX=<the build under test>"

# ext_scaffold <name> — `naut new --no-input` (the Demo template: program.fbd,
# sim.st, interlocks.ld, blocks.st, demo_test.yaml), committed, so the diff
# shot has a HEAD to diff against and the explorer shows no untracked noise.
ext_scaffold() {
  PROJ=$HOME/$1
  rm -rf "$PROJ"
  ( cd "$HOME" && naut new "$1" --no-input >/dev/null ) || die "naut new failed"
  git -C "$PROJ" config user.name "Demo"
  git -C "$PROJ" config user.email "demo@example.com"
  git -C "$PROJ" add -A
  git -C "$PROJ" commit -qm "scaffold: naut new $1"
  point_extension_at "$PROJ" "$PORT"
}

# diagram <FBD|Ladder|SFC> — the diagram for the file open in the editor,
# given the WHOLE editor. Trap 2 in tools/rig/README.md: .fbd/.ld/.sfc register
# at priority "option", so a plain open is the TEXT view. The preview
# command (the tested path — wk01 beat 7) opens the diagram in a second pane
# beside the text: right for a take, wrong for a still. So close the text
# pane after: group 1, Ctrl+W, and the empty group folds away.
#
# Not tried again: workbench.editorAssociations (ignored for a file opened on
# the command line before the extension activates — the still came out as
# text) and "Reopen Editor With…" through vs_cmd (its picker opened late and
# the label was typed into program.fbd — lib.sh's open_file story, again).
diagram() {
  vs_cmd "nautilus: Open $1 Diagram Preview" "${2:-10}"
  xdotool key --clearmodifiers ctrl+1; sleep 0.6
  xdotool key --clearmodifiers ctrl+w; sleep 2
}

# ext_run — `naut run` in $PROJ, and wait for it to have moved: the sim task
# seeds LevelPct/TempC at 60 and needs a few seconds to look alive.
ext_run() {
  start_controller "$PROJ" "$PORT"
  sleep "${1:-6}"
}

# ext_open <file> — VS Code on $PROJ with <file> open (command line, not
# Ctrl+P — see lib.sh), first-run furniture cleared.
# EXTRA_SETTINGS='"key": value, …' ext_open … — per-shot additions to the
# recording profile, spliced in after its opening brace (the profile is
# JSONC, so no JSON tooling).
ext_open() {
  vscode_profile
  [[ -n ${EXTRA_SETTINGS:-} ]] && sed -i "0,/^{/s//{\n  $EXTRA_SETTINGS,/" "$PROFILE/User/settings.json"
  launch_vscode "$PROJ" ${1:+"$PROJ/$1"}
  xdotool key Escape; sleep 1
  vs_cmd "Notifications: Clear All Notifications" 1
}

# park — pointer off every button, so no tooltip sits over the still.
park() { xdotool mousemove --window "$WIN" "${1:-$((CAP_W - 40))}" "${2:-$((CAP_H / 2))}"; sleep "${3:-1.5}"; }

# ── diagram framing ─────────────────────────────────────────────────────────
# The diagram editors fit the WHOLE program on open, and their text scales
# with the canvas, not with window.zoomLevel — so program.fbd, which is tall,
# fits at a size that is ~5 px text once the Marketplace shrinks it. The
# stills zoom in on the part worth reading, with the editor's own controls.
# Coordinates are window coordinates at CAP_W x CAP_H = 1600x1000 with the
# side bar hidden; change the size and re-measure.

# fit_view — the Controls panel's fit button (bottom-left of the canvas).
fit_view() { xdotool mousemove --window "$WIN" 75 879; sleep 0.3; xdotool click 1; sleep 1.5; }

# zoom_at <x> <y> <clicks> — wheel-zoom about a point (svelte-flow zooms
# toward the pointer).
zoom_at() {
  xdotool mousemove --window "$WIN" "$1" "$2"; sleep 0.4
  local i; for ((i = 0; i < $3; i++)); do xdotool click 4; sleep 0.5; done
  sleep 0.8
}

# pan <from x> <from y> <dx> <dy> — drag empty canvas. <from> must be empty
# canvas, or the drag moves a block (a real edit to the file).
pan() {
  local x=$1 y=$2 dx=$3 dy=$4 i
  xdotool mousemove --window "$WIN" "$x" "$y"; sleep 0.3
  xdotool mousedown 1
  for i in 1 2 3 4 5 6 7 8 9 10; do
    xdotool mousemove --window "$WIN" $((x + dx * i / 10)) $((y + dy * i / 10)); sleep 0.05
  done
  xdotool mouseup 1; sleep 1
}

# hide_sidebar — Ctrl+B, BEFORE the diagram opens, so its fit uses the full
# width. (The recording profile already hides the activity bar.)
hide_sidebar() { xdotool key --clearmodifiers ctrl+b; sleep 1.2; }

# wait_state '<python expr over t, the tag dict>' [timeout s] — poll the
# controller until the expression holds, e.g. 't["Mixer"] and t["Heater"]'.
# For stills whose subject is a MOMENT (an active step), not just "running".
wait_state() {
  local expr=$1 limit=${2:-90} i
  for ((i = 0; i < limit * 4; i++)); do
    curl -sf --max-time 1 "localhost:$PORT/api/state" 2>/dev/null \
      | python3 -c "import sys,json; t=json.load(sys.stdin)['tags']; sys.exit(0 if ($expr) else 1)" 2>/dev/null \
      && return 0
    sleep 0.25
  done
  die "controller never reached: $expr"
}

# ext_fixture <name> — a project from ~/fixtures/<name>, committed like a
# scaffold, instead of `naut new`.
ext_fixture() {
  PROJ=$HOME/$1
  rm -rf "$PROJ"; cp -r "$FIX/$1" "$PROJ"
  git -C "$PROJ" init -q
  git -C "$PROJ" config user.name "Demo"
  git -C "$PROJ" config user.email "demo@example.com"
  git -C "$PROJ" add -A
  git -C "$PROJ" commit -qm "$1"
  point_extension_at "$PROJ" "$PORT"
}

# ── CDP: let gestures.sh ask the webviews where things are ──────────────────
# VS Code started with --remote-debugging-port lets cdp.js read the diagram's
# DOM (element boxes — never input; every gesture is still xdotool). Wrapped
# as `code` on PATH so lib.sh's launch_vscode, which calls plain `code`, picks
# it up untouched. Only a window launch gets the flag.
export CDP_PORT=${CDP_PORT:-9229}
mkdir -p "$HOME/.rigbin"
cat >"$HOME/.rigbin/code" <<WRAP
#!/bin/sh
for a in "\$@"; do case \$a in --list-extensions|--install-extension*|--uninstall-extension*|--version) exec /usr/bin/code "\$@";; esac; done
exec /usr/bin/code --remote-debugging-port=$CDP_PORT "\$@"
WRAP
chmod +x "$HOME/.rigbin/code"
case :$PATH: in *":$HOME/.rigbin:"*) ;; *) export PATH=$HOME/.rigbin:$PATH ;; esac
