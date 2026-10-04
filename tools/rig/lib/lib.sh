# Shared plumbing for the desktop-captured beats (VS Code, and the browser
# when a cursor is wanted). Sourced, not executed.
#
# THE ONE THING THAT MAKES THIS WORK: capture mira1's LOCAL X session, not the
# SSH-forwarded display. `DISPLAY=localhost:10.0` renders on the laptop you
# ssh'd from, so there is no framebuffer on mira1 to read and X_GetImage fails
# even on the root window. `:1` is the real session and has one.
#
# Two consequences worth knowing:
#   - `x11grab -window_id` reads the compositor's offscreen pixmap, so a window
#     can be captured even while it is obscured or the screen is locked.
#   - INPUT is different: xdotool goes through XTEST, which the lock screen's
#     keyboard grab swallows. The session must be UNLOCKED to drive anything.
#     Capture-while-locked works; typing-while-locked does not.

# gdm starts Xorg with -displayfd, so the number is whatever was free at boot:
# :1 for most of the series, :0 after the 2026-08 reboot. When exactly one X
# socket exists, that is the session; the hard-coded :1 is only the fallback.
_xsock=$(ls /tmp/.X11-unix/ 2>/dev/null | grep -c '^X[0-9]*$')
if [[ -z ${CAPTURE_DISPLAY:-} && $_xsock == 1 ]]; then
  CAPTURE_DISPLAY=":$(ls /tmp/.X11-unix/ | grep -o '^X[0-9]*$' | tr -d X)"
fi
unset _xsock
export DISPLAY=${CAPTURE_DISPLAY:-:1}
# XAUTHORITY names gdm's cookie for mira1's :1. A rig that brings its own X
# server — the container's Xvfb, which is started with no auth at all — has no
# gdm and no cookie, and pointing a client at a file that is not there is not
# the same as not needing one. Default it only if it is actually readable.
_xauth=${XAUTHORITY:-/run/user/1000/gdm/Xauthority}
if [[ -r $_xauth ]]; then export XAUTHORITY=$_xauth; else unset XAUTHORITY; fi
unset _xauth

CAP_W=${CAP_W:-2560}
CAP_H=${CAP_H:-1440}
# The CALLER owns out/, not this file's neighbour: lib.sh lives in the
# nautilus repo (tools/rig/lib) and is shared by the smoke suite, the verb
# self-test and every content episode, so resolving output relative to ITSELF
# would drop everything into tools/rig/lib/out.
#
# So the caller says. The container rig copies THIS FILE into a throwaway
# container that has no repo in it and hands it OUT_DIR directly; a content
# beat run on mira1's display gets OUT_DIR from the content repo's
# lib/rig.sh (its episode's out/). Running the same helpers everywhere is the
# point: the look of a VS Code take comes from vscode_profile() and
# launch_vscode(), so a second implementation is a second look, and the cut
# would show the seam wherever it spliced between them. A caller that says
# nothing gets ./out, with a note.
if [[ -z ${OUT_DIR:-} ]]; then
  OUT_DIR=$PWD/out
  echo "lib.sh: OUT_DIR not set — writing to $OUT_DIR" >&2
fi
PROFILE=${PROFILE:-/tmp/vscode-rec}
mkdir -p "$OUT_DIR"

die() { echo "error: $*" >&2; exit 1; }
note() { printf '\033[1m▸ %s\033[0m\n' "$*"; }

# ── Real input via ydotool (for context menus) ───────────────────────────
#
# xdotool drives everything through XTEST, and a VS Code context-menu item
# CANNOT be activated that way: the menu is an Electron overlay whose item
# activates on a real button event, and XTEST's synthetic click is swallowed
# (the menu opens and the item highlights, but nothing fires — proven six
# ways 2026-08-22). ydotool injects at the kernel uinput layer, so its click
# carries the real button event the overlay accepts.
#
# The reliable pattern is HYBRID: position with xdotool (a warp highlights
# the item precisely — ydotool's own relative moves are mangled by pointer
# acceleration), then fire the activating click with ydotool. Verified: warp
# onto "Set Live Value" + `yd_click` sets the tag; XTEST click does not.
#
# ydotool needs /dev/uinput writable (it is, world-rw on mira1); no daemon
# (0.1.8 ships none — direct mode, the stderr "backend unavailable" notice is
# harmless). Without ydotool installed, yd_click falls back to xdotool with a
# warning, so a rig lacking it still runs — it just can't hit menu items.
YDOTOOL=${YDOTOOL:-$(command -v ydotool || true)}

# yd_click [button] — a REAL click at the current pointer (default 1=left).
# ydotool buttons: 1 left, 2 right, 3 middle.
yd_click() {
  if [[ -x $YDOTOOL ]]; then
    "$YDOTOOL" click "${1:-1}" 2>/dev/null
  else
    note "ydotool absent — xdotool click (will NOT activate a context menu)"
    xdotool click "${1:-1}"
  fi
}

# menu_pick <window_x> <window_y> — activate a context-menu item at those
# window coords: xdotool warp highlights it, ydotool click activates it.
# (Open the menu first with an xdotool right-click; ydotool right-click is
# unnecessary — XTEST opens menus fine, it only can't select in them.)
menu_pick() {
  xdotool mousemove --window "$WIN" "$1" "$2"
  sleep 0.5
  yd_click 1
  sleep 0.4
}

