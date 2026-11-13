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
#
# Every target is found in the DOM: the editor-title button by its label,
# the TIC-101 heater clamp by the wire into MX of Heater's LIMIT (data-id), the
# SFC "+ step" button by its text, empty canvas by elementFromPoint.
set -euo pipefail
CHECK=03-preview-undo
source "$HOME/smoke/lib.sh"
export G_PACE=fast
source "$HOME/fixtures/gestures.sh"   # cdp / click_el / dclick_el / click_button
rm -rf "$PROFILE"

ext_scaffold my-plant
F=$PROJ/program.fbd
# The constant: Heater's upper clamp, LIMIT(0.0, …, 100.0), a REAL literal
# wired straight into a block pin. (TAL-101's threshold was this check's
# constant until #574 made it the TempLowSP setpoint.)
K0=100.0
const() { grep -oP 'Heater := LIMIT\(0\.0, ADD\(pTerm, integral\), \K[0-9.]+' "$F"; }
[[ $(const) == "$K0" ]] || { fail "scaffold changed: Heater's LIMIT MX is '$(const)', not $K0"; exit 1; }

smoke_open "$PROJ" program.fbd
key Escape; hide_sidebar
# The editor-title button (navigation@2, right of "Open as Diagram Editor"),
# not the palette: the button is what a user clicks.
png=$(shot text-title-buttons)
page_click "$(title_action_el 'Open FBD Diagram Preview')" 8 || fail "no 'Open FBD Diagram Preview' button in the editor title" "$png"
# The preview opens beside with preserveFocus: click empty canvas to focus it.
wait_js "$_G_READY" 10 || true
click_canvas "" 1 || true
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
  info "after $1: iframe hasFocus=$(wv 'doc.hasFocus()') active=$(wv 'doc.activeElement?.tagName ?? ""') title=$(title)"
}

# ── an edit through the float editor: 100.0 → 55.0 ─────────────────────────
# The TIC-101 clamp: the chip whose wire runs into MX of block b:c.Heater
# (FbdEdge's data-id is "<from>-><to>:<pin>", the chip's data-id <from>).
CONST_EL='(() => { const e = [...doc.querySelectorAll("[data-kind=edge]")].find(e => e.dataset.id.endsWith("->b:c.Heater:MX")); return e ? doc.querySelector(`[data-kind=chip][data-id="${CSS.escape(e.dataset.id.split("->")[0])}"]`) : null; })()'
chip() { wv "$CONST_EL?.textContent.trim() ?? ''"; }
chip0=$(chip)
dclick_el "$CONST_EL" || fail "no TIC-101 clamp chip (wire into the MX of Heater's LIMIT) on the preview"
key ctrl+a; xdotool type --delay 40 55.0; sleep 0.3; key Return; sleep 2
png=$(shot edited)
[[ $(const) == "$K0" ]] && pass "float-editor edit is in the buffer, not yet on disk" "$png" \
  || fail "edit reached disk without a save (autosave?) — $(const)" "$png"

keyin ctrl+s; sleep 1
[[ $(const) == 55.0 ]] && pass "Ctrl+S in the preview saved the document (disk: 55.0)" \
  || fail "Ctrl+S in the preview did not save (disk: $(const))"

keyin ctrl+z; sleep 1.5
png=$(shot undone)
keyin ctrl+s; sleep 1
[[ $(const) == "$K0" ]] && pass "Ctrl+Z in the preview reverted the document (saved: $K0)" "$png" \
  || fail "Ctrl+Z in the preview did not revert the document (saved: $(const))" "$png"
# The diagram re-rendered from the text: the constant's chip reads what it
# read before the edit.
chip_is() { [[ $(chip) == "$1" ]]; }
wait_for 5 chip_is "$chip0" || true
c=$(chip)
[[ $chip0 == "$K0" && $c == "$chip0" ]] && pass "diagram re-rendered to $K0 after undo (the chip reads '$c' again, as before the edit)" "$png" \
  || fail "diagram did not re-render after undo (the chip reads '$c'; before the edit '$chip0')" "$png"

keyin ctrl+shift+z; sleep 1.5; keyin ctrl+s; sleep 1
[[ $(const) == 55.0 ]] && pass "Ctrl+Shift+Z redo (saved: 55.0)" || fail "Ctrl+Shift+Z did not redo (saved: $(const))"
keyin ctrl+z; sleep 1.5; keyin ctrl+y; sleep 1.5; keyin ctrl+s; sleep 1
png=$(shot redone)
[[ $(const) == 55.0 ]] && pass "Ctrl+Y redo (saved: 55.0)" "$png" || fail "Ctrl+Y did not redo (saved: $(const))" "$png"
on_diagram && pass "focus stayed on the diagram through every undo/redo/save" || fail "focus ended outside the diagram: $(title)"

