#!/usr/bin/env bash
# 20 — the component preview (componentPreview.ts): a {Name}.svelte open in
# the text editor, "Open Component Preview" in its editor title, and a pane
# beside it that renders the component for real, compiled with the
# PROJECT's own svelte from the BUFFER:
#
#   open         a .svelte's editor title has the button; the pane opens
#                beside and the island mounts (the component's own SVG in
#                the webview DOM) showing the component's default label
#   on type      an unsaved edit to that default shows in the pane, no save;
#                so does an edit inside <style> (injected CSS is keyed by
#                filename in Svelte 5 — the pane must drop the stale sheet)
#   props strip  adding `label` in the strip renders it at once and writes
#                {Name}.component.json next to the .svelte, preview.props
#   ports        Ports mode on, double-click the render's top edge: a dot,
#                a panel row and `ports: [p1]` in the same sidecar
#   ports-drag   drag that dot along the edge: it moves, the sidecar follows
#   error        a syntax error shows the banner OVER the last good render
#                (the island stays, dimmed); undoing it clears the banner
#   follow       opening another .svelte moves the pane to that component
#   sidecar      a {Name}.component.json opens as plain JSON; its title
#                button lands the pane on that component
#   builtin      Edit Component Ports… → Tank: the pane renders the kit's
#                Tank with its 4 default ports; "+ Add port" writes a
#                Tank.component.json override (5 ports) into the project
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

# ── on type, <style>: a CSS edit reaches the pane too ──────────────────────
# Svelte 5 keys injected styles by filename; the pane must drop the old
# <style> on every rebuild or the first stylesheet wins forever.
text_replace "$F" "font-style: normal;" "font-style: italic;"
if wait_js 'win.getComputedStyle(doc.querySelector(".stage [data-role=value]")).fontStyle === "italic"' 30; then
  pass "a <style> edit re-styled the island (value is italic now)" "$(shot on-type-css)"
else
  fail "the <style> edit did not reach the pane (font-style '$(wv 'win.getComputedStyle(doc.querySelector(".stage [data-role=value]")).fontStyle')', status '$(status)')" "$(shot on-type-css-missed)"
fi

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

# ── ports on the render: Ports mode, double-click the top edge ─────────────
# The sidecar's `ports` (the mimic editor's pipe connection points) are
# edited on the live render in the same pane; the gesture takes the pointer
# only in Ports mode so the component stays clickable otherwise.
click_el 'doc.querySelector("header [data-kind=ports-toggle]")' || fail "no ports toggle in the pane header"
dclick_el 'doc.querySelector(".stage svg.ports")' 0.5 0.04 || fail "the ports overlay is not on screen"
ports_on_disk() {
  python3 - "$SIDE" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
ps = d.get("ports") or []
sys.exit(0 if len(ps) == 1 and ps[0].get("name") == "p1" and ps[0].get("y") == 0 else 1)
PY
}
if wait_js 'doc.querySelectorAll(".stage svg.ports circle[data-kind=port]").length === 1' 10 \
   && wait_js '[...doc.querySelectorAll("aside [data-kind=port-name]")].some(i => i.value === "p1")' 10; then
  # The dot is optimistic; the host's write (and its save — the sidecar is
  # open in no tab) follows a beat behind.
  for _ in $(seq 1 20); do ports_on_disk && break; sleep 0.5; done
  ports_on_disk \
    && pass "a double-click on the render in Ports mode added port p1 (dot on the render, row in the panel, sidecar ports: $(python3 -c 'import json,sys; print(json.dumps(json.load(open(sys.argv[1])).get("ports")))' "$SIDE"))" "$(shot ports)" \
    || fail "the dot and row appeared but the sidecar's ports are not [p1 on the top edge]: $(tr -d '\n' <"$SIDE")"
else
  fail "double-clicking the render in Ports mode added no port (dots: $(wv 'doc.querySelectorAll(".stage svg.ports circle").length'), rows: $(wv 'doc.querySelectorAll("aside [data-kind=port-name]").length'))" "$(shot ports-missed)"
fi

