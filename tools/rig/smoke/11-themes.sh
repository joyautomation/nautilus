#!/usr/bin/env bash
# 11 — themes: the FBD diagram (with its MiniMap and Controls), a Ladder
# diff, and the SFC chart under Light Modern and High Contrast (plus Dark
# Modern as the reference). Evidence is the PNGs; the assertions are that
# each surface actually FOLLOWS the theme — canvas, minimap and controls take
# the theme's background rather than staying dark-on-light.
set -euo pipefail
CHECK=11-themes
source "$HOME/smoke/lib.sh"

# mean luminance of a region of a snap
lum() {
  python3 - "$OUT_DIR/$1.png" "$2" "$3" "$4" "$5" <<'PY'
import sys
from PIL import Image, ImageStat
box = tuple(map(int, sys.argv[2:6]))
print(int(ImageStat.Stat(Image.open(sys.argv[1]).convert("L").crop(box)).mean[0]))
PY
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
  png=$(shot "$tag-fbd")
  expect "$tag FBD canvas"   "$png" 200 200 1500 900 "$kind"
  expect "$tag FBD minimap"  "$png" 1645 985 1870 1150 "$kind"
  expect "$tag FBD controls" "$png" 36 1030 80 1150 "$kind"
  vs_cmd "View: Close All Editors" 1
  open_file interlocks.ld 3
  vs_cmd "nautilus: Diff Ladder Diagram (vs git HEAD)" 10
  xdotool key --clearmodifiers ctrl+1; sleep 0.5; xdotool key --clearmodifiers ctrl+w; sleep 2
  png=$(shot "$tag-ladder-diff")
  expect "$tag Ladder diff" "$png" 100 400 1800 1000 "$kind"
  vs_cmd "View: Close All Editors" 1
  open_file batch.sfc 3
  vs_cmd "nautilus: Open as Diagram Editor" 8
  png=$(shot "$tag-sfc")
  expect "$tag SFC chart" "$png" 1100 200 1900 1000 "$kind"
  vs_cmd "View: Close All Editors" 1
done