# check_theme fails fast if the theme extension is absent. VS Code does not
# error on an unknown colorTheme — it quietly uses the default, and you find
# out when the footage does not match the rest of the series.
check_theme() {
  local want=${REC_THEME:-Night Owl}
  [[ $want == "Default Dark Modern" ]] && return 0
  code --list-extensions 2>/dev/null | grep -qi "night-owl" \
    || die "theme '$want' needs the sdras.night-owl extension — install it, or set REC_THEME='Default Dark Modern'"
}

require_unlocked() {
  local sid
  sid=$(loginctl list-sessions --no-legend 2>/dev/null | awk '$4=="seat0"{print $1; exit}')
  [[ -n $sid ]] || return 0
  if [[ $(loginctl show-session "$sid" -p LockedHint --value 2>/dev/null) == yes ]]; then
    die "session $sid is locked — xdotool input is swallowed by the lock screen's keyboard grab.
       Unlock mira1 (or: loginctl unlock-session $sid) and re-run. Capture works locked; typing does not."
  fi
}

# vscode_profile writes the recording profile. Deliberately NOT your daily
# settings: bigger type, no minimap, no chat panel, no command centre.
vscode_profile() {
  mkdir -p "$PROFILE/User"
  cat > "$PROFILE/User/settings.json" <<EOF
{
  // Night Owl, to match the branding in assets/. Installed as sdras.night-owl;
  // if the theme is missing VS Code falls back to its default dark silently,
  // so check_theme() below fails the run instead of recording the wrong look.
  "workbench.colorTheme": "${REC_THEME:-Night Owl}",
  "editor.fontSize": ${REC_FONT_SIZE:-24},
  // window.zoomLevel scales the WHOLE UI — tabs, sidebar, status bar, hover
  // tooltips, the Problems panel. editor.fontSize alone only grows the code,
  // leaving 11px chrome around it that is unreadable once YouTube downscales
  // 1440p and someone watches on a phone. Each step is 20%.
  "window.zoomLevel": ${REC_ZOOM:-1.5},
  "editor.lineHeight": 1.6,
  "editor.minimap.enabled": false,
  "workbench.startupEditor": "none",
  "workbench.activityBar.location": "hidden",
  "breadcrumbs.enabled": false,
  "window.commandCenter": false,
  "chat.commandCenter.enabled": false,
  // VS Code 1.127 (2026-09) opens the secondary side bar (Chat) in a fresh
  // profile by default. It steals a third of the frame and shifts every
  // split-layout fitView, so the drag anchors measured in August miss —
  // wk04's re-cut open landed a drag on empty canvas before this was pinned.
  "workbench.secondarySideBar.defaultVisibility": "hidden",
  // VS Code 1.139 (2026-09) turns on "modern UI" by default: the workbench
  // becomes rounded, inset cards with a frame around the whole window, and
  // every measured coordinate moves ~10 px (the ext-stable FBD still zoomed
  // onto empty canvas the first time it ran on a rebuilt rig). Off, so new
  // takes match the series' existing ones.
  "workbench.experimental.modernUI": false,
  "window.menuBarVisibility": "hidden",
  "security.workspace.trust.enabled": false,
  "telemetry.telemetryLevel": "off",
  "update.mode": "none",
  "editor.cursorBlinking": "solid",
  "workbench.tips.enabled": false,
  "workbench.editor.enablePreview": false,
  // Session restore is why the quick-open guard was vacuous: VS Code reopened
  // program.st by itself, so the window title already matched and the check
  // passed without Ctrl+P ever working. Start clean, every time.
  "window.restoreWindows": "none",
  // Hot exit is why one corrupted take poisoned every run after it. VS Code
  // saves DIRTY BUFFERS to $PROFILE/Backups and restores them next launch —
  // over a freshly scaffolded file, so the project on disk is pristine and
  // the editor still shows last run's damage. It survived deleting
  // workspaceStorage, and it survived removing the keystroke that caused the
  // original corruption, which is what made it look impossible.
  "files.hotExit": "off",
  "workbench.welcomePage.walkthroughs.openOnInstall": false,
  // The "Welcome to VS Code / Sign in to use GitHub Copilot" dialog is shipped
  // in the product, not by an extension, so --disable-extension does not touch
  // it. This setting asks for it not to appear; dismiss_first_run() in
  // launch_vscode() is what actually guarantees it, because the setting's name
  // has changed twice and a take is not the place to find out it changed again.
  "chat.disableAIFeatures": true,
  "workbench.remoteIndicator.showExtensionRecommendations": false
}
EOF
}

# point_extension_at rewrites the WORKSPACE runtime URL.
#
# This is not optional and it is not obvious: `nautilus new` writes
# .vscode/settings.json with "nautilus.runtimeUrl": "http://localhost:8080",
# and workspace settings beat user settings. Leave it and the extension
# happily connects to whatever else is on 8080 — it reports "nautilus: live",
# says "program differs", and shows no live values, which looks like the
# extension being broken rather than pointed somewhere else.
point_extension_at() {
  local dir=$1 port=$2
  [[ -f $dir/.vscode/settings.json ]] || return 0
  sed -i "s|\"nautilus.runtimeUrl\": \"http://localhost:[0-9]*\"|\"nautilus.runtimeUrl\": \"http://localhost:$port\"|" \
    "$dir/.vscode/settings.json"
}

