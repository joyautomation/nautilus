#!/usr/bin/env bash
# 16 — the online-edit commands 08 does not reach: Diff Program with
# Controller (nautilus.program.diff, inventory C09) and Pull Program from
# Controller (nautilus.program.pull, C11), against a real `naut run`.
#
#   Diff, nothing changed → a diff editor with no marked lines, and the sync
#   status says nothing (in sync: the status item hides itself). Diff after
#   an unsaved edit to plant.st → the tab names the controller (its hash),
#   the edited line is marked on both sides, and the sync status says
#   "program differs".
#
#   Pull → the controller is made to differ WITHOUT the editor (a PUT
#   /api/program of an edited source, the request Download makes; the file
#   on disk is untouched), then Pull: a preview diff and a modal, "Pull and
#   overwrite", and the file on disk is the controller's program — its
#   composition (`naut compose`) hashes to the running program's hash, the
#   sync status stops saying "differs", and `git diff` is exactly the one
#   pulled line.
#
#   Pull over UNSAVED edits → it must not drop them silently.
#
# The extension's test-state snapshot (NAUTILUS_TEST_STATE, testState.ts)
# carries the sync state, the sync status item's text and the last
# notification (a modal's message included); the workbench DOM (cdp.js page)
# carries the diff editor's tab label and its marked lines.
#
# The project is the tank-batch fixture: two programs (batch.sfc, plant.st),
# so the commands route by the active file — plant.st, PROGRAM Plant.
set -euo pipefail
CHECK=16-diff-pull
source "$HOME/smoke/lib.sh"
G_PACE=fast source "$HOME/fixtures/gestures.sh"
rm -rf "$PROFILE"

TSTATE=$OUT_DIR/$CHECK-state.json
rm -f "$TSTATE"
export NAUTILUS_TEST_STATE=$TSTATE

PORT=$(free_port 18090 18091 18092 18093)
ext_fixture tank-batch
point_extension_at "$PROJ" "$PORT"
# Commit the port so `git diff` later shows only what Pull wrote.
git -C "$PROJ" commit -qam "rig: controller on :$PORT"
start_controller "$PROJ" "$PORT"
sleep 3
F=$PROJ/plant.st
h0=$(prog_hash Plant)
[[ -n $h0 ]] && pass "controller on :$PORT, Plant hash $h0" || { fail "no Plant program hash from /api/program"; exit 1; }

# ── helpers ─────────────────────────────────────────────────────────────────

# ts '<python expr over s, the snapshot>' — print it (status 1 if no snapshot).
ts() {
  python3 - "$TSTATE" "$1" <<'PY'
import json, sys
try:
    s = json.load(open(sys.argv[1]))
except Exception:
    sys.exit(1)
sync = next((e["text"] for e in s.get("statusBar", []) if e["name"] == "nautilus.sync"), "")
note = (s.get("lastNotification") or {}).get("text", "")
print(eval(sys.argv[2]))
PY
}
ts_true() { [[ $(ts "bool($1)" 2>/dev/null) == True ]]; }
sync_state() { ts 's["syncState"]' 2>/dev/null || echo "?"; }
sync_text() { ts 'sync' 2>/dev/null || echo "?"; }
last_note() { ts 'note' 2>/dev/null || echo ""; }

# diff_probe — the active editor group's diff editor, as JSON: the tab's
# label, the window title, and each side's marked lines (the text of every
# view line under a line-insert / line-delete decoration).
diff_probe() {
  cdp page '(() => {
    const g = document.querySelector(".editor-group-container.active") || document;
    const d = g.querySelector(".monaco-diff-editor");
    const tab = g.querySelector(".tab.active");
    const out = { diff: !!d, tab: tab ? (tab.getAttribute("aria-label") || tab.textContent.trim()) : "", title: document.title };
    if (!d) return out;
    const side = (cls, mark) => {
      const ed = d.querySelector(".editor." + cls);
      if (!ed) return null;
      const tops = new Set([...ed.querySelectorAll(".view-overlays ." + mark)].map((e) => e.parentElement.style.top));
      const lines = [...ed.querySelectorAll(".view-lines .view-line")];
      const me = ed.querySelector(".monaco-editor") || ed;
      return {
        uri: me.getAttribute("data-uri") || "",
        marked: lines.filter((l) => tops.has(l.style.top)).map((l) => l.textContent.replace(/ /g, " ").trim()),
        n: tops.size,
      };
    };
    out.modified = side("modified", "line-insert");
    out.original = side("original", "line-delete");
    return out;
  })()' 2>/dev/null || echo '{"diff": false, "tab": "", "title": "", "modified": null, "original": null}'
}
# pj <json> '<python expr over d>' — read a field of a probe's JSON.
pj() { python3 -c 'import json,sys; d=json.loads(sys.argv[1]); print(eval(sys.argv[2]))' "$1" "$2"; }

