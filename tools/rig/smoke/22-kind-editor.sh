#!/usr/bin/env bash
# 22 — the 3D kind editor (kindEditor.ts): a *.kind.json — a scene's
# `kinds` entry in a file of its own — opens, by default, as the kind on a
# turntable in the hmi-3d renderer with its fields and drives beside it,
# and every change is one text edit:
#
#   open     pump.kind.json opens in the Kind Editor: the panel shows the
#            kind's name, the stage has a WebGL canvas (or, without WebGL,
#            the notice), and the drive row lists the file's drive
#   drive    "Add drive" puts a second drive in the buffer, on one line,
#            and Ctrl+S writes it to disk in the canonical form
#   undo     Ctrl+Z takes the drive back out — text undo covers the gesture
#   field    a typed `type` lands in the file
#   pick     a click on the model picks a part — through the tank's glass
#            and water to the opaque Base, the way the scene picks — the
#            panel names it, and "Add drive on Base" puts a drive on it
#   scene    the scene that names the file opens in the Scene Editor with
#            the kind in its palette captioned as a file, and no banner
#
# The model here is the package's own tank.glb (copied into hmi/static/
# models/ the way an app does), so the mesh menu lists its Fluid. Same
# WebGL arrangement as check 21 (VSCODE_ARGS → SwiftShader); without it
# the editor must say so (.nogl): NOTE + the gesture verdicts SKIPped.
set -euo pipefail
CHECK=22-kind-editor
source "$HOME/smoke/lib.sh"
export G_PACE=fast
source "$HOME/fixtures/gestures.sh"   # cdp / wait_js / click_el / g_click
rm -rf "$PROFILE"

ext_scaffold my-plant
mkdir -p "$PROJ/hmi/src/lib/kinds" "$PROJ/hmi/static/models"
# The built-in tank's model ships inside the extension's hmi-3d bundle
# source; the rig image carries the repo's hmi-3d/models beside the smoke
# fixtures (build.sh), so copy it from there.
cp "$HOME/fixtures/models/tank.glb" "$PROJ/hmi/static/models/tank.glb"
F=$PROJ/hmi/src/lib/kinds/wet-well.kind.json
cat >"$F" <<'JSON'
{
  "type": "Tank",
  "model": "models/tank.glb",
  "status": "{Level:1} %",
  "drive": [
    { "mesh": "Fluid", "scale": { "axis": "y", "to": { "bind": "Level", "scale": 0.0039, "min": 0.005, "max": 0.39 } } }
  ]
}
JSON
S=$PROJ/plant.scene.json
cat >"$S" <<'JSON'
{
  "name": "Smoke plant",
  "kinds": { "wet-well": "hmi/src/lib/kinds/wet-well.kind.json" },
  "camera": { "pos": [2.2, 1.8, 2.6], "target": [0.6, 0.1, 0] },
  "grid": { "pos": [0, 0, 0], "size": [4, 4], "cell": 0.1 },
  "nodes": [
    { "id": "WW1", "kind": "wet-well", "label": "WW-1", "pos": [0.6, 0, 0.2], "bind": { "level": "T101.Level" } }
  ]
}
JSON
git -C "$PROJ" add -A && git -C "$PROJ" commit -qm "a kind and a scene"

export VSCODE_ARGS="--enable-unsafe-swiftshader=1 --use-angle=swiftshader --ignore-gpu-blocklist=1"
smoke_open "$PROJ" sim.st
key Escape
hide_sidebar
open_file wet-well.kind.json 10
[[ $(title) == "wet-well.kind.json"* ]] || { fail "VS Code did not open wet-well.kind.json: $(title)"; exit 1; }

