#!/usr/bin/env bash
# 13 — the live-value surfaces, asserted by CONTENT (07 counts green pixels;
# this one reads the text the surfaces show and compares it with what the
# controller says, /api/state):
#
#   X11  inline pills beside identifiers in the .st / .fbd / .ld TEXT views
#        (liveValues.ts: TextEditorDecorations whose `after.contentText` is
#        the value — VS Code draws them as an empty <span> whose ::after
#        content is that text, so the page's computed style reads them back);
#        a value written over the API is followed within ~3 s.
#   X12  the Live Values panel (nautilusLiveValues): every tag in /api/state
#        by name, with a value, and the program locals (task.local).
#   C06  nautilus: Set Live Value… — (a) from the .st editor's context menu
#        on an identifier, (b) from the pencil inline action on a Live Values
#        row; the controller shows the value.
#   X39  the mimic editor's canvas follows the controller: a level written
#        over the API shows in the tank's own aria-label and in the LT-101
#        readout, and the pump's running state follows the control loop.
#
# Project: the Demo scaffold (`naut new`) with smoke/fixtures/
# heated-tank.mimic.json beside it (bound to LevelPct, TempC, Heater,
# PumpRun) — the same pairing 07 uses.
#
# The context menu: VS Code's NATIVE menu ignores XTEST clicks (lib.sh,
# yd_click) and the rig image has no ydotool, so the profile sets
# window.menuStyle "custom": the menu is then workbench DOM, found and
# clicked like anything else. If no menu shows, the check falls back to the
# palette with the cursor on the identifier, and says so in the row.
set -euo pipefail
CHECK=13-live-values
source "$HOME/smoke/lib.sh"
source "$HOME/fixtures/gestures.sh"
G_PACE=fast
rm -rf "$PROFILE"

export NAUTILUS_TEST_STATE=$OUT_DIR/$CHECK-state.json
rm -f "$NAUTILUS_TEST_STATE"

# ── helpers (this check's own) ──────────────────────────────────────────────

# tagv <name> — the controller's value for a tag (or a local), as JSON.
tagv() {
  api /api/state | python3 -c '
import sys, json
d = json.load(sys.stdin); n = sys.argv[1]
t = d.get("tags", {}); l = d.get("locals", {})
v = t[n] if n in t else l.get(n)
print(json.dumps(v))' "$1"
}
post_tag() { api_post /api/tags "{\"name\":\"$1\",\"value\":$2}" >/dev/null; }

# tstate <python expr over s, the test-state snapshot> — prints the result.
tstate() {
  python3 - "$NAUTILUS_TEST_STATE" "$1" <<'PY'
import json, sys
try: s = json.load(open(sys.argv[1]))
except Exception: s = {}
print(eval(sys.argv[2]))
PY
}

# pills — the active editor's inline pills as JSON [{id, v}]: every leaf
# <span> in a view-line whose ::after has content and the pill's 5px radius
# (pillDecoration), with the identifier it sits behind (the text up to it).
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
        const m = acc.replace(/ /g, " ").match(/([A-Za-z_][A-Za-z0-9_.]*)\s*$/);
        let v = c; try { v = JSON.parse(c); } catch (e) {}
        out.push({ id: m ? m[1] : "", v, top: parseFloat(line.style.top) || 0 });
      }
      acc += sp.textContent;
    }
  }
  return out.sort((a, b) => a.top - b.top);
})()'
pills() { cdp page "$PILLS_JS"; }
# pill <identifier> — the first pill's text behind <identifier> (case-insensitive), or "".
pill() {
  pills | python3 -c '
import sys, json
ps = json.load(sys.stdin) or []
n = sys.argv[1].lower()
print(next((p["v"] for p in ps if p["id"].lower() == n), ""))' "$1"
}
# near <a> <b> <tol> — numeric |a-b| <= tol; TRUE/FALSE/true/false compare exactly.
near() {
  python3 - "$1" "$2" "$3" <<'PY'
import sys
a, b, t = sys.argv[1].strip('"'), sys.argv[2].strip('"'), float(sys.argv[3])
def num(s):
    if s.lower() in ("true", "false"): return s.lower()
    return float(s)
try:
    x, y = num(a), num(b)
except ValueError:
    sys.exit(1)
if isinstance(x, str) or isinstance(y, str): sys.exit(0 if x == y else 1)
sys.exit(0 if abs(x - y) <= t else 1)
PY
}
# pill_follows <identifier> <target> <tol> <seconds> — poll until the pill
# reads within tol of target; prints the last pill text.
pill_follows() {
  local id=$1 want=$2 tol=$3 secs=$4 i v=
  for ((i = 0; i < secs * 4; i++)); do
    v=$(pill "$id"); near "$v" "$want" "$tol" && { echo "$v"; return 0; }
    sleep 0.25
  done
  echo "$v"; return 1
}
# pill_vs_api <identifier> <tol> — the pill against the controller, sampled
# either side of the read (the plant moves while we look): prints
# "pill api0 api1", status 0 if the pill sits within tol of the span.
pill_vs_api() {
  local id=$1 tol=$2 a0 p a1
  a0=$(tagv "$id"); p=$(pill "$id"); a1=$(tagv "$id")
  echo "$p $a0 $a1"
  [[ -n $p ]] || return 1
  near "$p" "$a0" "$tol" || near "$p" "$a1" "$tol"
}