free_port() {
  local p
  for p in "$@"; do
    python3 -c "import socket,sys
s=socket.socket()
try: s.bind(('127.0.0.1',$p)); s.close()
except OSError: sys.exit(1)" 2>/dev/null && { echo "$p"; return; }
  done
  die "none of these ports are free: $*"
}

start_controller() { # <project dir> <port>
  ( cd "$1" && NAUTILUS_ADDR=localhost:$2 exec "$NAUTILUS" run >/tmp/capture-controller.log 2>&1 ) &
  CONTROLLER_PID=$!
  local i
  for i in $(seq 60); do
    curl -sf --max-time 1 "localhost:$2/api/state" >/dev/null 2>&1 && return 0
    sleep 0.25
  done
  die "controller never came up on $2"
}

# launch_vscode opens a window and returns its id in $WIN.
#
# setsid, because a plain `nohup ... &` from a tool-call shell dies with the
# call and you get no window at all. And kill by the PROFILE path, not by a
# flag string: VS Code reorders its own argv, so `pkill -f "user-data-dir
# /tmp/vscode-rec"` matches nothing and the old instance survives — after
# which a new `code` invocation just attaches to it and your settings change
# appears not to have worked.
# unmaximize_window sends the EWMH _NET_WM_STATE remove message — what
# wmctrl would send, but wmctrl is not installed on mira1 and xdotool's
# windowstate needs a newer xdotool than 3.2016. python-xlib is present.
unmaximize_window() { # <window id>
  # The container rig has wmctrl and no python-xlib (and openbox does not
  # auto-maximize anyway) — use what is there rather than dying on an import.
  if ! python3 -c 'import Xlib' 2>/dev/null; then
    command -v wmctrl >/dev/null && wmctrl -i -r "$1" -b remove,maximized_vert,maximized_horz 2>/dev/null
    return 0
  fi
  python3 - "$1" <<'PYEOF'
import sys
from Xlib import X, display
from Xlib.protocol import event
d = display.Display(); root = d.screen().root
w = d.create_resource_object('window', int(sys.argv[1]))
a = d.intern_atom
ev = event.ClientMessage(window=w, client_type=a('_NET_WM_STATE'),
    data=(32, [0, a('_NET_WM_STATE_MAXIMIZED_HORZ'), a('_NET_WM_STATE_MAXIMIZED_VERT'), 1, 0]))
root.send_event(ev, event_mask=X.SubstructureRedirectMask | X.SubstructureNotifyMask)
d.flush(); d.sync()
PYEOF
}

# ensure_work_area makes room for a CAP_WxCAP_H window. mutter clamps every
# window to the work area, and under the 32 px top bar on the 3440x1440
# display that is 1408 tall — 32 short of the master, and x11grab then fails
# with "Capture area 2560x1440 ... outside the screen size 2560x1408" (the
# take that found this: wk03 beat 4, 2026-08-27; the 08-17 takes had a
# different display arrangement). `xrandr --fb` is silently reverted by
# mutter; `--panning` is honoured and grows the work area by the deficit.
# The strip below the glass is never seen, but the window's PIXMAP is what
# -window_id reads, so the take is whole. cleanup_capture puts it back.
ensure_work_area() { # [needed height, default CAP_H — Chrome adds its CSD margin]
  local need=${1:-$CAP_H} wa_h out scr_w scr_h
  wa_h=$(xprop -root _NET_WORKAREA 2>/dev/null | grep -oE '[0-9]+' | sed -n 4p)
  [[ -n $wa_h ]] || return 0                       # no EWMH WM (Xvfb rig): nothing clamps
  (( wa_h < need )) || return 0
  out=$(xrandr --current 2>/dev/null | awk '/ connected primary/{print $1; exit}')
  [[ -n $out ]] || out=$(xrandr --current 2>/dev/null | awk '/ connected/{print $1; exit}')
  read -r scr_w scr_h <<<"$(xrandr --current | sed -n 's/.*current \([0-9]*\) x \([0-9]*\).*/\1 \2/p')"
  [[ -n $out && -n $scr_h ]] || die "work area is only ${wa_h} tall and xrandr can't tell me the output to pan"
  PAN_OUT=$out
  xrandr --output "$out" --panning "${scr_w}x$((scr_h + need - wa_h))"
  sleep 1.5
  note "work area was ${wa_h} tall — panning $out to $((scr_h + need - wa_h)) for the take (restored on exit)"
}

restore_work_area() {
  [[ -n ${PAN_OUT:-} ]] || return 0
  xrandr --output "$PAN_OUT" --panning 0x0 2>/dev/null || true
  PAN_OUT=
}

