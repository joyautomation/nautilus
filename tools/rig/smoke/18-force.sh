#!/usr/bin/env bash
# 18 — forcing, end to end (gesture inventory X43; issues #211, #192):
#
#   a. Force… from the Live Values panel (the lock on a row): LevelPct, a
#      value the sim task integrates every scan, forced high — the confirm
#      modal names it; the controller's table has it; it HOLDS against the
#      plant for seconds; the logic reacts (the pump seal-in drops out).
#   b. The F badge: the inline pill in sim.st reads "F <value>", the Live
#      Values panel lists a Forces group and the tag row reads "F <value>".
#   c. The status bar: "1 force active", then "2 forces active" after an
#      OUTPUT is forced from the editor context menu (PumpRun held TRUE
#      against the logic that wants it off).
#   d. Remove: the status-bar item's list removes LevelPct (the plant owns
#      it again — it moves); the panel's Remove All Forces (title bar)
#      clears the rest: table empty, status item gone, pill without F.
#   e. SFC: on tank-batch, the chart's context menu — Set Active Step on a
#      step, Fire Transition on a transition — moves the running chart
#      (GET /api/sfc), each once.
#   f. Every action left an audit line in the controller's log.
#
# Project: the Demo scaffold (`naut new`), then tank-batch (batch.sfc) for e.
# Assertions read /api/forces, /api/state, /api/sfc, the extension's test
# state (NAUTILUS_TEST_STATE) and the workbench DOM over CDP — not pixels.
# Menus: window.menuStyle custom, so the editor's and the webview's context
# menus are workbench DOM (13 explains why).
set -euo pipefail
CHECK=18-force
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
forces() {
  api /api/forces | python3 -c '
import sys, json
print(" ".join("%s=%s" % (f["name"], json.dumps(f["value"])) for f in json.load(sys.stdin)["forces"]))'
}
tstate() {
  python3 - "$NAUTILUS_TEST_STATE" "$1" <<'PY'
import json, sys
try: s = json.load(open(sys.argv[1]))
except Exception: s = {}
print(eval(sys.argv[2]))
PY
}
# force_status — the "N forces active" status-bar item's text in the DOM ("" if none).
force_status() {
  pg '[...document.querySelectorAll(".statusbar-item")].map(e => e.innerText.trim()).find(t => /forces? active/.test(t)) || ""'
}
force_status_box() { page_el_box '[...document.querySelectorAll(".statusbar-item")].find(e => /forces? active/.test(e.innerText)) || null'; }
# poll <seconds> <cmd…> — retry until the command succeeds.
poll() { local s=$1 i; shift; for ((i = 0; i < s * 4; i++)); do "$@" && return 0; sleep 0.25; done; return 1; }
is_forced() { [[ " $(forces) " == *" $1="* ]]; }
not_forced() { ! is_forced "$1"; }
no_forces() { [[ -z $(forces) ]]; }

PILLS_JS='(() => {
  const g = document.querySelector(".editor-group-container.active") || document;
  const ed = g.querySelector(".monaco-editor");
  if (!ed) return null;
  const out = [];
  for (const line of ed.querySelectorAll(".view-lines .view-line")) {
    let acc = "";
    for (const sp of line.querySelectorAll("span")) {
      if (sp.children.length) continue;
      const a = getComputedStyle(sp, "::after");
      const c = a.content;
      if (c && c !== "none" && c !== "normal" && a.borderTopLeftRadius === "5px") {
        const m = acc.replace(/ /g, " ").match(/([A-Za-z_][A-Za-z0-9_.]*)\s*$/);
        let v = c; try { v = JSON.parse(c); } catch (e) {}
        out.push({ id: m ? m[1] : "", v });
      }
      acc += sp.textContent;
    }
  }
  return out;
})()'
pill() {
  cdp page "$PILLS_JS" | python3 -c '
import sys, json
ps = json.load(sys.stdin) or []
n = sys.argv[1].lower()
print(next((p["v"] for p in ps if p["id"].lower() == n), ""))' "$1"
}
pill_forced() { [[ $(pill "$1") == F\ * ]]; }
pill_plain() { local v; v=$(pill "$1"); [[ -n $v && $v != F\ * ]]; }