# ident_point <line regex> <identifier> — window "x y" of <identifier> on
# the first visible editor line matching <line regex> (a DOM Range over the
# text node, so the point is on the word itself, not its token span).
ident_point() {
  local fl ft
  read -r fl ft <<<"$(_g_frame)"
  cdp page "(() => {
    const ed = (document.querySelector('.editor-group-container.active') || document).querySelector('.monaco-editor');
    const re = new RegExp($(_q "$1")), id = $(_q "$2");
    for (const line of ed.querySelectorAll('.view-lines .view-line')) {
      if (!re.test(line.textContent.replace(/ /g, ' '))) continue;
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

# The workbench's own overlays can sit in shadow roots (custom menus do).
DEEP_JS='const deep = (sel) => { const out = [...document.querySelectorAll(sel)]; for (const h of document.querySelectorAll("*")) if (h.shadowRoot) out.push(...h.shadowRoot.querySelectorAll(sel)); return out; };'
# menu_item_el <label regex> — a visible context-menu item, as a page expr.
menu_item_el() { printf '(() => { %s return deep(".monaco-menu .action-item a.action-menu-item, .monaco-menu .action-item .action-label").find(a => a.offsetParent !== null && /%s/.test(a.textContent.trim())) || null; })()' "$DEEP_JS" "$1"; }
# input_title — the quick input's title while it is up, else "".
input_title() {
  cdp page '(() => { const w = document.querySelector(".quick-input-widget"); if (!w || w.style.display === "none" || !w.offsetParent) return ""; return (w.querySelector(".quick-input-title")?.textContent || "").trim(); })()' | python3 -c 'import sys,json; print(json.load(sys.stdin) or "")'
}
# set_in_input <expected title substring> <value> — the Set Live Value input:
# wait for it, check its title, type over the prefilled value, Enter.
set_in_input() {
  local want=$1 val=$2 t= i
  for ((i = 0; i < 20; i++)); do t=$(input_title); [[ -n $t ]] && break; sleep 0.25; done
  [[ $t == *"$want"* ]] || { echo "input title '$t', want *$want*"; return 1; }
  g_key ctrl+a; g_type "$val"; g_key Return
  echo "$t"
}
# api_follows <tag> <value> <tol> <seconds> — poll /api/state.
api_follows() {
  local i v=
  for ((i = 0; i < $4 * 4; i++)); do v=$(tagv "$1"); near "$v" "$2" "$3" && { echo "$v"; return 0; }; sleep 0.25; done
  echo "$v"; return 1
}

# ── project + controller ────────────────────────────────────────────────────
ext_scaffold my-plant
cp "$FIX/heated-tank.mimic.json" "$PROJ/"
git -C "$PROJ" add -A; git -C "$PROJ" commit -qm "mimic"
PORT=$(free_port 18080 18081 18082 18083)
point_extension_at "$PROJ" "$PORT"
start_controller "$PROJ" "$PORT"
sleep 3
api /api/state >/dev/null && pass "naut run on :$PORT, /api/state answers" || { fail "controller not answering on :$PORT"; exit 1; }

EXTRA_SETTINGS='"window.menuStyle": "custom"' smoke_open "$PROJ" sim.st
key Escape; hide_sidebar
sleep 5
c=$(tstate 's.get("connected")'); u=$(tstate 's.get("runtimeUrl")')
info "test state: connected=$c runtimeUrl=$u liveValuesEnabled=$(tstate 's.get("liveValuesEnabled")') status=$(tstate '[b["text"] for b in s.get("statusBar", [])]')"

# ── X11: pills in the .st text ──────────────────────────────────────────────
png=$(shot st-pills)
pj=$(pills)
n=$(python3 -c 'import sys,json; print(len(json.loads(sys.argv[1]) or []))' "$pj")
ids=$(python3 -c 'import sys,json; print(" ".join(sorted({p["id"] for p in json.loads(sys.argv[1]) or []})))' "$pj")
known=$(api /api/state | python3 -c 'import sys,json; d=json.load(sys.stdin); print(" ".join(k.lower() for k in list(d.get("tags",{}))+list(d.get("locals",{}))))')
unknown=$(for i in $ids; do [[ " $known " == *" ${i,,} "* ]] || printf '%s ' "$i"; done)
if (( n >= 5 )) && [[ -z $unknown ]]; then
  pass "X11 sim.st: $n pills, each behind a controller tag ($ids)" "$png"
else
  fail "X11 sim.st: $n pills (behind: ${ids:-none}; not tags: ${unknown:-none})" "$png"
fi
r=$(pill_vs_api LevelPct 1.0) && pass "X11 sim.st: the LevelPct pill reads the controller (pill/api before/after: $r)" "$png" \
  || fail "X11 sim.st: the LevelPct pill does not match /api/state (pill/api before/after: $r)" "$png"
r=$(pill_vs_api PumpRun 0) && pass "X11 sim.st: the PumpRun pill reads the controller (BOOL: $r)" "$png" \
  || fail "X11 sim.st: the PumpRun pill does not match /api/state ($r)" "$png"

# A value written over the API: the pill follows. LevelPct is the sim's
# state (it integrates from whatever it holds, ±1.7 %/s), so the write
# sticks long enough to read; the band allows for the drift.
post_tag LevelPct 12.5
t0=$(date +%s.%N)
if v=$(pill_follows LevelPct 12.5 2.0 3); then
  dt=$(python3 -c "import time; print(f'{time.time()-$t0:.1f}')")
  png=$(shot st-pill-follows)
  pass "X11 sim.st: LevelPct written 12.5 over the API → the pill reads $v within ${dt}s" "$png"
else
  png=$(shot st-pill-follows)
  fail "X11 sim.st: LevelPct written 12.5 → the pill still reads '$v' after 3 s (api $(tagv LevelPct))" "$png"
fi

# ── C06 (a): Set Live Value from the editor context menu ────────────────────
# The assignment line `LevelPct := LIMIT(0.0, LevelPct + …` — right-click the
# first LevelPct on it (moves the cursor there too).
route=menu
p=$(ident_point 'LevelPct := LIMIT\(0\.0, LevelPct \+' LevelPct)
if [[ -z $p ]]; then
  fail "C06a: no LevelPct on screen in sim.st to right-click"
else
  read -r x y <<<"$p"
  xdotool mousemove --window "$WIN" "$x" "$y"; sleep 0.3
  xdotool click 1; sleep 0.3      # cursor onto the word
  xdotool click 3; sleep 1.2
  png=$(shot c06a-context-menu)
  if b=$(page_el_box "$(menu_item_el 'Set Live Value')"); then
    read -r mx my mw mh <<<"$b"
    info "C06a: the context menu offers 'nautilus: Set Live Value…'" "$png"
    g_click $((mx + mw / 2)) $((my + mh / 2)) 1
  else
    route=palette
    warn "C06a: no 'Set Live Value' item found in a custom context menu (window.menuStyle custom) — palette fallback with the cursor on LevelPct" "$png"
    key Escape
    xdotool mousemove --window "$WIN" "$x" "$y"; xdotool click 1; sleep 0.3
    vs_cmd "nautilus: Set Live Value" 1
  fi
  png=$(shot c06a-input)
  # A target the plant cannot drift to on its own in the window: far from
  # where the level is now (it moves under 2 %/s either way).
  cur=$(tagv LevelPct); want=$(python3 -c "print(90 if float('$cur') < 50 else 10)")
  if t=$(set_in_input "LevelPct" "$want"); then
    if v=$(api_follows LevelPct "$want" 5 3); then
      png2=$(shot c06a-set)
      pass "C06a ($route): '$t' ← $want (was $cur) → /api/state LevelPct $v; notification: $(tstate '(s.get("lastNotification") or {}).get("text")')" "$png2"
    else
      fail "C06a ($route): typed $want into '$t' (was $cur) but /api/state LevelPct is $v" "$(shot c06a-set)"
    fi
  else
    fail "C06a ($route): no Set Live Value input for LevelPct ($t)" "$png"
    key Escape
  fi
fi

# ── X11 on the .fbd TEXT view: a setpoint, exact ────────────────────────────
vs_cmd "View: Close All Editors" 1
open_file program.fbd 4
sleep 2
png=$(shot fbd-text-pills)
n=$(cdp page "$PILLS_JS" | python3 -c 'import sys,json; print(len(json.load(sys.stdin) or []))')
r=$(pill_vs_api TempSP 0.0005) && pass "X11 program.fbd (text): $n pills; the TempSP pill reads the controller exactly ($r)" "$png" \
  || fail "X11 program.fbd (text): $n pills; TempSP pill vs api: $r" "$png"
post_tag TempSP 71.25
if v=$(pill_follows TempSP 71.25 0.0005 3); then
  pass "X11 program.fbd (text): TempSP written 71.25 over the API → the pill reads $v" "$(shot fbd-pill-follows)"
else
  fail "X11 program.fbd (text): TempSP written 71.25 → the pill reads '$v' after 3 s" "$(shot fbd-pill-follows)"
fi
post_tag TempSP 65

# ── X11 on the .ld TEXT view ────────────────────────────────────────────────
vs_cmd "View: Close All Editors" 1
open_file interlocks.ld 4
sleep 2
png=$(shot ld-text-pills)
n=$(cdp page "$PILLS_JS" | python3 -c 'import sys,json; print(len(json.load(sys.stdin) or []))')
r=$(pill_vs_api TempC 0.5) && pass "X11 interlocks.ld (text): $n pills; the TempC pill reads the controller ($r)" "$png" \
  || fail "X11 interlocks.ld (text): $n pills; TempC pill vs api: $r" "$png"

# ── X12: the Live Values panel ──────────────────────────────────────────────
vs_cmd "View: Close All Editors" 1
open_file sim.st 3
vs_cmd "nautilus: Focus on Live Values View" 3
sleep 1.5
ROWS_JS='(() => {
  const sb = document.querySelector(".part.sidebar");
  if (!sb) return null;
  return [...sb.querySelectorAll(".monaco-list-row")].map(r => ({
    i: +r.getAttribute("data-index"),
    name: (r.querySelector(".label-name")?.textContent || "").trim(),
    desc: (r.querySelector(".label-description")?.textContent || "").trim() }));
})()'
# Rows are virtualised: wheel down the list, collecting by data-index.
ROWS=$OUT_DIR/$CHECK-rows.json; echo '{}' >"$ROWS"
sb=$(page_el_box 'document.querySelector(".part.sidebar .monaco-list")' || true)
for pass_n in 1 2 3 4 5 6; do
  cdp page "$ROWS_JS" | python3 -c '
