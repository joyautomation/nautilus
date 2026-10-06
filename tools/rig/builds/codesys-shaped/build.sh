#!/usr/bin/env bash
# build.sh — the Codesys-shaped build: a washer wash cycle, gesture-built in
# a real VS Code from `naut new --template minimal`, the way a Codesys
# programmer would reach for it. The finished program is reference/ (naut
# check clean, naut test 6/6); PLAN.md lists the beats and which parts are
# gestured and which pasted; FINDINGS.md is the dogfood log.
#
#     export PATH=$HOME/.nvm/versions/node/v24.18.0/bin:/usr/local/go/bin:$PATH
#     eval "$(tools/rig/smoke/build.sh)"           # NAUT=, VSIX=, NAUTILUS_SHA=
#     RIG_NAME=nautilus-build-codesys G_PACE=fast RIG_CLIPS=1 \
#       tools/rig/builds/codesys-shaped/build.sh
#
# On the host it brings up the rig container (lib/container.sh), installs
# naut and the VSIX, pushes lib.sh, the verbs (as ~/fixtures) and this build
# folder (as ~/build), runs itself in there with RIG_IN_CONTAINER=1, and
# pulls ~/out back to tools/rig/out/builds/codesys-shaped/ (RIG_OUT= moves
# tools/rig/out):
#
#   build.tsv              <n> <row> PASS|FAIL|XFAIL|XPASS <detail>
#   NN-<row>.png / .mp4    the window right after each row, and the row as
#                          it happened (RIG_CLIPS=0: no clips)
#   clips.html, clips.md   the review index
#   built/                 the project as the build left it, `naut check`,
#                          `naut test`, and the compare against reference/
#
# Rows: a gesture verb (every verb saves and reads the file back), a paste
# (what no gesture can author — PLAN.md says which and why), a `naut check`
# after each beat, and the Codesys HABITS: what a Codesys programmer types
# first. A habit row expects the product to accept it; where it does not,
# the row is XFAIL and FINDINGS.md says how the product told the user and
# what they do instead. XPASS means a habit started working — go and look.
#
# Exit status: the number of FAIL rows (capped at 100); 2 if it never got as
# far as a table.

if [[ -z ${RIG_IN_CONTAINER:-} ]]; then
  set -uo pipefail
  BUILD_DIR=$(cd "$(dirname "$0")" && pwd)
  RIG_DIR=$(cd "$BUILD_DIR/../.." && pwd)
  export RIG_NAME=${RIG_NAME:-nautilus-build-codesys}
  source "$RIG_DIR/lib/container.sh"
  source "$RIG_DIR/lib/clips-index.sh"
  OUT=${RIG_OUT:-$RIG_DIR/out}/builds/codesys-shaped

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
  rig_push_tree "$BUILD_DIR" "/home/$RIG_USER/build"
  _rig rm -f /tmp/ext.vsix; rig_push "$VSIX" /tmp/ext.vsix; _rig chmod 644 /tmp/ext.vsix
  _rig chown -R "$RIG_USER:$RIG_USER" "/home/$RIG_USER"
  echo "▸ installing $(basename "$VSIX")"
  rig_run "code --install-extension /tmp/ext.vsix --force 2>&1 | tail -1"

  echo "▸ build (G_PACE=${G_PACE:-human}${FRAME:+ $FRAME})"
  # NB: nothing in this command line may contain "vscode-rec" (lib.sh's
  # launch_vscode pkills by that string).
  rig_run "rm -rf ~/out; mkdir -p ~/out && cd ~ && DISPLAY=$RIG_DISPLAY $FRAME RIG_IN_CONTAINER=1 RIG_CLIPS=${RIG_CLIPS:-1} G_PACE=${G_PACE:-human} timeout 5400 bash ~/build/build.sh" \
    || echo "  (build exited non-zero)"

  PULL=$(mktemp -d)
  incus file pull -r "$RIG_NAME/home/$RIG_USER/out" "$PULL/" >/dev/null 2>&1
  rm -rf "$OUT"; mkdir -p "$(dirname "$OUT")"; mv "$PULL/out" "$OUT" 2>/dev/null; rm -rf "$PULL"
  TSV=$OUT/build.tsv
  [[ -s $TSV ]] || { echo "no $TSV — the build never reached its table" >&2; exit 2; }
  awk -F'\t' '{ b = sprintf("%s-%s", $1, $2); c = ""; p = ""
    if (system("test -f \"'"$OUT"'/" b ".mp4\"") == 0) c = b ".mp4"
    if (system("test -f \"'"$OUT"'/" b ".png\"") == 0) p = b ".png"
    printf "%s\t%s\t%s\t%s\t%s\n", b, $3, $4, c, p }' "$TSV" \
    | _clips_write "$OUT" "codesys-shaped build — $(cat "$OUT/versions.txt" 2>/dev/null | tr '\n' ' ')"
  echo; echo "════ results — $TSV"
  awk -F'\t' '{ printf "%-3s %-34s %-6s %s\n", $1, $2, $3, $4 }' "$TSV"
  echo
  for v in PASS FAIL XFAIL XPASS; do printf '%s %d  ' "$v" "$(awk -F'\t' -v v=$v '$3==v' "$TSV" | wc -l)"; done; echo
  fails=$(awk -F'\t' '$3=="FAIL" || $3=="XPASS"' "$TSV" | wc -l)
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
printf '%s\n%s\n' "$(naut version 2>&1 | head -1)" \
  "$(code --list-extensions --show-versions 2>/dev/null | grep -i '^joyauto.vscode-iec@')" >"$OUT_DIR/versions.txt"

