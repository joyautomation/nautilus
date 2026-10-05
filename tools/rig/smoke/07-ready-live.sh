#!/usr/bin/env bash
# 07 — the ready handshake (webviewReady.ts): with `naut run` going, every
# editor shows the LIVE pill and live values the first time it opens, and
# again after "Developer: Reload Webviews" (the page remounts; the host
# replays the latest state on the page's `ready`).
#
# Live is read from the editor's DOM: the header pill is "● live" (class
# on; the mimic's pill drops .off), and the canvas carries fresh live
# values — FBD's value pills (.nx-pill.val, .off when stale), Ladder's
# text.liveval, the SFC's active steps (g.step.active).
set -euo pipefail
CHECK=07-ready-live
source "$HOME/smoke/lib.sh"
export G_PACE=fast
source "$HOME/fixtures/gestures.sh"   # cdp eval / editor_groups
rm -rf "$PROFILE"

# live_read — the active editor's "<pill state>|<pill text>|<live values>":
# state on / off / none (no pill), values the count of fresh value marks.
live_read() {
  wv '(() => {
    const p = doc.querySelector(".bar button.livepill, header button.live.nx-pill");
    const on = p && (p.classList.contains("livepill") ? p.classList.contains("on") : !p.classList.contains("off"));
    const vals = doc.querySelectorAll(".nx-pill.val:not(.off)").length
      + [...doc.querySelectorAll("text.liveval")].filter((t) => t.textContent.trim()).length
      + doc.querySelectorAll("g.step.active").length;
    return (p ? (on ? "on" : "off") : "none") + "|" + (p ? p.textContent.trim() : "") + "|" + vals;
  })()'
}

# live_ok <label> <png> <min live values> — pill live, and (if asked) values
live_ok() {
  local what=$1 png=$2 vmin=$3 r st txt v
  r=$(live_read); IFS='|' read -r st txt v <<<"$r"
  if [[ $st == on && $txt == *live* ]] && (( ${v:-0} >= vmin )); then
    pass "$what: live pill + values ('$txt', $v live value(s))" "$png"
  else
    fail "$what: not live (pill ${st:-?} '${txt}', ${v:-0} live value(s); want '● live' / ≥$vmin)" "$png"
  fi
}
# wait_live <min live values> — up to 10 s for the pill and values.
wait_live() { local r st txt v; r=$(live_read); IFS='|' read -r st txt v <<<"$r"; [[ $st == on ]] && (( ${v:-0} >= $1 )); }

# check_editor <label> <how to open: a command> <min live values>
check_editor() {
  local lbl=$1 open=$2 vmin=$3 png
  vs_cmd "View: Close All Editors" 1
  eval "$open"
  sleep 6
  wait_for 10 wait_live "$vmin" || true
  png=$(shot "$lbl-first-open")
  live_ok "$lbl, first open" "$png" "$vmin"
  vs_cmd "Developer: Reload Webviews" 8
  wait_for 10 wait_live "$vmin" || true
  png=$(shot "$lbl-reloaded")
  live_ok "$lbl, after Reload Webviews" "$png" "$vmin"
  if [[ $lbl == FBD* ]] && ! js_true "$_G_READY"; then
    info "$lbl: the webview is BLANK after the reload (no header, no canvas) — App.svelte:388 show(saved) assigns fbdSource (declared let at :552) before its declaration runs: a TDZ throw aborts the mount, so 'ready' (:394) is never posted" "$png"
  fi
}

ext_scaffold my-plant
cp "$FIX/heated-tank.mimic.json" "$PROJ/"
PORT=$(free_port 18080 18081 18082 18083)
point_extension_at "$PROJ" "$PORT"
start_controller "$PROJ" "$PORT"
sleep 4
api /api/state >/dev/null && pass "naut run on :$PORT, /api/state answers" || { fail "controller not answering on :$PORT"; exit 1; }