import sys, json
f = sys.argv[1]; acc = json.load(open(f)); new = 0
for r in json.load(sys.stdin) or []:
    if str(r["i"]) not in acc: new += 1
    acc[str(r["i"])] = r
json.dump(acc, open(f, "w")); sys.exit(0 if new else 1)' "$ROWS" || break
  [[ -n $sb ]] || break
  read -r lx ly lw lh <<<"$sb"
  xdotool mousemove --window "$WIN" $((lx + lw / 2)) $((ly + lh / 2)); xdotool click 5 click 5 click 5; sleep 0.6
done
# back to the top for the evidence frame
[[ -n $sb ]] && { for _ in 1 2 3 4 5 6; do xdotool click 4; done; sleep 0.6; }
png=$(shot live-values-panel)
res=$(api /api/state | python3 -c '
import sys, json
d = json.load(sys.stdin); rows = json.load(open(sys.argv[1])).values()
names = {r["name"]: r["desc"] for r in rows}
tags = list(d.get("tags", {})); locs = list(d.get("locals", {}))
miss = [t for t in tags if t not in names]
noval = [t for t in tags if t in names and names[t] == ""]
lrows = [l for l in locs if l in names and names[l] != ""]
lmiss = [l for l in locs if l not in lrows]
dotted = [l for l in lrows if "." in l]
print(json.dumps({"rows": len(names), "tags": len(tags), "miss": miss, "noval": noval,
  "locals": len(locs), "lshown": lrows, "lmiss": lmiss, "dotted": dotted[:2],
  "groups": [g for g in ("Tags", "Locals") if g in names],
  "TempSP": names.get("TempSP", ""), "apiTempSP": d.get("tags", {}).get("TempSP")}))' "$ROWS")
echo "  panel: $res"
jq_() { python3 -c 'import sys,json; v=json.loads(sys.argv[1])[sys.argv[2]]; print(v if not isinstance(v,list) else " ".join(map(str,v)))' "$res" "$1"; }
if [[ -z $(jq_ miss) && -z $(jq_ noval) && $(jq_ tags) -gt 0 ]]; then
  pass "X12 Live Values: all $(jq_ tags) tags of /api/state listed with a value (groups: $(jq_ groups); $(jq_ rows) rows)" "$png"
else
  fail "X12 Live Values: tags missing: [$(jq_ miss)], without a value: [$(jq_ noval)] ($(jq_ rows) rows)" "$png"
fi
# Locals: /api/state keys them by their bare name (runtime.AllLocals merges
# every task's locals into one map), and the panel lists them as keyed.
if [[ $(jq_ locals) == 0 ]]; then
  fail "X12 Live Values: /api/state has no locals to list" "$png"
elif [[ -z $(jq_ lmiss) ]]; then
  pass "X12 Live Values: all $(jq_ locals) program locals of /api/state listed with a value ($(jq_ lshown))" "$png"
else
  fail "X12 Live Values: locals missing or without a value: [$(jq_ lmiss)] (listed: [$(jq_ lshown)])" "$png"
fi
[[ -z $(jq_ dotted) ]] && info "X12 Live Values: locals are named bare, not task.local (HiSecs, not interlocks.HiSecs) — that is /api/state's keying; two tasks with a same-named local would show as one row"
near "$(jq_ TempSP)" "$(jq_ apiTempSP)" 0.0005 \
  && pass "X12 Live Values: the TempSP row reads $(jq_ TempSP) = /api/state" "$png" \
  || fail "X12 Live Values: the TempSP row reads '$(jq_ TempSP)', /api/state $(jq_ apiTempSP)" "$png"

# The panel's title bar: its actions should be icons. One without an icon
# (package.json gives nautilus.connect none) renders as its full title text
# and squeezes the view's own name to "NAUTI…".
tb=$(cdp page '[...document.querySelectorAll(".part.sidebar .title-actions .action-label")].filter(a => a.offsetParent !== null).map(a => (a.textContent || "").trim() || "[icon]").join(" | ")' | python3 -c 'import sys,json; print(json.load(sys.stdin) or "")')
ttl=$(cdp page '(() => { const h = document.querySelector(".part.sidebar .title-label h2"); if (!h) return ""; return h.textContent.trim() + (h.scrollWidth > h.clientWidth ? " (truncated)" : ""); })()' | python3 -c 'import sys,json; print(json.load(sys.stdin) or "")')
if [[ $tb == *nautilus:* ]]; then
  warn "X12 Live Values title bar: an action renders as text, not an icon ($tb) — container title '$ttl'; nautilus.connect has no icon in package.json (#138)" "$png"
else
  info "X12 Live Values title bar actions: $tb (title '$ttl')" "$png"
fi

# ── C06 (b): the pencil on a Live Values row ────────────────────────────────
ROW_KP='[...document.querySelectorAll(".part.sidebar .monaco-list-row")].find(r => (r.querySelector(".label-name")?.textContent || "").trim() === "Kp")'
if b=$(page_el_box "$ROW_KP"); then
  read -r rx ry rw rh <<<"$b"
  xdotool mousemove --window "$WIN" $((rx + rw / 3)) $((ry + rh / 2)); sleep 0.8
  png=$(shot c06b-hover)
  if pb=$(page_el_box "(() => { const r = $ROW_KP; return r && [...r.querySelectorAll('.actions .action-label')].find(a => a.offsetParent !== null && /Set Live Value/.test(a.title || a.getAttribute('aria-label') || '')) || null; })()"); then
    read -r px py pw ph <<<"$pb"
    g_click $((px + pw / 2)) $((py + ph / 2)) 1
    if t=$(set_in_input "Kp" 13.5); then
      if v=$(api_follows Kp 13.5 0.0005 3); then
        pass "C06b (pencil): '$t' ← 13.5 → /api/state Kp $v" "$(shot c06b-set)"
      else
        fail "C06b (pencil): typed 13.5 into '$t' but /api/state Kp is $v" "$(shot c06b-set)"
      fi
    else
      fail "C06b (pencil): no Set Live Value input for Kp after the pencil ($t)" "$(shot c06b-noinput)"
      key Escape
    fi
  else
    fail "C06b: no visible 'Set Live Value' inline action on the hovered Kp row" "$png"
  fi
else
  fail "C06b: no Kp row in the Live Values panel" "$(shot c06b-norow)"
fi
post_tag Kp 12

# ── X39: the mimic canvas ───────────────────────────────────────────────────
vs_cmd "View: Close Primary Side Bar" 1
ed_open_diagram heated-tank.mimic.json || fail "X39: the mimic editor did not open heated-tank.mimic.json" "$(shot mimic-open)"
sleep 2
MIMIC_JS='(() => {
  const a = (id) => doc.querySelector(`.canvas > .eq[data-id="${id}"] svg[role="img"]`)?.getAttribute("aria-label") || "";
  const ro = [...doc.querySelectorAll(".canvas .lbl")].map(l => ({ t: l.textContent.trim(), num: l.querySelector(".num")?.textContent.trim() ?? null }));
  return { tank: a("T101"), pump: a("P101"), labels: ro };
})()'
mimic() { cdp eval "$MIMIC_JS"; }
# tank_pct / pump_state / lt_num from the canvas
canvas() {
  mimic | python3 -c '
import sys, json, re
d = json.load(sys.stdin)
m = re.search(r":\s*(-?[\d.]+)%", d["tank"]); pct = m.group(1) if m else ""
ps = "running" if "running" in d["pump"] else ("stopped" if "stopped" in d["pump"] else "")
lt = next((l["num"] for l in d["labels"] if l["t"].startswith("LT-101")), None) or ""
print(pct, ps or "-", lt or "-")'
}
# mimic_follows <level> — write LevelPct, wait for the tank's % and the LT-101
# readout to come within 3 of it (the sim moves ±1.7 %/s; the aria-label
# rounds to 0 dp). Prints "pct pump lt".
mimic_follows() {
  local want=$1 i c pct ps lt
  post_tag LevelPct "$want"
  for ((i = 0; i < 16; i++)); do
    c=$(canvas); read -r pct ps lt <<<"$c"
    near "${pct:-x}" "$want" 3 && near "${lt%%[^0-9.-]*}" "$want" 3 && { echo "$c"; return 0; }
    sleep 0.25
  done
  echo "$c"; return 1
}
c=$(canvas)
png=$(shot mimic-before)
info "X39 mimic before: $(mimic)" "$png"
read -r pct ps lt <<<"$c"
if [[ -n $pct && $ps != - ]]; then
  pass "X39 mimic: the canvas renders bound values (tank ${pct}%, pump $ps, LT-101 $lt; api LevelPct $(tagv LevelPct), PumpRun $(tagv PumpRun))" "$png"
