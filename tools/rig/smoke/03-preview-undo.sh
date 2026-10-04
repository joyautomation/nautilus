#!/usr/bin/env bash
# 03 — undo / redo / save from inside a diagram (PR #24).
#
#   preview panel   Ctrl+Z / Ctrl+Shift+Z / Ctrl+Y / Ctrl+S with focus in the
#                   diagram act on the .fbd document (diagramKeys.ts), and
#                   the diagram re-renders from the text.
#   custom editor   Ctrl+Z in a text field (the SFC "Add step" name) undoes
#                   the FIELD, never the document (keyForward.ts).
#
# Save is how "the buffer reverted" is read back: every step saves and reads
# the file, and the window title must stay on the diagram throughout (no
# focus left behind in the text editor).
set -euo pipefail
CHECK=03-preview-undo
source "$HOME/smoke/lib.sh"
rm -rf "$PROFILE"

ext_scaffold my-plant
F=$PROJ/program.fbd
const() { grep -oP 'cold = LT\(TempC, \K[0-9.]+' "$F"; }
[[ $(const) == 62.0 ]] || { fail "scaffold changed: cold threshold is $(const), not 62.0"; exit 1; }

smoke_open "$PROJ" program.fbd
key Escape; hide_sidebar
# The editor-title button (navigation@2, right of "Open as Diagram Editor"),
# not the palette: the button is what a user clicks.
png=$(shot text-title-buttons)
click_at 1830 56 8
# The preview opens beside with preserveFocus: click empty canvas to focus it.
click_at 1750 400 1
[[ $(title) == "FBD: program.fbd"* ]] && pass "editor-title preview button opened 'FBD: program.fbd' beside the text" "$png" \
  || { fail "preview did not open: $(title)" "$png"; exit 1; }
png=$(shot preview)

on_diagram() { [[ $(title) == "FBD: program.fbd"* ]]; }
# keyin <chord> — press it with focus in the diagram, and watch the title
# for the next 2 s: any frame where the text editor held focus is logged.
keyin() {
  local t seen=""
  xdotool key --clearmodifiers "$1"
  for _ in $(seq 20); do t=$(title); [[ $t == "FBD: program.fbd"* ]] || seen="$seen|$t"; sleep 0.1; done
  [[ -z $seen ]] || info "focus left the diagram during $1:$seen"
}

# ── an edit through the float editor: 62.0 → 55.0 ──────────────────────────
# The TAL-101 constant (IN2 of LT). Measured at 1920x1200, side bar hidden.
dclick_at 1103 875 1
key ctrl+a; xdotool type --delay 40 55.0; sleep 0.3; key Return; sleep 2
png=$(shot edited)
[[ $(const) == 62.0 ]] && pass "float-editor edit is in the buffer, not yet on disk" "$png" \
  || fail "edit reached disk without a save (autosave?) — $(const)" "$png"

keyin ctrl+s; sleep 1
[[ $(const) == 55.0 ]] && pass "Ctrl+S in the preview saved the document (disk: 55.0)" \
  || fail "Ctrl+S in the preview did not save (disk: $(const))"

keyin ctrl+z; sleep 1.5
png=$(shot undone)
keyin ctrl+s; sleep 1
[[ $(const) == 62.0 ]] && pass "Ctrl+Z in the preview reverted the document (saved: 62.0)" "$png" \
  || fail "Ctrl+Z in the preview did not revert the document (saved: $(const))" "$png"
# The diagram re-rendered from the text: the constant's chip is pixel-for-
# pixel what it was before the edit.
d=$(python3 - "$OUT_DIR/$CHECK-preview.png" "$OUT_DIR/$png.png" <<'PY'
import sys
from PIL import Image, ImageChops
a, b = (Image.open(p).convert("RGB").crop((1070, 855, 1140, 895)) for p in sys.argv[1:])
print(sum(1 for p in ImageChops.difference(a, b).getdata() if max(p) > 40))
PY
)
(( d < 30 )) && pass "diagram re-rendered to 62.0 after undo (chip matches the pre-edit frame)" "$png" \
  || fail "diagram did not re-render after undo ($d px differ from the pre-edit chip)" "$png"

