#!/usr/bin/env bash
# build.sh — the tia-shaped build: a TIA Portal (S7-1500) programmer's first
# Nautilus project, built from `naut new --template minimal` in a real VS
# Code by the rig. ST is TYPED (xdotool, the language server live), the FBD
# is GESTURED (verbs/gestures.sh), and only what no gesture or sane typing
# authors is pasted. PLAN.md lists the beats; FINDINGS.md is the log.
#
#     export PATH=$HOME/.nvm/versions/node/v24.18.0/bin:/usr/local/go/bin:$PATH
#     eval "$(tools/rig/smoke/build.sh)"
#     RIG_NAME=nautilus-build-tia G_PACE=fast RIG_CLIPS=1 tools/rig/builds/tia-shaped/build.sh
#
# On the host it brings up the rig container (lib/container.sh), the way
# selftest.sh does: naut into /usr/local/bin, the VSIX installed, lib.sh,
# the verbs (as ~/fixtures) and this directory (as ~/build) pushed, then runs
# itself in there with RIG_IN_CONTAINER=1 and pulls ~/out back to
# tools/rig/out/builds/tia-shaped/ (RIG_OUT= moves tools/rig/out):
#
#   build.tsv          <n> <row> PASS|FAIL|XFAIL|XPASS|NOTE <detail>
#   NN-<row>.png/.mp4  the window right after each row, and the row as it
#                      happened (RIG_CLIPS=0: no clips). READ THE PNGs.
#   check-<beat>.txt   `naut check` after every beat
#   test.txt           `naut test` at the end
#   compare.txt        the built project against reference/
#   built/             the project as the build left it (with the FBD
#                      editor's own @layout block)
#
# XFAIL rows probe what a TIA programmer expects that Nautilus does not do;
# XPASS means it started working — go and look. A gestured row that FAILs
# gets its statement written by text afterwards (a FALLBACK NOTE row), so
# one broken verb does not stop the build.
#
# Exit status: the number of FAIL rows (capped at 100); 2 if it never got as
# far as a table.

if [[ -z ${RIG_IN_CONTAINER:-} ]]; then
  set -uo pipefail
  HERE=$(cd "$(dirname "$0")" && pwd)
  RIG_DIR=$(cd "$HERE/../.." && pwd)
  export RIG_NAME=${RIG_NAME:-nautilus-build-tia}
  source "$RIG_DIR/lib/container.sh"
  OUT=${RIG_OUT:-$RIG_DIR/out}/builds/tia-shaped

  if [[ -z ${NAUT:-} || -z ${VSIX:-} ]]; then
    echo "▸ building the candidate (NAUT/VSIX not given)"
    eval "$("$RIG_DIR/smoke/build.sh")" || exit 2
  fi
  [[ -x $NAUT && -f $VSIX ]] || { echo "need NAUT=<naut binary> and VSIX=<vscode-iec.vsix>" >&2; exit 2; }
  FRAME=""
  for v in CAP_W CAP_H REC_ZOOM REC_FONT_SIZE; do [[ -n ${!v:-} ]] && FRAME+="$v=${!v} "; done

  echo "▸ $RIG_NAME: fresh container"
  trap '[[ -n ${RIG_KEEP:-} ]] || rig_down' EXIT
  rig_up || exit 2
  incus file push "$NAUT" "$RIG_NAME/usr/local/bin/.naut.new" >/dev/null
  _rig chmod +x /usr/local/bin/.naut.new
  _rig mv -f /usr/local/bin/.naut.new /usr/local/bin/naut
  _rig ln -sf /usr/local/bin/naut /usr/local/bin/nautilus
  rig_push "$RIG_DIR/lib/lib.sh" "/home/$RIG_USER/lib.sh"
  rig_push_tree "$RIG_DIR/verbs" "/home/$RIG_USER/fixtures"
  _rig rm -rf "/home/$RIG_USER/build"
  rig_push_tree "$HERE" "/home/$RIG_USER/build"
  _rig rm -f /tmp/ext.vsix; rig_push "$VSIX" /tmp/ext.vsix; _rig chmod 644 /tmp/ext.vsix
  _rig chown -R "$RIG_USER:$RIG_USER" "/home/$RIG_USER"
  echo "▸ installing $(basename "$VSIX")"
  rig_run "code --install-extension /tmp/ext.vsix --force 2>&1 | tail -1"

  echo "▸ tia-shaped build (G_PACE=${G_PACE:-fast}${FRAME:+ $FRAME})"
  # NB: nothing in this command line may contain "vscode-rec" (lib.sh's
  # launch_vscode pkills by that string).
  rig_run "rm -rf ~/out; mkdir -p ~/out && cd ~ && DISPLAY=$RIG_DISPLAY $FRAME RIG_IN_CONTAINER=1 RIG_CLIPS=${RIG_CLIPS:-1} G_PACE=${G_PACE:-fast} timeout 3600 bash ~/build/build.sh" \
    || echo "  (build exited non-zero)"

  PULL=$(mktemp -d)
  incus file pull -r "$RIG_NAME/home/$RIG_USER/out" "$PULL/" >/dev/null 2>&1
  rm -rf "$OUT"; mkdir -p "$(dirname "$OUT")"; mv "$PULL/out" "$OUT" 2>/dev/null; rm -rf "$PULL"
  TSV=$OUT/build.tsv
  [[ -s $TSV ]] || { echo "no $TSV — the build never reached its table" >&2; exit 2; }
  echo; echo "════ results — $TSV"
  awk -F'\t' '{ printf "%-3s %-44s %-6s %s\n", $1, $2, $3, substr($4, 1, 140) }' "$TSV"
  echo
  for v in PASS FAIL XFAIL XPASS NOTE; do printf '%s %d  ' "$v" "$(awk -F'\t' -v v=$v '$3==v' "$TSV" | wc -l)"; done; echo
  fails=$(awk -F'\t' '$3=="FAIL"' "$TSV" | wc -l)
  exit $(( fails > 100 ? 100 : fails ))
