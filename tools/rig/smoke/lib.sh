# Shared plumbing for the extension SMOKE suite. SOURCED inside the rig
# container by every check (smoke/NN-*.sh), after run.sh has pushed:
#
#   ~/lib.sh            tools/rig/lib/lib.sh (the capture plumbing)
#   ~/fixtures/         tools/rig/verbs (prep.sh: ext_scaffold, diagram, ...;
#                       gestures.sh, cdp.js), with smoke/fixtures laid over it
#                       (tank-batch/, heated-tank.mimic.json)
#   ~/smoke/            this directory
#   /opt/smoke-bin/naut the CLI under test — deliberately NOT on PATH and not
#                       in /usr/local/bin, which the extension also searches
#                       (cliResolve.ts fallbackDirs), so check 01 can see the
#                       extension with no naut at all. with_naut puts it back.
#
# Unlike the stills, a check ASSERTS: it reads files in the container after
# a gesture, asks the controller's /api, and asks X for window state. Each
# verdict is one line in ~/out/smoke/results.tsv:
#
#   <check id>  PASS|FAIL|SKIP|WARN|NOTE  <what>  <evidence png, if any>
#
# and the whole check is recorded to ~/out/smoke/<check id>.mp4 (RIG_CLIPS=0
# skips that), which run.sh lists in out/smoke/clips.html beside the PNGs.
#
# and run.sh prints the table at the end. A check that dies part-way leaves
# the verdicts it reached, then a FAIL for the die.

export OUT_DIR=$HOME/out/smoke
mkdir -p "$OUT_DIR"
RESULTS=$OUT_DIR/results.tsv
SMOKE_BIN=/opt/smoke-bin

# A roomier frame than the Marketplace stills: the checks read toasts,
# title bars and modal text, and a 1920x1200 window at zoom 1 fits all of it.
export CAP_W=${CAP_W:-1920} CAP_H=${CAP_H:-1200}
export REC_ZOOM=${REC_ZOOM:-1} REC_FONT_SIZE=${REC_FONT_SIZE:-14}
# Every check gets a FRESH profile (globalState, globalStorage): first-run
# behaviour is the point of 01, and no other check may inherit its flags.
export PROFILE=$HOME/.vscode-rec-${CHECK:-smoke}

# prep.sh insists on naut on PATH (it resolves $NAUTILUS for start_controller).
[[ :$PATH: == *":$SMOKE_BIN:"* ]] || export PATH=$SMOKE_BIN:$PATH
# no_naut — PATH without the CLI under test (every copy of the entry).
no_naut() { export PATH=$(tr : "\n" <<<"$PATH" | grep -vx "$SMOKE_BIN" | paste -sd:); }
source "$HOME/fixtures/prep.sh"
# Undo prep.sh's OUT_DIR default if it moved it.
export OUT_DIR=$HOME/out/smoke
# The frame and pace the checks ran at, for run.sh's manifest (lib/manifest.sh).
printf 'CAP_W=%s\nCAP_H=%s\nREC_ZOOM=%s\nREC_FONT_SIZE=%s\nG_PACE=%s\n' \
  "$CAP_W" "$CAP_H" "$REC_ZOOM" "$REC_FONT_SIZE" "${G_PACE:-human}" >"$OUT_DIR/frame.env"

CHECK=${CHECK:-$(basename "$0" .sh)}
export PROFILE=$HOME/.vscode-rec-$CHECK

