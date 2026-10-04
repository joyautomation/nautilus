#!/usr/bin/env bash
# 17 — three extension claims nothing else exercised in a real VS Code
# (INVENTORY rows C25, X01, X21):
#
#   C25  "nautilus: Create Project…" (nautilus.newProject): parent folder,
#        name, template quick pick (Minimal), then the "created <name>/"
#        toast's Open Here — the project is on disk, checks clean with the
#        CLI under test, and the window reopens on it. Then the same flow
#        from the Get Started walkthrough's "Create a project" step (the link
#        in the step's markdown media, a webview).
#   X01  workbench.editorAssociations {"*.ld": "nautilus.ldDiagram"} opens a
#        .ld as the Ladder diagram custom editor; without it, as text.
#   X21  the FbMonitorLenses CodeLens over a FUNCTION_BLOCK header (blocks.st
#        RateOfChange, instantiated twice in program.fbd) runs
#        nautilus.fb.monitor: a quick pick of the declared instances; picking
#        one retitles the lens and the block body's live-value pills read
#        that instance's members.
#
# Everything here lives in the WORKBENCH page (quick input, toasts, tabs,
# CodeLens), so it is read with cdp.js `page` and clicked by xdotool at the
# element's box (gestures.sh's page_el_box).
#
# The parent-folder picker is files.simpleDialog (a quick input, not the
# native GTK dialog the grab cannot see): the content beat 01-create.sh's
# approach. That beat documents a first-contact race — the command is what
# ACTIVATES the extension in a folder with no nautilus.yaml, and activation
# also auto-opens the walkthrough (first run) — so the first palette attempt
# is "cold" and gets retried; the retry is reported as a NOTE.
set -euo pipefail
CHECK=17-create-project
source "$HOME/smoke/lib.sh"
export G_PACE=fast
source "$HOME/fixtures/gestures.sh"
rm -rf "$PROFILE"

# ── workbench-page helpers ──────────────────────────────────────────────────
pg() { cdp page "$1" 2>/dev/null || true; }
pg_true() { [[ $(pg "!!($1)") == true ]]; }
wait_pg() { # <js boolean> [seconds]
  local i
  for ((i = 0; i < ${2:-10} * 4; i++)); do pg_true "$1" && return 0; sleep 0.25; done
  return 1
}
# pg_click <js -> Element> [settle] — xdotool click at the element's centre.
pg_click() {
  local b x y w h
  b=$(page_el_box "$1") || return 1
  read -r x y w h <<<"$b"
  (( w > 0 && h > 0 )) || return 1
  g_click $((x + w / 2)) $((y + h / 2)) "${2:-1}"
}
# js_str <text> — a JS string literal.
js_str() { python3 -c 'import json,sys; print(json.dumps(sys.argv[1]))' "$1"; }
# A visible workbench button (dialog / picker / toast) with exactly this text.
btn_js() { echo "[...document.querySelectorAll('a.monaco-button, .monaco-button, button')].find(b => b.textContent.trim() === $(js_str "$1") && b.offsetParent)"; }

QI_TITLE='(() => { const w = document.querySelector(".quick-input-widget"); return w && w.style.display !== "none" ? (w.querySelector(".quick-input-title")?.textContent.trim() ?? "") : null; })()'
QI_ROWS='[...document.querySelectorAll(".quick-input-widget .quick-input-list .monaco-list-row")].map(r => r.getAttribute("aria-label") || r.textContent.trim())'
qi_title() { pg "$QI_TITLE" | python3 -c 'import json,sys; v=json.load(sys.stdin); print("" if v is None else v)' 2>/dev/null || true; }
qi_rows() { pg "$QI_ROWS" | python3 -c 'import json,sys; print(" | ".join(json.load(sys.stdin)))' 2>/dev/null || true; }
qi_row_js() { echo "[...document.querySelectorAll('.quick-input-widget .quick-input-list .monaco-list-row')].find(r => (r.getAttribute('aria-label') || r.textContent).trim().startsWith($(js_str "$1")))"; }
TOASTS='[...document.querySelectorAll(".notification-list-item")].map(n => n.textContent.trim())'

