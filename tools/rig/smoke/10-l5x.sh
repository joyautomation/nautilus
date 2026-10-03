#!/usr/bin/env bash
# 10 — a Rockwell L5X export (nautilus lang/l5x/testdata/DemoProgram.L5X,
# pushed in by run.sh as ~/smoke-assets/) opens in the ladder diagram editor
# READ-ONLY: the "read-only · Logix export" pill, the live pill, and no
# element palette. Editing gestures change nothing.
set -euo pipefail
CHECK=10-l5x
source "$HOME/smoke/lib.sh"
rm -rf "$PROFILE"

SRC=$HOME/smoke-assets/DemoProgram.L5X
[[ -f $SRC ]] || { skip "no sample L5X pushed (run.sh looks in \$NAUTILUS_REPO/lang/l5x/testdata)"; exit 0; }
ext_scaffold my-plant
cp "$SRC" "$PROJ/"
F=$PROJ/DemoProgram.L5X; before=$(sum "$F")

smoke_open "$PROJ" DemoProgram.L5X
key Escape; hide_sidebar; sleep 2
vs_cmd "nautilus: Open as Diagram Editor" 8
png=$(shot diagram)
[[ $(title) == "DemoProgram.L5X"* ]] && pass "L5X opens in the ladder diagram editor (rungs render)" "$png" \
  || fail "L5X diagram did not open: $(title)" "$png"
# The palette row (⊣⊢ ⊣/⊢ FN( ) … + rung) sits at y≈132 on an .ld; here the
# rungs start there instead — so count button-border pixels across the row's
# left third, where the .ld palette's buttons are.
row=$(px_count "$png" 30 118 700 146 'r + g + b > 330')
(( row < 400 )) && pass "no element palette on the L5X diagram ($row lit px in the palette row)" "$png" \
  || fail "an element palette is showing on the read-only L5X ($row px)" "$png"
info "header pills: 'read-only · Logix export' beside the title, live/offline at the right" "$png"

# Gestures that would edit an .ld: select a contact + Delete, dblclick, Ctrl+Z.
click_at 120 166 0.6           # the StartPB contact, rung 0
key Delete; sleep 1
dclick_at 120 166 1; key Escape
key ctrl+z; sleep 1
png=$(shot after-gestures)
if [[ $(sum "$F") == "$before" && $(title) != "●"* ]]; then
  pass "Delete / double-click / Ctrl+Z on the L5X diagram changed nothing (file identical, not dirty)" "$png"
else
  fail "the read-only L5X changed: dirty=$(title) md5 $before → $(sum "$F")" "$png"
fi