# ── drag the dot: p1 slides along the top edge to its right quarter ────────
# A real press-carry-release (xdotool), since pointer capture + window
# pointermove is what a synthetic click can't exercise.
DOT='doc.querySelector(".stage svg.ports circle[data-kind=port]")'
read -r dx dy <<<"$(el_at "$DOT")"
read -r bx by bw bh _ <<<"$(el_box 'doc.querySelector(".stage svg.ports")')"
g_drag "$dx" "$dy" $((bx + bw * 3 / 4)) "$dy"
ports_dragged() {
  python3 - "$SIDE" <<'PY'
import json, sys
ps = json.load(open(sys.argv[1])).get("ports") or []
sys.exit(0 if len(ps) == 1 and ps[0]["name"] == "p1" and abs(ps[0]["x"] - 0.75) < 0.05 and ps[0]["y"] == 0 else 1)
PY
}
if wait_js '(() => { const c = doc.querySelector(".stage svg.ports circle[data-kind=port]"); const s = doc.querySelector(".stage svg.ports"); return c && s && +c.getAttribute("cx") > s.width.baseVal.value * 0.65; })()' 5; then
  for _ in $(seq 1 20); do ports_dragged && break; sleep 0.5; done
  ports_dragged \
    && pass "dragging the dot moved p1 to the top edge's right quarter (sidecar ports: $(python3 -c 'import json,sys; print(json.dumps(json.load(open(sys.argv[1])).get("ports")))' "$SIDE"))" "$(shot ports-dragged)" \
    || fail "the dot moved on screen but the sidecar did not follow: $(tr -d '\n' <"$SIDE")"
else
  fail "dragging the dot did not move it (cx: $(wv "$DOT"'?.getAttribute("cx")') of $(wv 'doc.querySelector(".stage svg.ports")?.getAttribute("width")'), sidecar: $(tr -d '\n' <"$SIDE"))" "$(shot ports-drag-missed)"
fi
click_el 'doc.querySelector("header [data-kind=ports-toggle]")' || true   # back off, so the next steps' clicks reach the editor as before

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

# ── sidecar: a .component.json is plain JSON with the preview button ───────
# The standalone sidecar editor is gone; the preview pane is the editor for
# the file. Open SmokeDial's sidecar (the strip created it above): a text
# editor (the Ports panel is not in a custom editor), the title button
# present, and a click on it moves the pane back to SmokeDial.
open_file SmokeDial.component.json 3
if [[ $(title) == "SmokeDial.component.json"* ]] && pg 'document.querySelector(".editor-group-container.active .monaco-editor") !== null' | grep -q true \
   && [[ -n $(pg "$(title_action_el 'Open Component Preview')") ]]; then
  page_click "$(title_action_el 'Open Component Preview')" 3
  if wait_js 'doc.querySelector(".stage [data-role=label]")' 30 && [[ $(wv 'doc.querySelector("header .title")?.textContent.trim()') == "SmokeDial.svelte" ]]; then
    pass "SmokeDial.component.json opened as plain JSON; its preview button moved the pane to SmokeDial (label '$(label)')" "$(shot sidecar)"
  else
    fail "the sidecar's preview button did not land the pane on SmokeDial (title '$(wv 'doc.querySelector("header .title")?.textContent.trim()')', status '$(status)')" "$(shot sidecar-missed)"
  fi
else
  fail "SmokeDial.component.json did not open as a plain text editor with a preview button (title '$(title)')" "$(shot sidecar-missed)"
fi

# ── builtin: Edit Component Ports… → Tank renders from the kit ─────────────
vs_cmd "nautilus: Edit Component Ports" 2
g_type "Tank"; sleep 0.8; key Return; sleep 3
if wait_js 'doc.querySelectorAll(".stage svg.ports circle[data-kind=port]").length === 4' 20 \
   && [[ $(status) == "built-in" ]]; then
  click_button "+ Add port" || true
  TANK_SIDE=
  for _ in $(seq 1 20); do
    TANK_SIDE=$(find "$PROJ" -name Tank.component.json -not -path "*/node_modules/*" | head -1)
    [[ -n $TANK_SIDE ]] && python3 -c 'import json,sys; sys.exit(0 if len(json.load(open(sys.argv[1])).get("ports") or []) == 5 else 1)' "$TANK_SIDE" && break
    TANK_SIDE=; sleep 0.5
  done
  if [[ -n $TANK_SIDE ]]; then
    pass "Edit Component Ports… → Tank renders the kit's Tank in the pane with its 4 default ports; + Add port wrote ${TANK_SIDE#$PROJ/} with 5 ports" "$(shot builtin)"
  else
    fail "Tank rendered with its default ports but + Add port wrote no 5-port Tank.component.json ($(find "$PROJ" -name Tank.component.json -not -path '*/node_modules/*' | head -1))" "$(shot builtin-missed)"
  fi
else
  fail "Edit Component Ports… → Tank did not render the built-in in the pane (dots: $(wv 'doc.querySelectorAll(".stage svg.ports circle").length'), status '$(status)', title '$(wv 'doc.querySelector("header .title")?.textContent.trim()')')" "$(shot builtin-missed)"
fi
