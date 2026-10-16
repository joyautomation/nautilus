#!/usr/bin/env bash
# 20 — the component preview (componentPreview.ts): a {Name}.svelte open in
# the text editor, "Open Component Preview" in its editor title, and a pane
# beside it that renders the component for real, compiled with the
# PROJECT's own svelte from the BUFFER:
#
#   open         a .svelte's editor title has the button; the pane opens
#                beside and the island mounts (the component's own SVG in
#                the webview DOM) showing the component's default label
#   on type      an unsaved edit to that default shows in the pane, no save
#   props strip  adding `label` in the strip renders it at once and writes
#                {Name}.component.json next to the .svelte, preview.props
#   error        a syntax error shows the banner OVER the last good render
#                (the island stays, dimmed); undoing it clears the banner
#   follow       opening another .svelte moves the pane to that component
#
# Everything is read from the DOM (the island's data-role text, header
# .status, .banner, .stage.stale) and from the sidecar on disk. The project's
# svelte comes from the tarball build.sh assembles (the container has no
# npm); without it the check is a SKIP, not a FAIL.
set -euo pipefail
CHECK=20-component-preview
source "$HOME/smoke/lib.sh"
export G_PACE=fast
source "$HOME/fixtures/gestures.sh"   # cdp / wait_js / click_el / g_type
rm -rf "$PROFILE"

TGZ=$HOME/smoke-assets/svelte-node_modules.tgz
[[ -f $TGZ ]] || { skip "no svelte-node_modules.tgz beside the VSIX (build.sh assembles it) — nothing to compile with"; exit 0; }

ext_scaffold my-plant
mkdir -p "$PROJ/hmi"
cp -r "$FIX/component-preview/hmi/." "$PROJ/hmi/"
tar -C "$PROJ/hmi" -xzf "$TGZ"
git -C "$PROJ" add -A && git -C "$PROJ" commit -qm "hmi: smoke components"
F=$PROJ/hmi/src/lib/SmokeDial.svelte
SIDE=$PROJ/hmi/src/lib/SmokeDial.component.json
[[ -f $PROJ/hmi/node_modules/svelte/package.json ]] || { fail "the svelte tarball did not unpack into hmi/node_modules"; exit 1; }

smoke_open "$PROJ" hmi/src/lib/SmokeDial.svelte
key Escape
vs_cmd "Notifications: Clear All Notifications" 1   # a "Svelte extension recommended" toast would sit over the strip
hide_sidebar
[[ $(title) == "SmokeDial.svelte"* ]] || { fail "VS Code did not open SmokeDial.svelte: $(title)"; exit 1; }