# One Ctrl+Z is one undo. A key the diagram holds is posted by the webview;
# one that reaches the workbench (the frame lost focus in an undo round
# trip) goes through nautilus.diagramPreview.key. Never both: a second edit
# on the stack shows a double undo, which a single edit cannot.
click_canvas "" 0.6 || true
dclick_el "$CONST_EL" || fail "no TIC-101 clamp chip for the second edit"
key ctrl+a; xdotool type --delay 40 60.0; sleep 0.3; key Return; sleep 2
keyin ctrl+s; sleep 1
[[ $(const) == 60.0 ]] || fail "second edit did not save (disk: $(const))"
keyin ctrl+z; sleep 1.5; keyin ctrl+s; sleep 1
[[ $(const) == 55.0 ]] && pass "one Ctrl+Z undid one edit (60.0 → 55.0)" \
  || fail "one Ctrl+Z did not undo exactly one edit: 60.0 → $(const), want 55.0"

# ── keys with the panel active but its frame unfocused ──────────────────────
# The state an undo/redo round trip can leave behind: the panel is the
# active editor (the title says so) but the webview frame lost keyboard
# focus, so the keys reach the workbench, not the diagram. Every key after
# that used to be dropped (nautilus#639, #654; 1 run in 3 here). The
# workbench now hands them to the panel (nautilus.diagramPreview.key).
# Blurring the frame from the workbench page makes the state on purpose.
unfocus_frame() { pg '(document.activeElement?.blur(), document.activeElement?.tagName ?? "")' >/dev/null; sleep 0.4; }
unfocus_frame
info "frame blurred: hasFocus=$(wv 'doc.hasFocus()') title=$(title)"
keyin ctrl+z; sleep 1.5; unfocus_frame; keyin ctrl+s; sleep 1
[[ $(const) == "$K0" ]] && pass "frame unfocused: Ctrl+Z and Ctrl+S still reach the document (saved: $K0)" \
  || fail "frame unfocused: Ctrl+Z / Ctrl+S were dropped (saved: $(const), want $K0)"
unfocus_frame; keyin ctrl+shift+z; sleep 1.5; unfocus_frame; keyin ctrl+s; sleep 1
[[ $(const) == 55.0 ]] && pass "frame unfocused: Ctrl+Shift+Z redo (saved: 55.0)" \
  || fail "frame unfocused: Ctrl+Shift+Z did not redo (saved: $(const))"

# ── the preview with NO text editor open ───────────────────────────────────
# diagramKeys.ts means to open the text beside and run `undo` there (there
# is no API to undo a document directly). But once the last text editor for
# program.fbd closes, VS Code closes the TextDocument and the preview looks
# it up in workspace.textDocuments — so check whether anything still lands.
xdotool key --clearmodifiers ctrl+1; sleep 0.6; xdotool key --clearmodifiers ctrl+w; sleep 1.5
click_canvas "" 0.6 || true  # empty canvas, focus in the diagram
keyin ctrl+z; sleep 1.5
png=$(shot undo-no-text-editor)
keyin ctrl+s; sleep 1
[[ $(const) == "$K0" ]] && pass "text editor closed: Ctrl+Z in the preview still reverted the document" "$png" \
  || fail "text editor closed: Ctrl+Z in the preview did nothing (no editor beside, saved $(const)) — key dropped: fbdPreview.ts:564 finds the doc in workspace.textDocuments, which no longer has it" "$png"
# A float-editor edit in the same state (the constant, now in a full-width
# preview).
before=$(const)
dclick_el "$CONST_EL" || fail "no TIC-101 clamp chip on the full-width preview"
key ctrl+a; xdotool type --delay 40 50.0; sleep 0.3; key Return; sleep 2
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
add_step_form() {
  click_button "+ step" || return 1
  wait_js 'doc.activeElement?.matches(".addform input")' 4 || g_err "the Add step form did not take focus"
}
add_step_form || fail "SFC: no '+ step' button"
xdotool type --delay 40 Extra; key Return; sleep 2
# Then the field: "Alpha", replaced by "Beta", Ctrl+Z → "Alpha".
add_step_form || true
xdotool type --delay 40 Alpha; sleep 0.4; key ctrl+a; xdotool type --delay 40 Beta; sleep 0.4
shot field-beta >/dev/null
key ctrl+z; sleep 1
png=$(shot field-undo)
info "Add-step name field after Ctrl+Z: 'Alpha' expected (was 'Beta'); the field reads '$(wv 'doc.querySelector(".addform input")?.value ?? ""')'" "$png"
[[ $(title) == "● batch.sfc"* ]] && pass "document still dirty after Ctrl+Z in the field (the Extra edit survived)" "$png" \
  || fail "document no longer dirty after Ctrl+Z in the field: $(title)" "$png"
key Escape; key ctrl+s; sleep 1.5
if grep -q "STEP Extra" "$S" && ! grep -q "Alpha\|Beta" "$S"; then
  pass "saved batch.sfc has step Extra and no field text — Ctrl+Z undid only the field"
else
  fail "Ctrl+Z in the field reached the document: $(grep -c 'STEP Extra' "$S") Extra"
fi
# And on the canvas, Ctrl+Z IS the document's (the custom editor path).
click_canvas "" 0.6 || true
key ctrl+z; sleep 1.5; key ctrl+s; sleep 1.5
png=$(shot canvas-undo)
! grep -q "STEP Extra" "$S" && pass "Ctrl+Z on the SFC canvas undid the add-step in the document" "$png" \
  || fail "Ctrl+Z on the SFC canvas did not undo the add-step" "$png"
