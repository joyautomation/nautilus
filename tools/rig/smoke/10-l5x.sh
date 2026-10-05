#!/usr/bin/env bash
# 10 — a Rockwell L5X export (nautilus lang/l5x/testdata/DemoProgram.L5X,
# pushed in by run.sh as ~/smoke-assets/) opens in the ladder diagram editor
# READ-ONLY: the "read-only · Logix export" pill, the live pill, and no
# element palette. Editing gestures change nothing.
#
# The palette, the pills and the contact the gestures aim at are read from
# the webview's DOM.
set -euo pipefail
CHECK=10-l5x
source "$HOME/smoke/lib.sh"
export G_PACE=fast
source "$HOME/fixtures/gestures.sh"   # cdp eval / click_el / dclick_el
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
# The element palette (⊣⊢ ⊣/⊢ FN( ) … + rung) is LadderView's .palette row;
# a read-only ladder must not render it at all.
wait_js 'doc.querySelector("svg.rsvg")' 10 || true
row=$(wv 'String(doc.querySelectorAll(".palette button").length)')
[[ $row == 0 ]] && pass "no element palette on the L5X diagram (0 palette buttons in the DOM)" "$png" \
  || fail "an element palette is showing on the read-only L5X (${row:-?} palette buttons)" "$png"
pills=$(wv '[...doc.querySelectorAll(".bar .ropill, .bar button.livepill")].map((e) => e.textContent.trim()).join(" · ")')
info "header pills: 'read-only · Logix export' beside the title, live/offline at the right (DOM: $pills)" "$png"

# Gestures that would edit an .ld: select a contact + Delete, dblclick, Ctrl+Z.
# The StartPB contact (rung 0), by its operand.
START_PB='(() => { const g = [...doc.querySelectorAll("svg.rsvg g.node")].find((g) => g.querySelector("text.operand")?.textContent.trim() === "StartPB"); return g?.querySelector("rect.hit") ?? g; })()'
click_el "$START_PB" || fail "no StartPB contact on the L5X diagram"
key Delete; sleep 1
dclick_el "$START_PB" || true; key Escape
key ctrl+z; sleep 1
png=$(shot after-gestures)
if [[ $(sum "$F") == "$before" && $(title) != "●"* ]]; then
  pass "Delete / double-click / Ctrl+Z on the L5X diagram changed nothing (file identical, not dirty)" "$png"
else
  fail "the read-only L5X changed: dirty=$(title) md5 $before → $(sum "$F")" "$png"
fi