keyin ctrl+shift+z; sleep 1.5; keyin ctrl+s; sleep 1
[[ $(const) == 55.0 ]] && pass "Ctrl+Shift+Z redo (saved: 55.0)" || fail "Ctrl+Shift+Z did not redo (saved: $(const))"
keyin ctrl+z; sleep 1.5; keyin ctrl+y; sleep 1.5; keyin ctrl+s; sleep 1
png=$(shot redone)
[[ $(const) == 55.0 ]] && pass "Ctrl+Y redo (saved: 55.0)" "$png" || fail "Ctrl+Y did not redo (saved: $(const))" "$png"
on_diagram && pass "focus stayed on the diagram through every undo/redo/save" || fail "focus ended outside the diagram: $(title)"

# ── the preview with NO text editor open ───────────────────────────────────
# diagramKeys.ts means to open the text beside and run `undo` there (there
# is no API to undo a document directly). But once the last text editor for
# program.fbd closes, VS Code closes the TextDocument and the preview looks
# it up in workspace.textDocuments — so check whether anything still lands.
xdotool key --clearmodifiers ctrl+1; sleep 0.6; xdotool key --clearmodifiers ctrl+w; sleep 1.5
click_at 1750 400 0.6        # empty canvas, focus in the diagram
keyin ctrl+z; sleep 1.5
png=$(shot undo-no-text-editor)
keyin ctrl+s; sleep 1
[[ $(const) == 62.0 ]] && pass "text editor closed: Ctrl+Z in the preview still reverted the document" "$png" \
  || fail "text editor closed: Ctrl+Z in the preview did nothing (no editor beside, saved $(const)) — key dropped: fbdPreview.ts:564 finds the doc in workspace.textDocuments, which no longer has it" "$png"
# A float-editor edit in the same state (the constant, now at the left of a
# full-width preview).
before=$(const)
dclick_at 143 875 1; key ctrl+a; xdotool type --delay 40 50.0; sleep 0.3; key Return; sleep 2
keyin ctrl+s; sleep 1
png=$(shot edit-no-text-editor)
[[ $(const) == 50.0 ]] && pass "text editor closed: a float-editor edit still reached the document" "$png" \
  || fail "text editor closed: the float-editor edit was dropped (saved $(const), was $before) — fbdPreview.ts:579 same lookup" "$png"
vs_cmd "View: Close All Editors" 1

# ── custom editor: Ctrl+Z in a text field is the field's ───────────────────
ext_fixture tank-batch
S=$PROJ/batch.sfc
smoke_open "$PROJ" batch.sfc
key Escape; hide_sidebar
vs_cmd "nautilus: Open as Diagram Editor" 8
# A document edit first, so a Ctrl+Z that wrongly reached the document
# would have something to take away: add step "Extra".
click_at 73 132 1; xdotool type --delay 40 Extra; key Return; sleep 2
# Then the field: "Alpha", replaced by "Beta", Ctrl+Z → "Alpha".
click_at 73 132 1
xdotool type --delay 40 Alpha; sleep 0.4; key ctrl+a; xdotool type --delay 40 Beta; sleep 0.4
shot field-beta >/dev/null
key ctrl+z; sleep 1
png=$(shot field-undo)
info "Add-step name field after Ctrl+Z: 'Alpha' expected (was 'Beta')" "$png"
[[ $(title) == "● batch.sfc"* ]] && pass "document still dirty after Ctrl+Z in the field (the Extra edit survived)" "$png" \
  || fail "document no longer dirty after Ctrl+Z in the field: $(title)" "$png"
key Escape; key ctrl+s; sleep 1.5
if grep -q "STEP Extra" "$S" && ! grep -q "Alpha\|Beta" "$S"; then
  pass "saved batch.sfc has step Extra and no field text — Ctrl+Z undid only the field"
else
  fail "Ctrl+Z in the field reached the document: $(grep -c 'STEP Extra' "$S") Extra"
fi
# And on the canvas, Ctrl+Z IS the document's (the custom editor path).
click_at 1500 1100 0.6
key ctrl+z; sleep 1.5; key ctrl+s; sleep 1.5
png=$(shot canvas-undo)
! grep -q "STEP Extra" "$S" && pass "Ctrl+Z on the SFC canvas undid the add-step in the document" "$png" \
  || fail "Ctrl+Z on the SFC canvas did not undo the add-step" "$png"