fi

# ── in the container ────────────────────────────────────────────────────────
set -uo pipefail
source "$HOME/fixtures/prep.sh"
source "$HOME/fixtures/gestures.sh"
trap 'clip_stop; cleanup_capture' EXIT
REF=$HOME/build/reference
TSV=$OUT_DIR/build.tsv
: >"$TSV"
N=0
TYPE_DELAY=$([[ $G_PACE == human ]] && echo 45 || echo 12)

# row <name> <expect PASS|XFAIL> <cmd> [args…] — run, snap, clip, record.
# NOT in a subshell: verbs set state later rows read (G_FILE).
row() {
  local name=$1 expect=$2 rc verdict err base; shift 2
  N=$((N + 1))
  base=$(printf '%02d-%s' "$N" "$name")
  clip_start "$base"
  "$@" >"$HOME/.row-out" 2>"$HOME/.row-err"; rc=$?
  err=$(cat "$HOME/.row-out" "$HOME/.row-err" | sed 's/\x1b\[[0-9;]*m//g' | tr '\n\t' '  ' | sed 's/  */ /g; s/^ //; s/ $//')
  if [[ $expect == XFAIL ]]; then (( rc == 0 )) && verdict=XPASS || verdict=XFAIL
  else (( rc == 0 )) && verdict=PASS || verdict=FAIL; fi
  if [[ -n ${WIN:-} ]]; then park "$((CAP_W - 30))" "$((CAP_H - 60))" 0.4; snap "$base" >/dev/null; fi
  clip_stop
  printf '%02d\t%s\t%s\t%s\n' "$N" "$name" "$verdict" "${err:-ok}" >>"$TSV"
  case $verdict in
    PASS|XFAIL) printf '  \033[32m%-5s\033[0m %02d %s %s\n' "$verdict" "$N" "$name" "${err:+— ${err:0:200}}" ;;
    *)          printf '  \033[31m%-5s\033[0m %02d %s %s\n' "$verdict" "$N" "$name" "${err:+— ${err:0:200}}" ;;
  esac
  [[ $verdict == PASS || $verdict == XPASS ]]
}
# note <name> <detail> — an information row (no verdict of its own).
note_row() { N=$((N + 1)); printf '%02d\t%s\t%s\t%s\n' "$N" "$1" NOTE "$2" >>"$TSV"; printf '  NOTE  %02d %s — %s\n' "$N" "$1" "$2"; }

# ── naut, at every intermediate ─────────────────────────────────────────────
# check_clean <beat> — `naut check` exits 0 (warnings allowed; they are
# counted in the detail).
check_clean() {
  local out rc
  out=$(cd "$PROJ" && naut check . 2>&1); rc=$?
  echo "$out" >"$OUT_DIR/check-$1.txt"
  echo "$(tail -1 <<<"$out")"
  (( rc == 0 )) || { grep -v '^naut check' <<<"$out" | grep -v warning | head -3 >&2; return 1; }
}
# same_as_ref <file> [sed expr applied to the reference first] — the built
# file equals reference/<file> modulo trailing whitespace, blank lines and
# the FBD editor's (* @layout … *) block.
same_as_ref() {
  python3 - "$PROJ/$1" "$REF/$1" "${2:-}" <<'PY'
import re, subprocess, sys
built, ref, sedx = sys.argv[1], sys.argv[2], sys.argv[3]
def norm(t):
    t = re.sub(r"\(\*\s*@layout\b.*?\*\)", "", t, flags=re.S)
    return [l.strip() for l in t.splitlines() if l.strip()]
b = norm(open(built).read())
r = open(ref).read()
if sedx: r = subprocess.run(["sed", sedx], input=r, capture_output=True, text=True).stdout
r = norm(r)
if b == r: print(f"{len(b)} lines == reference"); sys.exit(0)
import difflib
print(" | ".join(difflib.unified_diff(r, b, "reference", "built", n=0, lineterm=""))[:900])
sys.exit(1)
PY
}

