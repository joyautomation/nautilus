#!/usr/bin/env bash
# 11 — themes: the FBD diagram (with its MiniMap and Controls), a Ladder
# diff, and the SFC chart under Light Modern and High Contrast (plus Dark
# Modern as the reference). Evidence is the PNGs; the assertions are that
# each surface actually FOLLOWS the theme — canvas, minimap and controls take
# the theme's background rather than staying dark-on-light.
#
# The subject here IS colour, so this check still reads pixels: the mean
# luminance of each surface in the frame. But each surface's BOX comes from
# the webview's DOM (xyflow's pane, minimap and controls panels; the
# ZoomPane of the Ladder diff and the SFC chart), not from coordinates.
set -euo pipefail
CHECK=11-themes
source "$HOME/smoke/lib.sh"
export G_PACE=fast
source "$HOME/fixtures/gestures.sh"   # cdp rect (el_box)

# mean luminance of a region of a snap
lum() {
  python3 - "$OUT_DIR/$1.png" "$2" "$3" "$4" "$5" <<'PY'
import sys
from PIL import Image, ImageStat
box = tuple(map(int, sys.argv[2:6]))
print(int(ImageStat.Stat(Image.open(sys.argv[1]).convert("L").crop(box)).mean[0]))
PY
}
# region <js → Element in the active webview> [inset fraction] — its box in
# the frame (snap coordinates "x0 y0 x1 y1"), shrunk by the fraction on
# every side; status 1 if the element is not there.
region() {
  local b x0 y0 x1 y1 f=${2:-0}
  b=$(el_box "$1") || return 1
  read -r x0 y0 x1 y1 <<<"$(snap_box "$b")"
  awk -v a="$x0" -v b="$y0" -v c="$x1" -v d="$y1" -v f="$f" \
    'BEGIN { w = c - a; h = d - b; printf "%d %d %d %d\n", a + w*f, b + h*f, c - w*f, d - h*f }'
}
# expect_el <label> <png> <js → Element> <inset> <light|dark> — expect, over
# the element's box.
expect_el() {
  local r
  r=$(region "$3" "$4") || { fail "$1: no such surface in the DOM (${3:0:60})" "$2"; return; }
  # shellcheck disable=SC2086
  expect "$1" "$2" $r "$5"
}
# expect <label> <png> <region> <light|dark> — region lum on the right side
expect() {
  local what=$1 png=$2 kind=$7 l
  l=$(lum "$png" "$3" "$4" "$5" "$6")
  local ok=
  [[ $kind == light ]] && (( l >= 150 )) && ok=1
  [[ $kind == dark ]] && (( l <= 60 )) && ok=1
  if [[ -n $ok ]]; then
    pass "$what: $kind as the theme (mean luminance $l)" "$png"
  else
    fail "$what: expected $kind, mean luminance $l" "$png"
  fi
}

ext_scaffold my-plant
# a real change for the ladder diff
sed -i 's/GT(TempC, 90.0)/GT(TempC, 85.0)/' "$PROJ/interlocks.ld"
mkdir -p "$HOME/sfc"; cp "$FIX/tank-batch/batch.sfc" "$PROJ/"

for theme in "Default Light Modern:light:LightModern" "Default High Contrast:dark:HighContrast" "Default Dark Modern:dark:DarkModern"; do
  IFS=: read -r name kind tag <<<"$theme"
  export REC_THEME=$name
  PROFILE=$HOME/.vscode-rec-$CHECK-$tag; rm -rf "$PROFILE"
  smoke_open "$PROJ" program.fbd
  key Escape; hide_sidebar
  vs_cmd "nautilus: Open as Diagram Editor" 8
  wait_js 'doc.querySelector(".svelte-flow__minimap")' 10 || true
  png=$(shot "$tag-fbd")
  # the canvas's middle: inset 15% clear of the corner panels
  expect_el "$tag FBD canvas"   "$png" 'doc.querySelector(".svelte-flow__pane")' 0.15 "$kind"
  expect_el "$tag FBD minimap"  "$png" 'doc.querySelector(".svelte-flow__minimap")' 0 "$kind"
  expect_el "$tag FBD controls" "$png" 'doc.querySelector(".svelte-flow__controls")' 0 "$kind"
  vs_cmd "View: Close All Editors" 1
  open_file interlocks.ld 3
  vs_cmd "nautilus: Diff Ladder Diagram (vs git HEAD)" 10
  xdotool key --clearmodifiers ctrl+1; sleep 0.5; xdotool key --clearmodifiers ctrl+w; sleep 2
  wait_js 'doc.querySelector(".host.diffing .zpane")' 10 || true
  png=$(shot "$tag-ladder-diff")
  expect_el "$tag Ladder diff" "$png" 'doc.querySelector(".host.diffing .zpane")' 0 "$kind"
  vs_cmd "View: Close All Editors" 1
  open_file batch.sfc 3
  vs_cmd "nautilus: Open as Diagram Editor" 8
  wait_js 'doc.querySelector("svg.chart")' 10 || true
  png=$(shot "$tag-sfc")
  expect_el "$tag SFC chart" "$png" 'doc.querySelector(".zpane")' 0 "$kind"
  vs_cmd "View: Close All Editors" 1
done