# put_program <pou> <python expr over src → new src> — make the controller
# run an edited program WITHOUT the editor: GET its source, edit it, PUT it
# back with its baseHash (what Download sends). Prints the new hash.
put_program() {
  api "/api/program?pou=$1" | python3 -c '
import json, sys, urllib.request
d = json.load(sys.stdin); src = d["source"]; new = eval(sys.argv[2])
assert new != src, "the edit changed nothing"
req = urllib.request.Request("http://localhost:" + sys.argv[1] + "/api/program", method="PUT",
    data=json.dumps({"source": new, "baseHash": d["hash"]}).encode(), headers={"Content-Type": "application/json"})
print(json.load(urllib.request.urlopen(req, timeout=5))["hash"])' "$PORT" "$2"
}
# composed_hash — the hash the controller would report for plant.st as it
# is ON DISK: `naut compose` (the composition Download sends) → the
# runtime's sourceHash (the first 12 hex of its SHA-256).
composed_hash() {
  (cd "$PROJ" && naut compose --json plant.st) | python3 -c '
import hashlib, json, sys
print(hashlib.sha256(json.load(sys.stdin)["source"].encode()).hexdigest()[:12])'
}
# The running source's program body (the controller's source less the
# project's prelude, as onlineEdit.ts splitProgram does).
controller_body() {
  local pre
  pre=$(cd "$PROJ" && naut compose --json plant.st | python3 -c 'import json,sys; print(json.load(sys.stdin)["prelude"], end="")')
  api "/api/program?pou=Plant" | python3 -c '
import json, sys
src = json.load(sys.stdin)["source"]; pre = sys.argv[1]
print(src[len(pre):] if src.startswith(pre) else "<prelude differs>", end="")' "$pre"
}
BODY=$OUT_DIR/$CHECK-controller-body.st

smoke_open "$PROJ" plant.st
key Escape; hide_sidebar; sleep 3

# ── C09 Diff: nothing changed ────────────────────────────────────────────────
if wait_for 30 ts_true 's["syncState"] == "sync"'; then
  pass "workspace and controller agree: syncState sync, sync status item hidden ('$(sync_text)')"
else
  fail "never in sync before any edit (syncState $(sync_state), status '$(sync_text)')"
fi
vs_cmd "nautilus: Diff Program with Controller" 3
p=$(diff_probe)
png=$(shot diff-in-sync)
info "probe: $p"
if [[ $(pj "$p" 'd["diff"]') == True ]] \
  && [[ $(pj "$p" '(d["modified"] or {}).get("n", -1) + (d["original"] or {}).get("n", -1)') == 0 ]]; then
  pass "Diff with no change: a diff editor ('$(pj "$p" 'd["tab"]')') with no marked lines" "$png"
else
  fail "Diff with no change: no diff editor, or lines marked" "$png"
fi
st=$(sync_state)
[[ $st == sync && -z $(sync_text) ]] \
  && pass "Diff with no change: still in sync (syncState $st, no 'program differs' in the status bar)" "$png" \
  || fail "Diff with no change: syncState $st, status '$(sync_text)'" "$png"
n=$(last_note)
if [[ -z $n ]]; then
  info "Diff in sync posts no message of its own (no notification); the in-sync signal is the status item hiding and an empty diff"
else
  info "Diff in sync: last notification '$n'"
fi
key ctrl+w; sleep 1.5

# ── C09 Diff: an unsaved edit ────────────────────────────────────────────────
text_replace "$F" "6.0" "7.0"
if wait_for 30 ts_true 's["syncState"] == "differs"'; then
  pass "an unsaved edit (6.0 → 7.0): syncState differs, status '$(sync_text)'"
else
  fail "the unsaved edit never showed as differs (syncState $(sync_state), status '$(sync_text)')"
fi
vs_cmd "nautilus: Diff Program with Controller" 3
p=$(diff_probe)
png=$(shot diff-differs)
info "probe: $p"
tab=$(pj "$p" 'd["tab"]'); wt=$(title)
if [[ $(pj "$p" 'd["diff"]') == True && $tab == *controller* && $tab == *"$h0"* ]]; then
  pass "Diff: a diff editor titled '$tab' — names the controller and its running hash" "$png"
else
  fail "Diff: no diff editor naming the controller (tab '$tab')" "$png"
fi
if [[ $tab == *plant.st* || $tab == *Plant* || $wt == *plant.st* ]]; then
  pass "Diff: the title names the program (tab '$tab', window '$wt')" "$png"