verdict() { # <PASS|FAIL|SKIP|NOTE> <message> [png basename]
  local png=${3:-} msg=${2//$'\n'/ | }
  [[ -n $png ]] && png="out/smoke/$png.png"
  printf '%s\t%s\t%s\t%s\n' "$CHECK" "$1" "$msg" "$png" >>"$RESULTS"
  case $1 in
    PASS) printf '  \033[32mPASS\033[0m %s\n' "$2" ;;
    FAIL) printf '  \033[31mFAIL\033[0m %s\n' "$2" ;;
    SKIP) printf '  \033[33mSKIP\033[0m %s\n' "$2" ;;
    WARN) printf '  \033[35mWARN\033[0m %s\n' "$2" ;;
    *)    printf '  NOTE %s\n' "$2" ;;
  esac
}
pass() { verdict PASS "$@"; }
fail() { verdict FAIL "$@"; }
skip() { verdict SKIP "$@"; }
info() { verdict NOTE "$@"; }
# warn — a finding that is not what the check set out to test (a papercut,
# or behaviour worth a look) but should reach the report.
warn() { verdict WARN "$@"; }
# check <message> <png|""> <command...> — PASS/FAIL on the command's status.
check() {
  local msg=$1 png=$2; shift 2
  if "$@"; then pass "$msg" "$png"; else fail "$msg" "$png"; fi
}

# A die() from lib.sh mid-check should still land in the table.
trap '_rc=$?; (( _rc )) && fail "check aborted (exit $_rc) — see the log above"; clip_stop; cleanup_capture' EXIT

# The whole check is one review clip, out/smoke/<check>.mp4 (lib.sh's
# clip_start: the root screen, so relaunches and menus are in it too).
# RIG_CLIPS=0 skips it.
clip_start "$CHECK"

# shot <name> — snap, prefixed with the check id so out/smoke sorts by check.
shot() { snap "$CHECK-$1" >/dev/null; echo "$CHECK-$1"; }

# smoke_open <project dir> [file] — VS Code on a project with the smoke
# profile; like prep.sh's ext_open but on an arbitrary dir. Extra settings as
# in ext_open. window.dialogStyle custom: a modal is then drawn IN the window
# (and in the snap), not as a separate GTK window the grab cannot see.
# window.zoomPerWindow false: a window zoom is then written to settings.json,
# which is how check 06 proves the diagram kept Ctrl+= to itself.
smoke_open() {
  local dir=$1 file=${2:-}
  vscode_profile
  local extra='"window.dialogStyle": "custom", "window.zoomPerWindow": false'
  [[ -n ${EXTRA_SETTINGS:-} ]] && extra="$extra, $EXTRA_SETTINGS"
  sed -i "0,/^{/s//{\n  $extra,/" "$PROFILE/User/settings.json"
  launch_vscode "$dir" ${file:+"$dir/$file"}
  echo "$WIN" >"$HOME/.smoke-win"; echo "${REC_OFF:-}" >"$HOME/.smoke-recoff"
}

# The extension's own log channel ("nautilus") and the extension host log,
# from the CURRENT profile's newest session.
ext_log() { cat "$(ls -td "$PROFILE"/logs/*/ | head -1)"window*/exthost/*/*-nautilus.log 2>/dev/null; }
exthost_log() { cat "$(ls -td "$PROFILE"/logs/*/ | head -1)"window*/exthost/exthost.log 2>/dev/null; }

# api <path> — GET the controller, JSON on stdout.
api() { curl -sf --max-time 3 "localhost:$PORT$1"; }
# prog_hash [pou] — the running program's hash from GET /api/program.
prog_hash() {
  api "/api/program${1:+?pou=$1}" | python3 -c 'import sys,json; d=json.load(sys.stdin); print(d.get("hash",""))'
}

# title — the window title (VS Code puts the active editor's name first).
title() { xdotool getwindowname "$WIN" 2>/dev/null; }

# wait_for <seconds> <command...> — poll until the command succeeds.
wait_for() {
  local limit=$1 i; shift
  for ((i = 0; i < limit * 4; i++)); do "$@" && return 0; sleep 0.25; done
  return 1
}

key() { xdotool key --clearmodifiers "$@"; sleep 0.6; }

# md5 of a file, for "did the save reach disk".
sum() { md5sum "$1" | cut -d' ' -f1; }

# gstate — the extension's globalState as JSON.
gstate() {
  python3 - "$PROFILE/User/globalStorage/state.vscdb" <<'PY'
import sqlite3, sys
v = sqlite3.connect(sys.argv[1]).execute("select value from ItemTable where key='joyauto.vscode-iec'").fetchone()
print(v[0] if v else "{}")
PY
}