# row <name> <expect PASS|XFAIL> <cmd> [args…] — run, snap, record. Not in
# a subshell: verbs set state later verbs read (G_FILE).
row() {
  local name=$1 expect=$2 rc verdict err base; shift 2
  N=$((N + 1))
  base=$(printf '%02d-%s' "$N" "$name")
  clip_start "$base"
  "$@" >"$HOME/.row-out" 2>"$HOME/.row-err"; rc=$?
  err=$(cat "$HOME/.row-out" "$HOME/.row-err" | sed 's/\x1b\[[0-9;]*m//g' | tr '\n\t' '  ' | sed 's/  */ /g; s/^ //; s/ $//')
  if [[ $expect == XFAIL ]]; then (( rc == 0 )) && verdict=XPASS || verdict=XFAIL
  else (( rc == 0 )) && verdict=PASS || verdict=FAIL; fi
  # before ext_open there is no window to park in or snap
  if [[ -n ${WIN:-} ]]; then park "$((CAP_W - 30))" "$((CAP_H - 60))" 0.4; snap "$base" >/dev/null; fi
  clip_stop
  printf '%02d\t%s\t%s\t%s\n' "$N" "$name" "$verdict" "${err:-ok}" >>"$TSV"
  case $verdict in
    PASS|XFAIL) printf '  \033[32m%-5s\033[0m %02d %s %s\n' "$verdict" "$N" "$name" "${err:+— ${err:0:200}}" ;;
    *)          printf '  \033[31m%-5s\033[0m %02d %s %s\n' "$verdict" "$N" "$name" "${err:+— ${err:0:200}}" ;;
  esac
}

# ── build-local helpers (the shared verbs are in verbs/gestures.sh) ──────────

# naut_check — `naut check` in $PROJ; prints its summary (and the first
# error) as the row's detail, status is naut's.
naut_check() {
  local out rc
  out=$(cd "$PROJ" && naut check . 2>&1); rc=$?
  printf '%s\n' "$out" >>"$OUT_DIR/check-log.txt"
  local nw; nw=$(grep -c ': warning:' <<<"$out")
  echo "$(tail -1 <<<"$out") ($nw warnings)$( ((rc)) && echo " — $(grep -m1 -E 'error|:[0-9]+:[0-9]+: ' <<<"$out")")"
  return $rc
}
# check_after <beat> — a naut check row, expected clean.
check_after() { row "check-$1" "${2:-PASS}" naut_check; }

# reload_diagram — after a paste to the open .sfc on disk: the diagram's
# document reloads from disk (the buffer is clean: g_save ran first).
reload_diagram() { vs_cmd "File: Revert File" 2 || true; sleep 1; }

