# gestures.sh — the rig's verbs: drive the nautilus VS Code extension's
# GRAPHICAL editors (SFC, Ladder, FBD, Mimic, Component ports) and the
# workbench's own dialogs the way a person does: pointer and keyboard, through
# xdotool, on the rig's window. SOURCED inside the container, after prep.sh
# (which sources lib.sh and puts the CDP `code` wrapper on PATH); every entry
# point pushes tools/rig/verbs/ in as ~/fixtures:
#
#     source "$HOME/fixtures/prep.sh"
#     source "$HOME/fixtures/gestures.sh"
#
# HOW A VERB FINDS ITS TARGET. Semantic arguments ("step Fill", "rung
# pump_run", "pin IN1 of block w1", "equipment P101 port out") are resolved to
# window pixels at the moment of the gesture by asking the webview's DOM where
# that element is (cdp.js, beside this file, over VS Code's DevTools port —
# READ-ONLY: getBoundingClientRect, never a synthetic event). Every click,
# drag and keystroke is still xdotool on the real window, so what a take
# records is what a person would do. Nothing here assumes a window size, a zoom level or
# a layout: a chart that grew three steps since the last beat still resolves.
#
# The few pixel constants left are named where they are used, with the frame
# they were measured at.
#
# Every verb is SAVE-then-READ: it waits for the edit to round-trip (the
# diagram re-renders from the text), saves, and greps the file. On a miss it
# returns non-zero with a one-line reason on stderr; `g_try` in a self-test
# turns that into a FAIL line instead of a dead beat.
#
# THE VERBS (each returns 0 only once the saved file shows the edit):
#   ed_open_diagram <file>                    diagram EDITOR, one editor group
#   diagram_zoom in|out|fit [n]               Ctrl+= / Ctrl+- / Ctrl+0
#   SFC   sfc_init · sfc_add_step <after|-> <name>
#         sfc_add_transition <from> <to> [cond]          (adds <to> if new)
#         sfc_add_transition_condition <From->To> <cond>
#         sfc_add_action <step> <qualifier> <target> [time]
#         sfc_add_alt_branch <from> <to> [cond] · sfc_add_parallel_branch <From->To> <step>
#         sfc_rename_step <old> <new>
#   LD    ld_add_rung [name] · ld_add_contact <rung> <tag> [nc]
#         ld_add_coil <rung> <tag> [set|reset] · ld_add_block <rung> <TYPE> [inst] [args]
#         ld_rename_block <rung> <old> <new> · ld_declare <name> [VAR_EXTERNAL|VAR]
#         ld_add_branch <rung> <around-tag> [leg-tag]
#   FBD   fbd_add_block <FN|FB TYPE> <name> [inputs|args] [x y] · fbd_zoom_to <node> [notches]
#         fbd_wire <node.PIN> <node.PIN> · fbd_add_tag_ref <tag> [node.PIN]
#         fbd_add_comment <text>
#   Mimic mimic_drop <Component> <x> <y> [id] · mimic_bind <id> <prop> <tag>
#         mimic_pipe <id.port> <id.port>  (mimic_pipe_direct: the documented
#         gesture, broken in 0.11.1 — see the content repo's
#         ex01-lift-station/GESTURE-FINDINGS.md)
#   Ports component_edit_ports <Component> · component_add_port <name>
#   assert_file_contains <file> <ERE>         save, then grep
#   LD+   ld_select_node · ld_add_contact_after · ld_wrap_branch · ld_add_leg
#         ld_toggle_nc · ld_retag_fb_args · ld_edit_fb_args · ld_retag_placeholder
#         ld_delete_node · ld_add_contact_c · ld_add_coil_c · ld_op_trunc
#   Mimic+ mimic_bind_at <id> <fx> <fy> <prop> <tag>
#   Page  page_el_box · page_click_button · page_text · dialog_up
#         (the WORKBENCH page: custom dialogs, notification toasts)
#   API   api_get · api_post (the controller at $PORT) · px_count (a frame)
# The LD+/Mimic+/Page/API verbs were gestures-c.sh and beats-lib-b.sh in the
# content repo (ex01-lift-station's build beats); they are folded in here,
# below the editors they drive, and are not all in selftest.sh yet.
# Lower-level: cdp/js/el_box/el_at/el_settled, click_el/dclick_el, g_drag_to,
# float_edit, wait_js. No verb assumes a window size; the rig's (prep.sh:
# 1600x1000, zoomLevel 2.5) is simply what they were proven at.
#
# PACE. G_PACE=human (default) moves the pointer with lib.sh's human_move —
# what a take wants. G_PACE=fast teleports (rehearsals, the self-test's
# re-runs). Either way there are settles after each gesture for the round
# trip through `naut <lang> edit`.

G_PACE=${G_PACE:-human}
# cdp.js sits beside this file (tools/rig/verbs/, pushed in as ~/fixtures).
G_RIG=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)

# ── plumbing ────────────────────────────────────────────────────────────────

g_err() { printf '  \033[31mgesture:\033[0m %s\n' "$*" >&2; return 1; }

# _g_frame — the CSD shadow margins (launch_vscode grabs inside them; xdotool
# --window coordinates include them). cdp.js adds them to every box.
_g_frame() {
  local ext l r t b
  ext=$(xprop -id "$WIN" _GTK_FRAME_EXTENTS 2>/dev/null | grep -oE '[0-9]+(, [0-9]+){3}') || true
  [[ -n $ext ]] || { echo "0 0"; return; }
  IFS=', ' read -r l r t b <<<"$ext"
  echo "$l $t"
}

# cdp <cmd> <js> — see cdp.js. `doc` is the ACTIVE webview's document.
cdp() {
  local fl ft
  read -r fl ft <<<"$(_g_frame)"
  FRAME_L=$fl FRAME_T=$ft ELECTRON_RUN_AS_NODE=1 /usr/share/code/code "$G_RIG/cdp.js" "$@"
}
# js <expr> — evaluate in the active webview, print the JSON result.
js() { cdp eval "$1"; }
# js_true <boolean expr> — status only.
js_true() { [[ $(cdp eval "!!($1)" 2>/dev/null) == true ]]; }

# el_box <js → Element> — "x y w h" in window coordinates; fails if absent.
el_box() { cdp rect "$1" 2>/dev/null; }
# g_reveal <js → Element> — bring the element into view and out from under
# anything drawn over it, the way a person would: Ladder and SFC scroll on a
# plain wheel (up/down), and the zoom controls sit on their bottom-left, so
# a covered or off-screen target is wheeled clear. Off to a SIDE (a zoomed
# ladder) or anywhere off-screen in FBD (whose wheel ZOOMS, and whose empty-
# canvas drag pans — a drag that starts on a node moves it instead) gets the
# editor's own fit button, once: a fitted view shows everything.
g_reveal() {
  local b x y w h v i vx vy fitted= prev=
  for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14; do
    b=$(el_box "$1") || return 1
    read -r x y w h v <<<"$b"
    [[ ${v:-1} == 1 ]] && return 0
    # wheeling no longer moves it (the pane is at the end of its scroll and
    # the zoom controls still cover it): treat like off-to-a-side
    [[ "$x $y" == "$prev" ]] && v=3
    prev="$x $y"
    if js_true 'doc.querySelector(".svelte-flow")'; then
      [[ -n $fitted ]] && break
      _g_fit_button 'doc.querySelector(".svelte-flow__controls-fitview")' || break
      fitted=1; continue
    fi
    # mimic / component: the canvas is scaled to fit — nothing to scroll to
    js_true 'doc.querySelector(".canvas > .eq, .stage, aside button.item")' && return 0
    if [[ $v == 3 ]]; then
      [[ -n $fitted ]] && break
      _g_fit_button 'doc.querySelector(".zctl button[aria-label=\"fit view\"]")' || break
      fitted=1; continue
    fi
    read -r vx vy <<<"$(cdp vp)"
    xdotool mousemove --window "$WIN" "$vx" "$vy"; sleep 0.2
    case $v in
      -1) xdotool click 4 ;;
      2)  xdotool click 5 ;;
      0)  if (( y > vy )); then xdotool click 5; else xdotool click 4; fi ;;
    esac
    sleep 0.5
  done
  g_err "could not bring ${1:0:60}… into view"
}
# _g_fit_button <js → button> — click a fit-view button (no reveal: it is
# always on screen) and let the view settle.
_g_fit_button() {
  local b x y w h v
  b=$(el_box "$1") || return 1
  read -r x y w h v <<<"$b"
  g_click $((x + w / 2)) $((y + h / 2)) 1.2
}

# el_at <js → Element> [fx] [fy] — a point inside the element's box at
# fraction fx,fy (default the centre): "x y". Scrolls it into view first.
el_at() {
  local b x y w h v
  g_reveal "$1" || true
  b=$(el_box "$1") || return 1
  read -r x y w h v <<<"$b"
  awk -v x="$x" -v y="$y" -v w="$w" -v h="$h" -v fx="${2:-0.5}" -v fy="${3:-0.5}" \
    'BEGIN{printf "%d %d\n", x + w*fx, y + h*fy}'
}

# wait_js <boolean expr> [seconds] — poll the webview until it holds.
wait_js() {
  local limit=${2:-10} i
  for ((i = 0; i < limit * 4; i++)); do js_true "$1" && return 0; sleep 0.25; done
  return 1
}

# g_move <x> <y> — pointer to a window point, at the take's pace.
g_move() {
  if [[ $G_PACE == human ]]; then human_move "$1" "$2" 30 0.6
  else xdotool mousemove --window "$WIN" "$1" "$2"; fi
  sleep 0.2
}
g_click() { g_move "$1" "$2"; xdotool click 1; sleep "${3:-0.6}"; }
g_dclick() { g_move "$1" "$2"; xdotool click --repeat 2 --delay 90 1; sleep "${3:-0.8}"; }
# click_el / dclick_el <js → Element> [fx fy] — aim at an element, click.
click_el() { local p; p=$(el_at "$1" "${2:-0.5}" "${3:-0.5}") || { g_err "not on screen: ${1:0:90}…"; return 1; }; g_click $p; }
dclick_el() { local p; p=$(el_at "$1" "${2:-0.5}" "${3:-0.5}") || { g_err "not on screen: ${1:0:90}…"; return 1; }; g_dclick $p; }
# g_drag <x1> <y1> <x2> <y2> — press, carry, release (human_drag at human
# pace; a stepped drag otherwise — a teleport between press and release is
# not a drag to pointer-event code).
g_drag() {
  if [[ $G_PACE == human ]]; then human_drag "$@"; sleep 0.6; return; fi
  local i
  xdotool mousemove --window "$WIN" "$1" "$2"; sleep 0.2; xdotool mousedown 1; sleep 0.2
  for i in 1 2 3 4 5 6 7 8 9 10; do
    xdotool mousemove --window "$WIN" $(($1 + ($3 - $1) * i / 10)) $(($2 + ($4 - $2) * i / 10)); sleep 0.04
  done
  sleep 0.2; xdotool mouseup 1; sleep 0.6
}
# el_settled <js → Element> [fx fy] — el_at, once the element has stopped
# moving (two reads 0.4 s apart agree): the FBD re-lays out a network after
# an insert, and a pin measured mid-shuffle is a pin on the wrong block.
el_settled() {
  local a b i
  a=$(el_at "$@") || return 1
  for i in 1 2 3 4 5 6 7 8 9 10; do
    sleep 0.4; b=$(el_at "$@") || return 1
    [[ $a == "$b" ]] && { echo "$b"; return 0; }
    a=$b
  done
  echo "$b"
}
# g_drag_to <x1> <y1> <js → target Element> — press at x1,y1, carry to the
# target, RE-AIM at the target's current centre before letting go (a drag
# that starts a relayout would otherwise drop where the target used to be).
g_drag_to() {
  local x1=$1 y1=$2 t=$3 p
  p=$(el_settled "$t") || { g_err "no drop target"; return 1; }
  if [[ $G_PACE == human ]]; then human_move "$x1" "$y1" 40 0.8; sleep 0.5
  else xdotool mousemove --window "$WIN" "$x1" "$y1"; sleep 0.2; fi
  xdotool mousedown 1; sleep 0.35
  if [[ $G_PACE == human ]]; then human_move $p 55 1.2
  else local i x2 y2; read -r x2 y2 <<<"$p"; for i in 1 2 3 4 5 6 7 8 9 10; do xdotool mousemove --window "$WIN" $((x1 + (x2 - x1) * i / 10)) $((y1 + (y2 - y1) * i / 10)); sleep 0.04; done; fi
  sleep 0.3
  p=$(el_at "$t") && xdotool mousemove --window "$WIN" $p
  sleep 0.3; xdotool mouseup 1; sleep 0.8
}