# ── the workbench page (text editors, quick input) over CDP ─────────────────
wb() { cdp page "$1" 2>/dev/null | python3 -c 'import sys, json; v = json.load(sys.stdin); print(v if isinstance(v, str) else json.dumps(v))'; }
hover_text() { wb '[...document.querySelectorAll(".monaco-hover")].filter(e => !e.classList.contains("hidden") && e.getBoundingClientRect().height > 0 && e.innerText.trim()).map(e => e.innerText.trim().replace(/\s*\n\s*/g, " / ")).join(" ¦ ")'; }
suggest_rows() { wb '[...document.querySelectorAll(".suggest-widget.visible .monaco-list-row")].map(r => (r.querySelector(".label-name")?.innerText || r.getAttribute("aria-label") || "").trim()).join("\n")'; }
has_rows() { [[ -n $(suggest_rows) ]]; }
squiggles() { local n; n=$(wb 'String(document.querySelectorAll(".monaco-editor .squiggly-error").length)'); [[ $n =~ ^[0-9]+$ ]] && echo "$n" || echo -1; }
has_squiggle() { (( $(squiggles) > 0 )); }
no_squiggle() { (( $(squiggles) == 0 )); }
errors() {
  local t; t=$(wb '(() => { const e = document.getElementById("status.problems"); return e ? e.innerText.replace(/\s+/g, " ") : ""; })()')
  t=$(grep -oE '[0-9]+' <<<"$t" | head -1); echo "${t:--1}"
}
title() { xdotool getwindowname "$WIN" 2>/dev/null; }
dirty() { [[ $(title) == ●* ]]; }
wait_for() { local limit=$1 i; shift; for ((i = 0; i < limit * 4; i++)); do "$@" && return 0; sleep 0.25; done; return 1; }
goto() { xdotool key --clearmodifiers Escape; sleep 0.2; xdotool key --clearmodifiers ctrl+g; sleep 0.6; xdotool type "$1:$2"; sleep 0.3; xdotool key Return; sleep 0.5; }
line_of() { grep -nE -m1 -- "$2" "$1" | cut -d: -f1; }
goto_word() {
  local l c
  l=$(line_of "$1" "$2"); [[ -n $l ]] || { echo "goto_word: no line /$2/ in $1" >&2; return 1; }
  c=$(awk -v l="$l" -v s="$3" 'NR==l{print index($0,s)}' "$1")
  goto "$l" $((c + 1))
}
show_hover() {
  local i h
  for i in 1 2 3 4 5; do
    xdotool key --clearmodifiers Escape; sleep 0.3
    xdotool key --clearmodifiers ctrl+k ctrl+i; sleep 1.5
    h=$(hover_text); [[ -n $h ]] && { echo "$h"; return 0; }
    sleep 1.5
  done
  return 1
}
# text_replace <file> <old> <new> — select the first <old> (located on DISK)
# in the active text editor and type <new> over it (smoke/lib.sh's).
text_replace() {
  local f=$1 old=$2 new=$3 l c i
  l=$(grep -nF -- "$old" "$f" | head -1 | cut -d: -f1)
  [[ -n $l ]] || { echo "text_replace: no '$old' in $f" >&2; return 1; }
  c=$(awk -v l="$l" -v s="$old" 'NR==l{print index($0,s)}' "$f")
  goto "$l" "$c"
  for ((i = 0; i < ${#old}; i++)); do xdotool key shift+Right; done
  xdotool type --delay "$TYPE_DELAY" -- "$new"; sleep 0.6
}
save() { xdotool key --clearmodifiers Escape; sleep 0.1; xdotool key --clearmodifiers ctrl+s; sleep 1.2; }

# type_text <text> — keystrokes into the active text editor, line by line:
# Escape before every Return (an open suggest widget would ACCEPT on Return
# instead of breaking the line — 03-sim-st's trap), the indentation typed
# (autoIndent is off), nothing auto-closed.
type_text() {
  local first=1 line
  while IFS= read -r line || [[ -n $line ]]; do
    if [[ -z $first ]]; then
      xdotool key --clearmodifiers Escape; sleep 0.05
      xdotool key --clearmodifiers Return; sleep 0.08
    fi
    first=
    [[ -n $line ]] && xdotool type --delay "$TYPE_DELAY" -- "$line"
    sleep 0.05
  done <<<"$1"
}
# new_text_file <relpath> — an empty file, opened as text (quick-open).
new_text_file() {
  mkdir -p "$(dirname "$PROJ/$1")"; : >"$PROJ/$1"
  vs_cmd "View: Close All Editors" 1
  open_file "$(basename "$1")" 2
}

# ── 01 scaffold ─────────────────────────────────────────────────────────────
PROJ=$HOME/tia-dosing
rm -rf "$PROJ"
b01() {
  ( cd "$HOME" && naut new tia-dosing --no-input --template minimal >/dev/null ) || return 1
  git -C "$PROJ" config user.name "Demo"; git -C "$PROJ" config user.email "demo@example.com"
  git -C "$PROJ" add -A; git -C "$PROJ" commit -qm "scaffold: naut new tia-dosing --template minimal"
  point_extension_at "$PROJ" "$PORT"
  check_clean 01-scaffold
}
row 01-scaffold PASS b01 || exit 1
EXTRA_SETTINGS='"editor.autoClosingBrackets": "never", "editor.autoClosingQuotes": "never", "editor.autoClosingComments": "never", "editor.autoSurround": "never", "editor.autoIndent": "none", "editor.quickSuggestions": { "other": false, "comments": false, "strings": false }, "editor.suggestOnTriggerCharacters": false, "editor.acceptSuggestionOnEnter": "off", "editor.acceptSuggestionOnCommitCharacter": false, "git.decorations.enabled": false' \
  ext_open
hide_sidebar
sleep 3

# ── 02 types.st: the PLC data type, typed ───────────────────────────────────
b02() {
  new_text_file types.st || return 1
  type_text "$(cat "$REF/types.st")"
  save
  same_as_ref types.st || return 1
  check_clean 02-types
}
row 02-type-DoseRecipe PASS b02 || { cp "$REF/types.st" "$PROJ/types.st"; note_row FALLBACK "types.st copied from the reference"; }

# ── 03 the tag table: pasted; tag-files: typed into nautilus.yaml ───────────
b03() {
  mkdir -p "$PROJ/tags"; cp "$REF/tags/plc_tags.yaml" "$PROJ/tags/plc_tags.yaml"
  vs_cmd "View: Close All Editors" 1
  open_file nautilus.yaml 3
  # on the blank line above the tasks comment, and no trailing Return: YAML
  # keeps a line's indentation on Return whatever editor.autoIndent says
  goto "$(( $(line_of "$PROJ/nautilus.yaml" '^# One program per task') - 1 ))" 1
  type_text $'\n# The PLC tag table (TIA: "PLC tags" / "Default tag table").\ntag-files:\n  - tags/plc_tags.yaml'
  save
  grep -qE '^  - tags/plc_tags.yaml$' "$PROJ/nautilus.yaml" || { echo "tag-files entry did not land" >&2; return 1; }
  check_clean 03-tags
}
row 03-tag-table PASS b03 || { python3 - "$PROJ/nautilus.yaml" <<'PY'
import sys; p = sys.argv[1]; s = open(p).read()
if "tag-files:" not in s: s = s.replace("\ntasks:", "\ntag-files:\n  - tags/plc_tags.yaml\n\ntasks:", 1)
open(p, "w").write(s)
PY
  note_row FALLBACK "tag-files written by text"; }
# the TIA habit: a data type in the tag table's type column (Int, Bool …)
b03_types() {
  local out
  cp "$PROJ/tags/plc_tags.yaml" "$HOME/.tags.bak"
  sed -i 's/{ name: FT101_Raw, role: input,/{ name: FT101_Raw, role: input, type: INT,/' "$PROJ/tags/plc_tags.yaml"
  out=$(cd "$PROJ" && naut check . 2>&1); local rc=$?
  cp "$HOME/.tags.bak" "$PROJ/tags/plc_tags.yaml"
  echo "$(grep -m1 FT101_Raw <<<"$out")"
  return $rc
}
row 03-tag-elementary-type XFAIL b03_types

# ── 04 scale.st: the FC, typed, with the language server ────────────────────
SCALE_TYPED=$(sed 's/SCALEANALOG/ScaleAnalog/g' "$REF/scale.st")
b04() {
  new_text_file scale.st || return 1
  local head tail
  head=$(sed -n '1,/^frac := /p' <<<"$SCALE_TYPED")
  type_text "$head"
  xdotool key --clearmodifiers Escape; sleep 0.05; xdotool key --clearmodifiers Return; sleep 0.1
  xdotool type --delay "$TYPE_DELAY" -- 'ScaleAnalog := LIM'
  sleep 0.6
  xdotool key --clearmodifiers ctrl+space; sleep 1.5
  wait_for 6 has_rows || true
  local rows; rows=$(suggest_rows)
  snap 04-limit-completion >/dev/null
  echo "$rows" >"$OUT_DIR/04-limit-completion.txt"
  xdotool key --clearmodifiers Escape; sleep 0.3
  xdotool type --delay "$TYPE_DELAY" -- 'IT('
  sleep 0.5
  # signature help: Ctrl+Shift+Space (editor.action.triggerParameterHints)
  xdotool key --clearmodifiers ctrl+shift+space; sleep 1.8
  wb '(() => { const w = document.querySelector(".parameter-hints-widget"); return w && w.classList.contains("visible") ? w.innerText.replace(/\s+/g, " ") : ""; })()' >"$OUT_DIR/04-signature-help.txt"
  snap 04-signature-help >/dev/null
  xdotool key --clearmodifiers Escape; sleep 0.3
  xdotool type --delay "$TYPE_DELAY" -- 'EngLo, EngLo + frac * (EngHi - EngLo), EngHi);'
  tail=$(sed -n '/^ScaleAnalog := /,$p' <<<"$SCALE_TYPED" | tail -n +2)
  xdotool key --clearmodifiers Escape; sleep 0.05; xdotool key --clearmodifiers Return; sleep 0.1
  type_text "$tail"
  save
  same_as_ref scale.st 's/SCALEANALOG/ScaleAnalog/g' || return 1
  grep -qx LIMIT "$OUT_DIR/04-limit-completion.txt" || { echo "completion after 'LIM' did not list LIMIT: $(head -5 "$OUT_DIR/04-limit-completion.txt" | paste -sd' ')" >&2; return 1; }
  echo "completion after LIM: $(head -4 "$OUT_DIR/04-limit-completion.txt" | paste -sd' ')"
}
row 04-type-ScaleAnalog PASS b04 || { sed 's/SCALEANALOG/ScaleAnalog/g' "$REF/scale.st" >"$PROJ/scale.st"; note_row FALLBACK "scale.st written from the reference"; }
b04_sig() { local s; s=$(cat "$OUT_DIR/04-signature-help.txt"); echo "parameter hints: ${s:-<none>}"; [[ -n $s ]]; }
row 04-signature-help-LIMIT XFAIL b04_sig
b04_hover() {
  goto_word "$PROJ/scale.st" '^ScaleAnalog := LIMIT' EngHi || return 1
  local h; h=$(show_hover) || { echo "no hover" >&2; return 1; }
  echo "hover: $h"
  [[ $h == *"EngHi"*REAL* ]]
}
row 04-hover-pin PASS b04_hover
row 04-check PASS check_clean 04-scale

# ── 05 dosing.st: the FB, typed; a TIA habit and a typo, each diagnosed ─────
b05() {
  new_text_file dosing.st || return 1
  type_text "$(cat "$REF/dosing.st")"
  save
  same_as_ref dosing.st || return 1
  check_clean 05-dosing
}
row 05-type-Dosing PASS b05 || { cp "$REF/dosing.st" "$PROJ/dosing.st"; note_row FALLBACK "dosing.st copied from the reference"; }
# diag_probe <old> <new> <png> — type <new> over <old>, wait for the squiggle,
# read the problem's hover on the edited line, then Ctrl+Z back to clean.
diag_probe() {
  local F=$PROJ/dosing.st e0 e1 h l
  wait_for 10 no_squiggle || true
  e0=$(errors)
  text_replace "$F" "$1" "$2" || return 1
  local ok=1
  wait_for 12 has_squiggle || { echo "no squiggle after typing '$2'" >&2; ok=0; }
  sleep 1; e1=$(errors)
  # the problem itself: F8 (Go to Next Problem) opens its marker widget on
  # the edited line, the message where a person reads it
  l=$(line_of "$F" "$(sed 's/[][\.*^$()+?{}|]/\\&/g' <<<"$1")")
  goto "$l" 1
  xdotool key --clearmodifiers F8; sleep 1.5
  h=$(wb '(() => { const w = document.querySelector(".marker-widget"); return w ? w.innerText.replace(/\s+/g, " ").trim().slice(0, 240) : ""; })()')
  snap "$3" >/dev/null
  echo "errors $e0 -> $e1; squiggles $(squiggles); problem: ${h:-<none>}"
  # fixed the way a person fixes it: undo
  xdotool key --clearmodifiers Escape; sleep 0.3
  local i; for i in $(seq 40); do dirty || break; xdotool key --clearmodifiers ctrl+z; sleep 0.2; done
  (( ok )) || return 1
  wait_for 12 no_squiggle || { echo "squiggle still there after the fix" >&2; return 1; }
  ! dirty
}
# SCL's # prefix on a local: on the right-hand side it is a parse error; on
# an assignment target it is silently dropped (no squiggle — FINDINGS)
row 05-diag-hash-prefix-rhs PASS diag_probe "Step := state;" "Step := #state;" 05-hash-prefix-rhs
row 05-diag-hash-prefix-target XFAIL diag_probe "    state := 10;" "    #state := 10;" 05-hash-prefix-target
row 05-diag-typo PASS diag_probe "Recipe.TargetL > 0.0" "Recipe.TargetLL > 0.0" 05-typo
# REGION … END_REGION, the SCL folding habit
row 05-diag-region PASS diag_probe "justDone := FALSE;" "REGION init justDone := FALSE; END_REGION" 05-region
# Outline: Go to Symbol in Editor (Ctrl+Shift+O) — the block interface.
b05_outline() {
  xdotool key --clearmodifiers Escape; sleep 0.3
  xdotool key --clearmodifiers ctrl+shift+o; sleep 2.5
  local t; t=$(wb '(() => { const q = document.querySelector(".quick-input-widget"); if (!q || q.style.display === "none") return ""; return [...q.querySelectorAll(".monaco-list-row")].map(r => r.innerText.replace(/\s+/g, " ").trim()).join(" | ") || q.innerText.replace(/\s+/g, " ").trim(); })()')
  snap 05-outline >/dev/null
  xdotool key --clearmodifiers Escape; sleep 0.4
  echo "Ctrl+Shift+O: ${t:-<nothing>}"
  [[ $t == *Dosing* && $t == *state* ]]
}
row 05-outline-symbols XFAIL b05_outline
# Cross-reference: Find All References (Shift+F12) on `state`.
b05_refs() {
  goto_word "$PROJ/dosing.st" '^ValveOpen := state = 10;' state || return 1
  xdotool key --clearmodifiers shift+F12; sleep 3
  local t; t=$(wb '(() => { const p = document.querySelector(".peekview-widget, .reference-zone-widget"); const n = [...document.querySelectorAll(".notifications-toasts .notification-toast, .monaco-editor .message-widget, .monaco-editor-overlaymessage")].map(e => e.innerText.trim()).join(" | "); return (p ? "peek: " + p.innerText.replace(/\s+/g, " ").slice(0, 200) : "") + (n ? " msg: " + n : ""); })()')
  snap 05-references >/dev/null
  xdotool key --clearmodifiers Escape; sleep 0.4
  echo "Shift+F12: ${t:-<nothing>}"
  [[ $t == peek:* ]]
}
row 05-find-references XFAIL b05_refs
row 05-check PASS check_clean 05-dosing

# ── 06 main.fbd: a blank file, its header declared from the palette ─────────
: >"$PROJ/main.fbd"
row 06-open-main-fbd PASS ed_open_diagram main.fbd
while read -r -u 3 name typ; do
  row "06-declare-$name" PASS fbd_declare "$name" "$typ" || {
    python3 - "$PROJ/main.fbd" "$name" "$typ" <<'PY'
import sys; p, n, t = sys.argv[1:4]; s = open(p).read()
if not s.strip(): s = "PROGRAM Main\nVAR_EXTERNAL\nEND_VAR\nFBD\nEND_FBD\nEND_PROGRAM\n"
if "VAR_EXTERNAL" not in s: s = s.replace("\nFBD", "\nVAR_EXTERNAL\nEND_VAR\nFBD", 1)
i = s.index("END_VAR", s.index("VAR_EXTERNAL")); s = s[:i] + f"    {n} : {t};\n" + s[i:]
open(p, "w").write(s)
PY
    note_row FALLBACK "declared $name : $typ by text"; }
done 3< <(sed -n '/^VAR_EXTERNAL/,/^END_VAR/p' "$REF/main.fbd" | grep -E '^ +[A-Za-z0-9_]+ *:' | sed -E 's/^ +([A-Za-z0-9_]+) *: *([A-Za-z0-9_]+);.*/\1 \2/')
b06_header() {
  python3 - "$PROJ/main.fbd" "$REF/main.fbd" <<'PY'
import sys
def hdr(p):
    t = open(p).read(); return [l.strip() for l in t[:t.index("FBD\n")].splitlines() if l.strip()]
b, r = hdr(sys.argv[1]), hdr(sys.argv[2])
print(f"{len(b)} header lines" + ("" if b == r else f" — built {b} vs reference {r}")); sys.exit(0 if b == r else 1)
PY
}
row 06-header-matches PASS b06_header
row 06-check PASS check_clean 06-header

# ── FBD fallback: write a statement by text when its gesture failed ─────────
# fb_fallback <ERE that must be in main.fbd> <statement> [replace-ERE]
fb_fallback() {
  grep -Eq -- "$1" "$PROJ/main.fbd" && return 0
  python3 - "$PROJ/main.fbd" "$2" "${3:-}" <<'PY'
import re, sys; p, stmt, rx = sys.argv[1:4]; s = open(p).read()
if rx and re.search(rx, s, re.M): s = re.sub(rx, stmt, s, count=1, flags=re.M)
else: s = s.replace("\nEND_FBD", "\n  " + stmt + "\nEND_FBD", 1)
open(p, "w").write(s)
PY
  note_row FALLBACK "main.fbd: wrote '$2' by text"
  sleep 2
}
ref_line() { grep -E -m1 -- "$1" "$REF/main.fbd" | sed 's/^ *//'; }
NET1=$(ref_line '// Network 1'); NET2=$(ref_line '// Network 2'); NET3=$(ref_line '// Network 3')
NET4=$(ref_line '// Network 4'); NET5=$(ref_line '// Network 5')

# ── 07 network 1: the FC call ───────────────────────────────────────────────
row 07-comment PASS fbd_add_comment "${NET1#// }" || fb_fallback 'Network 1' "$NET1"
# Does "block → wire"'s function field offer the project's FUNCTION?
b07_suggest() {
  click_button "+ add" || return 1
  wait_js '[...doc.querySelectorAll("button.item")].some(b => !b.closest(".suggest"))' 3 || return 1
  click_el "[...doc.querySelectorAll('button.item')].find(b => !b.closest('.suggest') && b.querySelector('span')?.textContent.trim() === 'block → wire')" || return 1
  click_el "[...doc.querySelectorAll('label.field')].find(l => l.querySelector('span')?.textContent.trim() === 'function')?.querySelector('input')" || return 1
  g_key ctrl+a; g_type "Sca"; sleep 1
  local items; items=$(js '[...doc.querySelectorAll(".suggest .list *")].filter(e => !e.children.length).map(e => e.textContent.trim()).filter(Boolean).join(", ")')
  snap 07-function-suggest >/dev/null
  g_key Escape; sleep 0.3
  g_key Escape; sleep 0.5
  js_true 'doc.querySelector("label.field")' && { click_button back; sleep 0.3; click_button "+ add"; }
  echo "function field after 'Sca': ${items:-<no list>}"
  [[ $items == *ScaleAnalog* ]]
}
row 07-palette-lists-user-FUNCTION XFAIL b07_suggest
row 07-block-ScaleAnalog PASS fbd_add_function ScaleAnalog ft "FT101_Raw, 0.0, 120.0" || fb_fallback '^ *ft = ' "ft = ScaleAnalog(FT101_Raw, 0.0, 120.0)"
row 07-coil-FT101_Flow PASS fbd_add_coil FT101_Flow ft || fb_fallback '^ *FT101_Flow := ft' "FT101_Flow := ft"
row 07-check-user-FUNCTION-from-FBD XFAIL check_clean 07-net1
# the workaround: the FUNCTION's name in capitals (FBD upper-cases a call's
# name; a user FUNCTION is looked up case-sensitively). Typed in scale.st.
b07_rename() {
  vs_cmd "View: Close All Editors" 1
  open_file scale.st 2
  text_replace "$PROJ/scale.st" "FUNCTION ScaleAnalog : REAL" "FUNCTION SCALEANALOG : REAL" || return 1
  text_replace "$PROJ/scale.st" "ScaleAnalog := LIMIT(" "SCALEANALOG := LIMIT(" || return 1
  save
  same_as_ref scale.st || return 1
  check_clean 07-workaround
}
row 07-workaround-uppercase-FC PASS b07_rename || { cp "$REF/scale.st" "$PROJ/scale.st"; note_row FALLBACK "scale.st: the SCALEANALOG rename by text"; }
row 07-reopen-main-fbd PASS ed_open_diagram main.fbd

# ── 08 network 2: the FB instance (the "instance DB") ───────────────────────
row 08-comment PASS fbd_add_comment "${NET2#// }" || fb_fallback 'Network 2' "$NET2"
row 08-fb-picker-Dosing PASS fbd_add_block Dosing doseA || fb_fallback '^ *doseA : Dosing' "doseA : Dosing(Start := _, Stop := _, FlowLpm := _, Dt := _, NoFlowTime := _, Recipe := _)"
row 08-check-with-open-pins XFAIL check_clean 08-open-pins
for pr in StartA:Start StopAll:Stop FT101_Flow:FlowLpm MainDtS:Dt RecipeA:Recipe; do
  t=${pr%%:*}; p=${pr##*:}
  row "08-wire-$t-to-$p" PASS fbd_add_tag_ref "$t" "doseA.$p" || fb_fallback "doseA : Dosing\\(.*\\b$p := $t\\b" "\\1$p := $t" "^( *doseA : Dosing\\(.*?)$p := _"
done
row 08-disconnect-NoFlowTime PASS fbd_disconnect doseA.NoFlowTime || {
  sed -i -E 's/, NoFlowTime := _//' "$PROJ/main.fbd"; note_row FALLBACK "main.fbd: dropped ', NoFlowTime := _' by text"; sleep 2; }
for pr in XV101_Open:ValveOpen DoseFault:Fault DoneLamp:Done DoseStep:Step; do
  t=${pr%%:*}; p=${pr##*:}
  row "08-coil-$t" PASS fbd_add_coil "$t" "doseA.$p" || fb_fallback "^ *$t := doseA.$p" "$t := doseA.$p"
done
row 08-check PASS check_clean 08-net2

# ── 09 network 3: LIMIT and SEL, REAL math ──────────────────────────────────
row 09-comment PASS fbd_add_comment "${NET3#// }" || fb_fallback 'Network 3' "$NET3"
row 09-block-LIMIT PASS fbd_add_block LIMIT spA "0.0, RecipeA.FlowSP, MaxFlowLpm" || fb_fallback '^ *spA = ' "spA = LIMIT(0.0, RecipeA.FlowSP, MaxFlowLpm)"
row 09-block-SEL PASS fbd_add_block SEL spOut "doseA.ValveOpen, 0.0, _" || fb_fallback '^ *spOut = ' "spOut = SEL(doseA.ValveOpen, 0.0, _)"
row 09-wire-spA-to-SEL.IN1 PASS fbd_wire spA.OUT spOut.IN1 || fb_fallback '^ *spOut = SEL\(doseA.ValveOpen, 0.0, spA\)' "spOut = SEL(doseA.ValveOpen, 0.0, spA)" '^ *spOut = SEL\(.*\)$'
# EN/ENO: TIA draws EN and ENO on every box; is there an EN pin on LIMIT?
b09_eno() {
  local en eno
  en=$(js "!!$(fbd_node_el spA)?.querySelector('.svelte-flow__handle.target[data-handleid=\"EN\"]')")
  eno=$(js "!!$(fbd_node_el spA)?.querySelector('.svelte-flow__handle.source[data-handleid=\"ENO\"]')")
  local pins; pins=$(js "[...($(fbd_node_el spA))?.querySelectorAll('.svelte-flow__handle') ?? []].map(h => h.dataset.handleid || '·').join(' ')")
  fbd_zoom_to spA 2 >/dev/null 2>&1 || true
  echo "LIMIT spA pins: $pins (EN $en, ENO $eno)"
  [[ $en == true && $eno == true ]]
}
row 09-EN-ENO-on-LIMIT XFAIL b09_eno
b09_en_text() {
  # and by text, what a Siemens programmer would try: EN as a named input
  local src out
  src=$(python3 -c 'import json,sys; print(json.dumps({"source": open(sys.argv[1]).read(), "op": {"type": "insertStatement", "text": "spB = LIMIT(EN := doseA.ValveOpen, MN := 0.0, IN := RecipeA.FlowSP, MX := MaxFlowLpm)"}}))' "$PROJ/main.fbd")
  out=$(naut fbd edit <<<"$src" 2>&1)
  echo "insertStatement 'spB = LIMIT(EN := …, MN := …, IN := …, MX := …)': ${out:0:200}"
  [[ $out != *error* ]]
}
row 09-EN-input-by-text XFAIL b09_en_text
row 09-coil-FCV101_SP PASS fbd_add_coil FCV101_SP spOut || fb_fallback '^ *FCV101_SP := spOut' "FCV101_SP := spOut"
row 09-check PASS check_clean 09-net3

# ── 10 network 4: compare + TON ─────────────────────────────────────────────
row 10-comment PASS fbd_add_comment "${NET4#// }" || fb_fallback 'Network 4' "$NET4"
row 10-block-GT PASS fbd_add_block GT hiFlow "FT101_Flow, MaxFlowLpm" || fb_fallback '^ *hiFlow = ' "hiFlow = GT(FT101_Flow, MaxFlowLpm)"
row 10-fb-picker-TON PASS fbd_add_block TON tHi "IN := _, PT := T#2S" || fb_fallback '^ *tHi : TON' "tHi : TON(IN := _, PT := T#2S)"
row 10-wire-hiFlow-to-tHi.IN PASS fbd_wire hiFlow.OUT tHi.IN || fb_fallback '^ *tHi : TON\(IN := hiFlow' '\1hiFlow' '^( *tHi : TON\(IN := )_'
row 10-coil-HighFlowAlm PASS fbd_add_coil HighFlowAlm tHi.Q || fb_fallback '^ *HighFlowAlm := tHi.Q' "HighFlowAlm := tHi.Q"
row 10-check PASS check_clean 10-net4

# ── 11 network 5: CTU; network numbers; a pinned layout ─────────────────────
row 11-comment PASS fbd_add_comment "${NET5#// }" || fb_fallback 'Network 5' "$NET5"
row 11-fb-picker-CTU PASS fbd_add_block CTU cDoses "CU := _, R := _, PV := 1000" || fb_fallback '^ *cDoses : CTU' "cDoses : CTU(CU := _, R := _, PV := 1000)"
row 11-wire-doseA.Done-to-CU PASS fbd_wire doseA.Done cDoses.CU || fb_fallback '^ *cDoses : CTU\(CU := doseA.Done' '\1doseA.Done' '^( *cDoses : CTU\(CU := )_'
row 11-wire-ResetCount-to-R PASS fbd_add_tag_ref ResetCount cDoses.R || fb_fallback 'R := ResetCount' '\1ResetCount' '^( *cDoses : CTU\(.*R := )_'
row 11-coil-DosesToday PASS fbd_add_coil DosesToday cDoses.CV || fb_fallback '^ *DosesToday := cDoses.CV' "DosesToday := cDoses.CV"
b11_netno() {
  diagram_zoom fit >/dev/null 2>&1 || true
  local t; t=$(js '[...doc.querySelectorAll(".netno, .network, [class*=netnum], [class*=exec-order], [class*=execorder]")].map(e => e.className + ":" + e.textContent.trim()).join(" ")')
  local nums; nums=$(js '[...doc.querySelectorAll("*")].filter(e => !e.children.length && !e.closest("[data-id^=\"cm:\"]") && /^(#|Nw\.?\s*|Network\s+)?\d{1,2}$/.test(e.textContent.trim()) && !e.closest(".svelte-flow__controls")).map(e => (e.closest("[data-id]")?.dataset.id || e.tagName) + "=" + e.textContent.trim()).slice(0, 12).join(", ")')
  echo "numbered labels outside notes: ${nums//\"/}; network/order elements: ${t//\"/}"
  [[ -n ${nums//\"/} || -n ${t//\"/} ]]
}
row 11-network-numbers-or-exec-order XFAIL b11_netno
row 11-move-node-doseA PASS fbd_move_node doseA 60 40
row 11-check PASS check_clean 11-net5

# ── 12 go live: main.fbd becomes the task; the tests go in ──────────────────
b12() {
  local y=$PROJ/nautilus.yaml from to
  vs_cmd "View: Close All Editors" 1
  open_file nautilus.yaml 3
  text_replace "$y" "program: program.st" "program: main.fbd" || return 1
  # Typed at column 1 of the line BELOW scan:, then Return: VS Code's
  # built-in [yaml] defaults keep auto-indent on whatever the user setting
  # says, so a Return typed AFTER "scan: 100ms" indents the new line itself
  # and the typed indentation doubles (run 3: `dt-tag` at 8 spaces, a YAML
  # error).
  goto "$(( $(line_of "$y" '^    scan: 100ms') + 1 ))" 1
  xdotool type --delay "$TYPE_DELAY" -- "    dt-tag: MainDtS"
  xdotool key --clearmodifiers Escape; xdotool key --clearmodifiers Return; sleep 0.1
  save
  # the template's three example tags, and their comment: select, Delete
  from=$(line_of "$y" '^# Tags by role'); to=$(line_of "$y" 'name: Actuator')
  [[ -n $from && -n $to ]] || { echo "template tags block not found" >&2; return 1; }
  goto "$from" 1
  local i; for ((i = from; i <= to; i++)); do xdotool key shift+Down; done
  xdotool key Delete; sleep 0.4
  save
  grep -q 'program: main.fbd' "$y" && grep -q 'dt-tag: MainDtS' "$y" && ! grep -q 'name: Sensor' "$y" || { echo "nautilus.yaml edits did not land" >&2; return 1; }
  rm -f "$PROJ/program.st" "$PROJ/tia-dosing_test.yaml"
  cp "$REF/tia-dosing_test.yaml" "$PROJ/tia-dosing_test.yaml"
  check_clean 12-live
}
row 12-task-to-main-fbd PASS b12 || { cp "$REF/nautilus.yaml" "$PROJ/nautilus.yaml"; rm -f "$PROJ/program.st"; cp "$REF/tia-dosing_test.yaml" "$PROJ/"; note_row FALLBACK "nautilus.yaml copied from the reference"; }
b12_test() {
  local out rc
  out=$(cd "$PROJ" && naut test . 2>&1); rc=$?
  echo "$out" >"$OUT_DIR/test.txt"
  tail -1 <<<"$out"
  (( rc == 0 )) && grep -qE '^6 tests, 6 passed, 0 failed$' <<<"$out"
}
row 12-naut-test PASS b12_test

# ── 13 compare: with the reference; and two TIA habits ─────────────────────
b13_compare() {
  local f bad=0
  for f in types.st scale.st dosing.st main.fbd tags/plc_tags.yaml tia-dosing_test.yaml; do
    printf '%s: %s\n' "$f" "$(same_as_ref "$f" 2>&1)" >>"$OUT_DIR/compare.txt" || bad=1
    same_as_ref "$f" >/dev/null 2>&1 || bad=1
  done
  # nautilus.yaml: the parts that run (the template's header comment stays)
  python3 - "$PROJ/nautilus.yaml" "$REF/nautilus.yaml" >>"$OUT_DIR/compare.txt" <<'PY' || bad=1
import re, sys
def body(p):
    t = open(p).read(); t = t[t.index("\nserver:"):]
    return [re.sub(r"\s+#.*$", "", l).rstrip() for l in t.splitlines() if l.strip() and not l.lstrip().startswith("#")]
b, r = body(sys.argv[1]), body(sys.argv[2])
print("nautilus.yaml (from server: on, comments dropped): " + ("== reference" if b == r else f"built {b} vs reference {r}"))
sys.exit(0 if b == r else 1)
PY
  cat "$OUT_DIR/compare.txt" | tr '\n' ' '
  return $bad
}
row 13-compare-with-reference PASS b13_compare
# Compare blocks (TIA: compare editor): commit, change T#2S → T#3S on the
# diagram, then Diff FBD Diagram (vs git HEAD).
b13_diff() {
  git -C "$PROJ" add -A; git -C "$PROJ" commit -qm "tia-dosing: built"
  ed_open_diagram main.fbd || return 1
  local k; k=$(js '[...doc.querySelectorAll(".svelte-flow__node")].find(n => n.dataset.id.startsWith("k:") && n.textContent.trim() === "T#2S")?.dataset.id ?? ""' | tr -d '"')
  [[ -n $k ]] || { echo "no T#2S constant chip" >&2; return 1; }
  dclick_el "doc.querySelector('.svelte-flow__node[data-id=\"$k\"]')" || return 1
  float_edit "T#3S" || return 1
  g_save
  grep -q 'PT := T#3S' "$PROJ/main.fbd" || { echo "the constant edit did not land" >&2; return 1; }
  vs_cmd "nautilus: Diff FBD Diagram (vs git HEAD)" 4
  local tab; tab=$(wb '[...document.querySelectorAll(".tabs-container .tab")].map(t => t.innerText.replace(/\s+/g, " ").trim()).join(" | ")')
  snap 13-diff >/dev/null
  echo "tabs: $tab"
  [[ $tab == *"HEAD"*"working tree"* ]] || { echo "no diff tab" >&2; return 1; }
  # put the constant back the same way
  ed_open_diagram main.fbd || return 1
  k=$(js '[...doc.querySelectorAll(".svelte-flow__node")].find(n => n.dataset.id.startsWith("k:") && n.textContent.trim() === "T#3S")?.dataset.id ?? ""' | tr -d '"')
  dclick_el "doc.querySelector('.svelte-flow__node[data-id=\"$k\"]')" || return 1
  float_edit "T#2S" || return 1
  g_save
  grep -q 'PT := T#2S' "$PROJ/main.fbd" || { echo "could not put T#2S back" >&2; return 1; }
}
row 13-diff-vs-HEAD PASS b13_diff
# Force (TIA: force table): is there any force command?
b13_force() {
  vs_cmd "View: Close All Editors" 1
  xdotool key --clearmodifiers Escape; sleep 0.4
  xdotool key --clearmodifiers ctrl+shift+p; sleep 1.4
  xdotool type --delay 40 "nautilus: force"; sleep 1.6
  local t; t=$(wb '[...document.querySelectorAll(".quick-input-widget .monaco-list-row")].map(r => r.innerText.replace(/\s+/g, " ").trim()).slice(0, 8).join(" | ")')
  snap 13-force >/dev/null
  xdotool key --clearmodifiers Escape; sleep 0.5
  echo "palette 'nautilus: force': ${t:-<no commands>}"
  grep -qi 'force' <<<"$t"
}
row 13-force-command XFAIL b13_force

# ── the end state ───────────────────────────────────────────────────────────
row 99-naut-check PASS check_clean 99-final
row 99-naut-test PASS b12_test
mkdir -p "$OUT_DIR/built"
( cd "$PROJ" && git ls-files --others --cached --exclude-standard | grep -v '^\.' | while read -r f; do mkdir -p "$OUT_DIR/built/$(dirname "$f")"; cp "$f" "$OUT_DIR/built/$f"; done )
printf '%s · %s\n' "$(naut version 2>&1 | head -1)" "$(code --list-extensions --show-versions 2>/dev/null | grep -i '^joyauto.vscode-iec@')" >"$OUT_DIR/built/MANIFEST.txt"

echo
awk -F"\t" '{ printf "%-3s %-44s %-6s %s\n", $1, $2, $3, substr($4, 1, 140) }' "$TSV"
