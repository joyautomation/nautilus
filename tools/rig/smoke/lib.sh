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
trap '_rc=$?; (( _rc )) && fail "check aborted (exit $_rc) — see the log above"; cleanup_capture' EXIT

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

# click_at <x> <y> — a plain left click at window coords (DOM buttons: toasts,
# the diagram canvas; NOT context-menu items, which need ydotool — lib.sh).
#
# Coordinates are SNAP coordinates (what you measure on a PNG from shot):
# the grab starts inside launch_vscode's CSD margin, so window coords are
# snap + the left/top frame extents.
_frame() { xprop -id "$WIN" _GTK_FRAME_EXTENTS 2>/dev/null | grep -oE '[0-9]+(, [0-9]+){3}' || echo "0, 0, 0, 0"; }
to_win() { local l r t b; IFS=', ' read -r l r t b <<<"$(_frame)"; echo "$(($1 + l)) $(($2 + t))"; }
move_to() { xdotool mousemove --window "$WIN" $(to_win "$1" "$2"); sleep 0.3; }
click_at() { move_to "$1" "$2"; xdotool click 1; sleep "${3:-0.8}"; }
dclick_at() { move_to "$1" "$2"; xdotool click --repeat 2 --delay 80 1; sleep "${3:-0.8}"; }

# ld_palette_x <label> — the Ladder element palette's button CENTRES (SNAP x,
# 1920-wide capture). Measured 2026-09-25 against the layout PR #47 left
# behind: ⊣ ⊢ · ⊣/⊢ · FN( ) · FB… · [ | ] · ( ) · (S) · (R) | // · + rung |
# ✂ ⧉ ⎘ ✕ (LadderView.svelte's PALETTE array, then the toolbar's own // and
# + rung buttons). #47 replaced the old separate TON/CTU buttons with the
# single FB… button, which is what shifted every button after FN( ) about
# 57px to the left and left 09-empty-files' hard-coded "+ rung" click on the
# gap after it — the reason this table exists instead of another hard-code.
# The row's Y is context, not label, so it is NOT in this table: y≈132 on an
# already-populated .ld (10-l5x.sh) and y≈168 when the Empty-file banner is
# showing above it (09-empty-files.sh) — pass whichever Y applies to click_at.
ld_palette_x() {
  case $1 in
    '⊣ ⊢')     echo 63  ;;
    '⊣/⊢')     echo 113 ;;
    'FN( )')   echo 170 ;;
    'FB…')     echo 228 ;;
    '[ | ]')   echo 286 ;;
    '( )')     echo 344 ;;
    '(S)')     echo 393 ;;
    '(R)')     echo 443 ;;
    '//')      echo 505 ;;
    '+ rung')  echo 563 ;;
    *) echo "ld_palette_x: no button '$1' — re-measure the row (see the comment above) and add it" >&2; return 1 ;;
  esac
}
key() { xdotool key --clearmodifiers "$@"; sleep 0.6; }

# banner_button_x <png basename> <row y> — the SNAP x centre of the button
# on a one-line banner row (09's Empty-file "initialize"), found by SIGHT in
# a frame of that row rather than from a coordinate table: the banner's text
# runs up to the button, so its x moves with the text — the file name in it
# ("PROGRAM seq" vs "PROGRAM alarms") — and with every left inset (PR #50's
# html/body reset dropped VS Code's 20px webview body padding, which moved
# the SFC button off 09's old hard-coded x=691). The button is the one thing
# on the row drawn as a rectangle OUTLINE: a top and a bottom border row of
# the same span, 12–34px apart, joined by lit left and right edge columns
# (a few px outside that span — the corners are rounded);
# text never makes that. Prints the rightmost such box's centre (the button
# follows the text); status 1 and a note on stderr if there is none.
banner_button_x() {
  python3 - "$OUT_DIR/$1.png" "$2" <<'PY'
import sys
from PIL import Image
im = Image.open(sys.argv[1]).convert("RGB"); px = im.load()
y0 = int(sys.argv[2]); W = min(im.size[0], 1400)
def lit(x, y, bg):
    p = px[x, y]
    return max(abs(p[i] - bg[i]) for i in range(3)) > 12
rows = {}
for y in range(y0 - 18, y0 + 19):
    bg = px[4, y]; s = None; runs = []
    for x in range(W + 1):
        on = x < W and lit(x, y, bg)
        if on and s is None:
            s = x
        elif not on and s is not None:
            if 30 <= x - s <= 240: runs.append((s, x - 1))
            s = None
    rows[y] = (bg, runs)
best = None
for ya in rows:
    for yb in rows:
        if not 12 <= yb - ya <= 34: continue
        bg = rows[ya][0]
        edge = lambda x: sum(lit(x, y, bg) for y in range(ya, yb + 1)) >= 0.8 * (yb - ya)
        for a in rows[ya][1]:
            for b in rows[yb][1]:
                if abs(a[0] - b[0]) > 1 or abs(a[1] - b[1]) > 1: continue
                # Rounded corners: the side edges stand a few px outside
                # the border rows' flat run.
                l = next((x for x in range(a[0] - 5, a[0] + 2) if edge(x)), None)
                r = next((x for x in range(a[1] + 5, a[1] - 2, -1) if edge(x)), None)
                if l is not None and r is not None and (best is None or l > best[0]):
                    best = (l, r)
if best is None:
    sys.exit("banner_button_x: no outlined button on row %d of %s" % (y0, sys.argv[1]))
print((best[0] + best[1]) // 2)
PY
}

# find_text_png <png> <regex> — nothing cheap reads text off a frame in the
# container (no OCR), so checks that need to FIND a button use known layout
# plus the logs, and the PNG is the human-readable evidence.

# md5 of a file, for "did the save reach disk".
sum() { md5sum "$1" | cut -d' ' -f1; }

# The recording profile hides the activity bar and the side bar is on the
# left: smoke checks want the side bar hidden too, so the diagram gets the width.

# px_count <png basename> <x0> <y0> <x1> <y1> <python cond over r,g,b> — how
# many pixels in the box satisfy the condition. The cheap way to ask a frame
# "is the amber warning icon there", "did the pill paint", with no OCR.
px_count() {
  python3 - "$OUT_DIR/$1.png" "$2" "$3" "$4" "$5" "$6" <<'PY'
import sys
from PIL import Image
im = Image.open(sys.argv[1]).convert("RGB"); px = im.load()
x0, y0, x1, y1 = map(int, sys.argv[2:6]); cond = eval("lambda r, g, b: " + sys.argv[6])
print(sum(1 for y in range(y0, min(y1, im.size[1])) for x in range(x0, min(x1, im.size[0])) if cond(*px[x, y])))
PY
}
# The notification centre's region at 1920x1200 (bottom-right), and VS Code's
# warning-icon amber in Night Owl.
NOTIF_BOX="1380 900 1410 1175"   # the icon column of the centre's rows
AMBER='r > 150 and 110 < g < 175 and b < 110 and r - b > 60'

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

# hover <x> <y> — lib.sh's hover_at, in snap coordinates, and safe under
# set -u (hover_at's `local x=$1 … ax=${3:-$((x …))}` reads x before it is
# assigned, which set -u rejects).
hover() {
  move_to "$(( $1 > 300 ? $1 - 300 : $1 + 300 ))" "$(( $2 + 300 ))"; sleep 0.6
  move_to "$1" "$2"; sleep 0.8
  local _
  for _ in 1 2 3; do xdotool mousemove_relative -- 2 0; sleep 0.25; xdotool mousemove_relative -- -2 0; sleep 0.25; done
}
