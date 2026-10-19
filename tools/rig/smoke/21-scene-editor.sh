#!/usr/bin/env bash
# 21 — the 3D scene editor (sceneEditor.ts): a *.scene.json opens, by
# default, as the hmi-3d renderer in a webview with a kinds palette, a
# gizmo and a node panel, and every gesture is one text edit:
#
#   open     plant.scene.json opens in the Scene Editor: the palette lists
#            the three built-in kinds, the stage has a WebGL canvas and a
#            label per node (or, in a window without WebGL, the notice)
#   place    click `tank` in the palette, then the ground: a third node of
#            kind tank is in the buffer, on the 0.1 m grid, and Ctrl+S
#            writes it to disk in the canonical form
#   undo     Ctrl+Z takes the node back out — text undo covers the gesture
#   pick     click a node: the panel shows its id; Delete removes it from
#            the buffer, and Ctrl+Z brings it back
#
# Everything is read from the webview DOM ([data-kind=palette-item], the
# .lbl labels the renderer floats over nodes, the Properties panel's id
# field) and from the file on disk. The stage needs WebGL in the webview;
# the Xvfb rig has no GPU, so VS Code's software fallback is what is under
# test here — if it is absent the editor must say so (.nogl), which is
# reported as a NOTE and the gesture verdicts are SKIPped, not failed.
set -euo pipefail
CHECK=21-scene-editor
source "$HOME/smoke/lib.sh"
export G_PACE=fast
source "$HOME/fixtures/gestures.sh"   # cdp / wait_js / click_el / g_click
rm -rf "$PROFILE"

ext_scaffold my-plant
F=$PROJ/plant.scene.json
cat >"$F" <<'JSON'
{
  "name": "Smoke plant",
  "camera": { "pos": [2.2, 1.8, 2.6], "target": [0.6, 0.1, 0] },
  "grid": { "pos": [0, 0, 0], "size": [4, 4], "cell": 0.1 },
  "nodes": [
    { "id": "T101", "kind": "tank", "tag": "T101", "label": "T-101", "pos": [1.2, 0, 0.4] },
    { "id": "P101", "kind": "pump", "tag": "P101", "label": "P-101", "pos": [0.2, 0, 0.2] }
  ]
}
JSON
git -C "$PROJ" add -A && git -C "$PROJ" commit -qm "a scene"

smoke_open "$PROJ" plant.scene.json
key Escape
hide_sidebar
[[ $(title) == "plant.scene.json"* ]] || { fail "VS Code did not open plant.scene.json: $(title)"; exit 1; }