else
  warn "Diff: the title names neither plant.st nor PROGRAM Plant (tab '$tab', window '$wt') — in a two-program project the tab does not say which program is diffed (Pull's preview title does)" "$png"
fi
mod=$(pj "$p" '(d["modified"] or {}).get("marked", [])'); org=$(pj "$p" '(d["original"] or {}).get("marked", [])')
if [[ $(pj "$p" 'len((d["modified"] or {}).get("marked", [])) == 1 and "7.0" in d["modified"]["marked"][0] and len((d["original"] or {}).get("marked", [])) == 1 and "6.0" in d["original"]["marked"][0]') == True ]]; then
  pass "Diff: exactly the edited line is marked — controller $org, workspace $mod" "$png"
else
  fail "Diff: marked lines are not the one edit (controller $org, workspace $mod)" "$png"
fi
[[ $(pj "$p" '(d["modified"] or {}).get("uri", "")') == *plant.st ]] \
  && info "the workspace side is the real file ($(pj "$p" 'd["modified"]["uri"]')), editable like any working-tree diff"
st=$(sync_state); stx=$(sync_text)
[[ $st == differs && $stx == *"program differs"* ]] \
  && pass "Diff: the sync status bar says '$stx'" "$png" \
  || fail "Diff: the sync status is '$stx' (syncState $st), not 'program differs'" "$png"
key ctrl+w; sleep 1
# Back to the committed file: revert the buffer.
vs_cmd "File: Revert File" 2
if wait_for 20 ts_true 's["syncState"] == "sync"' && grep -q '6.0 \* PlantDtS' "$F"; then
  pass "Revert File: back in sync"
else
  fail "after Revert File: syncState $(sync_state)"
fi

# ── C11 Pull: the controller changed behind the editor's back ────────────────
h1=$(put_program Plant 'src.replace("Level + 6.0 * PlantDtS", "Level + 8.0 * PlantDtS", 1)') || h1=
[[ -n $h1 && $h1 != "$h0" ]] && pass "PUT /api/program (the field edit, 6.0 → 8.0): controller Plant $h0 → $h1, plant.st untouched" \
  || { fail "could not change the controller's program ($h1)"; exit 1; }
git -C "$PROJ" diff --quiet && pass "plant.st on disk still matches HEAD (file ≠ controller)" || fail "the project changed before Pull: $(git -C "$PROJ" status --porcelain | tr '\n' ' ')"
wait_for 30 ts_true 's["syncState"] == "differs"' \
  && pass "the status bar notices: '$(sync_text)'" \
  || fail "the controller-side change never showed as differs (syncState $(sync_state), status '$(sync_text)')"

vs_cmd "nautilus: Pull Program from Controller" 4
png=$(shot pull-confirm)
n=$(last_note)
if dialog_up; then
  msg=$(page_text 'document.querySelector(".monaco-dialog-box .dialog-message-text")' 2>/dev/null || echo "")
  pass "Pull: a preview diff and a modal before writing ($msg)" "$png"
else
  fail "Pull: no confirmation modal (last notification '$n')" "$png"
fi
p=$(diff_probe)
info "Pull preview behind the modal: tab '$(pj "$p" 'd["tab"]')', incoming $(pj "$p" '(d["modified"] or {}).get("marked", [])')"
page_click_button '^Pull and overwrite$' 3 || fail "no 'Pull and overwrite' button"
wait_for 10 grep -q '8.0 \* PlantDtS' "$F" || true
png=$(shot pulled)
controller_body >"$BODY" || true
[[ -s $BODY ]] && cmp -s "$BODY" "$F" \
  && pass "Pull: plant.st on disk is byte-for-byte the controller's program body (GET /api/program?pou=Plant less the prelude)" "$png" \
  || fail "Pull: plant.st differs from the controller's program body" "$png"
hc=$(composed_hash) || hc=; hr=$(prog_hash Plant) || hr=
[[ $hc == "$hr" && $hr == "$h1" ]] \
  && pass "Pull: naut compose of the pulled file hashes to the running program ($hc == $hr); the controller was not written" "$png" \
  || fail "Pull: composed hash $hc, running $hr (pushed $h1)" "$png"
n=$(last_note)
[[ $n == *pulled\ plant.st* ]] && info "Pull says: '$n'"
if wait_for 30 ts_true 's["syncState"] in ("sync", "edit")'; then
  pass "Pull: the sync status leaves 'program differs' — syncState $(sync_state), '$(sync_text)' (the controller runs an online edit that now matches the file)" "$png"
else
  fail "Pull: still syncState $(sync_state), '$(sync_text)'" "$png"