# ════ C25: Create Project ═════════════════════════════════════════════════
# create_flow <label> <parent dir> <name> — from an open folder picker
# ("Create project here") to the window reopened on <parent>/<name>. One
# PASS/FAIL per step; status 1 at the first step that does not happen.
create_flow() {
  local lbl=$1 parent=$2 name=$3 png t rows i
  png=$(shot "$lbl-1-folder")
  pass "$lbl: parent-folder picker up (\"Create project here\", on $parent)" "$png"
  pg_click "$(btn_js 'Create project here')" 1.2 || { fail "$lbl: could not click \"Create project here\"" "$png"; return 1; }

  wait_pg "$QI_TITLE === 'nautilus: Create Project'" 10 || true
  png=$(shot "$lbl-2-name")
  t=$(qi_title)
  [[ $t == "nautilus: Create Project" ]] || { fail "$lbl: no name input box after the folder (quick input: '${t:-none}')" "$png"; return 1; }
  pass "$lbl: name input box (\"nautilus: Create Project\")" "$png"
  xdotool type --delay 40 "$name"; sleep 0.6; xdotool key Return; sleep 1.2

  wait_pg "($QI_TITLE || '').includes('template')" 10 || true
  rows=$(qi_rows); t=$(qi_title)
  png=$(shot "$lbl-3-template")
  if [[ $t == *template* && $rows == Demo* && $rows == *Minimal* && $rows == *"SDK demo"* ]]; then
    pass "$lbl: template quick pick — $rows" "$png"
  else
    fail "$lbl: template quick pick wrong or missing (title '$t'; rows: $rows)" "$png"; return 1
  fi
  pg_click "$(qi_row_js Minimal)" 1.5 || { fail "$lbl: could not click the Minimal row" "$png"; return 1; }

  # naut new runs under a progress toast, then "created <name>/" with
  # Open in New Window / Open Here.
  wait_pg "$(btn_js 'Open Here')" 30 || true
  png=$(shot "$lbl-4-created")
  if pg_true "$(btn_js 'Open Here')" && pg "$TOASTS" | grep -q "created $name/"; then
    pass "$lbl: \"nautilus: created $name/\" toast with Open Here / Open in New Window" "$png"
  else
    fail "$lbl: no \"created $name/\" toast (toasts: $(pg "$TOASTS"))" "$png"; return 1
  fi
  [[ -f $parent/$name/nautilus.yaml ]] && pass "$lbl: $parent/$name/nautilus.yaml exists" \
    || { fail "$lbl: no $parent/$name/nautilus.yaml (ls: $(ls "$parent/$name" 2>&1 | tr '\n' ' '))"; return 1; }
  if [[ -f $parent/$name/program.st && ! -e $parent/$name/sim.st ]] && grep -q '^name: '"$name" "$parent/$name/nautilus.yaml"; then
    pass "$lbl: Minimal template (program.st, no Demo sim.st; manifest name: $name)"
  else
    fail "$lbl: not the Minimal template: $(ls "$parent/$name" | tr '\n' ' ')"
  fi
  local out rc=0
  out=$(cd "$parent/$name" && naut check . 2>&1) || rc=$?
  (( rc == 0 )) && pass "$lbl: naut check (the CLI under test) clean — ${out//$'\n'/ }" \
    || fail "$lbl: naut check exit $rc — ${out//$'\n'/ }"

  pg_click "$(btn_js 'Open Here')" 1 || { fail "$lbl: could not click Open Here"; return 1; }
  for i in $(seq 60); do [[ $(title) == *"$name"* ]] && break; sleep 0.5; done
  sleep 3
  png=$(shot "$lbl-5-opened")
  [[ $(title) == *"$name"* ]] && pass "$lbl: Open Here reopened the window on $name (title: $(title))" "$png" \
    || fail "$lbl: window never reopened on $name (title: $(title))" "$png"
}

picker_up() { pg_true "$(btn_js 'Create project here')"; }

# ── from the palette ─────────────────────────────────────────────────────
PARENT=$HOME/c25-parent
rm -rf "$PARENT" "$HOME/c25-walk"; mkdir -p "$PARENT" "$HOME/c25-walk"
EXTRA_SETTINGS='"files.simpleDialog.enable": true' smoke_open "$PARENT"
key Escape; vs_cmd "Notifications: Clear All Notifications" 1