# paste_file <project path> [reference path] — copy a reference file in
# (the "paste": a whole file a Codesys programmer would type or paste).
paste_file() { mkdir -p "$(dirname "$PROJ/$1")"; cp "$REF/${2:-$1}" "$PROJ/$1"; }

# sfc_paste_header — the reference's PROGRAM line and header (comment,
# VAR_EXTERNAL, VAR, VAR CONSTANT) over the init skeleton's, up to SFC.
sfc_paste_header() {
  g_save
  python3 - "$PROJ/washer.sfc" "$REF/washer.sfc" <<'PY' || return 1
import re, sys
built, ref = (open(p).read() for p in sys.argv[1:3])
head = ref[:re.search(r"^SFC\s*$", ref, re.M).start()]
m = re.search(r"^SFC\s*$", built, re.M)
if not m: sys.exit("no SFC line in washer.sfc")
open(sys.argv[1], "w").write(head + built[m.start():])
PY
  reload_diagram
  wait_js "$(sfc_step_el Idle)" 8 || { g_err "the chart did not survive the header paste"; return 1; }
  assert_file_contains washer.sfc '^PROGRAM Washer' && assert_file_contains washer.sfc 'FC_DRAIN_TIMEOUT'
}

# sfc_paste_actions [stub-name…] — every ACTION block of the reference (with
# its comment), before END_SFC; a named stub gets a placeholder body instead,
# for the body editor to fill by gesture.
sfc_paste_actions() {
  g_save
  python3 - "$PROJ/washer.sfc" "$REF/washer.sfc" "$@" <<'PY' || return 1
import re, sys
p, r, stubs = sys.argv[1], sys.argv[2], sys.argv[3:]
built, ref = open(p).read(), open(r).read()
# the reference's action section: from the first comment-or-ACTION after
# the last END_TRANSITION, up to END_SFC
last_tr = list(re.finditer(r"END_TRANSITION\s*\n", ref))[-1].end()
end = re.search(r"^END_SFC", ref, re.M).start()
actions = ref[last_tr:end].strip("\n") + "\n\n"
# an ACTION the chart already has (written by gesture) is not pasted again,
# nor the comment block right above it
for name in re.findall(r"^\s*ACTION\s+(\w+)\s*:", built, re.M):
    actions = re.sub(r"(?:^[ \t]*\(\*(?:(?!\*\)).)*\*\)[ \t]*\n)*^[ \t]*ACTION %s:\n.*?END_ACTION[ \t]*\n\n?" % name, "", actions, flags=re.S | re.M)
for s in stubs:
    actions, n = re.subn(r"(ACTION %s:\n).*?(  END_ACTION)" % s, r"\1    SpinMotor := FALSE;\n\2", actions, flags=re.S)
    if n != 1: sys.exit(f"no ACTION {s} in the reference")
m = re.search(r"^END_SFC", built, re.M)
open(p, "w").write(built[:m.start()].rstrip("\n") + "\n\n" + actions + built[m.start():])
PY
  reload_diagram
  wait_js "$(sfc_step_el Idle)" 8 || { g_err "the chart did not survive the ACTION paste"; return 1; }
  assert_file_contains washer.sfc '^ *ACTION TrackState:'
}

# sfc_assoc_row_el <step> <target> — the association row naming <target>.
sfc_assoc_row_el() { printf '[...(%s)?.querySelectorAll("g.assocrow") ?? []].find(r => r.querySelector("text.assoctarget")?.textContent.startsWith(%s))' "$(sfc_step_el "$1")" "$(_q "$2")"; }