# g_type <text> — into whatever has focus, at a readable speed.
# _g_focus — keys go to the X-focused window, and the rig's window can lose
# focus (a dimmed title bar; seen after a quick-open): re-activate it first.
_g_focus() { [[ $(xdotool getactivewindow 2>/dev/null) == "$WIN" ]] || { xdotool windowactivate --sync "$WIN" 2>/dev/null; sleep 0.3; }; }
g_type() { _g_focus; xdotool type --delay "${G_TYPE_DELAY:-45}" -- "$1"; sleep 0.3; }
g_key() { _g_focus; xdotool key --clearmodifiers "$@"; sleep 0.4; }

# button_el <label> — a <button> whose text is exactly <label> (trimmed).
button_el() { printf '[...doc.querySelectorAll("button")].find(b => b.textContent.trim() === %s && b.offsetParent !== null)' "$(_q "$1")"; }
# _q <s> — a JS string literal.
_q() { python3 -c 'import json,sys; print(json.dumps(sys.argv[1]))' "$1"; }
click_button() { click_el "$(button_el "$1")" || g_err "no visible button '$1'"; }

# float_edit <text> — the in-place editor a double-click opens
# (FloatEditor.svelte): select what is there, type over it, Enter. Waits for
# the field to exist and hold focus first — typing into nothing would land
# the keystrokes on the canvas as shortcuts (N, M, B, Del…).
float_edit() {
  wait_js 'doc.activeElement && /^(INPUT|TEXTAREA)$/.test(doc.activeElement.tagName)' 5 \
    || { g_err "no in-place editor took focus"; return 1; }
  g_key ctrl+a
  g_type "$1"
  g_key Return
  sleep 0.8
}

# ── files ───────────────────────────────────────────────────────────────────

# g_save — Ctrl+S with focus in the diagram (every diagram editor forwards
# it to the document), then a beat for the write.
g_save() { g_key ctrl+s; sleep 1.2; }