DEEP_JS='const deep = (sel) => { const out = [...document.querySelectorAll(sel)]; for (const h of document.querySelectorAll("*")) if (h.shadowRoot) out.push(...h.shadowRoot.querySelectorAll(sel)); return out; };'
menu_item_el() { printf '(() => { %s return deep(".monaco-menu .action-item a.action-menu-item, .monaco-menu .action-item .action-label").find(a => a.offsetParent !== null && /%s/.test(a.textContent.trim())) || null; })()' "$DEEP_JS" "$1"; }
menu_items() { pg "(() => { $DEEP_JS return deep('.monaco-menu .action-item .action-label').filter(a => a.offsetParent !== null).map(a => a.textContent.trim()).join(' | '); })()"; }
click_menu_item() { local b; b=$(page_el_box "$(menu_item_el "$1")") || return 1; read -r mx my mw mh <<<"$b"; g_click $((mx + mw / 2)) $((my + mh / 2)) 1; }
input_title() {
  cdp page '(() => { const w = document.querySelector(".quick-input-widget"); if (!w || w.style.display === "none" || !w.offsetParent) return ""; return (w.querySelector(".quick-input-title")?.textContent || "").trim(); })()' | python3 -c 'import sys,json; print(json.load(sys.stdin) or "")'
}
# type_in_input <title substring> <value> — the Force… input: wait, check the title, type, Enter.
type_in_input() {
  local t= i
  for ((i = 0; i < 20; i++)); do t=$(input_title); [[ -n $t ]] && break; sleep 0.25; done
  [[ $t == *"$1"* ]] || { echo "input title '$t', want *$1*"; return 1; }
  g_key ctrl+a; g_type "$2"; g_key Return
  echo "$t"
}
# confirm <button> — the confirmation modal (nautilus.confirmControllerWrites
# is on by default): print its message, press <button>.
confirm() {
  wait_for 5 dialog_up || { echo "no confirmation modal"; return 1; }
  local m; m=$(dialog_message)
  page_click_button "^$1\$" 1.5 || { echo "modal without '$1' ($(dialog_buttons)): $m"; return 1; }
  echo "$m"
}
ROWS_JS='[...document.querySelectorAll(".part.sidebar .monaco-list-row")].map(r => ({ name: (r.querySelector(".label-name")?.textContent || "").trim(), desc: (r.querySelector(".label-description")?.textContent || "").trim() }))'
row_desc() { pg "(() => { const r = ($ROWS_JS).filter(r => r.name === $(_q "$1")); return r.map(x => x.desc).join(' / '); })()"; }
row_el() { printf '[...document.querySelectorAll(".part.sidebar .monaco-list-row")].find(r => (r.querySelector(".label-name")?.textContent || "").trim() === %s)' "$(_q "$1")"; }
# row_action <row name> <action title regex> — hover the row, click its inline action.
row_action() {
  local b pb
  b=$(page_el_box "$(row_el "$1")") || { echo "no '$1' row"; return 1; }
  read -r rx ry rw rh <<<"$b"
  xdotool mousemove --window "$WIN" $((rx + rw / 3)) $((ry + rh / 2)); sleep 0.8
  pb=$(page_el_box "(() => { const r = $(row_el "$1"); return r && [...r.querySelectorAll('.actions .action-label')].find(a => a.offsetParent !== null && /$2/.test(a.title || a.getAttribute('aria-label') || '')) || null; })()") \
    || { echo "no inline '$2' on the '$1' row"; return 1; }
  read -r px py pw ph <<<"$pb"
  g_click $((px + pw / 2)) $((py + ph / 2)) 1
}
ident_point() {
  local fl ft
  read -r fl ft <<<"$(_g_frame)"
  cdp page "(() => {
    const ed = (document.querySelector('.editor-group-container.active') || document).querySelector('.monaco-editor');
    const re = new RegExp($(_q "$1")), id = $(_q "$2");
    for (const line of ed.querySelectorAll('.view-lines .view-line')) {
      if (!re.test(line.textContent.replace(/ /g, ' '))) continue;
      const w = document.createTreeWalker(line, NodeFilter.SHOW_TEXT);
      for (let n; (n = w.nextNode()); ) {
        const i = n.data.indexOf(id);
        if (i < 0) continue;
        const r = document.createRange(); r.setStart(n, i); r.setEnd(n, i + id.length);
        const b = r.getBoundingClientRect(), k = window.devicePixelRatio;
        return Math.round((b.left + b.width / 2) * k + $fl) + ' ' + Math.round((b.top + b.height / 2) * k + $ft);
      }
    }
    return '';
  })()" | tr -d '"'
}
# holds <tag> <value> <seconds> — the tag reads exactly <value> at every
# sample over the window (a plant-integrated value would drift).
holds() {
  local i v
  for ((i = 0; i < $3 * 4; i++)); do
    v=$(tagv "$1")
    python3 -c 'import sys,json; sys.exit(0 if json.loads(sys.argv[1]) == json.loads(sys.argv[2]) else 1)' "$v" "$2" || { echo "$1 = $v"; return 1; }
    sleep 0.25
  done
  echo "$1 = $v for $3 s"
}