launch_vscode() { # <project dir> [file to open]
  # `|| true` is load-bearing under `set -e`: pkill exits 1 when it matches
  # nothing, which is the NORMAL case on a rig that has never run a take. On
  # mira1 a previous take's process was always still around, so this only ever
  # failed in the container — and it failed by killing the beat script before
  # the first frame, with no output and exit 1.
  pkill -9 -f "vscode-rec" 2>/dev/null || true
  sleep 3
  # Clear the per-workspace state (which editors were open) but NOT
  # globalStorage/state.vscdb — that holds the first-run flags, and deleting it
  # brings back a full-screen "Welcome to VS Code / Sign in to use GitHub
  # Copilot" modal that covers the entire take. `window.restoreWindows: none`
  # already does the job the deletion was for.
  rm -rf "$PROFILE/User/workspaceStorage" "$PROFILE/Backups" 2>/dev/null
  # Open the file on the COMMAND LINE rather than with Ctrl+P. Quick-open is a
  # race — if anything holds focus the chord is swallowed and the filename is
  # typed into the document — and it corrupted two full takes before this.
  # There is no chord to lose here.
  # Chat/AI extensions put panels and modals in frame and have nothing to do
  # with the beat. Disabled per-launch rather than uninstalled, so your daily
  # setup is untouched.
  # Every extension except the ones the frame needs. Naming the offenders
  # one by one lost a take on 2026-09-18: the recording profile lives in
  # /tmp, a reboot emptied it, and Deno's first-activation welcome page
  # opened itself over wk04 beat 3 the moment nautilus.yaml opened. The
  # allowlist is what is visible on camera — the editor, the theme, YAML
  # colouring for the manifest, and the two status-bar/title items that
  # every earlier take already shows (Prettier, Claude Code).
  local ext disable=()
  for ext in $(code --list-extensions 2>/dev/null); do
    case ${ext,,} in
      joyauto.vscode-iec|sdras.night-owl|esbenp.prettier-vscode|redhat.vscode-yaml|anthropic.claude-code) ;;
      *) disable+=(--disable-extension "$ext") ;;
    esac
  done
  setsid code --user-data-dir="$PROFILE" --new-window --disable-workspace-trust \
    "${disable[@]}" "$1" ${2:+"$2"} \
    </dev/null >/tmp/capture-vscode.log 2>&1 &
  disown
  local i n st
  for i in $(seq 90); do
    for w in $(xdotool search --class code 2>/dev/null); do
      n=$(xdotool getwindowname "$w" 2>/dev/null)
      st=$(xwininfo -id "$w" 2>/dev/null | grep -oP 'Map State: \K\w+')
      if [[ $st == IsViewable && $n == *"$(basename "$1")"* ]]; then WIN=$w; break 2; fi
    done
    sleep 1
  done
  [[ -n ${WIN:-} ]] || die "VS Code window never appeared"
  xdotool windowactivate --sync "$WIN"; sleep 1
  # mutter auto-maximizes a new window that covers most of the work area —
  # on the 3440-wide display VS Code's default size qualifies — and a
  # maximized window silently ignores windowsize. The grab then fails with
  # "Capture area 2560x1440 ... outside the screen size 3374x1408", which is
  # x11grab's wording for the WINDOW being the wrong size. Unmaximize first,
  # then insist on the geometry: a take at the wrong size is a wasted take.
  unmaximize_window "$WIN"
  ensure_work_area
  sleep 0.5
  # VS Code 1.139's Electron draws client-side shadow margins on Linux
  # (_GTK_FRAME_EXTENTS 4,4,0,4 in the container rig), which a -window_id
  # grab records as a dark strip down both sides and along the bottom.
  # Same cure as Chrome's (see gtk_frame_offset): grow the window by the
  # margins and grab from inside them, so the CONTENT is CAP_WxCAP_H.
  local fl=0 fr=0 ft=0 fb=0 ext
  ext=$(xprop -id "$WIN" _GTK_FRAME_EXTENTS 2>/dev/null | grep -oE '[0-9]+(, [0-9]+){3}') || true
  [[ -n $ext ]] && IFS=', ' read -r fl fr ft fb <<<"$ext"
  local ww=$((CAP_W + fl + fr)) wh=$((CAP_H + ft + fb))
  (( wh == CAP_H )) || ensure_work_area "$wh"
  xdotool windowsize "$WIN" "$ww" "$wh"; xdotool windowmove "$WIN" 0 0
  sleep 1
  if (( fl || ft )); then REC_OFF="+$fl,$ft"; else REC_OFF=${REC_OFF:-}; fi
  local geo
  geo=$(xdotool getwindowgeometry --shell "$WIN" 2>/dev/null)
  [[ $geo == *"WIDTH=$ww"* && $geo == *"HEIGHT=$wh"* ]] \
    || die "VS Code window is not ${ww}x${wh} after resize ($(tr '\n' ' ' <<<"$geo"))"
  sleep 5   # let the layout settle before anything is recorded
  dismiss_first_run
}

# dismiss_first_run clears the modal a never-used profile opens with.
#
# On a profile VS Code has seen before there is nothing here to do, which is
# why this went unnoticed for the whole series: mira1's recording profile
# answered the sign-in dialog once, months ago, and has been quiet since. The
# container has no such history, so the first take out of it was a correct
# recording of the extension-recommendation toast with a "Welcome to VS Code /
# Sign in to use GitHub Copilot" dialog dimming the whole window on top of it.
#
# Escape, twice, with a settle in between: the dialog takes one, and a second
# is harmless once it is gone — nothing else is focused and the editor ignores
# it. It happens BEFORE rec_start, so the dismissal is never in frame.
#
# It must not touch the notification toasts. It does not: a toast never holds
# keyboard focus, so Escape goes to the dialog or nowhere.
dismiss_first_run() {
  :
}