else
  fail "X39 mimic: no bound values on the canvas ($c)" "$png"
fi
# LOW: the FBD's seal-in starts the pump below PumpStartLevel (40) — the pump
# follows the controller, not just the level.
for step in "18 true running" "88 false stopped"; do
  read -r lvl run want <<<"$step"
  if c=$(mimic_follows "$lvl"); then
    png=$(shot "mimic-level-$lvl")
    pass "X39 mimic: LevelPct written $lvl → tank and LT-101 read it ($c)" "$png"
  else
    png=$(shot "mimic-level-$lvl")
    fail "X39 mimic: LevelPct written $lvl → canvas reads '$c' (api $(tagv LevelPct))" "$png"
  fi
  api_follows PumpRun "$run" 0 3 >/dev/null || true
  for ((i = 0; i < 12; i++)); do read -r _ ps _ <<<"$(canvas)"; [[ $ps == "$want" ]] && break; sleep 0.25; done
  apr=$(tagv PumpRun)
  if [[ $ps == "$want" && $apr == "$run" ]]; then
    pass "X39 mimic: P-101 shows $ps with PumpRun $apr (the seal-in at level $lvl)" "$(shot "mimic-pump-$ps")"
  elif [[ $ps == running && $apr == true || $ps == stopped && $apr == false ]]; then
    warn "X39 mimic: P-101 shows $ps = PumpRun $apr, but the control loop did not move it as expected at level $lvl" "$(shot "mimic-pump-$ps")"
  else
    fail "X39 mimic: P-101 shows '$ps' while /api/state PumpRun is $apr" "$(shot "mimic-pump-x")"
  fi
done