# ── project + controller ────────────────────────────────────────────────────
ext_scaffold my-plant
PORT=$(free_port 18090 18091 18092 18093)
point_extension_at "$PROJ" "$PORT"
start_controller "$PROJ" "$PORT"
sleep 3
api /api/meta | grep -q '"forces":true' && pass "naut run on :$PORT advertises forces (/api/meta)" || { fail "controller on :$PORT does not advertise forces"; exit 1; }

EXTRA_SETTINGS='"window.menuStyle": "custom"' smoke_open "$PROJ" sim.st
key Escape
sleep 4
vs_cmd "nautilus: Focus on Live Values View" 3
sleep 1.5

# ── a: Force… from the Live Values panel ────────────────────────────────────
# Drop the level below the pump's start level so the seal-in is RUNNING
# when the force lands: forcing the level high must then stop it — a change
# the force causes, not one the plant was about to make (the sim fills at
# 1.7 %/s, so from 30 the pump has ~25 s of run left; the gesture takes ~5).
api_post /api/tags '{"name":"LevelPct","value":30}' >/dev/null || true
sleep 1
pump0=$(tagv PumpRun)
if r=$(row_action LevelPct 'Force'); then
  png=$(shot a-input)
  if t=$(type_in_input "Force LevelPct" 90); then
    if m=$(confirm Force); then
      png=$(shot a-confirm)
      [[ $m == *LevelPct* && $m == *90* ]] && pass "a: Force… on the LevelPct row → '$t' → modal '$m'" "$png" \
        || fail "a: the confirmation does not name LevelPct and 90: '$m'" "$png"
    else
      fail "a: $m" "$(shot a-no-modal)"
    fi
  else
    fail "a: no Force input after the row's lock ($t)" "$png"; key Escape
  fi
else
  fail "a: $r" "$(shot a-no-row)"
fi
if poll 3 is_forced LevelPct; then
  pass "a: /api/forces lists $(forces)"
else
  fail "a: /api/forces after the gesture: '$(forces)'"
fi
if h=$(holds LevelPct 90 4); then
  pass "a: the forced value holds against the sim task that integrates it: $h"
else
  fail "a: the force did not hold: $h"
fi
pump1=$(tagv PumpRun)
[[ $pump0 == true && $pump1 == false ]] && pass "a: the logic reacts — PumpRun $pump0 → $pump1 (the seal-in drops out on the forced high level)" \
  || fail "a: PumpRun $pump0 → $pump1 under LevelPct forced to 90"

# ── b: the F badge, panel ───────────────────────────────────────────────────
sleep 1
png=$(shot b-panel)
groups=$(pg "($ROWS_JS).map(r => r.name).filter(n => /^Forces|^Tags$/.test(n)).join(' ')")
d=$(row_desc LevelPct)
if [[ $groups == Forces* && $d == F\ 90* ]]; then
  pass "b: the panel lists a Forces group first ($groups) and LevelPct reads '$d'" "$png"
else
  fail "b: panel groups '$groups', LevelPct row '$d'" "$png"
fi

# ── c: the status bar ───────────────────────────────────────────────────────
s=$(force_status); st=$(tstate '[b["text"] for b in s.get("statusBar", []) if b.get("name") == "nautilus.forces"]')
[[ $s == *"1 force active"* ]] && pass "c: status bar '$s' (test state $st)" "$png" || fail "c: status bar '$s' (test state $st)" "$png"

# ── b: the F badge, inline pill (sim.st) ───────────────────────────────────
hide_sidebar
sleep 1
if poll 4 pill_forced LevelPct; then
  pass "b: the LevelPct pill in sim.st reads '$(pill LevelPct)'" "$(shot b-pill)"
else
  fail "b: the LevelPct pill reads '$(pill LevelPct)', want 'F 90…'" "$(shot b-pill)"
fi