# The extension's "nautilus preview" output channel (componentPreview.ts's logPreview).
preview_log() { cat "$(ls -td "$PROFILE"/logs/*/ | head -1)"window*/exthost/*/*-nautilus\ preview.log 2>/dev/null; }
label() { wv 'doc.querySelector(".stage [data-role=label]")?.textContent.trim() ?? ""'; }
status() { wv 'doc.querySelector("header .status")?.textContent.trim() ?? ""'; }
banner() { wv 'doc.querySelector(".banner")?.textContent.trim() ?? ""'; }

# ── open: the title button, the pane, the first render ─────────────────────
page_hover "$(title_action_el 'Open Component Preview')" || true
png=$(shot title-button)
page_click "$(title_action_el 'Open Component Preview')" 4 \
  || { fail "no 'Open Component Preview' button in a .svelte's editor title" "$png"; exit 1; }
pass "a .svelte's editor title offers Open Component Preview" "$png"
# The first compile is cold (esbuild + the project's svelte/compiler).
if wait_js 'doc.querySelector(".stage [data-role=label]")' 45; then
  png=$(shot rendered)
  [[ $(label) == dial ]] \
    && pass "the pane opened beside and rendered the component with the project's svelte: label 'dial', status '$(status)'" "$png" \
    || fail "the island rendered but shows '$(label)', not the component's default 'dial'" "$png"
else
  png=$(shot no-render)
  fail "nothing rendered in the pane after 45 s (status '$(status)'; banner '$(banner)')" "$png"
  echo "--- nautilus preview channel ---"; preview_log | tail -30 || true
  echo "--- exthost log (tail) ---"; exthost_log | tail -20 || true
  exit 1
fi

# ── on type: an unsaved edit reaches the pane ──────────────────────────────
key ctrl+1   # focus the text (the pane opened with preserveFocus, but be sure)
text_replace "$F" "label = 'dial'" "label = 'dial v2'"
[[ $(title) == "● SmokeDial.svelte"* ]] || fail "the text edit did not register ($(title))"
if wait_js 'doc.querySelector(".stage [data-role=label]")?.textContent.trim() === "dial v2"' 30; then
  pass "the pane re-rendered from the UNSAVED buffer: label 'dial v2' ($(title))" "$(shot on-type)"
else
  fail "the pane did not follow the unsaved edit (label '$(label)', status '$(status)', banner '$(banner)')" "$(shot on-type-missed)"
fi
grep -qF "dial v2" "$F" && fail "the edit reached disk without a save" || true

# ── the props strip: renders at once, writes the sidecar ───────────────────
click_el 'doc.querySelector("form.add input[placeholder=prop]")' || fail "no prop-name field in the props strip"
g_type label; g_key Tab; g_type "From strip"; g_key Return; sleep 2
if wait_js 'doc.querySelector(".stage [data-role=label]")?.textContent.trim() === "From strip"' 10; then
  pass "a prop added in the strip renders at once (label 'From strip')" "$(shot strip)"
else
  fail "the strip's prop did not reach the island (label '$(label)')" "$(shot strip-missed)"
fi
if wait_for 10 test -f "$SIDE"; then
  python3 - "$SIDE" <<'PY' \
    && pass "SmokeDial.component.json was created next to the .svelte with preview.props.label = 'From strip'" \
    || fail "the sidecar's preview.props is not {label: 'From strip'}: $(tr -d '\n' <"$SIDE")"
import json, sys
d = json.load(open(sys.argv[1]))
sys.exit(0 if d.get("preview", {}).get("props", {}).get("label") == "From strip" else 1)
PY
else
  fail "no SmokeDial.component.json appeared next to the .svelte"
fi

# ── a syntax error: banner over the last render; undo clears it ───────────
key ctrl+1
text_replace "$F" "{label}</text>" "{label</text>"
if wait_js 'doc.querySelector(".banner") && doc.querySelector(".stage.stale [data-role=label]")' 30; then
  pass "a syntax error shows the banner over the last good render (island still there, dimmed): $(banner | head -c 80)" "$(shot error)"
else
  fail "no error banner over a kept render after a syntax error (banner '$(banner)', status '$(status)')" "$(shot error-missed)"
fi
cleared=
for _ in 1 2 3; do
  key ctrl+z; sleep 1
  wait_js '!doc.querySelector(".banner") && doc.querySelector(".stage:not(.stale) [data-role=label]")' 12 && { cleared=1; break; }
done
[[ -n $cleared ]] && pass "undoing the error clears the banner and the render is live again (label '$(label)')" "$(shot undone)" \
  || fail "the banner stayed after undo (banner '$(banner)')" "$(shot undone-missed)"

# ── follow: another .svelte moves the pane ─────────────────────────────────
open_file SmokePill.svelte 3
if wait_js 'doc.querySelector(".stage [data-role=pill]")' 30; then
  pass "opening SmokePill.svelte moved the pane to it (pill '$(wv 'doc.querySelector(".stage [data-role=pill]")?.textContent.trim()')')" "$(shot follow)"
else
  fail "the pane did not follow to SmokePill.svelte (status '$(status)')" "$(shot follow-missed)"
fi