# hover_at parks the pointer on something and makes VS Code notice.
#
# A single mousemove lands the pointer exactly where you aimed and raises NO
# tooltip: VS Code shows hovers on MOTION, so one teleport gives you a pointer
# sitting on the target with nothing to show for it — which reads as the hover
# being broken rather than as never having been asked for. Approach from
# somewhere else, then jiggle.
#
# Learned on beat 4b's squiggle and re-learned on beat 5a's status bar, which
# is what this function is for. --window, NOT screen coordinates: x11grab
# captures the window's own contents but mousemove without --window moves in
# screen space, and the window sits under a title bar. The first attempt at the
# squiggle landed 24px high, in the line-number gutter.
hover_at() { # <x> <y> [approach x] [approach y]
  local x=$1 y=$2 ax=${3:-$((x > 300 ? x - 300 : x + 300))} ay=${4:-$((y > 300 ? y - 300 : y + 300))}
  xdotool mousemove --window "$WIN" "$ax" "$ay"; sleep 1
  xdotool mousemove --window "$WIN" "$x" "$y"; sleep 1
  local _
  for _ in 1 2 3; do
    xdotool mousemove_relative -- 2 0; sleep 0.25
    xdotool mousemove_relative -- -2 0; sleep 0.25
  done
}

# open_file opens a file by quick-open, and VERIFIES it happened.
#
# Ctrl+P is a race. If anything else holds focus — a notification, a leftover
# palette, a webview still booting — the chord is swallowed and the filename
# you type next goes straight into the DOCUMENT. That produced a full-length,
# entirely plausible take whose line 14 read:
#
#     err := Setpoprogram.st
#     nt - Sensor;
#
# Nothing failed. The script ran to completion and wrote an mp4. So: Escape
# first to clear focus, then confirm the window title actually changed, and
# retry rather than typing into whatever happens to be focused.
open_file() { # <basename> <settle seconds>
  local want=$1 settle=${2:-3} try
  for try in 1 2 3; do
    xdotool key --clearmodifiers Escape; sleep 0.6
    xdotool key --clearmodifiers ctrl+p; sleep 1.4
    xdotool type --delay 50 "$want"; sleep 1.6
    xdotool key Return; sleep "$settle"
    [[ $(xdotool getwindowname "$WIN" 2>/dev/null) == *"$want"* ]] && return 0
    printf '  retry %d: quick-open did not land on %s\n' "$try" "$want" >&2
    xdotool key --clearmodifiers ctrl+z; sleep 1   # undo anything typed into the buffer
  done
  die "could not open $want — quick-open never took focus"
}

# assert_file_matches guards against the failure above surviving into the
# output: compare the buffer on disk with what the beat expects.
# snapshot_project records the pristine files so the take can be checked
# byte-for-byte afterwards.
snapshot_project() { # <dir>
  SNAPSHOT_DIR=$(mktemp -d)
  cp -r "$1/." "$SNAPSHOT_DIR/" 2>/dev/null
}

# assert_project_unchanged diffs the project against its snapshot.
#
# A grep-for-two-known-lines assert is not enough: it passes while the file has
# junk somewhere else. A swallowed Ctrl+Shift+P put the literal text
# "Clear All Notifications" on LINE 1 of program.st, shifting every line by
# one, and the presence check waved it through — the recording showed the typo
# landing on the wrong character.
assert_project_unchanged() { # <dir> [files to ignore]
  xdotool key --clearmodifiers ctrl+s; sleep 2
  local d
  d=$(diff -r -q "$SNAPSHOT_DIR" "$1" 2>/dev/null | grep -v "\.vscode\|Only in" || true)
  [[ -z $d ]] || die "the take modified the project:
$d
       (diff -r $SNAPSHOT_DIR $1)"
}

# assert_buffer_clean saves the editor first, THEN checks disk.
#
# Checking disk without saving is worthless for a beat that never saves: the
# buffer can be full of stray text while the file is pristine, and the assert
# passes on a take that is visibly wrong. Ask for the save, then compare.
assert_buffer_clean() { # <path> <pattern that must be present> ...
  xdotool key --clearmodifiers ctrl+s; sleep 2
  local path=$1; shift
  local pat
  for pat in "$@"; do
    grep -q -- "$pat" "$path" || die "the take corrupted the buffer — $path lost: $pat"
  done
}

# close_all_editors, via the chord rather than the palette — no focus race.
#
# Each "Open ... Diagram Preview" opens a NEW pane beside the last, so opening
# FBD then LD then SFC leaves three panes fighting over the width: the SFC
# chart ends up in the right third with steps clipped off the edge. Close
# between languages and each one gets a clean half.
close_all_editors() {
  xdotool key --clearmodifiers Escape; sleep 0.4
  xdotool key --clearmodifiers ctrl+k; sleep 0.4
  xdotool key --clearmodifiers ctrl+w; sleep 1.5
}