# ── c: force an OUTPUT from the editor context menu ─────────────────────────
p=$(ident_point 'PumpRun' PumpRun)
if [[ -z $p ]]; then
  fail "c: no PumpRun on screen in sim.st to right-click"
else
  read -r x y <<<"$p"
  xdotool mousemove --window "$WIN" "$x" "$y"; sleep 0.3
  xdotool click 1; sleep 0.3
  xdotool click 3; sleep 1.2
  png=$(shot c-context-menu)
  items=$(menu_items)
  if click_menu_item '^nautilus: Force'; then
    if t=$(type_in_input "Force PumpRun" TRUE) && m=$(confirm Force); then
      if poll 3 is_forced PumpRun && h=$(holds PumpRun true 2); then
        s=$(force_status)
        [[ $s == *"2 forces active"* ]] && pass "c: Force… from the context menu ($items) held PumpRun TRUE against the logic that wants it off ($h); status '$s'" "$(shot c-two)" \
          || fail "c: PumpRun forced but the status bar reads '$s'" "$(shot c-two)"
      else
        fail "c: PumpRun not held: forces '$(forces)', PumpRun $(tagv PumpRun)" "$(shot c-two)"
      fi
    else
      fail "c: Force input/modal for PumpRun: $t ${m:-}" "$(shot c-input)"; key Escape
    fi
  else
    fail "c: no 'nautilus: Force…' in the editor context menu ($items)" "$png"; key Escape
  fi
fi

# ── d: remove one from the status bar's list, then all from the panel ───────
if b=$(force_status_box); then
  read -r sx sy sw sh <<<"$b"
  g_click $((sx + sw / 2)) $((sy + sh / 2)) 1.2
  png=$(shot d-list)
  t=$(input_title)
  rowsq=$(pg '[...document.querySelectorAll(".quick-input-widget .monaco-list-row")].map(r => r.innerText.replace(/\s+/g, " ").trim()).join(" | ")')
  if [[ $t == *"2 forces active"* ]]; then
    g_type "LevelPct"; g_key Return; sleep 1
    if poll 3 not_forced LevelPct; then
      v0=$(tagv LevelPct); sleep 2; v1=$(tagv LevelPct)
      [[ $v0 != "$v1" || $v0 != 90 ]] && pass "d: the status-bar list ('$t': $rowsq) removed LevelPct — the plant owns it again ($v0 → $v1)" "$png" \
        || fail "d: LevelPct unforced but stuck at $v0 → $v1" "$png"
    else
      fail "d: LevelPct still forced after picking it in '$t' ($rowsq)" "$png"
    fi
  else
    fail "d: the status-bar item opened '$t'" "$png"; key Escape
  fi
else
  fail "d: no force status-bar item to click"
fi
vs_cmd "nautilus: Focus on Live Values View" 2
tb=$(page_el_box '[...document.querySelectorAll(".part.sidebar .title-actions .action-label")].find(a => a.offsetParent !== null && /Remove All Forces/.test(a.title || a.getAttribute("aria-label") || "")) || null' || true)
if [[ -n $tb ]]; then
  read -r ax ay aw ah <<<"$tb"
  g_click $((ax + aw / 2)) $((ay + ah / 2)) 1
  m=$(confirm "Remove All") || true
  if poll 3 no_forces; then
    sleep 1.5
    s=$(force_status)
    [[ -z $s ]] && pass "d: Remove All Forces (panel title, modal '$m') — table empty, status item gone" "$(shot d-cleared)" \
      || fail "d: table empty but the status bar still reads '$s'" "$(shot d-cleared)"
  else
    fail "d: Remove All Forces left '$(forces)' ($m)" "$(shot d-cleared)"
  fi
else
  fail "d: no 'Remove All Forces' action in the Live Values title bar" "$(shot d-no-title-action)"
fi
hide_sidebar
sleep 1
poll 4 pill_plain LevelPct && pass "d: the LevelPct pill is plain again ('$(pill LevelPct)')" "$(shot d-pill)" \
  || fail "d: the LevelPct pill reads '$(pill LevelPct)'" "$(shot d-pill)"

# ── f: the audit lines so far ───────────────────────────────────────────────
a=$(grep -cE 'force: (set|removed|cleared all)' /tmp/capture-controller.log || true)
(( a >= 4 )) && pass "f: $a force audit lines in the controller log ($(grep -E 'force: ' /tmp/capture-controller.log | sed 's/.*msg=//' | cut -c1-60 | paste -sd';'))" \
  || fail "f: $a force audit lines in the controller log, want ≥ 4"