palette() { wv '[...doc.querySelectorAll("[data-kind=palette-item]")].map(b => b.dataset.id).join(" ")'; }
labels()  { wv '[...doc.querySelectorAll(".stage .lbl b")].map(b => b.textContent.trim()).join(" ")'; }
panel_id() { wv 'doc.querySelector("aside[aria-label=Properties] input")?.value ?? ""'; }
nodes_in_buffer() { wv 'doc.querySelector("aside[aria-label=Properties]")?.textContent ?? ""'; }
tank_count() { grep -c '"kind": "tank"' "$F" || true; }
scene_log() { cat "$(ls -td "$PROFILE"/logs/*/ | head -1)"window*/exthost/*/*-nautilus\ scene.log 2>/dev/null; }

# ── open ───────────────────────────────────────────────────────────────────
if ! wait_js 'doc.querySelector("[data-kind=palette-item]")' 30; then
  png=$(shot no-editor)
  fail "plant.scene.json did not open in the Scene Editor (no palette after 30 s)" "$png"
  echo "--- nautilus scene channel ---"; scene_log | tail -20 || true
  exit 1
fi
if wait_js 'doc.querySelector(".stage .nogl")' 2; then
  png=$(shot nogl)
  info "this window has no WebGL: the editor shows its notice instead of a stage ($(wv 'doc.querySelector(".stage .nogl b")?.textContent'))" "$png"
  [[ $(palette) == "tank pump valve" ]] && pass "the palette still lists the built-in kinds: $(palette)" || fail "palette: '$(palette)'"
  skip "place / undo / pick need a stage to click — no WebGL in this window"
  exit 0
fi
wait_js 'doc.querySelectorAll(".stage .lbl").length >= 2' 20 || true
png=$(shot open)
if [[ $(palette) == "tank pump valve" ]] && wv 'doc.querySelector(".stage canvas")' | grep -q . && [[ $(labels) == *T-101* && $(labels) == *P-101* ]]; then
  pass "the scene opened in the editor: palette '$(palette)', a WebGL stage, labels '$(labels)'" "$png"
else
  fail "editor state after open: palette '$(palette)', canvas '$(wv 'doc.querySelector(".stage canvas") ? "yes" : "no"')', labels '$(labels)'" "$png"
  echo "--- nautilus scene channel ---"; scene_log | tail -20 || true
  exit 1
fi

# ── place ──────────────────────────────────────────────────────────────────
click_el '[...doc.querySelectorAll("[data-kind=palette-item]")].find(b => b.dataset.id === "tank")'
sleep 0.5
wv 'doc.querySelector("[data-kind=palette-item].armed")?.dataset.id' | grep -qx tank || { png=$(shot not-armed); fail "clicking the tank palette item did not arm it" "$png"; exit 1; }
# An empty spot of the ground: the canvas's lower-left quarter has no node in it.
p=$(cdp point '(() => { const r = doc.querySelector(".stage canvas").getBoundingClientRect(); return { x: r.left + r.width * 0.25, y: r.top + r.height * 0.8 }; })()') || { fail "no stage canvas to click"; exit 1; }
g_click $p 0.8
sleep 1.5
key ctrl+s; sleep 1.5
png=$(shot placed)
if [[ $(tank_count) == 2 ]] && grep -q '"id": "TANK1"' "$F" && grep -qE '"pos": \[-?[0-9]+(\.[0-9])?, 0, -?[0-9]+(\.[0-9])?\]' "$F"; then
  pass "a click on the ground placed TANK1 on the 0.1 m grid and Ctrl+S wrote it: $(grep -o '"id": "TANK1".*' "$F" | head -1 | cut -c1-80)" "$png"
else
  fail "after placing a tank the file has $(tank_count) tank node(s): $(tr '\n' ' ' <"$F" | cut -c1-300)" "$png"
  echo "--- nautilus scene channel ---"; scene_log | tail -20 || true
fi
wv 'doc.querySelector("[data-kind=palette-item].armed")' | grep -q . && warn "the tank stayed armed after the drop (expected to disarm)" || true

# ── undo ───────────────────────────────────────────────────────────────────
key ctrl+z; sleep 1.2
key ctrl+s; sleep 1.5
png=$(shot undone)
if [[ $(tank_count) == 1 ]] && ! grep -q TANK1 "$F"; then
  pass "Ctrl+Z took the placed node back out of the document" "$png"
else
  fail "after Ctrl+Z the file still has $(tank_count) tank node(s)" "$png"
fi

# ── pick ───────────────────────────────────────────────────────────────────
# A node is picked by clicking its model; its label floats over its top, so
# just under the label is the model.
p=$(cdp point '(() => { const l = [...doc.querySelectorAll(".stage .lbl")].find(l => l.textContent.includes("P-101")); if (!l) return null; const r = l.getBoundingClientRect(); return { x: r.left + r.width / 2, y: r.bottom + 36 }; })()') || { fail "no P-101 label on the stage to aim at"; exit 1; }
g_click $p 0.8
sleep 1
png=$(shot picked)
if [[ $(panel_id) == P101 ]]; then
  pass "a click on P-101 selected it: the panel shows id P101" "$png"
else
  fail "after clicking under P-101's label the panel shows '$(panel_id)'" "$png"
fi
key Delete; sleep 1.2
key ctrl+s; sleep 1.5
png=$(shot deleted)
if ! grep -q '"id": "P101"' "$F" && grep -q '"id": "T101"' "$F"; then
  pass "Delete removed P101 from the document and left T101" "$png"
else
  fail "after Delete the file reads: $(tr '\n' ' ' <"$F" | cut -c1-300)" "$png"
fi
key ctrl+z; sleep 1.2
key ctrl+s; sleep 1.5
grep -q '"id": "P101"' "$F" && pass "Ctrl+Z brought P101 back" || fail "Ctrl+Z after Delete did not restore P101"