up=
for attempt in 1 2 3; do
  vs_cmd "nautilus: Create Project" 2
  if wait_pg "$(btn_js 'Create project here')" 10; then up=$attempt; break; fi
  png=$(shot "palette-attempt$attempt-lost")
  info "palette attempt $attempt: no folder picker (title '$(title)'; quick input '$(qi_title)'; toasts $(pg "$TOASTS")) — retrying" "$png"
  key Escape; vs_cmd "Notifications: Clear All Notifications" 1; vs_cmd "View: Close All Editors" 1
done
if [[ -z $up ]]; then
  fail "palette: \"nautilus: Create Project…\" never opened the folder picker in 3 attempts" "$png"
else
  (( up > 1 )) && info "palette: the picker came up on attempt $up (the cold first run lost it — see the NOTE rows above)"
  create_flow palette "$PARENT" smoke-new || true
fi

# ── from the Get Started walkthrough ─────────────────────────────────────
# A fresh window on another empty folder (the walkthrough flag is already
# set, so open it with nautilus.getStarted), then the "Create a project"
# step, then the link in its markdown media.
EXTRA_SETTINGS='"files.simpleDialog.enable": true' smoke_open "$HOME/c25-walk"
key Escape; vs_cmd "Notifications: Clear All Notifications" 1
vs_cmd "nautilus: Get Started" 4
STEP_JS="[...document.querySelectorAll('.getting-started-step')].find(s => /Create a project/.test(s.textContent))"
if ! wait_pg "$STEP_JS" 10; then
  png=$(shot walkthrough-missing)
  info "walkthrough: no \"Create a project\" step in the page DOM (title '$(title)') — walkthrough half not driven" "$png"
else
  pg_click "$STEP_JS" 2.5
  LINK_JS='[...doc.querySelectorAll("a")].find(a => /Create a project/.test(a.textContent))'
  link=; for i in $(seq 20); do link=$(el_box "$LINK_JS") && break; link=; sleep 0.5; done
  png=$(shot walkthrough-step)
  if [[ -z $link ]]; then
    info "walkthrough: step expanded but its \"Create a project\" link (markdown media webview) was not reachable over CDP — walkthrough half not driven" "$png"
  else
    pass "walkthrough: \"Create a project\" step shows its Create a project link" "$png"
    read -r lx ly lw lh lv <<<"$link"
    g_click $((lx + lw / 2)) $((ly + lh / 2)) 2
    if wait_pg "$(btn_js 'Create project here')" 10; then
      create_flow walkthrough "$HOME/c25-walk" smoke-walk || true
    else
      png=$(shot walkthrough-no-picker)
      fail "walkthrough: clicking the step's link opened no folder picker (quick input '$(qi_title)')" "$png"
    fi
  fi
fi

# ════ X01: editorAssociations → diagram by default ═════════════════════════
ext_scaffold x01-plant
# program.fbd gets a second RateOfChange instance for X21 (committed, so
# the scaffold stays clean).
sed -i 's/^  TempRate := roc.OUT$/&\n  roc2 : RateOfChange(IN := LevelPct, DT := ScanDtS)/' "$PROJ/program.fbd"
git -C "$PROJ" commit -qam "a second RateOfChange instance"
WS=$PROJ/.vscode/settings.json
assoc() { # on|off — the workspace setting, edited as JSON (the scaffold's has // comments)
  python3 - "$WS" "$1" <<'PY'
import json, re, sys
p, mode = sys.argv[1], sys.argv[2]
src = open(p).read()
d = json.loads(re.sub(r'^\s*//.*$', '', src, flags=re.M))
if mode == "on":
    d["workbench.editorAssociations"] = {"*.ld": "nautilus.ldDiagram"}
else:
    d.pop("workbench.editorAssociations", None)
open(p, "w").write(json.dumps(d, indent=2) + "\n")
PY
}
# editor_kind — what the active editor group shows: "diagram" (no text
# editor, a visible joyauto.vscode-iec webview) / "text" / "?", then the
# tab's resource name; the raw page answer is in $KIND_RAW.
KIND_JS='(() => {
  const g = document.querySelector(".editor-group-container.active");
  const tab = g && g.querySelector(".tab.active");
  const te = g && [...g.querySelectorAll(".editor-container .monaco-editor")].find(e => e.offsetParent);
  const wv = [...document.querySelectorAll("iframe.webview")].filter(f => { const r = f.getBoundingClientRect(); return r.width > 50 && r.height > 50 && getComputedStyle(f).visibility !== "hidden"; }).map(f => f.src);
  return { tab: tab && tab.getAttribute("aria-label"), resource: tab && tab.getAttribute("data-resource-name"), textEditor: !!te, webviews: wv.map(s => s.replace(/^.*?\?/, "")) };
})()'
editor_kind() {
  local j; j=$(pg "$KIND_JS"); KIND_RAW=$j
  python3 -c '
import json, sys
d = json.loads(sys.argv[1] or "null") or {}
ours = [w for w in d.get("webviews", []) if "joyauto.vscode-iec" in w.lower()]
if not d.get("textEditor") and ours: print("diagram", d.get("resource"))
elif d.get("textEditor") and not ours: print("text", d.get("resource"))
else: print("?", d.get("resource"))' "$j"
}