# ── e: SFC — Set Active Step / Fire Transition from the chart ───────────────
vs_cmd "View: Close All Editors" 1
kill_controllers_for "$PROJ"; sleep 1
ext_fixture tank-batch
PORT=$(free_port 18094 18095 18096 18097)
point_extension_at "$PROJ" "$PORT"
start_controller "$PROJ" "$PORT"
sleep 3
smoke_open_sfc() { EXTRA_SETTINGS='"window.menuStyle": "custom", "nautilus.confirmControllerWrites": false' smoke_open "$PROJ" batch.sfc; }
smoke_open_sfc
key Escape
sleep 3
ed_open_diagram batch.sfc || fail "e: batch.sfc did not open as a diagram" "$(shot e-open)"
sleep 3
active() { api /api/sfc | python3 -c 'import sys,json; c=json.load(sys.stdin)["charts"][0]; print(",".join(s["name"] for s in c["steps"] if s["active"]))'; }
active_is() { [[ $(active) == "$1" ]]; }
# right_click_el <webview element expr> — right-click it in the webview.
right_click_el() { local p; p=$(el_at "$1") || return 1; read -r x y <<<"$p"; xdotool mousemove --window "$WIN" "$x" "$y"; sleep 0.3; xdotool click 3; sleep 1.2; }

a0=$(active)
if right_click_el "$(sfc_step_el Drain)"; then
  png=$(shot e-step-menu)
  items=$(menu_items)
  if click_menu_item 'Set Active Step'; then
    if poll 3 active_is Drain; then
      pass "e: right-click Drain → Set Active Step ($items): active $a0 → $(active)" "$(shot e-step)"
    else
      fail "e: Set Active Step Drain: active $a0 → $(active)" "$(shot e-step)"
    fi
  else
    fail "e: no 'Set Active Step' in the step's context menu ($items)" "$png"; key Escape
  fi
else
  fail "e: no Drain step on the chart" "$(shot e-nostep)"
fi
# Back to Idle, then fire t_start (Start is FALSE: only the command moves it).
api_post /api/sfc/step '{"step":"Idle"}' >/dev/null || true
poll 3 active_is Idle || info "e: could not park the chart in Idle ($(active))"
TS_EL='[...doc.querySelectorAll("svg.chart g.trans")].find(g => JSON.parse(g.getAttribute("data-vscode-context") || "{}").nautilusSfcTransition === "t_start")?.querySelector(".barhit, .jumpbox")'
if right_click_el "$TS_EL"; then
  png=$(shot e-trans-menu)
  items=$(menu_items)
  if click_menu_item 'Fire Transition'; then
    sleep 1
    a1=$(active)
    [[ $a1 != Idle && $a1 != "" ]] && pass "e: right-click t_start → Fire Transition ($items): Idle → $a1 with Start = $(tagv Start)" "$(shot e-trans)" \
      || fail "e: Fire Transition t_start: still $a1" "$(shot e-trans)"
  else
    fail "e: no 'Fire Transition' in the transition's context menu ($items)" "$png"; key Escape
  fi
else
  fail "e: no t_start transition on the chart" "$(shot e-notrans)"
fi
# b on a diagram: a forced action target is marked F on the live chart.
api_post /api/forces '{"name":"FillValve","value":true}' >/dev/null || fail "b: could not force FillValve over the API"
marked() { [[ $(js '[...doc.querySelectorAll(".assoctarget")].filter(t => t.querySelector(".nx-forced-mark")).map(t => t.textContent).join(",")') == *FillValve* ]]; }
if wait_for 5 marked; then
  pass "b: the SFC chart marks the forced FillValve action ($(js '[...doc.querySelectorAll(".assoctarget")].filter(t => t.querySelector(".nx-forced-mark")).map(t => t.textContent).join(",")'))" "$(shot e-diagram-badge)"
else
  fail "b: no F mark on the forced FillValve action in the chart" "$(shot e-diagram-badge)"
fi
api_post /api/forces/clear '' >/dev/null || true
a=$(grep -cE 'sfc: (set active step|fired transition)' /tmp/capture-controller.log || true)
(( a >= 2 )) && pass "f: $a SFC audit lines in the controller log" || fail "f: $a SFC audit lines in the controller log, want ≥ 2"
