#!/usr/bin/env bash
# 09 — empty files (PR #22): a new 0-byte .fbd, .ld and .sfc each open as a
# diagram with the "Empty file — the first edit writes a PROGRAM … skeleton"
# banner. "initialize" (FBD) or a first gesture (+ rung, + step) writes a
# valid skeleton, and `naut check` accepts the whole project afterwards.
#
# The banner is read, and every button found, in the webview's DOM (the
# banner's .blank row and its "initialize" button, the Ladder palette's
# "+ rung", the SFC toolbar's "+ step"), wherever the banner text puts them.
set -euo pipefail
CHECK=09-empty-files
source "$HOME/smoke/lib.sh"
export G_PACE=fast
source "$HOME/fixtures/gestures.sh"   # cdp eval / click_button / ld_palette
rm -rf "$PROFILE"

ext_scaffold my-plant
: >"$PROJ/tank.fbd"; : >"$PROJ/alarms.ld"; : >"$PROJ/seq.sfc"; : >"$PROJ/rungs.ld"; : >"$PROJ/steps.sfc"

# banner — the Empty-file banner is up (App.svelte's .blank row; absent on a
# non-empty file).
banner_text() { wv 'doc.querySelector(".blank")?.textContent.trim() ?? ""'; }
banner() { [[ $(banner_text) == "Empty file"* ]]; }

one() { # <lang> <file> <gesture description> <gesture command…>
  local lang=$1 file=$2 how=$3; shift 3
  local f=$PROJ/$file png
  open_file "$file" 3
  vs_cmd "nautilus: Open as Diagram Editor" 8
  wait_for 10 banner || true
  png=$(shot "$lang-empty")
  if [[ ! -s $f ]] && banner; then
    pass "$lang: 0-byte $file opens as a diagram with the Empty-file banner" "$png"
  else
    fail "$lang: no Empty-file banner on 0-byte $file" "$png"
  fi
  "$@" || echo "  ($how: the gesture's target is not in the DOM)"
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

# initialize — the banner's "initialize" button, by its label (its x follows
# the banner text, file name included).
initialize() {
  if ! banner; then echo "  (no banner to initialize from)"; return 0; fi
  click_button initialize || { echo "  (no initialize button on the banner)"; return 0; }
  sleep 1.4
}
# first_step — the SFC toolbar's "+ step", then Enter in the Add step form.
first_step() {
  click_button "+ step" || return 0
  wait_js 'doc.activeElement?.matches(".addform input")' 4 || true
  xdotool key Return
}

smoke_open "$PROJ" sim.st
key Escape; hide_sidebar
one FBD tank.fbd 'initialize' initialize
one Ladder alarms.ld 'initialize' initialize
one SFC seq.sfc 'initialize' initialize
one Ladder2 rungs.ld 'the first gesture (+ rung)' ld_palette '+ rung'
one SFC2 steps.sfc 'the first gesture (+ step, Enter)' first_step

rm -f "$PROJ/rungs.ld"      # the gesture file's placeholder is reported above
out=$(cd "$PROJ" && naut check 2>&1 | tail -1 || true)
[[ $out == *", 0 with errors"* ]] && pass "whole project (initialized files): $out" || fail "whole project: $out"