assoc on
smoke_open "$PROJ"
key Escape; vs_cmd "Notifications: Clear All Notifications" 1
open_file interlocks.ld 6
kind=$(editor_kind)
png=$(shot x01-assoc-on)
if [[ $kind == "diagram interlocks.ld" ]]; then
  pass "X01: with editorAssociations *.ld → nautilus.ldDiagram, Quick Open gives the Ladder diagram editor (tab interlocks.ld, webview, no text editor)" "$png"
else
  fail "X01: with the association set, interlocks.ld opened as '$kind' (want diagram; page: $(pg "$KIND_JS"))" "$png"
fi

assoc off
sleep 2
vs_cmd "View: Close All Editors" 1.5
# Quick Open lists interlocks.ld under "recently opened", and a history
# entry reopens in the editor it was last shown in (the diagram) whatever
# the associations now say — so clear the history first, or this half
# tests VS Code's history instead of the setting.
vs_cmd "Clear Editor History" 1.5
dialog_up && page_click_button 'Clear' 1.5 || true
open_file interlocks.ld 4
kind=$(editor_kind)
png=$(shot x01-assoc-off)
if [[ $kind == "text interlocks.ld" ]]; then
  pass "X01: association removed (settings.json edited live, editor history cleared) → interlocks.ld opens as text again" "$png"
else
  fail "X01: without the association interlocks.ld opened as '$kind' (want text; page: $(pg "$KIND_JS"))" "$png"
fi

# prep.sh says the association was ignored for a file passed on the COMMAND
# LINE (opened before the extension activates). Evidence only.
assoc on
smoke_open "$PROJ" interlocks.ld
sleep 3
kind=$(editor_kind)
png=$(shot x01-cmdline)
info "X01: association set, interlocks.ld passed on the command line → '$kind'" "$png"
assoc off

# ════ X21: the FB monitor CodeLens ════════════════════════════════════════
ext_run 6
smoke_open "$PROJ"
key Escape; vs_cmd "Notifications: Clear All Notifications" 1
open_file blocks.st 6
LENS_JS='[...document.querySelectorAll(".codelens-decoration")].map(e => e.textContent.trim()).filter(t => /live values/.test(t))'
lens() { pg "$LENS_JS" | python3 -c 'import json,sys; print(" | ".join(json.load(sys.stdin)))' 2>/dev/null || true; }
wait_pg "($LENS_JS).length > 0" 20 || true
# Two declared instances (roc, roc2): no auto-monitor, so the lens asks.
wait_pg "($LENS_JS).some(t => /monitor an instance/.test(t))" 10 || true
l=$(lens)
# Above the header: the lens's bottom edge sits on the FUNCTION_BLOCK line's top.
ABOVE_JS='(() => {
  const a = [...document.querySelectorAll(".codelens-decoration")].find(e => /live values/.test(e.textContent));
  const h = [...document.querySelectorAll(".view-line")].find(e => /^\s*FUNCTION_BLOCK\s+RateOfChange/.test(e.textContent));
  if (!a || !h) return null;
  const ra = a.getBoundingClientRect(), rh = h.getBoundingClientRect();
  return Math.round(rh.top - ra.bottom);
})()'
gap=$(pg "$ABOVE_JS")
png=$(shot x21-lens)
if [[ -z $l ]]; then
  fail "X21: no live-values CodeLens on blocks.st (controller running) — the lens never appeared" "$png"
