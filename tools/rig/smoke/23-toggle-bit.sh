#!/usr/bin/env bash
# 23 — Toggle Bit, and Set Live Value / Force from the ladder, FBD and SFC
# diagrams, in a real desktop VS Code (gesture inventory C29, L32, L33, F23,
# F24, S32, S33; C06 and X43's diagram entry points):
#
#   a. Ctrl+T on a selected contact flips its BOOL tag on the controller:
#      StartPB FALSE → TRUE, and the seal-in latches RunCmd. This is the
#      workbench keybinding (package.json "keybindings"), reached through
#      the webview's key forwarding — the webview itself posts nothing on
#      Ctrl+T (gesture-harness/ld-live.test.mjs).
#   b. T, the diagram's own one-letter key, flips it back; RunCmd stays
#      sealed in.
#   c. Right-click a contact: the menu offers Toggle Bit, Set Live Value…
#      and Force… for its tag; Toggle Bit on StopPB (NC) drops RunCmd out.
#   d. The palette's Set Live Value… with a diagram element selected asks
#      for THAT tag (StopPB), not a text cursor's.
#   e. Force… from the element's menu forces StartPB (confirm modal); Ctrl+T
#      on it is then refused ("is forced"), the value unchanged; Unforce
#      (offered while forces are active) removes it.
#   f. FBD (Panel.fbd): Ctrl+T on the selected LampTest chip flips it and
#      the lamp follows; its right-click menu's Set Live Value… writes it
#      back.
#   g. SFC (Fill.sfc): T on the selected transition `:= FillGo` flips
#      FillGo and the chart moves to Filling (FillValve on); the transition's
#      menu offers Toggle Bit beside Fire Transition; the FillValve action's
#      menu offers the live actions; Ctrl+T on the transition flips it back.
#
# Project: fixtures/toggle-bit (setpoint inputs, so a write sticks).
# Assertions read /api/state, /api/forces, the extension's test state
# (NAUTILUS_TEST_STATE) and the workbench DOM over CDP, not pixels. Menus:
# window.menuStyle custom, so the webview's context menu is workbench DOM.
set -euo pipefail
CHECK=23-toggle-bit
source "$HOME/smoke/lib.sh"
source "$HOME/fixtures/gestures.sh"
G_PACE=fast
rm -rf "$PROFILE"

export NAUTILUS_TEST_STATE=$OUT_DIR/$CHECK-state.json
rm -f "$NAUTILUS_TEST_STATE"

# ── helpers ────────────────────────────────────────────────────────────────
tagv() {
  api /api/state | python3 -c '
import sys, json
d = json.load(sys.stdin); print(json.dumps(d.get("tags", {}).get(sys.argv[1])))' "$1"
}
tag_is() { [[ $(tagv "$1") == "$2" ]]; }
forces() {
  api /api/forces | python3 -c '
import sys, json
print(" ".join("%s=%s" % (f["name"], json.dumps(f["value"])) for f in json.load(sys.stdin)["forces"]))'
}
is_forced() { [[ " $(forces) " == *" $1="* ]]; }
not_forced() { ! is_forced "$1"; }
tstate() {
  python3 - "$NAUTILUS_TEST_STATE" "$1" <<'PY'
import json, sys
try: s = json.load(open(sys.argv[1]))
except Exception: s = {}
print(eval(sys.argv[2]))
PY
}
last_note() { tstate '(s.get("lastNotification") or {}).get("text")'; }
poll() { local s=$1 i; shift; for ((i = 0; i < s * 4; i++)); do "$@" && return 0; sleep 0.25; done; return 1; }

