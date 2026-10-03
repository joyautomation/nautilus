#!/usr/bin/env bash
# 08 — online edit with confirmation (PR #18), against a real `naut run`.
#
#   Download Program to Controller → a modal naming the URL, the file, the
#   POU and the running program's hash. Cancel leaves the controller's
#   program hash (GET /api/program?pou=) alone; Download changes it.
#   Rollback asks too, and restores the previous hash.
#   nautilus.confirmControllerWrites: false → no modal, straight through.
#
# window.dialogStyle is "custom" in the smoke profile, so the modal is drawn
# in the window (and in the PNG); its buttons are clicked where they are.
set -euo pipefail
CHECK=08-online-edit
source "$HOME/smoke/lib.sh"
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
# The modal's buttons at 1920x1200 (custom dialog, centred).
CANCEL="1106 662"; OK="1196 662"
modal_up() { (( $(px_count "$1" 1170 645 1240 680 'r > 90 and b > 150 and g < 110') > 200 )); }

# ── Cancel ──────────────────────────────────────────────────────────────────
vs_cmd "nautilus: Download Program to Controller" 3
png=$(shot download-modal)
if modal_up "$png"; then
  pass "Download shows a modal (text names http://localhost:$PORT, program.fbd, MyPlant, $h0 — see PNG)" "$png"
else
  fail "no confirmation modal on Download" "$png"
fi
click_at $CANCEL 2
h=$(prog_hash MyPlant)
[[ $h == "$h0" ]] && pass "Cancel: controller unchanged ($h)" || fail "Cancel still downloaded: $h0 → $h"

# ── Download ────────────────────────────────────────────────────────────────
vs_cmd "nautilus: Download Program to Controller" 3
click_at $OK 3
h1=$(prog_hash MyPlant)
png=$(shot downloaded)
[[ -n $h1 && $h1 != "$h0" ]] && pass "Download: controller program changed ($h0 → $h1)" "$png" \
  || fail "Download did not change the controller ($h1)" "$png"
api "/api/program?pou=MyPlant" | grep -q 'LT(TempC, 61.0)' && pass "the running source has the edit (LT(TempC, 61.0))" \
  || fail "the running source lacks the edit"

# ── Rollback ────────────────────────────────────────────────────────────────
vs_cmd "nautilus: Rollback Controller Program" 3
png=$(shot rollback-modal)
modal_up "$png" && pass "Rollback asks first" "$png" || fail "no confirmation modal on Rollback" "$png"
click_at $OK 3
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
if ! modal_up "$png" && [[ $h3 != "$h0" ]]; then
  pass "confirmControllerWrites false: no modal, downloaded straight away ($h0 → $h3)" "$png"
else
  fail "confirmControllerWrites false: modal shown or no download (hash $h3)" "$png"
  modal_up "$png" && click_at $CANCEL 1
fi
vs_cmd "nautilus: Rollback Controller Program" 3
png=$(shot no-confirm-rollback)
h4=$(prog_hash MyPlant)
! modal_up "$png" && [[ $h4 == "$h0" ]] && pass "confirmControllerWrites false: Rollback without a modal ($h4)" "$png" \
  || fail "confirmControllerWrites false: Rollback modal or no rollback ($h4)" "$png"