else
  if [[ $gap =~ ^-?[0-9]+$ ]] && (( gap >= -2 && gap <= 12 )); then
    pass "X21: CodeLens \"$l\" directly above FUNCTION_BLOCK RateOfChange (gap ${gap}px)" "$png"
  else
    fail "X21: CodeLens \"$l\" is not on the FUNCTION_BLOCK header line (gap: $gap)" "$png"
  fi
  [[ $l == *"monitor an instance"* ]] || warn "X21: with two declared instances the lens reads \"$l\" (expected \"○ live values: monitor an instance…\")" "$png"
fi

# Pills in the block body: decoration spans whose ::after carries a value.
PILLS_JS='(() => {
  const ed = [...document.querySelectorAll(".editor-group-container.active .monaco-editor")].find(e => e.offsetParent);
  if (!ed) return [];
  return [...ed.querySelectorAll(".view-lines [class*=ced-]")].map(e => {
    const c = getComputedStyle(e, "::after").content;
    return (c && c !== "none" && c !== "normal") ? c.replace(/^"|"$/g, "") : e.textContent;
  }).filter(t => t && t.trim());
})()'
pills() { pg "$PILLS_JS" | python3 -c 'import json,sys; v=json.load(sys.stdin); print(len(v), " ".join(v[:8]))' 2>/dev/null || echo "0"; }
before=$(pills)

LENS_A='[...document.querySelectorAll(".codelens-decoration a")].find(a => /live values/.test(a.textContent))'
if [[ -n $l ]] && pg_click "$LENS_A" 1.5; then
  wait_pg "$QI_TITLE === 'Monitor which RateOfChange instance?'" 8 || true
  t=$(qi_title); rows=$(qi_rows)
  png=$(shot x21-quickpick)
  if [[ $t == "Monitor which RateOfChange instance?" && $rows == *roc* && $rows == *roc2* ]]; then
    pass "X21: clicking the lens runs nautilus.fb.monitor — quick pick \"$t\": $rows" "$png"
    pg_click "$(qi_row_js roc2)" 2 || fail "X21: could not click the roc2 row" "$png"
    wait_pg "($LENS_JS).some(t => /monitoring roc2/.test(t))" 10 || true
    sleep 2
    l=$(lens); after=$(pills)
    png=$(shot x21-monitoring)
    [[ $l == *"monitoring roc2"* ]] && pass "X21: picking roc2 retitles the lens: \"$l\"" "$png" \
      || fail "X21: after picking roc2 the lens reads \"$l\"" "$png"
    if (( ${after%% *} > ${before%% *} )); then
      pass "X21: the RateOfChange body's pills now read roc2 (${before%% *} → ${after%% *} pills: ${after#* })" "$png"
    else
      fail "X21: no live-value pills in the body after monitoring roc2 (before: $before; after: $after)" "$png"
    fi
    # roc2 is fed LevelPct, whose rate is ±90–102 %/min while the pump
    # cycles; roc (TempC) moves a few °C/min. The OUT pill says which.
    info "X21: body pills after the pick (roc2 = LevelPct's rate, ≈ -90 or +102 /min on OUT): ${after#* }" "$png"
    [[ $l == *"monitoring roc2 — 1 of 2"* ]] && warn "X21: the lens says \"1 of 2\" while monitoring roc2, the SECOND declared instance — the count is hard-coded \"1 of \${n}\" (liveValues.ts FbMonitorLenses), so it reads as a position (#146)" "$png"
  else
    fail "X21: the lens click opened no instance quick pick (quick input '$t'; rows: $rows; toasts: $(pg "$TOASTS"))" "$png"
  fi
elif [[ -n $l ]]; then
  fail "X21: could not click the CodeLens"
fi
true