DEEP_JS='const deep = (sel) => { const out = [...document.querySelectorAll(sel)]; for (const h of document.querySelectorAll("*")) if (h.shadowRoot) out.push(...h.shadowRoot.querySelectorAll(sel)); return out; };'
menu_item_el() { printf '(() => { %s return deep(".monaco-menu .action-item a.action-menu-item, .monaco-menu .action-item .action-label").find(a => a.offsetParent !== null && /%s/.test(a.textContent.trim())) || null; })()' "$DEEP_JS" "$1"; }
menu_items() { pg "(() => { $DEEP_JS return deep('.monaco-menu .action-item .action-label').filter(a => a.offsetParent !== null).map(a => a.textContent.trim()).join(' | '); })()"; }
click_menu_item() { local b; b=$(page_el_box "$(menu_item_el "$1")") || return 1; read -r mx my mw mh <<<"$b"; g_click $((mx + mw / 2)) $((my + mh / 2)) 1; }
input_title() {
  cdp page '(() => { const w = document.querySelector(".quick-input-widget"); if (!w || w.style.display === "none" || !w.offsetParent) return ""; return (w.querySelector(".quick-input-title")?.textContent || w.querySelector(".quick-input-box input")?.getAttribute("placeholder") || "").trim(); })()' | python3 -c 'import sys,json; print(json.load(sys.stdin) or "")'
}
type_in_input() {
  local t= i
  for ((i = 0; i < 20; i++)); do t=$(input_title); [[ -n $t ]] && break; sleep 0.25; done
  [[ $t == *"$1"* ]] || { echo "input '$t', want *$1*"; return 1; }
  g_key ctrl+a; g_type "$2"; g_key Return
  echo "$t"
}
confirm() {
  wait_for 5 dialog_up || { echo "no confirmation modal"; return 1; }
  local m; m=$(dialog_message)
  page_click_button "^$1\$" 1.5 || { echo "modal without '$1' ($(dialog_buttons)): $m"; return 1; }
  echo "$m"
}
# The ladder element for a tag in rung "seal" (contacts and coils carry
# data-xref = their tag, data-rung = their rung).
EL() { printf 'doc.querySelector(%s)' "$(_q "[data-rung=\"seal\"][data-xref=\"$1\"]")"; }
select_el() { click_el "$(EL "$1")"; }
right_click_js() { local p; p=$(el_at "$1") || return 1; read -r x y <<<"$p"; xdotool mousemove --window "$WIN" "$x" "$y"; sleep 0.3; xdotool click 3; sleep 1.2; }
right_click_el() { right_click_js "$(EL "$1")"; }
# FBD chips and SFC association rows, by their testability attributes.
FBD_CHIP() { printf 'doc.querySelector(%s)' "$(_q "[data-kind=\"chip\"][data-id=\"v:$1\"]")"; }
SFC_ASSOC() { printf 'doc.querySelector(%s)?.querySelector(".assoctarget")' "$(_q "[data-kind=\"assoc\"][data-id=\"st:$1\"]")"; }
live_items() { local i=$1; [[ $i == *"Toggle Bit"* && $i == *"Set Live Value"* && $i == *"Force"* ]]; }

# ── project + controller ────────────────────────────────────────────────────
ext_fixture toggle-bit
PORT=$(free_port 18098 18099 18100 18101)
point_extension_at "$PROJ" "$PORT"
start_controller "$PROJ" "$PORT"
sleep 3
tag_is StopPB true && pass "naut run on :$PORT: StartPB $(tagv StartPB), StopPB $(tagv StopPB), RunCmd $(tagv RunCmd)" \
  || { fail "controller on :$PORT: StopPB $(tagv StopPB), want true"; exit 1; }

EXTRA_SETTINGS='"window.menuStyle": "custom"' smoke_open "$PROJ" MainProgram.ld
key Escape; hide_sidebar
sleep 3
ed_open_diagram MainProgram.ld || { fail "MainProgram.ld did not open as a ladder diagram" "$(shot open)"; exit 1; }
sleep 3
connected() { [[ $(tstate 's.get("connected")') == True ]]; }
poll 15 connected || info "test state never said connected ($(tstate 's.get("connected")'))"

# ── a: Ctrl+T on a selected contact ─────────────────────────────────────────
if select_el StartPB; then
  g_key ctrl+t
  if poll 3 tag_is StartPB true; then
    poll 3 tag_is RunCmd true && pass "a: StartPB selected, Ctrl+T → StartPB true, the seal-in latched RunCmd ($(last_note))" "$(shot a-ctrl-t)" \
      || fail "a: StartPB true but RunCmd $(tagv RunCmd)" "$(shot a-ctrl-t)"
  else
    fail "a: Ctrl+T on StartPB: StartPB $(tagv StartPB), last notification '$(last_note)'" "$(shot a-ctrl-t)"
  fi
else
  fail "a: no StartPB contact on the diagram" "$(shot a-no-contact)"
fi

# ── b: T flips it back ──────────────────────────────────────────────────────
g_key t
if poll 3 tag_is StartPB false; then
  sleep 0.5
  tag_is RunCmd true && pass "b: T → StartPB false; RunCmd stays sealed in ($(last_note))" "$(shot b-t)" \
    || fail "b: StartPB false but RunCmd $(tagv RunCmd)" "$(shot b-t)"
else
  fail "b: T on StartPB: StartPB $(tagv StartPB), '$(last_note)'" "$(shot b-t)"
fi

# ── c: right-click → Toggle Bit on StopPB ───────────────────────────────────
if right_click_el StopPB; then
  png=$(shot c-menu)
  items=$(menu_items)
  if [[ $items == *"Toggle Bit"* && $items == *"Set Live Value"* && $items == *"Force"* ]]; then
    pass "c: the StopPB contact's menu: $items" "$png"
  else
    fail "c: the StopPB contact's menu lacks Toggle Bit / Set Live Value / Force: '$items'" "$png"
  fi
  if click_menu_item 'Toggle Bit'; then
    if poll 3 tag_is StopPB false && poll 3 tag_is RunCmd false; then
      pass "c: Toggle Bit from the menu → StopPB false, RunCmd dropped out" "$(shot c-toggled)"
    else
      fail "c: after Toggle Bit: StopPB $(tagv StopPB), RunCmd $(tagv RunCmd), '$(last_note)'" "$(shot c-toggled)"
    fi
  else
    fail "c: no Toggle Bit item to click ($items)" "$png"; key Escape
  fi
else
  fail "c: no StopPB contact to right-click" "$(shot c-no-contact)"
fi

# ── d: the palette's Set Live Value… acts on the diagram's selection ────────
select_el StopPB || true
vs_cmd "nautilus: Set Live Value" 1.5
if t=$(type_in_input StopPB TRUE); then
  poll 3 tag_is StopPB true && pass "d: palette Set Live Value… with StopPB selected asked '$t' → StopPB true" "$(shot d-palette)" \
    || fail "d: '$t' ← TRUE, StopPB $(tagv StopPB)" "$(shot d-palette)"
else
  fail "d: palette Set Live Value… with StopPB selected: $t" "$(shot d-palette)"; key Escape
fi

# ── e: Force… from the element; Ctrl+T refused while forced; Unforce ───────
if right_click_el StartPB && click_menu_item '^nautilus: Force'; then
  if t=$(type_in_input "Force StartPB" TRUE) && m=$(confirm Force); then
    poll 3 is_forced StartPB && pass "e: Force… from the StartPB contact → '$t' → modal '$m' → /api/forces $(forces)" "$(shot e-forced)" \
      || fail "e: StartPB not forced after the gesture: '$(forces)'" "$(shot e-forced)"
  else
    fail "e: Force input/modal for StartPB: ${t:-} ${m:-}" "$(shot e-input)"; key Escape
  fi
else
  fail "e: no Force… in the StartPB contact's menu ($(menu_items))" "$(shot e-menu)"; key Escape
fi
if is_forced StartPB; then
  select_el StartPB || true
  g_key ctrl+t
  sleep 1.5
  n=$(last_note)
  if [[ $n == *"StartPB is forced"* ]] && tag_is StartPB true; then
    pass "e: Ctrl+T on the forced StartPB refused: '$n'; StartPB still $(tagv StartPB)" "$(shot e-refused)"
  else
    fail "e: Ctrl+T on forced StartPB: notification '$n', StartPB $(tagv StartPB)" "$(shot e-refused)"
  fi
  if right_click_el StartPB; then
    items=$(menu_items)
    if click_menu_item 'Unforce|Remove Force'; then
      m=$(confirm "Remove|Unforce" 2>/dev/null) || true
      poll 3 not_forced StartPB && pass "e: Unforce from the menu ($items) → forces '$(forces)'" "$(shot e-unforced)" \
        || fail "e: StartPB still forced after Unforce ($items): '$(forces)'" "$(shot e-unforced)"
    else
      fail "e: no Unforce in the forced StartPB's menu: '$items'" "$(shot e-unforce-menu)"; key Escape
      api_post /api/forces/clear '' >/dev/null || true
    fi
  fi
fi

# ── f: FBD — Ctrl+T on a chip; Set Live Value… from its menu ─────────────────
if ed_open_diagram Panel.fbd; then
  if click_el "$(FBD_CHIP LampTest)"; then
    g_key ctrl+t
    if poll 3 tag_is LampTest true && poll 3 tag_is Lamp true; then
      pass "f: FBD LampTest chip selected, Ctrl+T → LampTest true, Lamp on ($(last_note))" "$(shot f-ctrl-t)"
    else
      fail "f: Ctrl+T on the LampTest chip: LampTest $(tagv LampTest), Lamp $(tagv Lamp), '$(last_note)'" "$(shot f-ctrl-t)"
    fi
  else
    fail "f: no LampTest chip on the FBD diagram" "$(shot f-no-chip)"
  fi
  if right_click_js "$(FBD_CHIP LampTest)"; then
    items=$(menu_items)
    if live_items "$items" && click_menu_item 'Set Live Value'; then
      if t=$(type_in_input LampTest FALSE) && poll 3 tag_is LampTest false; then
        pass "f: the LampTest chip's menu ($items) → Set Live Value… '$t' ← FALSE → LampTest false" "$(shot f-set)"
      else
        fail "f: Set Live Value… from the chip: ${t:-} LampTest $(tagv LampTest)" "$(shot f-set)"; key Escape
      fi
    else
      fail "f: the LampTest chip's menu lacks the live actions: '$items'" "$(shot f-menu)"; key Escape
    fi
  fi
else
  fail "f: Panel.fbd did not open as an FBD diagram" "$(shot f-open)"
fi

# ── g: SFC — T on a transition, the menus, Ctrl+T back ──────────────────────
if ed_open_diagram Fill.sfc; then
  if sfc_select_trans 'Idle->Filling'; then
    g_key t
    if poll 3 tag_is FillGo true && poll 5 tag_is FillValve true; then
      pass "g: SFC transition := FillGo selected, T → FillGo true, Filling active, FillValve on ($(last_note))" "$(shot g-t)"
    else
      fail "g: T on the FillGo transition: FillGo $(tagv FillGo), FillValve $(tagv FillValve), '$(last_note)'" "$(shot g-t)"
    fi
  else
    fail "g: no Idle->Filling transition to select" "$(shot g-no-trans)"
  fi
  if right_click_js "$(sfc_trans_el 'Idle->Filling')"; then
    items=$(menu_items)
    if live_items "$items" && [[ $items == *"Fire Transition"* ]]; then
      pass "g: the transition's menu keeps Fire Transition beside the live actions: $items" "$(shot g-trans-menu)"
    else
      fail "g: the FillGo transition's menu: '$items'" "$(shot g-trans-menu)"
    fi
    key Escape
  fi
  if right_click_js "$(SFC_ASSOC Filling:0)"; then
    items=$(menu_items)
    live_items "$items" && pass "g: the N FillValve action's menu: $items" "$(shot g-assoc-menu)" \
      || fail "g: the FillValve action's menu lacks the live actions: '$items'" "$(shot g-assoc-menu)"
    key Escape
  fi
  if sfc_select_trans 'Idle->Filling' || true; then
    g_key ctrl+t
    if poll 3 tag_is FillGo false && poll 5 tag_is FillValve false; then
      pass "g: Ctrl+T on the transition → FillGo false, back to Idle, FillValve off ($(last_note))" "$(shot g-ctrl-t)"
    else
      fail "g: Ctrl+T back: FillGo $(tagv FillGo), FillValve $(tagv FillValve), '$(last_note)'" "$(shot g-ctrl-t)"
    fi
  fi
else
  fail "g: Fill.sfc did not open as an SFC diagram" "$(shot g-open)"
fi