kind_name() { wv 'doc.querySelector("aside[aria-label=Kind] .fact code")?.textContent.trim() ?? ""'; }
drive_count() { wv 'doc.querySelectorAll("[data-kind=drive-row]").length'; }
first_mesh() { wv 'doc.querySelector("[data-kind=drive-row] [aria-label=mesh]")?.value ?? ""'; }
drives_in_file() { grep -c '"mesh":' "$F" || true; }
kind_log() { cat "$(ls -td "$PROFILE"/logs/*/ | head -1)"window*/exthost/*/*-nautilus\ kind.log 2>/dev/null; }

# ── open ───────────────────────────────────────────────────────────────────
if ! wait_js 'doc.querySelector("aside[aria-label=Kind]")' 30; then
  png=$(shot no-editor)
  fail "wet-well.kind.json did not open in the Kind Editor (no panel after 30 s)" "$png"
  echo "--- nautilus kind channel ---"; kind_log | tail -20 || true
  echo "--- exthost errors ---"; exthost_log | grep -iE "error|exception|kind" | tail -20 || true
  exit 1
fi
if wait_js 'doc.querySelector(".stage .nogl")' 2; then
  png=$(shot nogl)
  info "this window has no WebGL: the editor shows its notice instead of a stage" "$png"
  [[ $(kind_name) == "wet-well" ]] && pass "the panel still names the kind: $(kind_name)" || fail "panel name: '$(kind_name)'"
  skip "drive / undo / field need a stage — no WebGL in this window"
  exit 0
fi
wait_js 'doc.querySelector("header .state")?.textContent.includes("model")' 20 || true
png=$(shot open)
if [[ $(kind_name) == "wet-well" ]] && wv 'doc.querySelector(".stage canvas")' | grep -q . && [[ $(drive_count) == 1 ]] && [[ $(first_mesh) == "Fluid" ]]; then
  pass "the kind opened in the editor: name '$(kind_name)', a WebGL stage, 1 drive on $(first_mesh); state '$(wv 'doc.querySelector("header .state")?.textContent.trim()')'" "$png"
else
  fail "editor state after open: name '$(kind_name)', canvas '$(wv 'doc.querySelector(".stage canvas") ? "yes" : "no"')', drives $(drive_count), first mesh '$(first_mesh)'" "$png"
  echo "--- nautilus kind channel ---"; kind_log | tail -20 || true
  exit 1
fi

# ── drive ──────────────────────────────────────────────────────────────────
click_el '[...doc.querySelectorAll("aside[aria-label=Kind] button")].find(b => b.textContent.trim().startsWith("Add drive"))'
sleep 1.2
key ctrl+s; sleep 1.5
png=$(shot drive-added)
if [[ $(drives_in_file) == 2 ]] && [[ $(grep -c '^    { "mesh": ' "$F") == 2 ]]; then
  pass "Add drive put a second drive in the file, one per line, and Ctrl+S wrote it: $(grep '"mesh"' "$F" | tail -1 | cut -c1-90)" "$png"
else
  fail "after Add drive the file has $(drives_in_file) drive(s): $(tr '\n' ' ' <"$F" | cut -c1-300)" "$png"
  echo "--- nautilus kind channel ---"; kind_log | tail -20 || true
fi

# ── undo ───────────────────────────────────────────────────────────────────
key ctrl+z; sleep 1.2
key ctrl+s; sleep 1.5
png=$(shot undone)
if [[ $(drives_in_file) == 1 ]]; then
  pass "Ctrl+Z took the added drive back out of the file" "$png"
else
  fail "after Ctrl+Z the file still has $(drives_in_file) drive(s)" "$png"
fi

# ── field ──────────────────────────────────────────────────────────────────
click_el 'doc.querySelector("aside[aria-label=Kind] [aria-label=type]")'
key ctrl+a
xdotool type --delay 40 "WetWell"; sleep 0.6
key Tab
sleep 1.2
key ctrl+s; sleep 1.5
png=$(shot typed)
if grep -q '"type": "WetWell"' "$F"; then
  pass "a typed type landed in the file as one edit" "$png"
else
  fail "after typing a type the file reads: $(tr '\n' ' ' <"$F" | cut -c1-200)" "$png"
fi

# ── pick ───────────────────────────────────────────────────────────────────
# Hold the turntable, then click where the stage says the Fluid is: the
# shell is glass and the fluid water, so the pick goes through both to the
# first opaque part, the Base (resolvePick — a second click would cycle on
# to the shell, then the water).
click_el '[...doc.querySelectorAll("header button")].find(b => b.textContent.trim().startsWith("Turntable"))'
sleep 0.5
if ! p=$(cdp point 'win.__nxKind ? win.__nxKind.partAt("Fluid") : null'); then
  png=$(shot no-part)
  fail "the stage could not say where the Fluid is" "$png"
else
  g_click $p 0.8
  picked=$(wv 'doc.querySelector("[data-kind=picked] b")?.textContent.trim() ?? ""')
  if [[ $picked == "Base" ]]; then
    click_el '[...doc.querySelectorAll("aside[aria-label=Kind] button")].find(b => b.textContent.trim().startsWith("Add drive"))'
    sleep 1.2
    key ctrl+s; sleep 1.5
    png=$(shot picked)
    if [[ $(grep -c '"mesh": "Base"' "$F") == 1 ]] && [[ $(drives_in_file) == 2 ]]; then
      pass "a click on the tank picked the Base through the glass and the water, and Add drive put a drive on it: $(grep '"mesh"' "$F" | tail -1 | cut -c1-90)" "$png"
    else
      fail "after picking the Base, Add drive wrote: $(grep '"mesh"' "$F" | tr '\n' ' ' | cut -c1-200)" "$png"
    fi
    key ctrl+z; sleep 1.2; key ctrl+s; sleep 1.0
  else
    png=$(shot not-picked)
    fail "a click on the tank at $p picked '$picked', not the Base behind the water" "$png"
  fi
fi

# ── scene ──────────────────────────────────────────────────────────────────
open_file plant.scene.json 10
if wait_js 'doc.querySelector("[data-kind=palette-item][data-id=wet-well] .cap")?.textContent.includes("file")' 30; then
  png=$(shot scene)
  if wv 'doc.querySelector(".warn")' | grep -q .; then
    fail "the scene shows a banner with the kind file in place: $(wv 'doc.querySelector(".warn")?.textContent.trim()' | cut -c1-200)" "$png"
  else
    pass "the scene that names the file opened with wet-well captioned '$(wv 'doc.querySelector("[data-kind=palette-item][data-id=wet-well] .cap")?.textContent.trim()')' and no banner" "$png"
  fi
else
  png=$(shot scene-no-file)
  fail "the scene's palette does not caption wet-well as a file: '$(wv 'doc.querySelector("[data-kind=palette-item][data-id=wet-well] .cap")?.textContent.trim() ?? ""')'" "$png"
fi
