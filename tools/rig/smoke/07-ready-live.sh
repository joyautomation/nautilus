#!/usr/bin/env bash
# 07 — the ready handshake (webviewReady.ts): with `naut run` going, every
# editor shows the LIVE pill and live values the first time it opens, and
# again after "Developer: Reload Webviews" (the page remounts; the host
# replays the latest state on the page's `ready`).
#
# Live is read off the frame: the pill turns green ("● live"; amber when
# offline) and value badges paint green on the canvas.
set -euo pipefail
CHECK=07-ready-live
source "$HOME/smoke/lib.sh"
rm -rf "$PROFILE"

GREEN='g > 120 and g > r + 40 and g > b + 20'
pill() { px_count "$1" 1450 80 1910 112 "$GREEN"; }       # the header pill
vals() { px_count "$1" 20 120 1600 1170 "$GREEN"; }       # value badges
PILL_MIN=100

# live_ok <label> <png> <min value px> — pill green, and (if asked) values
live_ok() {
  local what=$1 png=$2 vmin=$3 p v
  p=$(pill "$png"); v=$(vals "$png")
  if (( p >= PILL_MIN && v >= vmin )); then
    pass "$what: live pill + values ($p/$v px)" "$png"
  else
    fail "$what: not live (pill $p px, values $v px; want ≥$PILL_MIN / ≥$vmin)" "$png"
  fi
}

# check_editor <label> <how to open: a command> <min value px>
check_editor() {
  local lbl=$1 open=$2 vmin=$3 png
  vs_cmd "View: Close All Editors" 1
  eval "$open"
  sleep 6
  png=$(shot "$lbl-first-open")
  live_ok "$lbl, first open" "$png" "$vmin"
  vs_cmd "Developer: Reload Webviews" 8
  png=$(shot "$lbl-reloaded")
  live_ok "$lbl, after Reload Webviews" "$png" "$vmin"
  if [[ $lbl == FBD* ]] && (( $(px_count "$png" 20 80 1900 1170 'r + g + b > 200') < 500 )); then
    info "$lbl: the webview is BLANK after the reload (no header, no canvas) — App.svelte:388 show(saved) assigns fbdSource (declared let at :552) before its declaration runs: a TDZ throw aborts the mount, so 'ready' (:394) is never posted" "$png"
  fi
}

ext_scaffold my-plant
cp "$FIX/heated-tank.mimic.json" "$PROJ/"
PORT=$(free_port 18080 18081 18082 18083)
point_extension_at "$PROJ" "$PORT"
start_controller "$PROJ" "$PORT"
sleep 4
api /api/state >/dev/null && pass "naut run on :$PORT, /api/state answers" || { fail "controller not answering on :$PORT"; exit 1; }

smoke_open "$PROJ" sim.st
key Escape; hide_sidebar
sleep 5
png=$(shot st-inline)
# ST: the inline pills are editor decorations, and the status bar item says
# "nautilus: live" — read the status bar's right end.
p=$(px_count "$png" 20 80 1900 1170 "$GREEN")
info "ST text: inline value pills ($p green px) — status bar at bottom right" "$png"

check_editor FBD-diagram 'open_file program.fbd 3; vs_cmd "nautilus: Open as Diagram Editor" 8' 400
check_editor FBD-preview 'open_file program.fbd 3; vs_cmd "nautilus: Open FBD Diagram Preview" 8; xdotool key --clearmodifiers ctrl+1; sleep 0.5; xdotool key --clearmodifiers ctrl+w; sleep 2' 400
check_editor Ladder-diagram 'open_file interlocks.ld 3; vs_cmd "nautilus: Open as Diagram Editor" 8' 0
check_editor Mimic 'open_file heated-tank.mimic.json 8' 0

# A diagram editor RESTORED by a window reload (the same saved-state path
# Reload Webviews takes, and what reopening VS Code does).
#
# ONE editor group into the reload, or the restored editor is not where
# pill() looks. By this point quick-open brings program.fbd up as the
# diagram already (VS Code remembers the editor last used for it), so
# "nautilus: Open as Diagram Editor" is not offered for it and the palette's
# fuzzy match runs the "Open … Diagram Preview" command instead: a preview
# BESIDE the diagram (2026-09-25 frames). A preview panel is not restored
# by a window reload (no serializer), which leaves an empty right group: the
# restored diagram sits in the left half (pill near x 700, outside pill()'s
# box), and the post-reload quick-open can land in the empty group and open
# a second copy there (Ladder). So: focus group 1 and close every other
# group's editors (empty groups close with them) before reloading. The
# assertion is unchanged: a restored-but-blank editor paints no pill and no
# values and still FAILs (proved against 9812ad6, 2026-09-25).
for pair in "FBD program.fbd 400" "Ladder interlocks.ld 0"; do
  read -r lbl file vmin <<<"$pair"
  vs_cmd "View: Close All Editor Groups" 1
  open_file "$file" 3; vs_cmd "nautilus: Open as Diagram Editor" 8
  xdotool key --clearmodifiers ctrl+1; sleep 0.5
  vs_cmd "View: Close Editors in Other Groups" 2
  vs_cmd "Developer: Reload Window" 15
  open_file "$file" 3        # activates the restored diagram tab
  sleep 6
  png=$(shot "$lbl-window-reload")
  # A green pill elsewhere in the header band means the layout split anyway:
  # say so, so a FAIL below is read as the rig's, not the extension's.
  band=$(px_count "$png" 20 80 1910 112 "$GREEN"); p=$(pill "$png")
  (( band > p )) && info "$lbl: $((band - p)) green header px outside the full-width pill box — more than one editor group after the reload" "$png"
  live_ok "$lbl diagram editor restored by Reload Window" "$png" "$vmin"
done

# SFC: tank-batch, its own controller.
kill_controllers_for "$PROJ"; [[ -n ${CONTROLLER_PID:-} ]] && kill "$CONTROLLER_PID" 2>/dev/null || true
ext_fixture tank-batch
point_extension_at "$PROJ" "$PORT"
start_controller "$PROJ" "$PORT"
sleep 3
smoke_open "$PROJ" batch.sfc
key Escape; hide_sidebar
check_editor SFC-diagram 'open_file batch.sfc 3; vs_cmd "nautilus: Open as Diagram Editor" 8' 0