smoke_open "$PROJ" sim.st
key Escape; hide_sidebar
sleep 5
png=$(shot st-inline)
# ST: the inline pills are editor decorations (injected text, its content
# in a ::after), and the status bar item says "nautilus: live".
p=$(pg '[...document.querySelectorAll(".editor-group-container.active .view-lines span[class]")].filter((e) => { const c = getComputedStyle(e, "::after").content; return c && c !== "none" && c !== "normal" && c !== "\"\""; }).length')
sb=$(pg '[...document.querySelectorAll("#workbench\\.parts\\.statusbar .statusbar-item")].map((e) => e.textContent.trim()).find((t) => /^nautilus: /.test(t)) || ""')
info "ST text: inline value pills (${p:-?} injected after-text decorations) — status bar '${sb}'" "$png"

check_editor FBD-diagram 'open_file program.fbd 3; vs_cmd "nautilus: Open as Diagram Editor" 8' 1
check_editor FBD-preview 'open_file program.fbd 3; vs_cmd "nautilus: Open FBD Diagram Preview" 8; xdotool key --clearmodifiers ctrl+1; sleep 0.5; xdotool key --clearmodifiers ctrl+w; sleep 2' 1
check_editor Ladder-diagram 'open_file interlocks.ld 3; vs_cmd "nautilus: Open as Diagram Editor" 8' 0
check_editor Mimic 'open_file heated-tank.mimic.json 8' 0

# A diagram editor RESTORED by a window reload (the same saved-state path
# Reload Webviews takes, and what reopening VS Code does).
#
# ONE editor group into the reload, or the restored editor is not the one
# the DOM read finds (cdp.js reads the LARGEST visible webview). By this point quick-open brings program.fbd up as the
# diagram already (VS Code remembers the editor last used for it), so
# "nautilus: Open as Diagram Editor" is not offered for it and the palette's
# fuzzy match runs the "Open … Diagram Preview" command instead: a preview
# BESIDE the diagram (2026-09-25 frames). A preview panel is not restored
# by a window reload (no serializer), which leaves an empty right group: the
# restored diagram sits in the left half, and the post-reload quick-open can land in the empty group and open
# a second copy there (Ladder). So: focus group 1 and close every other
# group's editors (empty groups close with them) before reloading. The
# assertion is unchanged: a restored-but-blank editor paints no pill and no
# values and still FAILs (proved against 9812ad6, 2026-09-25).
for pair in "FBD program.fbd 1" "Ladder interlocks.ld 0"; do
  read -r lbl file vmin <<<"$pair"
  vs_cmd "View: Close All Editor Groups" 1
  open_file "$file" 3; vs_cmd "nautilus: Open as Diagram Editor" 8
  xdotool key --clearmodifiers ctrl+1; sleep 0.5
  vs_cmd "View: Close Editors in Other Groups" 2
  vs_cmd "Developer: Reload Window" 15
  open_file "$file" 3        # activates the restored diagram tab
  sleep 6
  wait_for 10 wait_live "$vmin" || true
  png=$(shot "$lbl-window-reload")
  # More than one editor group means the layout split anyway: say so, so a
  # FAIL below is read as the rig's, not the extension's.
  g=$(editor_groups 2>/dev/null || echo "?")
  [[ $g == 1 ]] || info "$lbl: $g editor groups after the reload — more than one editor group after the reload" "$png"
  live_ok "$lbl diagram editor restored by Reload Window" "$png" "$vmin"
done

# SFC: tank-batch, its own controller.
kill_controllers_for "$PROJ"; [[ -n ${CONTROLLER_PID:-} ]] && kill "$CONTROLLER_PID" 2>/dev/null || true
ext_fixture tank-batch
point_extension_at "$PROJ" "$PORT"
start_controller "$PROJ" "$PORT"
sleep 3
smoke_open "$PROJ" batch.sfc
key Escape; hide_sidebar
check_editor SFC-diagram 'open_file batch.sfc 3; vs_cmd "nautilus: Open as Diagram Editor" 8' 0