# text_replace <file> <old> <new> — in the ACTIVE TEXT EDITOR (which must be
# showing <file>), select the first <old> and type <new> over it: an unsaved
# edit, made the way a person makes one. Ctrl+G takes "line:col".
text_replace() {
  local f=$1 old=$2 new=$3 l c i
  l=$(grep -nF -- "$old" "$f" | head -1 | cut -d: -f1)
  c=$(awk -v l="$l" -v s="$old" 'NR==l{print index($0,s)}' "$f")
  xdotool key --clearmodifiers ctrl+g; sleep 0.6
  xdotool type "$l:$c"; sleep 0.3; xdotool key Return; sleep 0.4
  for ((i = 0; i < ${#old}; i++)); do xdotool key shift+Right; done
  xdotool type --delay 30 -- "$new"; sleep 0.6
}

# ── the DOM: how a check finds things and reads verdicts ────────────────────
# DOM FIRST. A check locates what it clicks, and reads what it asserts, from
# the DOM: the workbench page (`cdp page`: notifications, the custom modal,
# editor-title actions, tabs, the status bar) or the active webview (`cdp
# eval`: every diagram element carries data-id / data-kind), or from the
# extension's NAUTILUS_TEST_STATE snapshot. Never from a coordinate measured
# on one frame: VS Code's layout moves under every release and every theme,
# and that is where this suite's flakes came from. Pixels are read only where
# the subject IS a colour (11-themes' luminance), and then inside a box the
# DOM located (snap_box). These build on gestures.sh (cdp, page_el_box,
# click_el, g_click, …), which every check sources after this file.

# _dom_str — a cdp JSON result on stdin, printed raw if a string ("" for null).
_dom_str() { python3 -c 'import json,sys; v=json.loads(sys.stdin.read() or "null"); print("" if v is None else v if isinstance(v, str) else json.dumps(v))' 2>/dev/null || true; }
# pg <js> — evaluate in the workbench page. wv <js> — in the active webview
# (`doc` is its document). Both print a string raw, null as "", and never fail.
pg() { cdp page "$1" 2>/dev/null | _dom_str; }
wv() { cdp eval "$1" 2>/dev/null | _dom_str; }

# page_click <js → Element in the workbench page> [settle] — click its centre.
page_click() {
  local box x y w h
  box=$(page_el_box "$1") || { g_err "not in the workbench: ${1:0:100}…"; return 1; }
  read -r x y w h <<<"$box"
  g_click $((x + w / 2)) $((y + h / 2)) "${2:-0.8}"
}
# page_hover <js → Element in the workbench page> — rest the pointer on it
# until its tooltip shows (the evidence PNG): approach from away, then a
# small wiggle, as lib.sh's hover_at does.
page_hover() {
  local box x y w h _
  box=$(page_el_box "$1") || { g_err "not in the workbench: ${1:0:100}…"; return 1; }
  read -r x y w h <<<"$box"
  x=$((x + w / 2)) y=$((y + h / 2))
  xdotool mousemove --window "$WIN" "$(( x > 300 ? x - 300 : x + 300 ))" "$(( y + 300 ))"; sleep 0.6
  xdotool mousemove --window "$WIN" "$x" "$y"; sleep 0.8
  for _ in 1 2 3; do xdotool mousemove_relative -- 2 0; sleep 0.25; xdotool mousemove_relative -- -2 0; sleep 0.25; done
}

# title_action_el <JS regex source> — the ACTIVE editor group's editor-title
# action whose aria-label (the command's title) matches. Ours, the git
# extension's "Open Changes", Split Editor and "More Actions..." all live
# there, in an order that shifts with the file's git state; by label it
# does not matter.
title_action_el() {
  printf '[...document.querySelectorAll(".editor-group-container.active .editor-actions .action-label")].find(a => /%s/.test(a.getAttribute("aria-label") || "") && a.getBoundingClientRect().width > 0)' "$1"
}

# notifications — every notification on screen, toasts and the centre, one
# per line: <severity>\t<message>\t<button / button …> (severity is the
# icon's codicon: error, warning or info).
notifications() {
  pg '[...document.querySelectorAll(".notification-list-item")].filter(e => e.getBoundingClientRect().height > 0).map(e => { const ic = e.querySelector(".notification-list-item-icon"); const sev = ic ? ((ic.className.match(/codicon-(error|warning|info)\b/) || [])[1] || "?") : "?"; return sev + "\t" + (e.querySelector(".notification-list-item-message")?.textContent.trim() || "") + "\t" + [...e.querySelectorAll(".notification-list-item-buttons-container .monaco-button")].map(b => b.textContent.trim()).join(" / "); }).join("\n")'
}
# notification <ERE> — the first notifications line whose MESSAGE matches;
# status 1 if none does.
notification() {
  local l; l=$(notifications | awk -F'\t' -v re="$1" '$2 ~ re { print; exit }')
  [[ -n $l ]] && echo "$l"
}

# dialog_message / dialog_buttons — the custom modal's text, and its buttons
# ("A / B / C"). window.dialogStyle is custom in the smoke profile, so the
# modal is workbench DOM (gestures.sh's dialog_up says whether one is up).
dialog_message() { pg '[...document.querySelectorAll(".monaco-dialog-box .dialog-message-row")].map(e => e.textContent.trim()).join(" ")'; }
dialog_buttons() { pg '[...document.querySelectorAll(".monaco-dialog-box .dialog-buttons .monaco-button")].map(b => b.textContent.trim()).join(" / ")'; }

# DIAGRAM_CANVAS — the active diagram's canvas: FBD's xyflow pane, or the
# Ladder / SFC ZoomPane's scroller.
DIAGRAM_CANVAS='doc.querySelector(".svelte-flow__pane, .zpane .flow")'
# canvas_spot [js → canvas element] — an EMPTY point of a diagram canvas in
# the active webview, as window coordinates "x y": the first point of a grid
# over the canvas (from its bottom-right, where the layouts leave room) whose
# top element is the canvas itself or plain background — not a node, step,
# rung, edge, pin, panel, zoom control or form. Status 1 if there is none.
canvas_spot() {
  cdp point "(() => {
    const c = (${1:-$DIAGRAM_CANVAS}); if (!c) return null;
    const r = c.getBoundingClientRect();
    const busy = 'g.node, g.step, g.trans, g.jump, g.note, g.spot, g.orphan, svg.rsvg, .svelte-flow__node, .svelte-flow__edge, .svelte-flow__panel, .svelte-flow__handle, .zctl, .addform, button, input, select, a';
    for (let fy = 0.92; fy > 0.04; fy -= 0.04)
      for (let fx = 0.92; fx > 0.04; fx -= 0.04) {
        const x = r.left + r.width * fx, y = r.top + r.height * fy;
        if (x < 1 || y < 1 || x > win.innerWidth - 1 || y > win.innerHeight - 1) continue;
        const h = doc.elementFromPoint(x, y);
        if (h && (h === c || c.contains(h)) && !h.closest(busy)) return { x, y };
      }
    return null;
  })()" 2>/dev/null
}
# click_canvas [js → canvas element] [settle] — click an empty spot of it
# (focus in the diagram, nothing selected).
click_canvas() {
  local p; p=$(canvas_spot "${1:-}") || { g_err "no empty spot on the diagram canvas"; return 1; }
  g_click $p "${2:-0.6}"
}

# snap_box <"x y w h" in window coordinates> — the same box in SNAP
# coordinates (a PNG from shot) as "x0 y0 x1 y1": the grab starts inside
# launch_vscode's CSD margin, so snap = window − the left/top frame extents.
snap_box() {
  local x y w h l t
  read -r x y w h _ <<<"$1"; read -r l t <<<"$(_g_frame)"
  echo "$((x - l)) $((y - t)) $((x - l + w)) $((y - t + h))"
}