# assert_file_contains <file> <regex> — save, then grep -E the file. The
# lib.sh assert_buffer_clean idea (save FIRST, then read disk), but with a
# return code instead of a die, so a self-test can go on to the next verb.
assert_file_contains() {
  local f=$1 re=$2
  [[ $f == /* ]] || f=$PROJ/$f
  g_save
  grep -Eq -- "$re" "$f" || { g_err "$(basename "$f") has no /$re/"; return 1; }
}

# ── opening ─────────────────────────────────────────────────────────────────

# editor_groups — how many editor groups the workbench has.
editor_groups() { cdp page 'document.querySelectorAll(".editor-group-container").length'; }

# diagram_ready — the active webview has painted its editor (any of them).
_G_READY='doc.querySelector(".svelte-flow__node, svg.rsvg, svg.chart, .palette, .canvas, .banner, .host .bar")'

# ed_open_diagram <file> — <file> (relative to $PROJ) in its diagram EDITOR,
# as the only editor group.
#
# .fbd/.ld/.sfc register their diagram editors at priority "option" (README
# trap 2), so this opens the TEXT by quick-open and then runs "Open as
# Diagram Editor", which REPLACES the tab. Everything else is closed first:
# that command fuzzy-matches the preview command when the file is already a
# diagram (README, "Pixel boxes assume ONE editor group"), and a second
# group halves the canvas every other verb aims into.
# *.mimic.json / *.component.json are default-priority custom editors, so
# quick-open alone gives the editor (once the extension is active — it is,
# after launch_vscode on a nautilus project).
ed_open_diagram() {
  local f=$1 base
  G_FILE=$f   # the file every verb after this saves and reads back
  base=$(basename "$f")
  vs_cmd "View: Close All Editors" 1.2
  open_file "$base" 3
  # Quick-open reopens a file with the editor it last had: a .fbd that was
  # a diagram earlier in the session comes back AS the diagram, and then
  # "Open as Diagram Editor" is not offered, the palette fuzzy-matches "Open
  # FBD Diagram Preview", and the area splits. So only ask when what opened
  # is the text (no webview on screen).
  case $f in
    *.fbd|*.ld|*.sfc|*.L5X|*.l5x)
      cdp eval 1 >/dev/null 2>&1 || vs_cmd "nautilus: Open as Diagram Editor" 4 ;;
  esac
  wait_js "$_G_READY" 20 || { g_err "$base: no diagram painted"; return 1; }
  local n; n=$(editor_groups)
  [[ $n == 1 ]] || { g_err "$base: $n editor groups, want 1"; return 1; }
  [[ $(xdotool getwindowname "$WIN") == *"$base"* ]] || { g_err "$base: title is $(xdotool getwindowname "$WIN")"; return 1; }
  sleep 1
}

# ── view ────────────────────────────────────────────────────────────────────

# diagram_zoom <in|out|fit> [times] — Ladder / SFC / FBD zoom by keyboard
# (Ctrl+= / Ctrl+- / Ctrl+0, which the diagrams keep to themselves). Focus
# must be in the diagram: click an empty spot of it first.
diagram_zoom() {
  local k n=${2:-1} i
  case $1 in in) k=ctrl+equal ;; out) k=ctrl+minus ;; fit) k=ctrl+0; n=1 ;; *) g_err "diagram_zoom in|out|fit"; return 1 ;; esac
  local pct='[...doc.querySelectorAll("div, span, button")].find(e => !e.children.length && /^\d+%$/.test(e.textContent.trim()))?.textContent.trim() ?? ""'
  local before after
  # focus the zoom pane (tabindex -1: a click on its empty area focuses it)
  js_true 'doc.querySelector(".zpane .flow")' && click_el 'doc.querySelector(".zpane .flow")' 0.6 0.5
  before=$(js "$pct")
  for ((i = 0; i < n; i++)); do g_key "$k"; sleep 0.3; done
  sleep 0.8
  after=$(js "$pct")
  # Ladder/SFC show a % readout; FBD has none (nothing to compare)
  [[ $before == '""' || $before != "$after" || $1 == fit ]] || { g_err "zoom readout stayed $before"; return 1; }
}

# fbd_zoom_to <node> [notches, default 3] — wheel-zoom the FBD about a node
# (xyflow zooms toward the pointer): the open view fits the WHOLE program,
# pins ~4 px — too small to see a wire land, or to aim a take at.
fbd_zoom_to() {
  local p i
  p=$(el_settled "$(fbd_node_el "$1")") || { g_err "no FBD node $1"; return 1; }
  g_move $p
  for ((i = 0; i < ${2:-3}; i++)); do xdotool click 4; sleep 0.35; done
  sleep 0.8
}

# ── SFC ─────────────────────────────────────────────────────────────────────
# SfcView.svelte: steps are <g class="step"> with <text class="stepname">;
# transitions are <g class="trans"> with a <rect class="barhit"> (or, for a
# loop back, a compact <g class="jump">) and a <text class="cond">. A
# transition has no name on the chart unless the text gives it one, so the
# verbs name it by its ends, "From->To", and find it by geometry: the bar
# between From's box and To's box, over To's column (a loop back is the ↩
# chip whose title says "jumps to To").

sfc_step_el() { printf '[...doc.querySelectorAll("svg.chart g.step")].find(g => g.querySelector("text.stepname")?.textContent.trim() === %s)' "$(_q "$1")"; }

# sfc_trans_el <From->To> [part] — part: "hit" (the click target, default)
# or "cond" (the condition text, for a double-click).
sfc_trans_el() {
  local from=${1%%->*} to=${1##*->} part=${2:-hit}
  cat <<JS
(() => {
  const step = (n) => [...doc.querySelectorAll("svg.chart g.step")].find(g => g.querySelector("text.stepname")?.textContent.trim() === n);
  const A = step($(_q "$from"))?.querySelector("rect.box").getBoundingClientRect();
  const Bg = step($(_q "$to")); const B = Bg?.querySelector("rect.box").getBoundingClientRect();
  if (!A || !B) return null;
  const bcx = B.left + B.width / 2, acx = A.left + A.width / 2;
  let best = null, bd = 1e9;
  for (const t of doc.querySelectorAll("svg.chart g.trans")) {
    const j = t.querySelector("g.jump");
    let r, d;
    if (j) {
      if (!(j.querySelector("title")?.textContent || "").includes("jumps to " + $(_q "$to"))) continue;
      r = j.querySelector("rect.jumpbox").getBoundingClientRect();
      // the ↩ chip hangs just under its SOURCE step
      if (r.top < A.bottom - 4 || r.top > A.bottom + A.height || Math.abs(r.left + r.width / 2 - acx) > A.width / 2) continue;
      d = r.top - A.bottom;
    } else {
      const h = t.querySelector("rect.barhit"); if (!h) continue;
      r = h.getBoundingClientRect();
      const cy = r.top + r.height / 2;
      if (cy < A.bottom - 4 || cy > B.top + 4) continue;
      if (!(r.left <= bcx && r.right >= bcx)) continue;
      d = cy - A.bottom;
    }
    if (d < bd) { bd = d; best = t; }
  }
  if (!best) return null;
  return "$part" === "cond" ? best.querySelector("text.cond") : (best.querySelector("rect.barhit") || best.querySelector("rect.jumpbox"));
})()
JS
}

# sfc_wait_model <js boolean> — the chart re-rendered to include the edit.
sfc_wait() { wait_js "$1" "${2:-10}" || { g_err "the chart never showed the edit (${G_WHAT:-?})"; return 1; }; }

# sfc_select_step <name> — click the step's box (upper third: the ⊙
# connect handle sits on the bottom edge, and a press there starts a
# connect drag instead of selecting).
sfc_select_step() {
  click_el "$(sfc_step_el "$1") && $(sfc_step_el "$1").querySelector('rect.box')" 0.5 0.3 \
    || { g_err "no step $1"; return 1; }
  wait_js "$(sfc_step_el "$1").classList.contains('selected')" 3 || { g_err "step $1 did not select"; return 1; }
}
sfc_select_trans() {
  click_el "$(sfc_trans_el "$1")" || { g_err "no transition $1"; return 1; }
  wait_js "$(sfc_trans_el "$1").closest('g.trans').classList.contains('selected')" 3 || { g_err "transition $1 did not select"; return 1; }
}

# sfc_init — the Empty-file banner's "initialize" (a 0-byte .sfc): writes a
# PROGRAM skeleton with INITIAL_STEP Start.
sfc_init() {
  click_button initialize || return 1
  sfc_wait "$(sfc_step_el Start)" && assert_file_contains "$G_FILE" 'INITIAL_STEP +Start'
}

# _sfc_form_field <n> — the add form's n-th text input (0-based).
_sfc_form_input() { printf '[...doc.querySelectorAll(".addform input.nx-input")][%d]' "$1"; }

# sfc_add_step <after-step|-> <name> [condition] — "+ step" with
# <after-step> selected CHAINS: the new step under it plus TRANSITION FROM
# <after-step> TO <name> := <condition> (the form's second field, TRUE by
# default). With nothing selected (-) it is a free step.
sfc_add_step() {
  local after=$1 name=$2 cond=${3:-}
  if [[ $after != - ]]; then sfc_select_step "$after" || return 1
  else click_el 'doc.querySelector("svg.chart")' 0.95 0.95; fi
  click_button "+ step" || return 1
  wait_js 'doc.activeElement?.matches(".addform input")' 4 || { g_err "the Add step form did not take focus"; return 1; }
  g_key ctrl+a; g_type "$name"
  if [[ $after != - && -n $cond ]]; then
    click_el "$(_sfc_form_input 1)" || { g_err "the Add step form has no condition field"; return 1; }
    g_key ctrl+a; g_type "$cond"
  fi
  g_key Return
  sfc_wait "$(sfc_step_el "$name")" || return 1
  assert_file_contains "$G_FILE" "STEP +$name\\b" || return 1
  [[ $after == - ]] || assert_file_contains "$G_FILE" "FROM +$after +TO +$name\\b"
}

# _sfc_pick <step> — in the add form's focused "to step" <select>, move to
# <step> (or to "other… (new step)" and type the name) with arrow keys: a
# closed <select> takes Up/Down without opening its popup, which xdotool
# could not click into reliably anyway.
_sfc_pick() {
  local want=$1 cur idx
  read -r cur idx <<<"$(js "(() => { const s = doc.querySelector('.addform select'); if (!s) return '-1 -1'; const o = [...s.options].map(o => o.value); let i = o.indexOf($(_q "$want")); if (i < 0) i = o.length - 1; return s.selectedIndex + ' ' + i; })()" | tr -d '"')"
  (( cur >= 0 )) || { g_err "no step picker in the form"; return 1; }
  while (( cur < idx )); do g_key Down; ((cur++)); done
  while (( cur > idx )); do g_key Up; ((cur--)); done
  if ! js_true "[...doc.querySelector('.addform select').options].some(o => o.value === $(_q "$want"))"; then
    g_key Tab; g_key ctrl+a; g_type "$want"
  fi
}

# sfc_add_transition <from> <to> [condition] — "+ transition" from the
# selected step. A <to> that is not a step yet goes through the picker's
# "other… (new step)", which writes the STEP along with the TRANSITION (it
# wrote the transition alone before vscode-iec's sfc-chain fix —
# GESTURE-FINDINGS, SFC 7).
sfc_add_transition() {
  local from=$1 to=$2 cond=${3:-TRUE} new=
  G_WHAT="transition $from->$to"
  js_true "$(sfc_step_el "$to")" || new=1
  sfc_select_step "$from" || return 1
  click_button "+ transition" || return 1
  wait_js 'doc.activeElement?.matches(".addform select")' 4 || { g_err "the transition form did not take focus"; return 1; }
  _sfc_pick "$to" || return 1
  # the condition is the form's last input
  click_el '[...doc.querySelectorAll(".addform input.nx-input")].pop()' || return 1
  g_key ctrl+a; g_type "$cond"; g_key Return
  sfc_wait "$(sfc_trans_el "$from->$to")" || return 1
  [[ -z $new ]] || assert_file_contains "$G_FILE" "STEP +$to:" || return 1
  assert_file_contains "$G_FILE" "FROM +$from +TO +$to\\b"
}

# sfc_add_transition_new_step <from> <to> [condition] — the documented
# gesture for growing a chart: "+ transition" → "other… (new step)" → type
# the name. <to> must NOT exist yet; both the STEP and the TRANSITION must
# land in the file, and no orphan chip may be left on the chart.
sfc_add_transition_new_step() {
  G_WHAT="new step $2 via + transition"
  js_true "$(sfc_step_el "$2")" && { g_err "step $2 already exists"; return 1; }
  sfc_add_transition "$@" || return 1
  sfc_wait "$(sfc_step_el "$2")" || return 1
  js_true '!doc.querySelector("svg.chart g.orphan")' || { g_err "an orphan chip is on the chart"; return 1; }
  assert_file_contains "$G_FILE" "^ *STEP +$2:" && assert_file_contains "$G_FILE" "TRANSITION +FROM +$1 +TO +$2\\b"
}

# sfc_add_transition_condition <From->To> <ST condition> — double-click the
# transition's condition text, type over it.
sfc_add_transition_condition() {
  dclick_el "$(sfc_trans_el "$1" cond)" || { g_err "no transition $1"; return 1; }
  float_edit "$2" || return 1
  local from=${1%%->*} to=${1##*->}
  sfc_wait "$(sfc_trans_el "$1" cond)?.textContent.includes($(_q "$2"))" || return 1
  assert_file_contains "$G_FILE" "$(printf '%s' "$2" | sed 's/[][\.*^$()+?{}|]/\\&/g')"
}

# sfc_add_action <step> <qualifier> <target> [time] — the step's "+ action"
# row: "Q target" or "Q target(T#5S)".
sfc_add_action() {
  local step=$1 q=$2 target=$3 t=${4:-} txt
  txt="$q $target${t:+($t)}"
  click_el "$(sfc_step_el "$step")?.querySelector('g.assocadd text')" || { g_err "no + action on $step"; return 1; }
  float_edit "$txt" || return 1
  sfc_wait "[...($(sfc_step_el "$step")).querySelectorAll('text.assoctarget')].some(t => t.textContent.startsWith($(_q "$target")))" || return 1
  assert_file_contains "$G_FILE" "$target"
}

# sfc_add_alt_branch <from> <to> [condition] — an alternative (OR)
# divergence out of <from>: select the step, "+ alt branch" (another
# transition out of it). A new <to> goes through "other… (new step)".
sfc_add_alt_branch() {
  local from=$1 to=$2 cond=${3:-TRUE} new=
  G_WHAT="alt branch $from->$to"
  js_true "$(sfc_step_el "$to")" || new=1
  sfc_select_step "$from" || return 1
  click_button "+ alt branch" || return 1
  wait_js 'doc.activeElement?.matches(".addform select")' 4 || { g_err "the alt-branch form did not take focus"; return 1; }
  _sfc_pick "$to" || return 1
  click_el '[...doc.querySelectorAll(".addform input.nx-input")].pop()' || return 1
  g_key ctrl+a; g_type "$cond"; g_key Return
  sfc_wait "$(sfc_trans_el "$from->$to")" || return 1
  [[ -z $new ]] || assert_file_contains "$G_FILE" "STEP +$to:" || return 1
  assert_file_contains "$G_FILE" "FROM +$from +TO +$to\\b"
}

# sfc_add_parallel_branch <From->To> <new step> — widen that transition
# into a simultaneous divergence with <new step> as a parallel leg.
sfc_add_parallel_branch() {
  sfc_select_trans "$1" || return 1
  click_button "+ parallel branch" || return 1
  wait_js 'doc.activeElement?.matches(".addform input")' 4 || { g_err "the parallel-branch form did not take focus"; return 1; }
  g_key ctrl+a; g_type "$2"; g_key Return
  sfc_wait "$(sfc_step_el "$2")" || return 1
  assert_file_contains "$G_FILE" "TO +\\(?[A-Za-z0-9_, ]*\\b$2\\b"
}

# sfc_join_step <From->To> <step> — "+ join": widen that transition's FROM
# with <step>, a simultaneous CONVERGENCE (FROM (From, step) TO To) that
# fires once every source is active. The form's picker lists the steps not
# already in FROM; the lift station's PostRun/Alternate join (beat 04) is
# this gesture (text-patched before vscode-iec's editor-ops-polish).
sfc_join_step() {
  local from=${1%%->*} to=${1##*->} step=$2 i
  G_WHAT="join $step into $1"
  sfc_select_trans "$1" || return 1
  click_button "+ join" || return 1
  wait_js 'doc.activeElement?.matches(".addform select")' 4 || { g_err "the join form did not take focus"; return 1; }
  js_true "[...doc.querySelector('.addform select').options].some(o => o.value === $(_q "$step"))" \
    || { g_err "the join picker does not offer $step"; return 1; }
  _sfc_pick "$step" || return 1
  g_key Return
  wait_js '!doc.querySelector(".addform")' 4 || { g_err "the join form did not close"; return 1; }
  # no new element to wait for on the chart: give the op its round trip
  for i in 1 2 3 4 5; do assert_file_contains "$G_FILE" "FROM +\\( *$from *, *$step *\\)" 2>/dev/null && break; done
  assert_file_contains "$G_FILE" "FROM +\\( *$from *, *$step *\\) +TO +$to\\b"
}

# sfc_rename_step <old> <new> — double-click the name, type over it.
sfc_rename_step() {
  dclick_el "$(sfc_step_el "$1")?.querySelector('text.stepname')" || { g_err "no step $1"; return 1; }
  float_edit "$2" || return 1
  sfc_wait "$(sfc_step_el "$2")" || return 1
  assert_file_contains "$G_FILE" "STEP +$2\\b"
}

# ── Ladder ──────────────────────────────────────────────────────────────────
# LadderView.svelte: one <svg class="rsvg"> per rung, its name in
# <tspan class="rungname"> (plus a <title> child — read the text nodes
# only); elements are <g class="node"> with the tag in <text
# class="operand"> (a function block's operand is its instance name). Kind
# by shape: a coil draws <path class="post"> arcs, a contact <line
# class="post"> bars, an FB a <rect class="fbbox">, a function contact a
# <text class="fntext">. Insert points are <g class="spot" data-spot=JSON>.
#
# Palette-click semantics (paletteClick): with a RUNG selected (its name
# clicked) an item appends at the end of that rung's series (a coil: at the
# end of its coils); with an ELEMENT selected it goes right after it. Every
# new contact/coil is a "_" placeholder, retagged by double-click.

_LD_JS='const rungName = (s) => [...s.querySelector("tspan.rungname").childNodes].filter(n => n.nodeType === 3).map(n => n.textContent).join("").trim();
const rung = (n) => [...doc.querySelectorAll("svg.rsvg")].find(s => rungName(s) === n);
const kind = (g) => g.querySelector("rect.fbbox") ? "fb" : g.querySelector("text.fntext") ? "fn" : g.querySelector("path.post") ? "coil" : "contact";
const operand = (g) => (g.querySelector("text.operand")?.textContent ?? g.querySelector("text.fntext")?.textContent ?? "").trim();'

ld_rung_el() { printf '(() => { %s return rung(%s); })()' "$_LD_JS" "$(_q "$1")"; }
ld_rungname_el() { printf '(() => { %s return rung(%s)?.querySelector("tspan.rungname"); })()' "$_LD_JS" "$(_q "$1")"; }
# ld_node_el <rung> <kind|*> <operand> [nth, default the LAST match] — its
# <rect class="hit"> (the node's own click target).
ld_node_el() {
  printf '(() => { %s const m = [...(rung(%s)?.querySelectorAll("g.node") ?? [])].filter(g => (%s === "*" || kind(g) === %s) && operand(g) === %s); const g = m.at(%s); return g?.querySelector("rect.hit") ?? g; })()' \
    "$_LD_JS" "$(_q "$1")" "$(_q "$2")" "$(_q "$2")" "$(_q "$3")" "${4:--1}"
}
ld_count() { js "(() => { $_LD_JS return [...(rung($(_q "$1"))?.querySelectorAll('g.node') ?? [])].filter(g => kind(g) === $(_q "$2") && operand(g) === $(_q "$3")).length; })()"; }

# ld_rung_text <rung> — the rung's text in the saved file (RUNG line to the
# line before the next RUNG / comment / END_LD), one line.
ld_rung_text() {
  local f=$G_FILE; [[ $f == /* ]] || f=$PROJ/$f
  awk -v r="$1" '$1 == "RUNG" { on = ($2 == r || $2 == r ":") } /^ *(\/\/|END_LD)/ { on = 0 } on' "$f" | tr -s ' \n' ' '
}
# ld_assert_rung <rung> <ERE> — save, then match the rung's text.
ld_assert_rung() {
  g_save
  local t; t=$(ld_rung_text "$1")
  grep -Eq -- "$2" <<<"$t" || { g_err "rung $1 is '$t' — no /$2/"; return 1; }
}

ld_wait() { wait_js "$1" "${2:-10}" || { g_err "the ladder never showed the edit (${G_WHAT:-?})"; return 1; }; }

ld_select_rung() {
  click_el "$(ld_rungname_el "$1")" || { g_err "no rung $1"; return 1; }
}

# ld_palette <label> — click a palette button (⊣ ⊢, ⊣/⊢, FN( ), FB…,
# [ | ], ( ), (S), (R), //, + rung). A press-and-release without movement
# is the palette's click-append; any drift past 5 px would start a drag.
ld_palette() {
  click_el "[...doc.querySelectorAll('.palette button')].find(b => b.textContent.trim() === $(_q "$1"))" \
    || { g_err "no palette button $1"; return 1; }
}

# _ld_retag <rung> <kind> <tag> — double-click the rung's newest "_" of
# <kind>, type the tag.
_ld_retag() {
  dclick_el "$(ld_node_el "$1" "$2" _)" || { g_err "no placeholder $2 in rung $1"; return 1; }
  float_edit "$3" || return 1
  ld_wait "$(ld_node_el "$1" "$2" "$3")"
}

# ld_add_rung [name] — "+ rung" (appends rungN at the bottom, with a "( _ )"
# coil), then renames it by double-clicking its name.
ld_add_rung() {
  local name=${1:-} before new
  G_WHAT="+ rung"
  before=$(js "(() => { $_LD_JS return [...doc.querySelectorAll('svg.rsvg')].map(rungName).join(' '); })()" | tr -d '"')
  ld_palette "+ rung" || return 1
  ld_wait "doc.querySelectorAll('svg.rsvg').length > $(wc -w <<<"$before")" || return 1
  new=$(js "(() => { $_LD_JS return [...doc.querySelectorAll('svg.rsvg')].map(rungName).filter(n => !$(printf '%s' "$before" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read().split()))').includes(n))[0]; })()" | tr -d '"')
  [[ -n $new ]] || { g_err "no new rung appeared"; return 1; }
  if [[ -n $name && $name != "$new" ]]; then
    dclick_el "$(ld_rungname_el "$new")" || return 1
    float_edit "$name" || return 1
    ld_wait "$(ld_rung_el "$name")" || return 1
  fi
  assert_file_contains "$G_FILE" "RUNG +${name:-$new}\\b"
}

# ld_add_contact <rung> <tag> [nc] — appended at the end of the rung's
# series (select the rung, palette ⊣ ⊢ / ⊣/⊢, retag the placeholder).
ld_add_contact() {
  local rung=$1 tag=$2 nc=${3:-} n0
  G_WHAT="contact $tag on $rung"
  n0=$(ld_count "$rung" contact _)
  ld_select_rung "$rung" || return 1
  ld_palette "$([[ $nc == nc ]] && echo '⊣/⊢' || echo '⊣ ⊢')" || return 1
  ld_wait "(() => { $_LD_JS return [...(rung($(_q "$rung"))?.querySelectorAll('g.node') ?? [])].filter(g => kind(g) === 'contact' && operand(g) === '_').length > $n0; })()" || return 1
  _ld_retag "$rung" contact "$tag" || return 1
  ld_assert_rung "$rung" "$([[ $nc == nc ]] && echo "/$tag\\b" || echo "(^|[ [|])$tag\\b")"
}

# ld_add_coil <rung> <tag> [set|reset] — a fresh rung's "( _ )" is used if
# there is one; otherwise the palette's ( ) / (S) / (R) appends a coil.
# The mode of the reused placeholder is set with the M key (normal → S → R).
ld_add_coil() {
  local rung=$1 tag=$2 mode=${3:-} n0 i
  G_WHAT="coil $tag on $rung"
  n0=$(ld_count "$rung" coil _)
  if (( n0 == 0 )); then
    ld_select_rung "$rung" || return 1
    case $mode in set) ld_palette "(S)" ;; reset) ld_palette "(R)" ;; *) ld_palette "( )" ;; esac || return 1
    ld_wait "(() => { $_LD_JS return [...(rung($(_q "$rung"))?.querySelectorAll('g.node') ?? [])].filter(g => kind(g) === 'coil' && operand(g) === '_').length > 0; })()" || return 1
  elif [[ -n $mode ]]; then
    click_el "$(ld_node_el "$rung" coil _)" || return 1
    for ((i = 0; i < $([[ $mode == set ]] && echo 1 || echo 2); i++)); do g_key m; sleep 0.8; done
  fi
  _ld_retag "$rung" coil "$tag" || return 1
  case $mode in
    set) ld_assert_rung "$rung" "\\( *S +$tag *\\)" ;;
    reset) ld_assert_rung "$rung" "\\( *R +$tag *\\)" ;;
    *) ld_assert_rung "$rung" "\\( *$tag *\\)" ;;
  esac
}

# ld_add_block <rung> <TYPE> [instance] [args] — the palette's FB… picker
# (vscode-iec with the ladder FB picker; the 0.11.1 palette had TON and CTU
# buttons only): select the rung (the block appends at its end), FB…, type
# the block type into the picker's filter and click it in the list — the
# standard blocks, then the project's own (a library FB such as blocks.st's
# RateOfChange) — then type <instance> over the prefilled next-free name and
# <args> over the prefilled arguments, and insert.
ld_add_block() {
  local rung=$1 type=$2 inst=${3:-} args=${4:-} got before item
  G_WHAT="$type on $rung"
  before=$(js "(() => { $_LD_JS return [...(rung($(_q "$rung"))?.querySelectorAll('g.node') ?? [])].filter(g => kind(g) === 'fb').map(operand); })()")
  ld_select_rung "$rung" || return 1
  ld_palette "FB…" || return 1
  wait_js 'doc.activeElement?.classList.contains("fbfilter")' 5 || { g_err "the FB picker did not open (or its filter has no focus)"; return 1; }
  g_type "$type"
  item="[...doc.querySelectorAll('.fbpick button.fbitem')].find(b => b.dataset.type === $(_q "$type"))"
  wait_js "$item" 3 || { g_err "the FB picker does not list $type"; return 1; }
  click_el "$item" || return 1
  if [[ -n $inst ]]; then
    click_el 'doc.querySelector(".fbpick input.fbinst")' || return 1
    g_key ctrl+a; g_type "$inst"
  fi
  if [[ -n $args ]]; then
    click_el 'doc.querySelector(".fbpick input.fbargs")' || return 1
    g_key ctrl+a; g_type "$args"
  fi
  click_el 'doc.querySelector(".fbpick button.fbinsert")' || return 1
  ld_wait "(() => { $_LD_JS return [...(rung($(_q "$rung"))?.querySelectorAll('g.node') ?? [])].filter(g => kind(g) === 'fb').length > $before.length; })()" || return 1
  got=$(js "(() => { $_LD_JS return [...(rung($(_q "$rung"))?.querySelectorAll('g.node') ?? [])].filter(g => kind(g) === 'fb').map(operand).filter(n => !$before.includes(n))[0]; })()" | tr -d '"')
  [[ -z $inst || $inst == "$got" ]] || { g_err "the $type landed as '$got', not '$inst'"; return 1; }
  ld_assert_rung "$rung" "(^|[ [|])$got *: *$type\\b" || return 1
  [[ -z $args ]] || ld_assert_rung "$rung" "$got *: *$type *\\( *$(sed 's/[][\\.*^$()|+?{}]/\\&/g' <<<"${args%%,*}")"
}

# ld_rename_block <rung> <old> <new> — double-click the FB's instance name
# (its header; the body edits the arguments), type the new name. The rung's
# declaration and every reference in the program follow, as one edit.
ld_rename_block() {
  local rung=$1 old=$2 new=$3
  G_WHAT="rename $old to $new on $rung"
  dclick_el "$(ld_node_el "$rung" fb "$old")?.closest('g.node')?.querySelector('text.inst')" || { g_err "no block $old on $rung"; return 1; }
  float_edit "$new" || return 1
  ld_wait "$(ld_node_el "$rung" fb "$new")" || return 1
  ld_assert_rung "$rung" "(^|[ [|])$new *: *" || return 1
  if grep -Eqi "(^|[^A-Za-z0-9_.#])$old([^A-Za-z0-9_]|$)" <(sed 's/(\*.*\*)//g; s://.*$::' "$([[ $G_FILE == /* ]] && echo "$G_FILE" || echo "$PROJ/$G_FILE")" | grep -v '^ *RUNG'); then
    g_err "$old is still referenced after the rename"; return 1
  fi
}

# ld_declare <name> [VAR_EXTERNAL|VAR] — the palette's amber "declare …"
# offer (it appears while a rung names something the program doesn't
# declare): open it, pick the section on <name>'s row (VAR_EXTERNAL is only
# offered for a nautilus.yaml tag, typed from the manifest).
ld_declare() {
  local name=$1 section=${2:-VAR_EXTERNAL} btn
  G_WHAT="declare $name in $section"
  btn="[...doc.querySelectorAll('.declpop button.declbtn')].find(b => b.dataset.name === $(_q "$name") && b.dataset.section === $(_q "$section"))"
  js_true "$btn" || { click_el 'doc.querySelector(".palette button.declare")' || { g_err "no declare offer in the palette"; return 1; }; }
  wait_js "$btn" 3 || { g_err "the declare offer has no $section for $name"; return 1; }
  click_el "$btn" || return 1
  ld_wait "!($btn)" || return 1
  assert_file_contains "$G_FILE" "^ *$name *: *[A-Za-z_]+ *;"
}

# ld_add_branch <rung> <around-tag> [leg-tag] — select the contact <around-
# tag> and press B: it is wrapped in a parallel branch (an OR around it)
# whose new second leg holds a "_" contact; with <leg-tag>, that placeholder
# is retagged. (The palette's [ | ] instead appends an empty two-leg branch
# at the end of the series; B is the gesture for "OR around this".)
ld_add_branch() {
  local rung=$1 around=$2 leg=${3:-} n0
  G_WHAT="branch around $around on $rung"
  n0=$(ld_count "$rung" contact _)
  click_el "$(ld_node_el "$rung" contact "$around")" || { g_err "no contact $around on $rung"; return 1; }
  g_key b
  ld_wait "(() => { $_LD_JS return [...(rung($(_q "$rung"))?.querySelectorAll('g.node') ?? [])].filter(g => kind(g) === 'contact' && operand(g) === '_').length > $n0; })()" || return 1
  if [[ -n $leg ]]; then
    _ld_retag "$rung" contact "$leg" || return 1
    ld_assert_rung "$rung" "\\[ *$around *\\| *$leg *\\]"
  else
    ld_assert_rung "$rung" "\\[ *$around *\\| *_ *\\]"
  fi
}

# ld_delete_last_coil <rung> [tag, default "_"] — select the rung's only
# coil and press Delete. On a rung that calls a function block the coil
# simply goes (the block is what the rung drives; permissives.ld's
# p101start ends in m101:MotorStarter(...)) — it refused with "a rung
# needs a coil" before vscode-iec's editor-ops-polish.
ld_delete_last_coil() {
  local rung=$1 tag=${2:-_}
  G_WHAT="delete the last coil on $rung"
  [[ $(ld_count "$rung" coil "$tag") == 1 ]] || { g_err "rung $rung has no single coil $tag"; return 1; }
  click_el "$(ld_node_el "$rung" coil "$tag")" || return 1
  g_key Delete
  ld_wait "(() => { $_LD_JS return [...(rung($(_q "$rung"))?.querySelectorAll('g.node') ?? [])].filter(g => kind(g) === 'coil').length === 0; })()" || return 1
  # ends in a block call's ")" — not a standalone "( X )" coil
  ld_assert_rung "$rung" "[A-Za-z0-9_] *: *[A-Za-z_][A-Za-z0-9_]* *\\(.*\\) *$" || return 1
  local t; t=$(ld_rung_text "$rung")
  ! grep -Eq '(^| )\( *([SRPN] +)?[A-Za-z_][A-Za-z0-9_.]* *\) *$' <<<"$t" || { g_err "rung $rung still ends in a coil: $t"; return 1; }
}

# ── FBD ─────────────────────────────────────────────────────────────────────
# App.svelte on @xyflow/svelte: every block, chip and note is a
# <div class="svelte-flow__node" data-id=…>, ids from `naut fbd graph`:
#   f:<inst>        an FB instance (a1, roc)      b:w.<wire>  a block → wire
#   b:c.<coil>      the block feeding a coil      c:<tag>     a coil chip
#   v:<tag>         an input chip (v:TempC#2 …)   k:<n>       a constant
#   cm:<n>          a note
# and every pin a <div class="svelte-flow__handle source|target"
# data-handleid=<PIN>> ("" for a chip's single pin, "+" to append an input).
# Nodes are named here the way the picture names them: an instance, a wire,
# a tag — fbd_node_el tries id, f:, b:w., c:, v:, b:c. in that order.
# The "+ add" palette (Palette.svelte) inserts TEXT (insertStatement): a new
# block lands where auto-layout puts it — there is no drop-at-point.

fbd_node_el() {
  printf '(() => { const n = %s; const all = [...doc.querySelectorAll(".svelte-flow__node")]; for (const id of [n, "f:" + n, "b:w." + n, "c:" + n, "v:" + n, "b:c." + n, "g:in." + n, "g:out." + n]) { const e = all.find(x => x.dataset.id === id); if (e) return e; } return null; })()' "$(_q "$1")"
}
# fbd_pin_el <node.PIN | node> <source|target> — a handle. With no ".PIN"
# (a chip), its only handle of that direction.
fbd_pin_el() {
  local spec=$1 dir=$2 node pin
  if [[ $spec == *.* ]] && js_true "$(fbd_node_el "${spec%.*}")?.querySelector('.svelte-flow__handle.$dir[data-handleid=\"${spec##*.}\"]')"; then
    node=${spec%.*}; pin=${spec##*.}
  else node=$spec; pin=""; fi
  printf '%s?.querySelector(".svelte-flow__handle.%s[data-handleid=\\"%s\\"]")' "$(fbd_node_el "$node")" "$dir" "$pin"
}
fbd_wait() { wait_js "$1" "${2:-10}" || { g_err "the FBD never showed the edit (${G_WHAT:-?})"; return 1; }; }

# _fbd_palette <template label> <key=value>… — "+ add", pick the template,
# fill each named field (click it, select all, type), Enter.
_fbd_palette() {
  local label=$1 kv key val; shift
  # "+ add" TOGGLES the popover: only press it when it is not already open
  # (a template form left open counts — "back" returns to the list).
  if js_true 'doc.querySelector("label.field")'; then click_button back || return 1
  elif ! js_true '[...doc.querySelectorAll("button.item")].some(b => !b.closest(".suggest"))'; then click_button "+ add" || return 1; fi
  wait_js '[...doc.querySelectorAll("button.item")].some(b => !b.closest(".suggest"))' 3 || { g_err "the + add palette did not open"; return 1; }
  click_el "[...doc.querySelectorAll('button.item')].find(b => !b.closest('.suggest') && b.querySelector('span')?.textContent.trim() === $(_q "$label"))" \
    || { g_err "no palette template '$label'"; return 1; }
  for kv in "$@"; do
    key=${kv%%=*}; val=${kv#*=}
    click_el "[...doc.querySelectorAll('label.field')].find(l => l.querySelector('span')?.textContent.trim() === $(_q "$key"))?.querySelector('input')" \
      || { g_err "no field '$key' in '$label'"; return 1; }
    g_key ctrl+a; g_type "$val"
    # A suggesting field (tags, functions) drops a list over the fields
    # below it: Esc dismisses the list and stops there. On a PLAIN field Esc
    # reaches the popover and throws the form away — so only when a list is up.
    js_true 'doc.activeElement?.closest(".suggest")?.querySelector(".list")' && g_key Escape
  done
  click_el "$(button_el insert)" || return 1
  sleep 1
}

# fbd_add_block <FUNCTION|FB TYPE> <name> [inputs|args] [x y] — a block from
# the "+ add" palette, dragged (with x y, WINDOW coordinates at the rig's
# size) by its title to a pinned position afterwards.
#   A FUNCTION goes through "block → wire": <name> = <FUNCTION>(<inputs>),
#   inputs default "_, _" (open pins to wire later).
#   A FUNCTION BLOCK — a standard one (TON … RS, PID), a CamelCase project
#   block, or anything given `Pin := …` args — goes through "function block",
#   the picker (nautilus >= the fbd-fb-palette change): typed into its
#   filter, picked, instance <name>, and <args> typed over the catalog's
#   default (every input `_`) when given. The file must still parse after.
_FBD_STD_FBS=" TON TOF TP CTU CTD CTUD R_TRIG F_TRIG SR RS PID "
fbd_add_block() {
  local fn=$1 name=$2 inputs=${3:-} x=${4:-} y=${5:-}
  if [[ $inputs == *":="* || $_FBD_STD_FBS == *" ${fn^^} "* || $fn =~ [a-z] ]]; then
    _fbd_add_fb "$fn" "$name" "$inputs" || return 1
  else
    G_WHAT="block $name = $fn"
    _fbd_palette "block → wire" "name=$name" "function=$fn" "inputs=${inputs:-_, _}" || return 1
    fbd_wait "$(fbd_node_el "$name")" || return 1
  fi
  if [[ -n $x && -n $y ]]; then
    local p; p=$(el_at "$(fbd_node_el "$name")?.querySelector('.title')") || return 1
    g_drag $p "$x" "$y"
  fi
  if [[ $G_WHAT == "fb $name"* ]]; then
    assert_file_contains "$G_FILE" "\\b$name *: *$fn *\\(" || return 1
    naut fbd graph "$([[ $G_FILE == /* ]] && echo "$G_FILE" || echo "$PROJ/$G_FILE")" >/dev/null \
      || { g_err "$G_FILE no longer parses after placing $name : $fn"; return 1; }
  else
    assert_file_contains "$G_FILE" "\\b$name *= *$fn *\\("
  fi
}

# _fbd_add_fb <TYPE> <inst> [args] — "+ add" → "function block" → the picker.
_fbd_add_fb() {
  local typ=$1 inst=$2 args=${3:-} item
  G_WHAT="fb $inst : $typ"
  if js_true 'doc.querySelector(".fbpick, label.field")'; then click_button back || return 1
  elif ! js_true '[...doc.querySelectorAll("button.item")].some(b => !b.closest(".suggest"))'; then click_button "+ add" || return 1; fi
  wait_js '[...doc.querySelectorAll("button.item")].some(b => !b.closest(".suggest"))' 3 || { g_err "the + add palette did not open"; return 1; }
  click_el "[...doc.querySelectorAll('button.item')].find(b => !b.closest('.suggest') && b.querySelector('span')?.textContent.trim() === 'function block')" \
    || { g_err "no 'function block' template in the + add palette (nautilus too old?)"; return 1; }
  wait_js 'doc.activeElement?.classList.contains("fbfilter")' 5 || { g_err "the FB picker did not open (or its filter has no focus)"; return 1; }
  g_type "$typ"
  item="[...doc.querySelectorAll('.fbpick button.fbitem')].find(b => b.dataset.type === $(_q "$typ"))"
  wait_js "$item" 3 || { g_err "the FB picker does not list $typ"; return 1; }
  click_el "$item" || return 1
  click_el 'doc.querySelector(".fbpick input.fbinst")' || return 1
  g_key ctrl+a; g_type "$inst"
  if [[ -n $args ]]; then
    click_el 'doc.querySelector(".fbpick input.fbargs")' || return 1
    g_key ctrl+a; g_type "$args"
  fi
  click_el 'doc.querySelector(".fbpick button.fbinsert")' || return 1
  sleep 1
  fbd_wait "$(fbd_node_el "$inst")" || return 1
}

# fbd_wire <from node.PIN> <to node.PIN> — drag output pin → input pin.
# Checked in the TEXT at the pin: the target statement's IN<n> argument (or
# its `PIN := …` for a named FB pin) must now name the source.
fbd_wire() {
  local from=$1 to=$2 a
  G_WHAT="wire $from → $to"
  a=$(el_settled "$(fbd_pin_el "$from" source)") || { g_err "no output pin $from"; return 1; }
  js_true "$(fbd_pin_el "$to" target)" || { g_err "no input pin $to"; return 1; }
  g_drag_to $a "$(fbd_pin_el "$to" target)" || return 1
  sleep 1.2
  _fbd_assert_pin "$to" "$from"
}

# _fbd_assert_pin <node.PIN> <source spec> — save, then check the pin's
# argument in the node's statement names the source (its last name part:
# a wire "low", a tag "PumpRun", an instance output "a1.Q" → "a1.Q").
_fbd_assert_pin() {
  local to=$1 from=$2 src
  src=$from
  [[ $from == *.OUT || $from == *. ]] && src=${from%.*}
  src=${src#*:}; src=${src#w.}; src=${src#c.}; src=${src#in.}
  g_save
  python3 - "$PROJ/$G_FILE" "${to%.*}" "${to##*.}" "$src" <<'PY2' || { g_err "$G_FILE: pin $to is not fed by $src"; return 1; }
import re, sys
text, node, pin, src = open(sys.argv[1]).read(), sys.argv[2], sys.argv[3], sys.argv[4]
node = node.split(":")[-1].removeprefix("w.").removeprefix("c.")
def args_of(call):
    depth, cur, out = 0, "", []
    for ch in call:
        if ch == "(": depth += 1
        if ch == ")": depth -= 1
        if ch == "," and depth == 0: out.append(cur.strip()); cur = ""; continue
        cur += ch
    out.append(cur.strip()); return out
for line in text.splitlines():
    m = re.match(r"\s*" + re.escape(node) + r"\s*(?:=|:=|:)\s*\w+\s*\((.*)\)\s*;?\s*$", line)
    if not m: continue
    a = args_of(m.group(1))
    named = {k.strip(): v.strip() for k, _, v in (x.partition(":=") for x in a if ":=" in x)}
    if pin in named: ok = named[pin] == src
    else:
        n = re.match(r"IN(\d+)$", pin)
        ok = bool(n) and int(n.group(1)) <= len(a) and a[int(n.group(1)) - 1] == src
    sys.exit(0 if ok else 1)
sys.exit(1)
PY2
}

# fbd_add_tag_ref <tag> [to node.PIN] — the palette's "input reference
# (bare)": a chip for <tag>. The chip is only a LAYOUT entry until a wire
# makes it text, so with <to> it is wired straight into that pin (and the
# statement is what gets checked); without, it stays a ghost.
fbd_add_tag_ref() {
  local tag=$1 to=${2:-}
  G_WHAT="tag chip $tag"
  _fbd_palette "input reference (bare)" "name=$tag" || return 1
  fbd_wait "doc.querySelector('.svelte-flow__node[data-id=\"g:in.$tag\"]')" || return 1
  [[ -z $to ]] && return 0
  local a
  a=$(el_settled "doc.querySelector('.svelte-flow__node[data-id=\"g:in.$tag\"] .svelte-flow__handle.source')") || return 1
  js_true "$(fbd_pin_el "$to" target)" || { g_err "no input pin $to"; return 1; }
  g_drag_to $a "$(fbd_pin_el "$to" target)" || return 1
  sleep 1.2
  _fbd_assert_pin "$to" "$tag"
}

# fbd_add_comment <text> — the palette's "comment": a `// text` note.
fbd_add_comment() {
  G_WHAT="comment"
  _fbd_palette "comment" "text=$1" || return 1
  fbd_wait "[...doc.querySelectorAll('.svelte-flow__node')].some(n => n.dataset.id.startsWith('cm:') && n.textContent.includes($(_q "$1")))" || return 1
  assert_file_contains "$G_FILE" "// *$(printf '%s' "$1" | sed 's/[][\.*^$()+?{}|]/\\&/g')"
}

# ── Mimic ───────────────────────────────────────────────────────────────────
# EditorCanvas.svelte: the canvas is a <div class="canvas"> at the file's
# canvas size, CSS-scaled to fit; equipment are <div class="eq"> in the
# file's `equipment` order (no id in the DOM — the verbs read the order from
# the saved file); port dots are <circle class="port"> whose <title> starts
# "<port name> ·", drawn while a tool that needs them (+ Pipe) is armed.
# Coordinates in these verbs are the FILE's canvas coordinates — the same
# numbers the JSON gets, whatever the zoom.

# _mimic_json <python args…> — run the python on stdin against the mimic file
# ($G_FILE), once the file is non-empty AND parses. The bare version (python3
# straight on the file) crashed building ex01's 11-mimic beat: a mimic_drop's
# read-back (mimic_ids, at the TOP of mimic_drop, to snapshot the equipment
# list) died with `json.decoder.JSONDecodeError: Expecting value: line 1
# column 1 (char 0)` — the file was 0 bytes for an instant. Seen twice, at
# two different points (content GESTURE-FINDINGS "Build beats (C)"): on the
# very FIRST drop into a freshly-emptied file (before the mimic editor's own
# EMPTY_DOC skeleton had landed on disk), and several drops in (a later
# save's truncate-then-write racing the read). An uncaught Python exception
# under a beat's `set -euo pipefail` kills the beat instead of returning a
# failure `g_err` could report, so the READ is retried, not the crash caught.
# Every verb built on it — mimic_ids, mimic_eq_el, mimic_drop, mimic_bind(_at),
# mimic_pipe(_direct), component_edit_ports — inherits the tolerance.
_mimic_json() {
  local f=$G_FILE i
  [[ $f == /* ]] || f=$PROJ/$f
  for ((i = 0; i < 40; i++)); do
    [[ -s $f ]] && python3 -c 'import json,sys; json.load(open(sys.argv[1]))' "$f" 2>/dev/null && break
    sleep 0.15
  done
  python3 - "$f" "$@"
}
mimic_ids() { _mimic_json <<'PY'
import json, sys
print(" ".join(e["id"] for e in json.load(open(sys.argv[1])).get("equipment", [])))
PY
}
mimic_eq_el() {
  local i
  i=$(_mimic_json "$1" <<'PY'
import json, sys
ids = [e["id"] for e in json.load(open(sys.argv[1])).get("equipment", [])]
print(ids.index(sys.argv[2]) if sys.argv[2] in ids else -1)
PY
)
  printf 'doc.querySelectorAll(".canvas > .eq")[%d]' "$i"
}
# _mimic_pt <x> <y> — window coordinates of a file-canvas point.
_mimic_pt() {
  local b bx by bw bh v cw
  b=$(el_box 'doc.querySelector(".canvas")') || return 1
  read -r bx by bw bh v <<<"$b"
  cw=$(js 'doc.querySelector(".canvas").offsetWidth')
  awk -v bx="$bx" -v by="$by" -v bw="$bw" -v cw="$cw" -v x="$1" -v y="$2" 'BEGIN{k=bw/cw; printf "%d %d\n", bx + x*k, by + y*k}'
}
mimic_tool() { click_el "[...doc.querySelectorAll('.tools button')].find(b => b.textContent.trim() === $(_q "$1"))" || { g_err "no tool $1"; return 1; }; }

# mimic_drop <Component> <x> <y> [id] — arm the palette tile, click the
# canvas at file coordinates x,y (the drop snaps to the grid). With [id],
# rename the new instance in the inspector's id field.
mimic_drop() {
  local comp=$1 x=$2 y=$3 id=${4:-} before new
  G_WHAT="drop $comp"
  before=$(mimic_ids)
  click_el "[...doc.querySelectorAll('aside button.item')].find(b => b.title === $(_q "Place a $comp"))" \
    || { g_err "no palette tile for $comp"; return 1; }
  g_click $(_mimic_pt "$x" "$y") 1.5
  g_save
  new=$(comm -13 <(tr ' ' '\n' <<<"$before" | sort) <(mimic_ids | tr ' ' '\n' | sort) | head -1)
  [[ -n $new ]] || { g_err "no new $comp in $G_FILE"; return 1; }
  mimic_tool Select >/dev/null 2>&1 || true
  if [[ -n $id && $id != "$new" ]]; then
    click_el "$(mimic_eq_el "$new")" || return 1
    click_el '[...doc.querySelectorAll("input.nx-input")].find(i => i.value === '"$(_q "$new")"')' || { g_err "no id field for $new"; return 1; }
    g_key ctrl+a; g_type "$id"; g_key Return; sleep 1
    new=$id
  fi
  g_save
  _mimic_json "$new" "$comp" <<'PY' || { g_err "$G_FILE has no $comp '$new'"; return 1; }
import json, sys
eq = {e["id"]: e for e in json.load(open(sys.argv[1]))["equipment"]}
sys.exit(0 if sys.argv[2] in eq and eq[sys.argv[2]]["component"] == sys.argv[3] else 1)
PY
  G_LAST_ID=$new
}

# mimic_bind <id> <prop> <tag> — select the instance, then the inspector's
# empty bind row: prop, tag, Enter (the row commits once both are filled).
mimic_bind() {
  local id=$1 prop=$2 tag=$3
  G_WHAT="bind $id.$prop"
  click_el "$(mimic_eq_el "$id")" || { g_err "no equipment $id"; return 1; }
  click_el 'doc.querySelector("input[aria-label=\"New bound prop\"]")' || { g_err "no bind row for $id"; return 1; }
  g_key ctrl+a; g_type "$prop"
  click_el 'doc.querySelector("input[aria-label=\"New tag name\"]")' || return 1
  g_key ctrl+a; g_type "$tag"; g_key Return; sleep 1
  g_save
  _mimic_json "$id" "$prop" "$tag" <<'PY' || { g_err "$id.bind.$prop is not $tag in $G_FILE"; return 1; }
import json, sys
eq = {e["id"]: e for e in json.load(open(sys.argv[1]))["equipment"]}
sys.exit(0 if eq.get(sys.argv[2], {}).get("bind", {}).get(sys.argv[3]) == sys.argv[4] else 1)
PY
}

# mimic_port_el <id.port> — that instance's port dot (only drawn while the
# + Pipe tool is armed, or in the ports editor).
mimic_port_el() {
  local id=${1%.*} port=${1##*.}
  printf '(() => { const eq = %s; if (!eq) return null; const r = eq.getBoundingClientRect(); const pad = 12; return [...doc.querySelectorAll("circle.port")].find(c => { if (!(c.querySelector("title")?.textContent || "").startsWith(%s + " ·")) return false; const q = c.getBoundingClientRect(), x = q.left + q.width / 2, y = q.top + q.height / 2; return x >= r.left - pad && x <= r.right + pad && y >= r.top - pad && y <= r.bottom + pad; }) ?? null; })()' \
    "$(mimic_eq_el "$id")" "$(_q "$port")"
}

# mimic_pipe <from id.port> <to id.port> — a pipe anchored at both ports.
#
# NOT the obvious gesture. The documented one (+ Pipe, click port, click
# port, Enter) never writes anything in 0.11.1: finishing a draft with an
# anchored end throws "DataCloneError: … could not be cloned" in the
# webview's postMessage (the anchor is a Svelte $state proxy), the op is
# lost, and the dashed draft stays on the canvas even after switching back
# to Select (GESTURE-FINDINGS, Mimic). A FLOATING pipe does commit, and an
# end dragged onto a port attaches — so: measure both port dots (they are
# only drawn while + Pipe is armed), draw a two-click floating pipe between
# them, Enter, then drag each end onto its port.
mimic_pipe() {
  local from=$1 to=$2 pa pb ax ay bx by n0 h
  G_WHAT="pipe $from → $to"
  n0=$(js 'doc.querySelectorAll("path.hit").length')
  mimic_tool "+ Pipe" || return 1
  wait_js "$(mimic_port_el "$from")" 3 || { g_err "no port dot $from"; g_key Escape; return 1; }
  pa=$(el_at "$(mimic_port_el "$from")") && pb=$(el_at "$(mimic_port_el "$to")") \
    || { g_err "no port dot $to"; g_key Escape; return 1; }
  read -r ax ay <<<"$pa"; read -r bx by <<<"$pb"
  # two points on the line between the ports, clear of both port snaps
  g_click $((ax + (bx - ax) * 3 / 10)) $((ay + (by - ay) * 3 / 10))
  g_click $((ax + (bx - ax) * 7 / 10)) $((ay + (by - ay) * 7 / 10))
  g_key Return; sleep 1
  wait_js "doc.querySelectorAll('path.hit').length > $n0" 5 || { g_err "the floating pipe did not commit"; g_key Escape; return 1; }
  mimic_tool Select || return 1
  h="[...doc.querySelectorAll('path.hit')].at(-1)"
  click_el "$h" || return 1
  g_drag $(el_at '[...doc.querySelectorAll(".pipe-handles circle.vtx, .pipe-handles circle.anchor")][0]') $pa
  sleep 1
  click_el "$h" || return 1
  g_drag $(el_at '[...doc.querySelectorAll(".pipe-handles circle.vtx, .pipe-handles circle.anchor")].at(-1)') $pb
  sleep 1
  # G_REROUTE=1: the inspector's Re-route (orthogonal). Off by default: in
  # 0.11.1 it treats the two END instances as non-obstacles, so a route from
  # a tank's side port can run straight through the tank (GESTURE-FINDINGS).
  if [[ ${G_REROUTE:-} == 1 ]]; then click_el "$h" && click_button "Re-route"; sleep 1; fi
  g_save
  _mimic_json "${from%.*}" "${from##*.}" "${to%.*}" "${to##*.}" <<'PY2' || { g_err "no pipe $from → $to in $G_FILE"; return 1; }
import json, sys
fe, fp, te, tp = sys.argv[2:6]
for p in json.load(open(sys.argv[1])).get("pipes", []):
    f, t = p.get("from", {}), p.get("to", {})
    if isinstance(f, dict) and isinstance(t, dict) and (f.get("equip"), f.get("port"), t.get("equip"), t.get("port")) == (fe, fp, te, tp):
        sys.exit(0)
sys.exit(1)
PY2
}

# mimic_pipe_direct <from id.port> <to id.port> — the DOCUMENTED gesture
# (+ Pipe, click port, click port, Enter). Kept so the self-test notices the
# day the DataCloneError is fixed; beats use mimic_pipe until then. Cleans up
# after itself (Esc drops the stuck draft) when it fails.
mimic_pipe_direct() {
  local from=$1 to=$2
  G_WHAT="direct pipe $from → $to"
  mimic_tool "+ Pipe" || return 1
  wait_js "$(mimic_port_el "$from")" 3 || { g_err "no port dot $from"; g_key Escape; return 1; }
  click_el "$(mimic_port_el "$from")" || return 1
  click_el "$(mimic_port_el "$to")" || { g_err "no port dot $to"; g_key Escape; g_key Escape; return 1; }
  g_key Return; sleep 1.5
  if js_true 'doc.querySelector("path.draft")'; then
    g_key Escape; g_key Escape
    g_err "Enter left the draft on the canvas and wrote nothing (webview: DataCloneError in postMessage)"
    return 1
  fi
  g_save
  _mimic_json "${from%.*}" "${from##*.}" "${to%.*}" "${to##*.}" <<'PY2' || { g_err "no pipe $from → $to in $G_FILE"; return 1; }
import json, sys
fe, fp, te, tp = sys.argv[2:6]
for p in json.load(open(sys.argv[1])).get("pipes", []):
    f, t = p.get("from", {}), p.get("to", {})
    if isinstance(f, dict) and isinstance(t, dict) and (f.get("equip"), f.get("port"), t.get("equip"), t.get("port")) == (fe, fp, te, tp):
        sys.exit(0)
sys.exit(1)
PY2
}

# ── Component ports ─────────────────────────────────────────────────────────

# component_edit_ports <Component> — "Nautilus: Edit Component Ports…",
# pick the component: its *.component.json opens in the Component Editor
# (created, prefilled with the built-in's default ports, if there is none).
component_edit_ports() {
  local comp=$1
  G_WHAT="edit ports of $comp"
  vs_cmd "Nautilus: Edit Component Ports" 2
  g_type "$comp"; sleep 0.8; g_key Return
  wait_js 'doc.querySelector(".stage .canvas, button.addbtn")' 15 || { g_err "the Component Editor did not open for $comp"; return 1; }
  G_FILE=$(find "$PROJ" -name "$comp.component.json" -not -path '*/node_modules/*' | head -1)
  [[ -n $G_FILE ]] || { g_err "no $comp.component.json was written"; return 1; }
  sleep 1
}

# component_add_port <name> — the ports panel's "+ Add port" (a new port at
# a free slot on the outline), then its name field.
component_add_port() {
  local name=$1 n0
  G_WHAT="port $name"
  n0=$(js 'doc.querySelectorAll("input.nx-input.name").length')
  click_button "+ Add port" || return 1
  wait_js "doc.querySelectorAll('input.nx-input.name').length > $n0" 5 || { g_err "no new port row"; return 1; }
  click_el '[...doc.querySelectorAll("input.nx-input.name")].pop()' || return 1
  g_key ctrl+a; g_type "$name"; g_key Return; sleep 1
  g_save
  python3 - "$G_FILE" "$name" <<'PY' || { g_err "$(basename "$G_FILE") has no port $name"; return 1; }
import json, sys
sys.exit(0 if any(p.get("name") == sys.argv[2] for p in json.load(open(sys.argv[1])).get("ports", [])) else 1)
PY
}

# ── Ladder: series, branch and polarity edits ───────────────────────────────
# Proven building ex01-lift-station's p101start/p101req rungs (06-ld; the
# content repo's GESTURE-FINDINGS "Build beats (C)"). Same conventions as the
# Ladder verbs above: SAVE-then-READ, `g_err` on a miss, `_LD_JS`/ld_node_el.

# ld_select_node <rung> <kind> <tag> [nth] — click the node's own hit target
# (no retag, no assertion beyond "it selected").
ld_select_node() {
  local rung=$1 kind=$2 tag=$3 nth=${4:--1}
  click_el "$(ld_node_el "$rung" "$kind" "$tag" "$nth")" || { g_err "no $kind $tag on $rung"; return 1; }
}

# ld_add_contact_after <rung> <after-kind> <after-tag> <new-tag> [nc] —
# select the existing node, then the palette's contact button: it inserts
# in series RIGHT AFTER the selection, in whatever series that node is
# already part of (top-level, or inside a branch leg — ladderLayout.ts's
# path/elPath addressing is the same either way, so the palette doesn't
# care which). Confirmed against LadderView.svelte's paletteClick/insert
# addressing, 2026-09-25 (GESTURE-FINDINGS "Build beats (C)").
ld_add_contact_after() {
  local rung=$1 akind=$2 atag=$3 tag=$4 nc=${5:-} nth=${6:--1} n0
  G_WHAT="contact $tag after $akind $atag on $rung"
  n0=$(ld_count "$rung" contact _)
  ld_select_node "$rung" "$akind" "$atag" || return 1
  ld_palette "$([[ $nc == nc ]] && echo '⊣/⊢' || echo '⊣ ⊢')" || return 1
  ld_wait "(() => { $_LD_JS return [...(rung($(_q "$rung"))?.querySelectorAll('g.node') ?? [])].filter(g => kind(g) === 'contact' && operand(g) === '_').length > $n0; })()" || return 1
  dclick_el "$(ld_node_el "$rung" contact _ "$nth")" || { g_err "no placeholder (nth=$nth) to retag after $akind $atag"; return 1; }
  float_edit "$tag" || return 1
  ld_wait "$(ld_node_el "$rung" contact "$tag")" || return 1
}

# ld_wrap_branch <rung> <kind> <tag> — select the node, press B: LadderView's
# wrapBranch turns that ONE element into leg 1 of a fresh 2-leg OR, leg 2 a
# placeholder "_" contact (edit.go opWrapBranch / LadderView.svelte "b" key).
ld_wrap_branch() {
  local rung=$1 kind=$2 tag=$3
  G_WHAT="wrap $kind $tag on $rung into a branch"
  ld_select_node "$rung" "$kind" "$tag" || return 1
  g_key b
  ld_wait "(() => { $_LD_JS return [...(rung($(_q "$rung"))?.querySelectorAll('g.node') ?? [])].filter(g => kind(g) === 'contact' && operand(g) === '_').length > 0; })()" || return 1
  ld_assert_rung "$rung" "\\[ *$tag *\\| *_ *\\]"
}

# _ld_leg_spot <rung> — the ONE `g.spot` whose op is "leg" for <rung>: the
# rendered "+ leg" hotspot (LadderView.svelte's `{#each lay.spots as s}`,
# `<g class="spot" data-spot='{"rung":...,"spot":{"op":"leg",...}}'>`).
# There is no DOM link from a branch's own contacts to this spot — legs
# render as flat sibling g.node with no wrapping element at all
# (ladderLayout.ts spreads each leg's nodes into the rung's flat arrays) —
# so this assumes exactly ONE branch per rung, true for every rung this
# file builds.
_ld_leg_spot() {
  printf '[...doc.querySelectorAll("g.spot")].find(g => { try { const d = JSON.parse(g.dataset.spot); return d.rung === %s && d.spot?.op === "leg"; } catch { return false; } })' "$(_q "$1")"
}

# ld_add_leg <rung> — the rung's (single) branch's "add leg" hotspot: a
# third (or Nth) leg, placeholder "_" contact, same shape as any other
# placeholder (edit.go addLeg: `Legs = append(Legs, [{contact "_"}])`).
ld_add_leg() {
  local rung=$1 n0
  G_WHAT="add leg to the branch on $rung"
  n0=$(ld_count "$rung" contact _)
  click_el "$(_ld_leg_spot "$rung")" || { g_err "no add-leg hotspot on $rung"; return 1; }
  ld_wait "(() => { $_LD_JS return [...(rung($(_q "$rung"))?.querySelectorAll('g.node') ?? [])].filter(g => kind(g) === 'contact' && operand(g) === '_').length > $n0; })()" || return 1
}

# ld_toggle_nc <rung> <kind> <tag> — select the node, press N: flips Neg in
# place (edit.go opToggleNeg / LadderView.svelte "n" key) — the polarity
# equivalent of B for branch-wrap, for a contact/fn already placed (a
# branch-leg placeholder, notably, has no NC palette button at creation).
ld_toggle_nc() {
  local rung=$1 kind=$2 tag=$3
  G_WHAT="toggle NC on $kind $tag ($rung)"
  ld_select_node "$rung" "$kind" "$tag" || return 1
  g_key n
  sleep 0.6
  ld_assert_rung "$rung" "/$tag\\b"
}

# ld_retag_fb_args <rung> <inst> <new args string> — double-click the FB's
# ARGS body (not its instance-name header — that's ld_rename_block's
# target) and retype the whole args string. The float editor takes the
# same select-all/type/Enter shape as every other retag; the args field
# accepts named `Pin := value` / `Pin => tag` pairs typed in one go
# (lang/ld: named-call syntax, unvalidated at the text layer — confirmed
# 2026-09-25).
# ld_retag_placeholder <rung> <kind> <new tag> — dclick the newest "_" of
# <kind> and retype it (the private _ld_retag, exposed
# for a placeholder that ld_wrap_branch/ld_add_leg left behind with no
# dedicated verb of its own — a branch's leg-2/leg-3 contact).
ld_retag_placeholder() { _ld_retag "$1" "$2" "$3"; }

# ld_delete_node <rung> <kind> <tag> — select it, Delete (the diagram's own
# hint: "…· Del · drag to move", LadderView.svelte's FB title). Used here
# to remove the "( _ )" coil "+ rung" leaves behind on a rung whose real
# ending is a bare FB call (p101start/p102start — motor.ld's MotorStarter
# terminates the rung with its own output pins, no separate coil).
ld_delete_node() {
  local rung=$1 kind=$2 tag=$3 n0
  G_WHAT="delete $kind $tag on $rung"
  n0=$(ld_count "$rung" "$kind" "$tag")
  ld_select_node "$rung" "$kind" "$tag" || return 1
  g_key Delete
  ld_wait "(() => { $_LD_JS return [...(rung($(_q "$rung"))?.querySelectorAll('g.node') ?? [])].filter(g => kind(g) === $(_q "$kind") && operand(g) === $(_q "$tag")).length < $n0; })()" || return 1
}

# ld_op_trunc <tag> [n=12] — LadderView.svelte's own `trunc(s, n=12)`:
# `t.slice(0, ceil(n/2)-1) + "…" + t.slice(-floor(n/2))` for any string over
# n chars. Contact/coil operand text (`.operand`, LadderView.svelte:877/890)
# renders through this — so a tag over 12 chars (P101_SealFail,
# P101_Permissive, every P10x_* fault/permissive tag in this fixture) never
# appears verbatim in the DOM, and an EXACT-match wait (_ld_retag/ld_add_contact/ld_add_coil above, proven only against the
# self-test's shorter tags) times out even though the SAVED FILE is correct — found
# building this beat (GESTURE-FINDINGS "Build beats (C)").
ld_op_trunc() {
  local s=$1 n=${2:-12} len=${#1}
  if (( len <= n )); then printf '%s' "$s"; return; fi
  local head=$(( (n + 1) / 2 - 1 )) tail=$(( n / 2 ))
  printf '%s…%s' "${s:0:head}" "${s: -$tail}"
}

# _ld_retag_c <rung> <kind> <new tag> — _ld_retag, but its confirmation
# accepts EITHER the full tag or its truncated rendering.
_ld_retag_c() {
  local rung=$1 kind=$2 tag=$3 short
  short=$(ld_op_trunc "$tag")
  dclick_el "$(ld_node_el "$rung" "$kind" _)" || { g_err "no placeholder $kind in rung $rung"; return 1; }
  float_edit "$tag" || return 1
  ld_wait "($(ld_node_el "$rung" "$kind" "$tag")) || ($(ld_node_el "$rung" "$kind" "$short"))"
}

# ld_add_contact_c <rung> <tag> [nc] — ld_add_contact, tolerant version.
ld_add_contact_c() {
  local rung=$1 tag=$2 nc=${3:-} n0
  G_WHAT="contact $tag on $rung"
  n0=$(ld_count "$rung" contact _)
  ld_select_rung "$rung" || return 1
  ld_palette "$([[ $nc == nc ]] && echo '⊣/⊢' || echo '⊣ ⊢')" || return 1
  ld_wait "(() => { $_LD_JS return [...(rung($(_q "$rung"))?.querySelectorAll('g.node') ?? [])].filter(g => kind(g) === 'contact' && operand(g) === '_').length > $n0; })()" || return 1
  _ld_retag_c "$rung" contact "$tag" || return 1
  ld_assert_rung "$rung" "$([[ $nc == nc ]] && echo "/$tag\\b" || echo "(^|[ [|])$tag\\b")"
}

# ld_add_coil_c <rung> <tag> [set|reset] — ld_add_coil, tolerant version.
ld_add_coil_c() {
  local rung=$1 tag=$2 mode=${3:-} n0 i
  G_WHAT="coil $tag on $rung"
  n0=$(ld_count "$rung" coil _)
  if (( n0 == 0 )); then
    ld_select_rung "$rung" || return 1
    case $mode in set) ld_palette "(S)" ;; reset) ld_palette "(R)" ;; *) ld_palette "( )" ;; esac || return 1
    ld_wait "(() => { $_LD_JS return [...(rung($(_q "$rung"))?.querySelectorAll('g.node') ?? [])].filter(g => kind(g) === 'coil' && operand(g) === '_').length > 0; })()" || return 1
  elif [[ -n $mode ]]; then
    click_el "$(ld_node_el "$rung" coil _)" || return 1
    for ((i = 0; i < $([[ $mode == set ]] && echo 1 || echo 2); i++)); do g_key m; sleep 0.8; done
  fi
  _ld_retag_c "$rung" coil "$tag" || return 1
  case $mode in
    set) ld_assert_rung "$rung" "\\( *S +$tag *\\)" ;;
    reset) ld_assert_rung "$rung" "\\( *R +$tag *\\)" ;;
    *) ld_assert_rung "$rung" "\\( *$tag *\\)" ;;
  esac
}

ld_retag_fb_args() {
  local rung=$1 inst=$2 args=$3
  G_WHAT="retag $inst's args on $rung"
  dclick_el "$(ld_node_el "$rung" fb "$inst")?.closest('g.node')?.querySelector('text.fbargs, text.args, tspan.fbargs')" \
    || { g_err "no args body on $inst ($rung) to double-click"; return 1; }
  float_edit "$args" || return 1
  ld_wait "$(ld_node_el "$rung" fb "$inst")" || return 1
  ld_assert_rung "$rung" "$(printf '%s' "${args%%,*}" | sed 's/[][\\.*^$()|+?{}]/\\&/g')"
}

# ld_edit_fb_args <rung> <inst> <new args text> — double-click the FB's node
# to open the args editor (GESTURE-FINDINGS #16: dblclick on an FB edits its
# arguments), type the new args, commit. ld_retag_fb_args, above, aims at the
# args text itself and asserts less; both are kept because beats use both.
ld_edit_fb_args() {
  local rung=$1 inst=$2 args=$3
  G_WHAT="edit args of $inst on $rung"
  dclick_el "$(ld_node_el "$rung" fb "$inst")" || { g_err "no FB $inst on $rung"; return 1; }
  float_edit "$args" || return 1
  sleep 0.5
  ld_assert_rung "$rung" "$inst *: *[A-Za-z_]+ *\\( *$(printf '%s' "$args" | sed 's/[][\.*^$()|+?{}]/\\&/g')"
}

# ── Mimic: bind at a point ──────────────────────────────────────────────────

# mimic_bind_at <id> <fx> <fy> <prop> <tag> — gestures.sh's mimic_bind, but
# clicking <id>'s own element at an explicit (fx, fy) fraction of ITS
# bounding box instead of the box's center (el_at's default 0.5 0.5).
# Needed for WW101: equipment carries no id in the DOM (GESTURE-FINDINGS
# #6 — the only way to find one is file order), so click_el hit-tests by
# COORDINATE, and WW101 (a Tank, dropped FIRST — earlier in DOM, so
# UNDER everything painted after it) has P101 (a SubmersiblePump, meant
# to sit inside the wet well) dropped at a spot whose bounding box
# contains the Tank's own bounding-box center — confirmed live 2026-09-25
# (GESTURE-FINDINGS "Build beats (C)"): `mimic_bind WW101 levelPct
# LIT101_Level` silently bound P101 instead, no error, because the
# center-point click landed on the pump drawn on top, not the tank
# beneath it. This fixture's own layout (equipment meant to be drawn
# INSIDE a vessel) makes an empty band of the Tank's box a per-instance
# fact, not something click_el's generic center-click can know — hence a
# verb of its own rather than a change to click_el. (0.5, 0.15) — a point
# near the wet well's top, above every pump/float/valve dropped inside it
# — is what this beat's own drop coordinates leave clear; a different
# layout would need different numbers.
mimic_bind_at() {
  local id=$1 fx=$2 fy=$3 prop=$4 tag=$5
  G_WHAT="bind $id.$prop"
  click_el "$(mimic_eq_el "$id")" "$fx" "$fy" || { g_err "no equipment $id at ($fx, $fy)"; return 1; }
  click_el 'doc.querySelector("input[aria-label=\"New bound prop\"]")' || { g_err "no bind row for $id"; return 1; }
  g_key ctrl+a; g_type "$prop"
  click_el 'doc.querySelector("input[aria-label=\"New tag name\"]")' || return 1
  g_key ctrl+a; g_type "$tag"; g_key Return; sleep 1
  g_save
  _mimic_json "$id" "$prop" "$tag" <<'PY' || { g_err "$id.bind.$prop is not $tag in $G_FILE"; return 1; }
import json, sys
eq = {e["id"]: e for e in json.load(open(sys.argv[1]))["equipment"]}
sys.exit(0 if eq.get(sys.argv[2], {}).get("bind", {}).get(sys.argv[3]) == sys.argv[4] else 1)
PY
}

# ── the workbench page: dialogs, notifications, the controller ──────────────
# For what lives in VS Code's own page rather than a diagram webview: the
# custom modal dialog (window.dialogStyle: custom), notification toasts. And
# the controller's API, for beats that assert what a gesture did to it.

# page_el_box <js -> Element, evaluated in the WORKBENCH page> — "x y w h"
# in window coordinates (dpr-scaled, CSD-offset), or fails if absent. For
# the custom dialog / notification toasts, which live in the page itself,
# not a diagram webview.
page_el_box() {
  local fl ft json
  read -r fl ft <<<"$(_g_frame)"
  json=$(cdp page "(() => { const e = ($1); if (!e) return null; const r = e.getBoundingClientRect(); return {x:r.left,y:r.top,w:r.width,h:r.height,dpr:window.devicePixelRatio}; })()") || return 1
  [[ $json != null ]] || return 1
  python3 -c "
import json,sys
d = json.loads(sys.argv[1])
k = d['dpr']
print(round((d['x'])*k) + $fl, round((d['y'])*k) + $ft, round(d['w']*k), round(d['h']*k))
" "$json"
}

# page_click_button <label regex (JS RegExp source, case-sensitive)> —
# find a <button>/<a class=monaco-button> in the workbench page whose
# trimmed textContent matches, and click its centre.
page_click_button() {
  local label=$1 settle=${2:-1.5} box
  box=$(page_el_box "[...document.querySelectorAll('.monaco-dialog-box button, .monaco-dialog-box a.monaco-button, .notification-list-item button')].find(b => /$label/.test(b.textContent.trim()))") \
    || { g_err "no dialog/notification button matching /$label/"; return 1; }
  local x y w h
  read -r x y w h <<<"$box"
  g_click $((x + w / 2)) $((y + h / 2)) "$settle"
}

# page_text <js -> string, in the workbench page> — trimmed text content,
# for reading a dialog's message or a notification's text back.
page_text() {
  cdp page "(() => { const e = ($1); return e ? e.textContent.trim() : null; })()"
}

# dialog_up — is a custom modal dialog currently on screen (window.dialogStyle: custom)?
dialog_up() { js_true 'false' 2>/dev/null; cdp page 'document.querySelector(".monaco-dialog-box") ? true : false' 2>/dev/null | grep -q true; }

# api_get / api_post — the controller's tag/program/alarm API, at $PORT.
api_get() { curl -sf --max-time 3 "localhost:$PORT$1"; }
api_post() { curl -sf --max-time 3 -X POST "localhost:$PORT$1" -H 'Content-Type: application/json' -d "$2"; }

# px_count <png basename in $OUT_DIR> <x0> <y0> <x1> <y1> <python cond over
# r,g,b> — how many pixels in the box satisfy the condition (the same helper
# smoke/lib.sh defines; smoke checks do not source this file).
px_count() {
  python3 - "$OUT_DIR/$1.png" "$2" "$3" "$4" "$5" "$6" <<'PY'
import sys
from PIL import Image
im = Image.open(sys.argv[1]).convert("RGB"); px = im.load()
x0, y0, x1, y1 = map(int, sys.argv[2:6]); cond = eval("lambda r, g, b: " + sys.argv[6])
print(sum(1 for y in range(y0, min(y1, im.size[1])) for x in range(x0, min(x1, im.size[0])) if cond(*px[x, y])))
PY
}