vs_cmd() { # run a command-palette command
  # Escape first. If anything holds focus the chord is swallowed and the
  # command NAME is typed into the document — see assert_project_unchanged.
  # Re-activate the take's window first: on mira1 a person at the desk can
  # click a terminal during the warm-up, and then the whole palette sequence
  # lands in THEIR window (wk04 re-cut, 2026-09-16: the command name arrived
  # in the chat prompt and the diagram never opened).
  [[ -n ${WIN:-} ]] && { xdotool windowactivate --sync "$WIN" 2>/dev/null || true; sleep 0.3; }
  xdotool key --clearmodifiers Escape; sleep 0.5
  xdotool key --clearmodifiers ctrl+shift+p; sleep 1.4
  xdotool type --delay 40 "$1"; sleep 1.4
  xdotool key Return; sleep "${2:-2}"
}

# find_squiggle locates the red diagnostic underline by scanning a live frame,
# and echoes "<x> <y>" in WINDOW coordinates.
#
# The hover target used to be hard-coded (420,540 at fontSize 22). That is
# exactly the kind of constant that rots silently: bump the zoom and the
# pointer lands in the gutter, the tooltip never opens, and the take looks
# like the hover feature is broken. Measure it at run time instead.
find_squiggle() {
  local shot; shot=$(mktemp --suffix=.png)
  ffmpeg -hide_banner -loglevel error -f x11grab -draw_mouse 0 -window_id "$WIN" \
    -video_size "${CAP_W}x${CAP_H}" -i "$DISPLAY" -vframes 1 "$shot" -y 2>/dev/null
  python3 - "$shot" <<'PYEOF'
import sys
from PIL import Image
im = Image.open(sys.argv[1]).convert("RGB"); px = im.load(); w, h = im.size
hits = [(x, y)
        for y in range(150, min(h, 1100))
        for x in range(200, min(w, 1600))
        if (lambda r, g, b: r > 110 and g < 80 and b < 80)(*px[x, y])]
if not hits:
    sys.exit("no diagnostic underline found in frame")
xs = [p[0] for p in hits]; ys = [p[1] for p in hits]
# aim at the middle of the underline, one line-height ABOVE it: the squiggle
# sits under the token, and the hover wants the token itself.
print((min(xs) + max(xs)) // 2, min(ys) - 12)
PYEOF
  rm -f "$shot"
}

# human_move: an eased, slightly arced pointer travel from wherever the
# pointer is to a window-relative point — what a hand on a mouse does, as
# opposed to xdotool's teleport or a string of equal hops. Ease-in-out on a
# quadratic curve whose control point sits a little off the straight line,
# plus a pixel of jitter; the last step lands exactly on target. A drag that
# reads as a person doing it (James, 2026-09-16) needs this, not a driver.
human_move() { # <x> <y> [steps] [seconds]
  local x=$1 y=$2 n=${3:-45} secs=${4:-0.9}
  local wx wy px py rx ry
  eval "$(xdotool getwindowgeometry --shell "$WIN" | sed 's/^/W/')"   # WX WY
  eval "$(xdotool getmouselocation --shell)"                           # X Y (root)
  wx=$WX wy=$WY; rx=$((X - wx)); ry=$((Y - wy))
  local dt
  dt=$(awk -v s="$secs" -v n="$n" 'BEGIN{printf "%.4f", s/n}')
  while read -r px py; do
    xdotool mousemove --window "$WIN" "$px" "$py"
    sleep "$dt"
  done < <(awk -v x0="$rx" -v y0="$ry" -v x1="$x" -v y1="$y" -v n="$n" 'BEGIN{
    srand();
    dx=x1-x0; dy=y1-y0; d=sqrt(dx*dx+dy*dy); if (d<1) d=1;
    # control point: midpoint pushed off the line by ~12% of the distance
    cx=(x0+x1)/2 - dy*0.12; cy=(y0+y1)/2 + dx*0.12;
    for (i=1;i<=n;i++){
      t=i/n; e=t*t*(3-2*t);                          # smoothstep
      bx=(1-e)*(1-e)*x0 + 2*(1-e)*e*cx + e*e*x1;
      by=(1-e)*(1-e)*y0 + 2*(1-e)*e*cy + e*e*y1;
      if (i<n){ bx+=rand()*2-1; by+=rand()*2-1 } else { bx=x1; by=y1 }
      printf "%d %d\n", bx, by
    }}')
}
# human_drag: approach the source, take hold, carry to the target, let go —
# with the small pauses a hand makes at each end.
human_drag() { # <x1> <y1> <x2> <y2>
  human_move "$1" "$2" 40 0.8
  sleep 0.5
  xdotool mousedown 1
  sleep 0.35
  human_move "$3" "$4" 55 1.2
  sleep 0.45
  xdotool mouseup 1
}
rec_start() { # <output basename> — records $WIN until rec_stop
  # REC_OFF="+x,y" grabs from an offset INSIDE the window: GTK client-side
  # decorations (Chrome) wrap the page in an invisible shadow margin that a
  # -window_id grab at +0,0 records as a black border (the 08-17 wk03 5b take
  # has one). gtk_frame_offset() computes it from _GTK_FRAME_EXTENTS.
  ffmpeg -hide_banner -loglevel error -f x11grab -draw_mouse 1 -window_id "$WIN" \
    -video_size "${CAP_W}x${CAP_H}" -framerate 30 -i "${DISPLAY}${REC_OFF:-}" \
    -c:v libx264 -preset veryfast -crf 18 -pix_fmt yuv420p "$OUT_DIR/$1.mp4" -y &
  REC_PID=$!
  sleep 1.5   # let the encoder open before the first action
}

# snap <output basename> — one lossless PNG of $WIN, no pointer.
#
# For stills (README and walkthrough images) rather than takes: the same
# -window_id grab rec_start uses, so a still and a take of the same recipe
# are the same pixels. Writes $OUT_DIR/<name>.png.
snap() {
  ffmpeg -hide_banner -loglevel error -f x11grab -draw_mouse 0 -window_id "$WIN" \
    -video_size "${CAP_W}x${CAP_H}" -i "${DISPLAY}${REC_OFF:-}" -vframes 1 \
    "$OUT_DIR/$1.png" -y
  note "still: $OUT_DIR/$1.png"
}

# gtk_frame_offset <window id> — prints "+left,top" from _GTK_FRAME_EXTENTS,
# or "" when the window has no CSD margin. Pair with a windowsize of
# CAP_W+left+right by CAP_H+top+bottom so the PAGE is the master size.
gtk_frame_offset() {
  local ext
  ext=$(xprop -id "$1" _GTK_FRAME_EXTENTS 2>/dev/null | grep -oE '[0-9]+(, [0-9]+){3}') || true
  [[ -n $ext ]] || return 0
  local l r t b
  IFS=', ' read -r l r t b <<<"$ext"
  echo "+$l,$t"
}

# rec_start_root records the ROOT display at +0,0 instead of $WIN's pixmap.
#
# For beats whose money shot is a native `title` tooltip (the FBD chip's
# amber "why"): Chromium renders those as their own override-redirect X
# window, not as page content, so a -window_id grab can never contain one —
# wk04 beats 0 and 2 each recorded a full take of a tooltip that was on the
# physical screen and absent from the file. launch_vscode parks the window
# at 0,0 sized CAP_WxCAP_H, so the root rect is the same pixels PLUS any
# popup drawn over them. The trade: this sees whatever else floats above the
# window too — other apps' notifications, and the lock screen (which a
# window grab records straight through; rec_stop's end-of-take lock check
# matters doubly here).
rec_start_root() { # <output basename> — records the root rect until rec_stop
  ffmpeg -hide_banner -loglevel error -f x11grab -draw_mouse 1 \
    -video_size "${CAP_W}x${CAP_H}" -framerate 30 -i "$DISPLAY" \
    -c:v libx264 -preset veryfast -crf 18 -pix_fmt yuv420p "$OUT_DIR/$1.mp4" -y &
  REC_PID=$!
  sleep 1.5   # let the encoder open before the first action
}

# rec_start_second records a SECOND window concurrently, into its own file.
#
# Beat 5's argument is not in the editor. The claim is "the process never
# stopped", and the only evidence for it is the dashboard's scan counter
# continuing to climb across the download — which happens in a different
# window. One capture cannot show both, so record both and let the edit cut
# between them, or composite side by side.
rec_start_second() { # <output basename> <window id>
  # Ask the WINDOW how big it is rather than assuming CAP_WxCAP_H. x11grab
  # fails outright ("Error opening input file :1") when the requested geometry
  # exceeds the drawable, and a window that was asked to be 2560x1440 is not
  # necessarily 2560x1440 — the WM may have clamped it, or decorations may
  # count differently. Then scale to the master size so both halves of a
  # dual capture line up in the edit.
  local geo w h
  geo=$(xdotool getwindowgeometry --shell "$2" 2>/dev/null) || die "no such window: $2"
  w=$(sed -n 's/^WIDTH=//p' <<<"$geo"); h=$(sed -n 's/^HEIGHT=//p' <<<"$geo")
  [[ -n $w && -n $h ]] || die "could not read geometry for window $2"
  ffmpeg -hide_banner -loglevel error -f x11grab -draw_mouse 0 -window_id "$2" \
    -video_size "${w}x${h}" -framerate 30 -i "$DISPLAY" \
    -vf "scale=${CAP_W}:${CAP_H}:force_original_aspect_ratio=decrease,pad=${CAP_W}:${CAP_H}:(ow-iw)/2:(oh-ih)/2" \
    -c:v libx264 -preset veryfast -crf 18 -pix_fmt yuv420p "$OUT_DIR/$1.mp4" -y &
  REC2_PID=$!
  sleep 1
}

rec_stop_second() {
  [[ -n ${REC2_PID:-} ]] || return 0
  sleep 1
  kill -INT "$REC2_PID" 2>/dev/null
  wait "$REC2_PID" 2>/dev/null || true
}

rec_stop() {
  sleep 1
  kill -INT "$REC_PID" 2>/dev/null
  wait "$REC_PID" 2>/dev/null || true
  # require_unlocked only checks at the START. GNOME's idle lock can land
  # mid-take, and when it does, capture keeps working (the compositor still
  # renders the window) while input stops — so you get a full-length recording
  # in which nothing was typed. Check again at the end and say so loudly.
  local sid
  sid=$(loginctl list-sessions --no-legend 2>/dev/null | awk '$4=="seat0"{print $1; exit}')
  if [[ -n $sid && $(loginctl show-session "$sid" -p LockedHint --value 2>/dev/null) == yes ]]; then
    printf '\033[31m✗ the session LOCKED during this take — input stopped, capture did not.\033[0m\n' >&2
    printf '  The recording is almost certainly wrong. Unlock, disable the idle lock, and re-run:\n' >&2
    printf '    gsettings set org.gnome.desktop.screensaver lock-enabled false\n' >&2
    return 1
  fi
}

# clip_start / clip_stop — a REVIEW clip, as opposed to a take: what the
# smoke checks and the verb self-test record so a run can be watched back
# (out/smoke/<check>.mp4, out/selftest/verbs-NN-<verb>.mp4). Not rec_start,
# for three reasons:
#   - it grabs the ROOT window (the CAP_WxCAP_H corner launch_vscode parks
#     VS Code in), not $WIN: a check relaunches VS Code
#     (new window id) mid-clip, starts before any window exists, and opens
#     context menus and tooltips, which are their own X windows;
#   - its pid is CLIP_PID, not REC_PID, which cleanup_capture SIGTERMs (an
#     mp4 killed that way has no index) — call clip_stop before it;
#   - small and robust over pretty: 15 fps, crf 30, and a fragmented mp4,
#     which still plays if the check is killed (run.sh's timeout) mid-clip.
# RIG_CLIPS=0 turns both into no-ops, for a quicker rehearsal.
clip_start() { # <output basename>
  [[ ${RIG_CLIPS:-1} != 0 ]] || return 0
  clip_stop
  local dims w h
  dims=$(xdpyinfo 2>/dev/null | awk '/dimensions:/ {print $2; exit}')
  [[ -n $dims ]] || { echo "clip_start: no X screen on $DISPLAY — no clip for $1" >&2; return 0; }
  # launch_vscode parks the window at 0,0, CAP_WxCAP_H plus its few px of
  # GTK frame: grab that corner, not the screen's empty margin.
  w=${dims%x*} h=${dims#*x}
  (( w > CAP_W + 16 )) && w=$((CAP_W + 16))
  (( h > CAP_H + 16 )) && h=$((CAP_H + 16))
  ffmpeg -nostdin -hide_banner -loglevel error -f x11grab -draw_mouse 1 \
    -video_size "$((w / 2 * 2))x$((h / 2 * 2))" -framerate 15 -i "$DISPLAY" \
    -c:v libx264 -preset veryfast -crf 30 -pix_fmt yuv420p \
    -movflags +frag_keyframe+empty_moov+default_base_moof "$OUT_DIR/$1.mp4" -y &
  CLIP_PID=$!
  sleep 0.5   # let the grab open before the first action
}

clip_stop() {
  [[ -n ${CLIP_PID:-} ]] || return 0
  kill -INT "$CLIP_PID" 2>/dev/null || true
  wait "$CLIP_PID" 2>/dev/null || true
  CLIP_PID=
}

# kill_controllers_for kills processes whose working directory is <dir>.
# Deliberately narrow: never kill by port or by binary name, because this
# machine runs other people's controllers.
kill_controllers_for() {
  local dir=$1 p cwd
  [[ -n $dir ]] || return 0
  # Both names: the CLI was renamed nautilus -> naut (2026-09), and the
  # container rig installs one as a symlink to the other.
  for p in $(pgrep -x naut 2>/dev/null) $(pgrep -x nautilus 2>/dev/null); do
    cwd=$(readlink "/proc/$p/cwd" 2>/dev/null) || continue
    [[ ${cwd%% (deleted)} == "$dir" ]] && kill "$p" 2>/dev/null
  done
  return 0
}

cleanup_capture() {
  # Every kill is `|| true`: this runs as an EXIT trap under set -e, and a
  # kill on an already-reaped pid (ffmpeg after rec_stop's wait, every time)
  # returns 1 — which aborted the whole trap RIGHT THERE and orphaned the
  # controller. That was the remaining way controllers piled up; found by
  # reproduction 2026-08-16 after two wk02 takes each left one behind.
  [[ -n ${REC_PID:-} ]] && kill "$REC_PID" 2>/dev/null || true
  [[ -n ${REC2_PID:-} ]] && kill "$REC2_PID" 2>/dev/null || true
  [[ -n ${CONTROLLER_PID:-} ]] && kill "$CONTROLLER_PID" 2>/dev/null || true
  # Kill any controller whose CWD is our project directory.
  #
  # Two earlier attempts failed silently and let controllers pile up until the
  # next beat died on "none of these ports are free":
  #   - pkill -f "NAUTILUS_ADDR=localhost:$PORT" never matches. Environment
  #     assignments made before `exec` do not appear in /proc/PID/cmdline.
  #   - fuser -k on the port is indiscriminate: it would happily kill a
  #     controller belonging to whoever else is using this machine.
  # Matching on CWD is both reliable and safe — it can only ever hit a process
  # we started, in a directory we made.
  kill_controllers_for "${PROJ:-}"
  restore_work_area
  return 0
}