# sfc_edit_action <step> <old target> <new "Q target[(time)]"> — double-click
# the association row, type over it (FloatEditor, Enter).
sfc_edit_action() {
  local step=$1 old=$2 txt=$3 new
  new=$(awk '{print $2}' <<<"$txt"); new=${new%%(*}
  dclick_el "$(sfc_assoc_row_el "$step" "$old")" 0.5 0.5 || { g_err "no association $old on $step"; return 1; }
  float_edit "$txt" || return 1
  sfc_wait "!!($(sfc_assoc_row_el "$step" "$new"))" || return 1
  assert_file_contains washer.sfc "^ *$(awk '{print $1}' <<<"$txt") +$new\\b"
}

# sfc_body_editor_opens <step> <target> — double-click a row whose target is
# meant to be an ACTION; true only if the multiline ST-body editor opens.
sfc_body_editor_opens() {
  dclick_el "$(sfc_assoc_row_el "$1" "$2")" 0.5 0.5 || { g_err "no association $2 on $1"; return 1; }
  sleep 0.6
  local got; got=$(js 'doc.activeElement?.tagName ?? "none"')
  g_key Escape; sleep 0.4
  [[ $got == '"TEXTAREA"' ]] || { g_err "double-click on $1's '$2' row opened ${got//\"/} (the one-line association field), not an ST-body editor: no gesture creates ACTION $2"; return 1; }
}

# sfc_edit_action_body <step> <action> <ST body> — double-click the ACTION's
# association row (an existing ACTION opens the multiline body editor),
# select all, type the body, Ctrl+Enter.
sfc_edit_action_body() {
  local step=$1 act=$2 body=$3 first
  dclick_el "$(sfc_assoc_row_el "$step" "$act")" 0.5 0.5 || { g_err "no association $act on $step"; return 1; }
  wait_js 'doc.activeElement?.tagName === "TEXTAREA"' 5 || { g_err "the ST-body editor did not open"; return 1; }
  g_key ctrl+a; g_type "$body"; g_key ctrl+Return; sleep 1
  first=$(head -1 <<<"$body" | sed 's/[][\.*^$()+?{}|]/\\&/g')
  assert_file_contains washer.sfc "$first"
}

# sfc_try_assoc <step> <raw text> — the "+ action" field, typed as a Codesys
# programmer types it; true only if the file took the association (or the
# webview showed why not). parseAssoc drops what it cannot parse.
sfc_try_assoc() {
  local step=$1 txt=$2 before after
  g_save; before=$(md5sum <"$PROJ/washer.sfc")
  click_el "$(sfc_step_el "$step")?.querySelector('g.assocadd text')" || { g_err "no + action on $step"; return 1; }
  float_edit "$txt" || return 1
  sleep 1.5; g_save; after=$(md5sum <"$PROJ/washer.sfc")
  [[ $before != "$after" ]] && return 0
  js_true '[...doc.querySelectorAll(".error, .toast, [role=alert]")].some(e => e.getClientRects().length)' && { g_err "not written; an error was shown"; return 1; }
  g_err "typed '$txt' on $step: nothing written, nothing shown (the field silently drops text it cannot parse)"; return 1
}

# sfc_check_says <step> <ERE> — the step's red "!" marker exists and its
# tooltip (the problems the diagram shows for it) matches.
sfc_check_says() {
  local t; t=$(js "($(sfc_step_el "$1"))?.querySelector('g.diag title')?.textContent ?? ''")
  echo "step $1 marker: ${t:-none}"
  grep -Eq -- "$2" <<<"$t" || { g_err "$1 shows no problem matching /$2/"; return 1; }
}

# habit_check <ERE> — naut check is expected to FAIL here (a habit); return 0
# only if it is clean (XPASS). Prints the matching diagnostics.
habit_check() {
  local out rc; out=$(cd "$PROJ" && naut check . 2>&1); rc=$?
  grep -E -- "$1" <<<"$out" | head -3
  (( rc == 0 ))
}

# sfc_transition_name_field — "+ transition" from <step>: does the form take
# a transition name (Codesys draws every transition with one)?
sfc_transition_name_field() {
  sfc_select_step "$1" || return 1
  click_button "+ transition" || return 1
  wait_js 'doc.querySelector(".addform")' 4 || { g_err "no form"; return 1; }
  local labels; labels=$(js '[...doc.querySelectorAll(".addform label > span:first-child")].map(s => s.textContent.trim()).join(",")')
  g_key Escape; sleep 0.4
  grep -qi 'name' <<<"$labels" || { g_err "the transition form's fields are ${labels//\"/}: no name"; return 1; }
}

# sfc_abort_first — is FROM Fill TO Aborted declared before FROM Fill TO
# (Heat, Wash)? (priority = declaration order; "+ alt branch" appends LAST)
sfc_abort_first() {
  g_save
  local a b
  a=$(grep -nE 'FROM +Fill +TO +Aborted' "$PROJ/washer.sfc" | head -1 | cut -d: -f1)
  b=$(grep -nE 'FROM +Fill +TO +\(Heat, *Wash\)' "$PROJ/washer.sfc" | head -1 | cut -d: -f1)
  [[ -n $a && -n $b ]] || { g_err "missing transitions (abort line ${a:-?}, normal line ${b:-?})"; return 1; }
  (( a < b )) || { g_err "Fill->Aborted (line $a) is declared AFTER Fill->(Heat, Wash) (line $b): the abort has the LOWEST priority (◀ priority / Alt+← reorders it)"; return 1; }
}

# sfc_vars_declare <name> <type> [ext|local] — the chart's "vars" panel
# (shared with FBD/LD): section toggle, name, type, Enter.
# (the shared verbs/gestures.sh sfc_declare: the toggle cycles ext → local →
# const)
sfc_vars_declare() { sfc_declare "$@"; }
# vars_close — the panel toggles on its own button
vars_close() { js_true 'doc.querySelector(".addrow")' && click_button vars; sleep 0.4; return 0; }
# sfc_vars_declare_constant <name> <type> <init> — what a Codesys
# programmer declares: a constant with its value, in the panel's CONSTANT
# section (const) and its init field (#180; the panel had ext/local and a
# type only, FINDINGS #6).
sfc_vars_declare_constant() {
  local rc; sfc_vars_declare "$1" "$2" const "$3"; rc=$?
  local note; note=$(page_text '[...document.querySelectorAll(".notification-list-item")].map(e => e.textContent).join(" | ")' 2>/dev/null)
  [[ -n $note && $note != null && $note != '""' ]] && echo "notification: $note"
  vars_close; return $rc
}

# sfc_join_drawn — the join FROM (Wash, HeatDone) TO Drain drawn as a
# simultaneous convergence (a double bar both legs run into), not as a "↩"
# jump glyph hanging under one of its sources.
sfc_join_drawn() {
  local j; j=$(js '[...doc.querySelectorAll("svg.chart g.trans g.jump title")].map(t => t.textContent).filter(s => s.includes("jumps to Drain")).join(" | ")')
  [[ $j == '""' ]] || { g_err "the join is drawn as a jump glyph (${j//\"/}): HeatDone shows no outgoing edge and there is no convergence bar"; return 1; }
}

# ── B01: the project ────────────────────────────────────────────────────────
PROJ=$HOME/washer
new_project() {
  rm -rf "$PROJ"
  ( cd "$HOME" && naut new washer --template minimal --no-input >/dev/null ) || { g_err "naut new failed"; return 1; }
  git -C "$PROJ" config user.name "Demo"; git -C "$PROJ" config user.email "demo@example.com"
  git -C "$PROJ" add -A; git -C "$PROJ" commit -qm "naut new washer --template minimal"
  point_extension_at "$PROJ" "$PORT"
  ls "$PROJ"
}
row naut-new-minimal PASS new_project
check_after naut-new

ext_open
hide_sidebar
sleep 3

# ── B02: the GVL habit — VAR_GLOBAL in its own .st file ─────────────────────
# A Codesys project's globals live in a GVL; the natural translation is a
# gvl.st holding one VAR_GLOBAL block. It is a "library file" here (no
# PROGRAM), so it composes into the prelude of every task.
paste_gvl_habit() {
  cat >"$PROJ/gvl.st" <<'EOF'
(* GVL — the washer's globals, the Codesys way *)
VAR_GLOBAL
    StartPB    : BOOL;
    StopPB     : BOOL;
    DoorClosed : BOOL;
    LevelPct   : REAL;
    FillValve  : BOOL;
END_VAR
EOF
}
row paste-gvl-habit PASS paste_gvl_habit
row habit-gvl-var-global XFAIL habit_check 'gvl|undeclared|VAR_GLOBAL|program'
# ...and the Codesys GVL of constants
paste_gvl_const_habit() {
  cat >"$PROJ/gvl.st" <<'EOF'
VAR_GLOBAL CONSTANT
    tMaxFill : TIME := T#60S;
    ST_FILL  : INT := 1;
END_VAR
EOF
}
row paste-gvl-constant-habit PASS paste_gvl_const_habit
row habit-gvl-var-global-constant XFAIL habit_check 'VAR_GLOBAL|init'
rm -f "$PROJ/gvl.st"

# the Nautilus way: the tag list in its own file (tag-files:), declared again
# in each POU's VAR_EXTERNAL. The template's program.st and its three tags
# stay until the chart replaces them, so every intermediate checks clean.
paste_tag_file() {
  paste_file tags/washer.yaml
  python3 - "$PROJ/nautilus.yaml" <<'PY'
import sys; p = sys.argv[1]; t = open(p).read()
t = t.replace("\ntasks:", "\n# The Codesys GVL: every global in one file, beside the manifest.\ntag-files:\n  - tags/washer.yaml\n\ntasks:", 1)
open(p, "w").write(t)
PY
  grep -q 'tags/washer.yaml' "$PROJ/nautilus.yaml"
}
row paste-tag-file PASS paste_tag_file
check_after tag-file

# the bench plant, as its own task
paste_sim() {
  paste_file sim.st
  python3 - "$PROJ/nautilus.yaml" <<'PY'
import sys, re; p = sys.argv[1]; t = open(p).read()
t = re.sub(r"(\ntasks:\n  - program: program\.st\n    scan: 100ms\n)", r"\1  - name: sim\n    program: sim.st\n    scan: 100ms\n    dt-tag: SimDtS\n", t, 1)
open(p, "w").write(t)
PY
  grep -q 'program: sim.st' "$PROJ/nautilus.yaml"
}
row paste-sim-task PASS paste_sim
check_after sim

# ── B03: the enum habit, then the library FB ────────────────────────────────
paste_enum_habit() {
  mkdir -p "$PROJ/lib"
  cat >"$PROJ/lib/types.st" <<'EOF'
(* DUT — the wash cycle's states, the Codesys way *)
TYPE E_WashState : (IDLE := 0, FILL := 1, WASH := 2, DRAIN := 3, SPIN := 4, ABORTED := 9);
END_TYPE
EOF
}
row paste-enum-habit PASS paste_enum_habit
row habit-enum-type XFAIL habit_check 'types.st|enum|expected'
rm -f "$PROJ/lib/types.st"

row paste-library-fb PASS paste_file lib/reverser.st
check_after library-fb

# ── B04: a new SFC POU, from an empty file ──────────────────────────────────
# Codesys: Add Object → POU → SFC. Here: a new file (no "new POU" command),
# the diagram's Empty-file banner, "initialize".
: >"$PROJ/washer.sfc"
# an empty chart no task names is one warning, not an error (#179)
check_after empty-sfc
row ed_open_diagram-washer PASS ed_open_diagram washer.sfc
row sfc_init PASS sfc_init
row sfc_rename_step-Start-Idle PASS sfc_rename_step Start Idle
row diagram_zoom PASS diagram_zoom in 1
check_after sfc-init

# the declaration part (Codesys: the POU's declaration editor). The chart's
# "vars" panel declares one at a time: two tags and the FB instance by
# gesture, then a constant the way a Codesys programmer writes one, then the
# rest of the header pasted (the paste rewrites the whole header to the
# reference's, the gestured three included)
row sfc_vars_declare-StartPB PASS sfc_vars_declare StartPB BOOL ext
row sfc_vars_declare-LevelPct PASS sfc_vars_declare LevelPct REAL ext
row sfc_vars_declare-drum PASS sfc_vars_declare drum FB_Reverser local
vars_close
row habit-vars-constant PASS sfc_vars_declare_constant tMaxFill TIME T#60S
row paste-sfc-header PASS sfc_paste_header
# the chart becomes the main task; the template's program.st and its three
# tags go
switch_tasks() {
  rm -f "$PROJ/program.st"
  paste_file nautilus.yaml
  grep -q 'program: washer.sfc' "$PROJ/nautilus.yaml"
}
row paste-manifest-tasks PASS switch_tasks
check_after header

# ── B05: the chart, gestured ────────────────────────────────────────────────
# Main path first (the Codesys habit: draw the sequence, then the branches).
row sfc_add_step-Fill PASS sfc_add_step Idle Fill "StartPB"
row sfc_add_transition_condition PASS sfc_add_transition_condition "Idle->Fill" "StartPB AND DoorClosed"
row sfc_add_transition_new_step-Heat PASS sfc_add_transition_new_step Fill Heat "LevelPct >= FillSP"
row sfc_add_parallel_branch-Wash PASS sfc_add_parallel_branch "Fill->Heat" Wash
row sfc_add_step-HeatDone PASS sfc_add_step Heat HeatDone "TempC >= TempSP"
row sfc_add_transition-Wash-Drain PASS sfc_add_transition Wash Drain "Wash.T >= tWash"
row sfc_join_step-HeatDone PASS sfc_join_step "Wash->Drain" HeatDone
row sfc_add_step-Spin PASS sfc_add_step Drain Spin "LevelPct <= 1.0"
row sfc_add_transition-Spin-Idle PASS sfc_add_transition Spin Idle "Spin.T >= tSpin"
row sfc-join-drawn-as-convergence PASS sfc_join_drawn
check_after main-path
# then the abort branch and the supervision exits
row sfc_add_alt_branch-Fill-Aborted PASS sfc_add_alt_branch Fill Aborted "StopPB OR NOT DoorClosed OR FaultCode <> 0"
row sfc_add_alt_branch-Drain-Aborted PASS sfc_add_alt_branch Drain Aborted "FaultCode <> 0"
row sfc_add_transition-Aborted-Idle PASS sfc_add_transition Aborted Idle "ResetPB AND LevelPct <= 1.0"
check_after branches
# Priority: the abort must win over a full drum on the same scan. "+ alt
# branch" lands last (lowest priority, as drawn rightmost); Alt+← moves it
# ahead of the main path (#181) — the text paste is no longer needed.
row sfc_reorder_branch-Fill-Aborted PASS sfc_reorder_branch "Fill->Aborted" left
row habit-abort-priority PASS sfc_abort_first
check_after priority

# Habits on the chart itself
row habit-keyboard-nav PASS sfc_keynav Fill Down "Fill->Aborted"
row habit-transition-name PASS sfc_transition_name_field Fill

# ── B06: actions ────────────────────────────────────────────────────────────
# The ACTION-body gap (04-sfc): an association to a not-yet-existing ACTION,
# then a double-click on it, hoping for a body editor.
row sfc_add_action-Heat-HeatCtl PASS sfc_add_action Heat N HeatCtl
row habit-create-action-body PASS sfc_body_editor_opens Heat HeatCtl
# ...and the body typed there writes ACTION HeatCtl (#182)
row sfc_create_action-HeatCtl PASS sfc_create_action Heat HeatCtl "Heater := Heat.X AND TempC < TempSP;"
check_after action-gap
# the other eight ACTION blocks are pasted (SpinCtl as a stub, typed below)
row paste-sfc-actions PASS sfc_paste_actions SpinCtl
check_after actions

# every step's associations, in the reference's order; the timed qualifiers
# a Codesys programmer types first are tried in place and retyped
row sfc_add_action-Idle-R-DoorLock PASS sfc_add_action Idle R DoorLock
row sfc_add_action-Idle-R-AlarmLamp PASS sfc_add_action Idle R AlarmLamp
row sfc_add_action-Idle-N-TrackState PASS sfc_add_action Idle N TrackState
row sfc_add_action-Fill-S-DoorLock PASS sfc_add_action Fill S DoorLock
row sfc_add_action-Fill-N-FillValve PASS sfc_add_action Fill N FillValve
row sfc_add_action-Fill-P1-CountCycle PASS sfc_add_action Fill P1 CountCycle
# Codesys qualifier syntax: the time typed after the target, no parens (#183)
row habit-assoc-time-syntax PASS sfc_try_assoc Fill "D Detergent T#3S"
row habit-D-qualifier XFAIL habit_check 'timed qualifier'
row chart-shows-timed-qualifier-error PASS sfc_check_says Fill 'timed qualifier|not implemented'
row sfc_edit_action-Fill-N-Dose PASS sfc_edit_action Fill Detergent "N Dose"
row sfc_add_action-Fill-N-Supervise PASS sfc_add_action Fill N Supervise
row sfc_add_action-Fill-N-TrackState PASS sfc_add_action Fill N TrackState
row sfc_add_action-Heat-N-TrackState PASS sfc_add_action Heat N TrackState
row sfc_add_action-Wash-N-DrumCtl PASS sfc_add_action Wash N DrumCtl
row sfc_add_action-Wash-N-TrackState PASS sfc_add_action Wash N TrackState
row sfc_add_action-HeatDone-N-TrackState PASS sfc_add_action HeatDone N TrackState
row sfc_add_action-Drain-N-DrainPump PASS sfc_add_action Drain N DrainPump
row sfc_add_action-Drain-SD-AlarmLamp PASS sfc_add_action Drain SD AlarmLamp T#45S
row habit-SD-qualifier XFAIL habit_check 'timed qualifier'
row sfc_edit_action-Drain-N-Supervise PASS sfc_edit_action Drain AlarmLamp "N Supervise"
row sfc_add_action-Drain-N-TrackState PASS sfc_add_action Drain N TrackState
row sfc_add_action-Spin-L-SpinMotor PASS sfc_add_action Spin L SpinMotor T#10S
row habit-L-qualifier XFAIL habit_check 'timed qualifier'
row sfc_edit_action-Spin-N-SpinCtl PASS sfc_edit_action Spin SpinMotor "N SpinCtl"
row sfc_add_action-Spin-N-DrainPump PASS sfc_add_action Spin N DrainPump
row sfc_add_action-Spin-N-TrackState PASS sfc_add_action Spin N TrackState
row sfc_add_action-Aborted-S-AlarmLamp PASS sfc_add_action Aborted S AlarmLamp
row sfc_add_action-Aborted-N-DrainPump PASS sfc_add_action Aborted N DrainPump
row sfc_add_action-Aborted-P1-RecordFault PASS sfc_add_action Aborted P1 RecordFault
row sfc_add_action-Aborted-P0-ClearFault PASS sfc_add_action Aborted P0 ClearFault
row sfc_add_action-Aborted-N-TrackState PASS sfc_add_action Aborted N TrackState
check_after associations
# the one ACTION body typed in the chart's body editor (L's replacement)
row sfc_edit_action_body-SpinCtl PASS sfc_edit_action_body Spin SpinCtl "SpinMotor := Spin.X AND Spin.T < tSpin;"
check_after spin-body

# ── B07: the tests, and the verdict ─────────────────────────────────────────
row paste-test-file PASS paste_file washer_test.yaml
fit_and_snap() { diagram_zoom fit; sleep 0.8; }
row diagram_zoom-fit PASS fit_and_snap
check_after final

mkdir -p "$OUT_DIR/built"
naut_test() {
  local out rc; out=$(cd "$PROJ" && naut test -v . 2>&1); rc=$?
  printf '%s\n' "$out" >"$OUT_DIR/built/naut-test.txt"
  tail -1 <<<"$out"; return $rc
}
row naut-test PASS naut_test
compare() {
  g_save
  (cd "$HOME/build" && python3 sfc_compare.py "$PROJ/washer.sfc" "$REF/washer.sfc") | tee "$OUT_DIR/built/compare.txt"
  return "${PIPESTATUS[0]}"
}
row compare-reference PASS compare
# ...and with the associations in the order they were typed
compare_order() {
  (cd "$HOME/build" && python3 sfc_compare.py --assoc-order "$PROJ/washer.sfc" "$REF/washer.sfc") | tee "$OUT_DIR/built/compare-order.txt"
  return "${PIPESTATUS[0]}"
}
row habit-assoc-typed-order PASS compare_order

(cd "$PROJ" && naut check . >"$OUT_DIR/built/naut-check.txt" 2>&1)
tar -C "$HOME" --exclude=.git -cf - washer | tar -C "$OUT_DIR/built" -xf -
cp "$OUT_DIR/versions.txt" "$OUT_DIR/built/MANIFEST.txt"

echo
awk -F"\t" '{ printf "%-3s %-34s %-6s %s\n", $1, $2, $3, $4 }' "$TSV"