fi
gd=$(git -C "$PROJ" diff --numstat); gl=$(git -C "$PROJ" diff -U0 | grep -E '^[-+][^-+]' | tr '\n' '|')
if [[ $gd == $'1\t1\tplant.st' && $gl == *"- "*"6.0 * PlantDtS"*"+ "*"8.0 * PlantDtS"* ]]; then
  pass "git diff: exactly the pulled line in plant.st ($gl)" "$png"
else
  fail "git diff is not just the pulled line: numstat '$gd' lines '$gl'" "$png"
fi
key ctrl+w; sleep 1   # the preview diff

# ── C11 Pull over unsaved edits ──────────────────────────────────────────────
# Commit the pull, change the controller again (8.0 → 9.0), then make an
# UNRELATED unsaved edit in the editor (the heater's 2.5 → 3.5) and pull.
git -C "$PROJ" commit -qam "pull the field edit"
h2=$(put_program Plant 'src.replace("Level + 8.0 * PlantDtS", "Level + 9.0 * PlantDtS", 1)') || h2=
[[ -n $h2 && $h2 != "$h1" ]] || { fail "could not change the controller's program again ($h2)"; exit 1; }
wait_for 10 grep -q '8.0 \* PlantDtS' "$F" || true
key ctrl+1
text_replace "$F" "2.5" "3.5"
sleep 1
png=$(shot unsaved-edit)
dirty=$(cdp page 'document.querySelector(".editor-group-container.active .tab.active.dirty") ? true : false' 2>/dev/null || echo "?")
[[ $dirty == true ]] && info "plant.st has an unsaved edit (2.5 → 3.5) and the controller runs 9.0" "$png" \
  || fail "could not make an unsaved edit (tab dirty: $dirty)" "$png"

vs_cmd "nautilus: Pull Program from Controller" 4
png=$(shot pull-unsaved-confirm)
if dialog_up; then
  msg=$(page_text 'document.querySelector(".monaco-dialog-box .dialog-message-text")' 2>/dev/null || echo "")
  pass "Pull over unsaved edits asks first ($msg)" "$png"
  if [[ $msg == *nsaved* || $msg == *dirty* ]]; then
    pass "the confirmation mentions the unsaved edits" "$png"
  else
    warn "the confirmation does not mention the unsaved edits — it reads the same as a clean pull ($msg)" "$png"
  fi
  p=$(diff_probe)
  info "the preview diffs against the BUFFER: tab '$(pj "$p" 'd["tab"]')', workspace side marked $(pj "$p" '(d["original"] or {}).get("marked", [])')"
  page_click_button '^Pull and overwrite$' 3 || fail "no 'Pull and overwrite' button"
else
  warn "Pull over unsaved edits did not ask (last notification '$(last_note)')" "$png"
fi
wait_for 10 grep -q '9.0 \* PlantDtS' "$F" || true
key ctrl+w; sleep 1   # the preview diff
key ctrl+1; sleep 1
png=$(shot pulled-over-unsaved)
# What the editor shows now: is the user's 3.5 still there, and is the tab
# still dirty (the buffer kept, disk replaced) — or did the pull replace it?
buf=$(cdp page '[...document.querySelectorAll(".editor-group-container.active .monaco-editor .view-lines .view-line")].map((l) => l.textContent.replace(/ /g, " ")).join("\n")' 2>/dev/null || echo "")
dirty=$(cdp page 'document.querySelector(".editor-group-container.active .tab.active.dirty") ? true : false' 2>/dev/null || echo "?")
ondisk_new=$(grep -c '9.0 \* PlantDtS' "$F" || true); ondisk_user=$(grep -c '3.5 \* PlantDtS' "$F" || true)
info "after the pull: disk has the controller's 9.0: $ondisk_new, the user's 3.5: $ondisk_user · editor shows 3.5: $([[ $buf == *'3.5 * PlantDtS'* ]] && echo yes || echo no), 9.0: $([[ $buf == *'9.0 * PlantDtS'* ]] && echo yes || echo no) · tab dirty: $dirty" "$png"
if [[ $buf == *'3.5 * PlantDtS'* && $dirty == true ]]; then
  pass "the unsaved edit survives the pull (still in the editor, tab still dirty)" "$png"
  if [[ $buf != *'9.0 * PlantDtS'* && $ondisk_new == 1 ]]; then
    warn "works-but-wrong: Pull wrote the controller's program to disk UNDER the dirty buffer — the editor still shows the pre-pull text (8.0), the pulled 9.0 is invisible, and the next save hits VS Code's 'file is newer' conflict (overwrite there silently undoes the pull)" "$png"
  fi
elif [[ $ondisk_user == 1 ]]; then
  pass "the unsaved edit was kept (merged into the pulled file)" "$png"
else
  warn "the unsaved edit (3.5) is gone from both the editor and the disk — Pull dropped it" "$png"
fi
key Escape
vs_cmd "File: Revert File" 2
