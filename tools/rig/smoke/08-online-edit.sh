#!/usr/bin/env bash
# 08 — online edit with confirmation (PR #18), against a real `naut run`.
#
#   Download Program to Controller → a modal naming the URL, the file, the
#   POU and the running program's hash. Cancel leaves the controller's
#   program hash (GET /api/program?pou=) alone; Download changes it.
#   Rollback asks too, and restores the previous hash.
#   nautilus.confirmControllerWrites: false → no modal, straight through.
#
# window.dialogStyle is "custom" in the smoke profile, so the modal is
# workbench DOM (and in the PNG): its text is read, and its buttons found
# by label, there.
set -euo pipefail
CHECK=08-online-edit
source "$HOME/smoke/lib.sh"
export G_PACE=fast
source "$HOME/fixtures/gestures.sh"   # cdp page / dialog_up / page_click_button
rm -rf "$PROFILE"

ext_scaffold my-plant
PORT=$(free_port 18080 18081 18082 18083)
point_extension_at "$PROJ" "$PORT"
start_controller "$PROJ" "$PORT"
sleep 3
h0=$(prog_hash MyPlant)
[[ -n $h0 ]] && pass "controller on :$PORT, MyPlant hash $h0" || { fail "no program hash from /api/program"; exit 1; }

smoke_open "$PROJ" program.fbd
key Escape; hide_sidebar; sleep 3
text_replace "$PROJ/program.fbd" "62.0" "61.0"
key ctrl+s; sleep 1
# modal_up — the custom confirmation dialog is on screen (workbench DOM).
modal_up() { dialog_up; }

# ── Cancel ──────────────────────────────────────────────────────────────────
vs_cmd "nautilus: Download Program to Controller" 3
wait_for 5 modal_up || true
png=$(shot download-modal)
if modal_up; then
  msg=$(dialog_message)
  if [[ $msg == *"http://localhost:$PORT"* && $msg == *program.fbd* && $msg == *MyPlant* && $msg == *"$h0"* ]]; then
    pass "Download shows a modal (text names http://localhost:$PORT, program.fbd, MyPlant, $h0: '$msg')" "$png"
  else
    fail "Download's modal does not name http://localhost:$PORT, program.fbd, MyPlant and $h0: '$msg'" "$png"
  fi
else
  fail "no confirmation modal on Download" "$png"
fi
page_click_button '^Cancel$' 2 || fail "the Download modal has no Cancel button ($(dialog_buttons))"
h=$(prog_hash MyPlant)
[[ $h == "$h0" ]] && pass "Cancel: controller unchanged ($h)" || fail "Cancel still downloaded: $h0 → $h"

# ── Download ────────────────────────────────────────────────────────────────
vs_cmd "nautilus: Download Program to Controller" 3
wait_for 5 modal_up || true
page_click_button '^Download$' 3 || fail "the Download modal has no Download button ($(dialog_buttons))"
h1=$(prog_hash MyPlant)
png=$(shot downloaded)
[[ -n $h1 && $h1 != "$h0" ]] && pass "Download: controller program changed ($h0 → $h1)" "$png" \
  || fail "Download did not change the controller ($h1)" "$png"
api "/api/program?pou=MyPlant" | grep -q 'LT(TempC, 61.0)' && pass "the running source has the edit (LT(TempC, 61.0))" \
  || fail "the running source lacks the edit"

# ── Rollback ────────────────────────────────────────────────────────────────
vs_cmd "nautilus: Rollback Controller Program" 3
wait_for 5 modal_up || true
png=$(shot rollback-modal)
modal_up && pass "Rollback asks first ('$(dialog_message)')" "$png" || fail "no confirmation modal on Rollback" "$png"
page_click_button '^Roll back$' 3 || fail "the Rollback modal has no Roll back button ($(dialog_buttons))"
h2=$(prog_hash MyPlant)
[[ $h2 == "$h0" ]] && pass "Rollback: controller back to $h0" || fail "Rollback: hash $h2 (want $h0)"

# ── confirmControllerWrites: false ──────────────────────────────────────────
python3 - "$PROJ/.vscode/settings.json" <<'PY'
import sys, re
p = sys.argv[1]; s = open(p).read()
s = s.replace('"nautilus.liveValues.enabled": true,', '"nautilus.liveValues.enabled": true,\n  "nautilus.confirmControllerWrites": false,', 1)
open(p, "w").write(s)
PY
grep -q '"nautilus.confirmControllerWrites": false' "$PROJ/.vscode/settings.json" || { fail "could not set confirmControllerWrites"; exit 1; }
sleep 2
vs_cmd "nautilus: Download Program to Controller" 3
png=$(shot no-confirm)
h3=$(prog_hash MyPlant)
if ! modal_up && [[ $h3 != "$h0" ]]; then
  pass "confirmControllerWrites false: no modal, downloaded straight away ($h0 → $h3)" "$png"
else
  fail "confirmControllerWrites false: modal shown or no download (hash $h3)" "$png"
  modal_up && page_click_button '^Cancel$' 1
fi
vs_cmd "nautilus: Rollback Controller Program" 3
png=$(shot no-confirm-rollback)
h4=$(prog_hash MyPlant)
! modal_up && [[ $h4 == "$h0" ]] && pass "confirmControllerWrites false: Rollback without a modal ($h4)" "$png" \
  || fail "confirmControllerWrites false: Rollback modal or no rollback ($h4)" "$png"
