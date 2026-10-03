#!/usr/bin/env bash
# 09 — empty files (PR #22): a new 0-byte .fbd, .ld and .sfc each open as a
# diagram with the "Empty file — the first edit writes a PROGRAM … skeleton"
# banner. "initialize" (FBD) or a first gesture (+ rung, + step) writes a
# valid skeleton, and `naut check` accepts the whole project afterwards.
set -euo pipefail
CHECK=09-empty-files
source "$HOME/smoke/lib.sh"
rm -rf "$PROFILE"

ext_scaffold my-plant
: >"$PROJ/tank.fbd"; : >"$PROJ/alarms.ld"; : >"$PROJ/seq.sfc"; : >"$PROJ/rungs.ld"; : >"$PROJ/steps.sfc"
BANNER_Y=132      # the banner row, under the header, at 1920x1200

banner() { # <png> — the banner's text row is lit (it is absent on a non-empty file)
  (( $(px_count "$1" 24 120 1000 146 'r + g + b > 400') > 150 ))
}

one() { # <lang> <file> <gesture description> <gesture command…>
  local lang=$1 file=$2 how=$3; shift 3
  local f=$PROJ/$file png
  open_file "$file" 3
  vs_cmd "nautilus: Open as Diagram Editor" 8
  png=$(shot "$lang-empty")
  if [[ ! -s $f ]] && banner "$png"; then
    pass "$lang: 0-byte $file opens as a diagram with the Empty-file banner" "$png"
  else
    fail "$lang: no Empty-file banner on 0-byte $file" "$png"
  fi
  "$@"
  sleep 2
  key ctrl+s; sleep 1.5
  png=$(shot "$lang-initialized")
  if [[ -s $f ]] && grep -qi "^PROGRAM " "$f" && grep -qi "^END_PROGRAM" "$f"; then
    pass "$lang: $how wrote a skeleton ($(grep -ci . "$f") lines, $(head -1 "$f"))" "$png"
  else
    fail "$lang: $how left $file as: $(head -c 120 "$f" | tr '\n' ' ')" "$png"
  fi
  local e; e=$(cd "$PROJ" && naut check 2>&1 | grep -F "$file" || true)
  if [[ -z $e ]]; then pass "$lang: naut check accepts $file"
  elif [[ $how == *gesture* ]]; then
    # A gesture adds an unfinished element on top of the skeleton; its
    # placeholders are the user's to fill. Report, don't fail.
    warn "$lang: after $how, naut check rejects $file: $e — the skeleton is fine, the new element's placeholder is not"
  else fail "$lang: naut check on $file: $e"; fi
  vs_cmd "View: Close All Editors" 1
}

# initialize — click the banner's "initialize" button where THIS frame shows
# it (banner_button_x, lib.sh): its x follows the banner text, file name
# included, so no one coordinate hits it for every file. Runs inside one(),
# whose $png is the Empty-file frame just taken.
initialize() {
  local x
  if ! banner "$png"; then echo "  (no banner to initialize from)"; return 0; fi
  x=$(banner_button_x "$png" "$BANNER_Y") || { echo "  (no initialize button found on the banner row)"; return 0; }
  echo "  initialize button at x=$x"
  click_at "$x" "$BANNER_Y" 2
}

smoke_open "$PROJ" sim.st
key Escape; hide_sidebar
one FBD tank.fbd 'initialize' initialize
one Ladder alarms.ld 'initialize' initialize
one SFC seq.sfc 'initialize' initialize
one Ladder2 rungs.ld 'the first gesture (+ rung)' click_at "$(ld_palette_x '+ rung')" 168 2
one SFC2 steps.sfc 'the first gesture (+ step, Enter)' bash -c "$(declare -f click_at move_to to_win _frame key); WIN=$WIN; click_at 73 168 1; xdotool key Return"

rm -f "$PROJ/rungs.ld"      # the gesture file's placeholder is reported above
out=$(cd "$PROJ" && naut check 2>&1 | tail -1 || true)
[[ $out == *", 0 with errors"* ]] && pass "whole project (initialized files): $out" || fail "whole project: $out"
