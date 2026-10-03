#!/usr/bin/env bash
# 06 — zoom keys stay in the diagram (PR #26). With focus in the Ladder and
# SFC diagrams, Ctrl+= / Ctrl+- / Ctrl+0 and Ctrl+wheel change the DIAGRAM's
# zoom (its % readout, bottom-left) and NOT VS Code's window zoom.
#
# Window zoom is read two ways: the profile sets window.zoomPerWindow false,
# so a window zoom is WRITTEN to settings.json as window.zoomLevel; and the
# UI chrome (tab strip, status bar) must be pixel-identical. A control at the
# end presses Ctrl+= in a text editor to prove the probe sees a real one.
set -euo pipefail
CHECK=06-zoom-keys
source "$HOME/smoke/lib.sh"
rm -rf "$PROFILE"

zl() { grep -oP '^\s*"window.zoomLevel":\s*\K[-0-9.]+' "$PROFILE/User/settings.json"; }
# region_diff <png a> <png b> <x0 y0 x1 y1> — pixels that differ
region_diff() {
  python3 - "$OUT_DIR/$1.png" "$OUT_DIR/$2.png" "$3" "$4" "$5" "$6" <<'PY'
import sys
from PIL import Image, ImageChops
box = tuple(map(int, sys.argv[3:7]))
a, b = (Image.open(p).convert("RGB").crop(box) for p in sys.argv[1:3])
print(sum(1 for p in ImageChops.difference(a, b).getdata() if max(p) > 30))
PY
}
# The ZoomPane % readout (x≈18–50), side bar hidden, with a few px of slack
# each side but none of the canvas beyond the control column (zoomed content
# there would count as a readout change). PR #50's html/body reset dropped VS
# Code's 20px webview body padding and moved the readout left from x≈38, so
# the old box (x 36–82) caught only its last digits and "143%" → "119%"
# differed by 11 px, under the 15-px bar: a false FAIL, zoom worked.
LABEL="12 1136 56 1158"
CHROME="0 36 700 78"         # tab strip (grows with window zoom)
STATUS="0 1174 900 1200"     # status bar

one() { # <lang> <file> <empty-canvas x> <y>
  local lang=$1 file=$2 cx=$3 cy=$4
  smoke_open "$PROJ" "$file"
  key Escape; hide_sidebar
  vs_cmd "nautilus: Open as Diagram Editor" 8
  local z0; z0=$(zl)
  click_at "$cx" "$cy" 0.6
  local s0 s1 s2 s3 s4
  s0=$(shot "$lang-0")
  key ctrl+equal; key ctrl+equal; sleep 1; s1=$(shot "$lang-1-zoomin")
  key ctrl+minus; sleep 1; s2=$(shot "$lang-2-zoomout")
  key ctrl+0; sleep 1; s3=$(shot "$lang-3-fit")
  xdotool keydown ctrl; for _ in 1 2 3; do xdotool click 4; sleep 0.25; done; xdotool keyup ctrl; sleep 1
  s4=$(shot "$lang-4-wheel")
  local step a b ok=1
  for step in "$s0 $s1 Ctrl+=" "$s1 $s2 Ctrl+-" "$s2 $s3 Ctrl+0" "$s3 $s4 Ctrl+wheel"; do
    read -r a b k <<<"$step"
    if (( $(region_diff "$a" "$b" $LABEL) > 15 )); then
      pass "$lang: $k changed the diagram zoom (readout changed)" "$b"
    else
      fail "$lang: $k did not change the diagram zoom" "$b"; ok=
    fi
  done
  [[ $(zl) == "$z0" ]] && pass "$lang: window.zoomLevel untouched ($z0) through Ctrl+= / - / 0 / wheel" \
    || fail "$lang: VS Code's window zoom moved: window.zoomLevel $z0 → $(zl)"
  local c; c=$(( $(region_diff "$s0" "$s4" $CHROME) + $(region_diff "$s0" "$s4" $STATUS) ))
  (( c < 40 )) && pass "$lang: UI chrome (tab strip, status bar) pixel-identical before/after" "$s4" \
    || fail "$lang: UI chrome changed size ($c px differ)" "$s4"
}

ext_scaffold my-plant
one Ladder interlocks.ld 1000 900
ext_fixture tank-batch
one SFC batch.sfc 1500 1000

# Control: the probe DOES see a real window zoom.
vs_cmd "View: Close All Editors" 1
open_file plant.st 3
z0=$(zl); key ctrl+equal; sleep 1.5; z1=$(zl)
png=$(shot control-window-zoom)
[[ $z1 != "$z0" ]] && pass "control: Ctrl+= in a TEXT editor moves window.zoomLevel ($z0 → $z1), so the probe works" "$png" \
  || fail "control: Ctrl+= in a text editor did not register — the probe is blind" "$png"
key ctrl+minus; sleep 1
