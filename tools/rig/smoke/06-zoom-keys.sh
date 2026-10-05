#!/usr/bin/env bash
# 06 — zoom keys stay in the diagram (PR #26). With focus in the Ladder and
# SFC diagrams, Ctrl+= / Ctrl+- / Ctrl+0 and Ctrl+wheel change the DIAGRAM's
# zoom (its % readout, bottom-left) and NOT VS Code's window zoom.
#
# Window zoom is read two ways: the profile sets window.zoomPerWindow false,
# so a window zoom is WRITTEN to settings.json as window.zoomLevel; and the
# UI chrome (tab strip, status bar) must keep its size: the workbench's
# zoom factor (devicePixelRatio) and the two bars' heights in device px, read
# from the DOM. A control at the end presses Ctrl+= in a text editor to
# prove the probe sees a real one.
#
# The diagram's zoom is its ZoomPane readout (.zpct, "120%"), read from the
# webview's DOM; the focusing click lands on empty canvas (elementFromPoint).
set -euo pipefail
CHECK=06-zoom-keys
source "$HOME/smoke/lib.sh"
export G_PACE=fast
source "$HOME/fixtures/gestures.sh"   # cdp eval / cdp page / g_click
rm -rf "$PROFILE"

zl() { grep -oP '^\s*"window.zoomLevel":\s*\K[-0-9.]+' "$PROFILE/User/settings.json"; }
# readout — the diagram's zoom readout ("" if none).
readout() { wv 'doc.querySelector(".zpane .zpct")?.textContent.trim() ?? ""'; }
# chrome — the workbench's zoom factor and the tab strip's and status bar's
# heights in device px: "dpr tabs status". A window zoom changes all three.
chrome() {
  pg '(() => { const k = window.devicePixelRatio, h = (e) => e ? Math.round(e.getBoundingClientRect().height * k) : -1; return k + " " + h(document.querySelector(".editor-group-container.active .tabs-and-actions-container, .editor-group-container.active .title")) + " " + h(document.getElementById("workbench.parts.statusbar")); })()'
}

one() { # <lang> <file>
  local lang=$1 file=$2
  smoke_open "$PROJ" "$file"
  key Escape; hide_sidebar
  vs_cmd "nautilus: Open as Diagram Editor" 8
  local z0; z0=$(zl)
  wait_js "$_G_READY" 10 || true
  click_canvas "" 0.6 || fail "$lang: no empty canvas to focus the diagram"
  local s0 s1 s2 s3 s4 r0 r1 r2 r3 r4 c0 c4
  c0=$(chrome)
  r0=$(readout); s0=$(shot "$lang-0")
  key ctrl+equal; key ctrl+equal; sleep 1; r1=$(readout); s1=$(shot "$lang-1-zoomin")
  key ctrl+minus; sleep 1; r2=$(readout); s2=$(shot "$lang-2-zoomout")
  key ctrl+0; sleep 1; r3=$(readout); s3=$(shot "$lang-3-fit")
  xdotool keydown ctrl; for _ in 1 2 3; do xdotool click 4; sleep 0.25; done; xdotool keyup ctrl; sleep 1
  r4=$(readout); s4=$(shot "$lang-4-wheel")
  c4=$(chrome)
  local step a b png k
  for step in "$r0 $r1 $s1 Ctrl+=" "$r1 $r2 $s2 Ctrl+-" "$r2 $r3 $s3 Ctrl+0" "$r3 $r4 $s4 Ctrl+wheel"; do
    read -r a b png k <<<"$step"
    if [[ $a == *% && $b == *% && $a != "$b" ]]; then
      pass "$lang: $k changed the diagram zoom (readout $a → $b)" "$png"
    else
      fail "$lang: $k did not change the diagram zoom (readout '$a' → '$b')" "$png"
    fi
  done
  [[ $(zl) == "$z0" ]] && pass "$lang: window.zoomLevel untouched ($z0) through Ctrl+= / - / 0 / wheel" \
    || fail "$lang: VS Code's window zoom moved: window.zoomLevel $z0 → $(zl)"
  read -r k a b <<<"$c0"
  if [[ -n $c0 && $c0 == "$c4" && $a != -1 && $b != -1 ]]; then
    pass "$lang: UI chrome (tab strip, status bar) the same size before/after (zoom factor $k, tab strip $a px, status bar $b px)" "$s4"
  else
    fail "$lang: UI chrome changed size (zoom factor, tab strip px, status bar px: '$c0' → '$c4')" "$s4"
  fi
}

ext_scaffold my-plant
one Ladder interlocks.ld
ext_fixture tank-batch
one SFC batch.sfc

# Control: the probe DOES see a real window zoom.
vs_cmd "View: Close All Editors" 1
open_file plant.st 3
c0=$(chrome); z0=$(zl); key ctrl+equal; sleep 1.5; z1=$(zl); c1=$(chrome)
png=$(shot control-window-zoom)
[[ $z1 != "$z0" ]] && pass "control: Ctrl+= in a TEXT editor moves window.zoomLevel ($z0 → $z1), so the probe works" "$png" \
  || fail "control: Ctrl+= in a text editor did not register — the probe is blind" "$png"
[[ $c1 != "$c0" ]] && info "control: the chrome probe sees it too (zoom factor, tab strip px, status bar px: $c0 → $c1)" "$png" \
  || warn "control: a real window zoom left the chrome probe unchanged ($c0 → $c1) — the UI-chrome rows above prove nothing" "$png"
key ctrl+minus; sleep 1
