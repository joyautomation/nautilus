#!/usr/bin/env bash
# build.sh — the Studio 5000-shaped build: a two-conveyor program the way an
# Allen-Bradley Logix programmer writes one on day one, gesture-built in the
# real VS Code ladder editor from `naut new --template minimal --language
# ld`, and logged as a dogfood run (PLAN.md: the beats, gestured vs pasted;
# FINDINGS.md: the friction, and the Studio 5000 habits with no equivalent).
#
#     tools/rig/builds/logix-shaped/build.sh               # build THIS checkout, run
#     NAUT=… VSIX=… RIG_NAME=nautilus-build-logix G_PACE=fast RIG_CLIPS=1 \
#       tools/rig/builds/logix-shaped/build.sh
#
# On the host it does what selftest.sh does: brings the rig container up
# (lib/container.sh), installs naut + the VSIX, pushes lib.sh, the verbs (as
# ~/fixtures), this build's reference/ (as ~/fixtures/logix-shaped-ref),
# compare.py and itself, runs itself in there with RIG_IN_CONTAINER=1, and
# pulls ~/out back to tools/rig/out/builds/logix-shaped/ (RIG_OUT= moves
# tools/rig/out):
#
#   build.tsv              <n> <beat/verb> PASS|FAIL|XFAIL|XPASS|PASTE <detail>
#   NN-<row>.png           the window right after each row — READ THEM
#   NN-<row>.mp4           the row as it happened (RIG_CLIPS=0: none)
#   clips.html, clips.md   the review index
#   checks/NN-<beat>.txt   `naut check` at every beat boundary
#   built/                 the gesture-built project as saved, `naut test`
#                          on it, and compare.py's verdict against reference/
#
# Exit status: the number of FAIL rows (capped at 100); 2 if it never got as
# far as a table. XFAIL rows are gestures (or checks) the product does not
# support yet — each one is a finding in FINDINGS.md; XPASS means it started
# working, so go and look.

if [[ -z ${RIG_IN_CONTAINER:-} ]]; then
  set -uo pipefail
  HERE=$(cd "$(dirname "$0")" && pwd)
  RIG_DIR=$(cd "$HERE/../.." && pwd)
  export RIG_NAME=${RIG_NAME:-nautilus-build-logix}
  source "$RIG_DIR/lib/container.sh"
  source "$RIG_DIR/lib/clips-index.sh"
  OUT=${RIG_OUT:-$RIG_DIR/out}/builds/logix-shaped

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
  rig_push_tree "$HERE/reference" "/home/$RIG_USER/fixtures/logix-shaped-ref"
  rig_push "$HERE/compare.py" "/home/$RIG_USER/fixtures/compare.py"
  rig_push "$HERE/build.sh" "/home/$RIG_USER/build.sh"
  _rig rm -f /tmp/ext.vsix; rig_push "$VSIX" /tmp/ext.vsix; _rig chmod 644 /tmp/ext.vsix
  _rig chown -R "$RIG_USER:$RIG_USER" "/home/$RIG_USER"
  echo "▸ installing $(basename "$VSIX")"
  rig_run "code --install-extension /tmp/ext.vsix --force 2>&1 | tail -1"

  echo "▸ build (G_PACE=${G_PACE:-human}${FRAME:+ $FRAME})"
  # NB: nothing in this command line may contain "vscode-rec" (lib.sh's
  # launch_vscode pkills by that string).
  rig_run "rm -rf ~/out; mkdir -p ~/out && cd ~ && DISPLAY=$RIG_DISPLAY $FRAME RIG_IN_CONTAINER=1 RIG_CLIPS=${RIG_CLIPS:-1} G_PACE=${G_PACE:-human} timeout 5400 bash ~/build.sh" \
    || echo "  (build exited non-zero)"

  PULL=$(mktemp -d)
  incus file pull -r "$RIG_NAME/home/$RIG_USER/out" "$PULL/" >/dev/null 2>&1
  rm -rf "$OUT"; mkdir -p "$(dirname "$OUT")"; mv "$PULL/out" "$OUT" 2>/dev/null; rm -rf "$PULL"
  TSV=$OUT/build.tsv
  [[ -s $TSV ]] || { echo "no results — the build never got as far as a table" >&2; exit 2; }
  if [[ ${RIG_CLIPS:-1} != 0 ]]; then
    while IFS=$'\t' read -r n name verdict detail; do
      base="$n-$name" clip="" png=""
      [[ -f $OUT/$base.mp4 ]] && clip=$base.mp4
      [[ -f $OUT/$base.png ]] && png=$base.png
      printf '%s\t%s\t%s\t%s\t%s\n' "$n $name" "$verdict" "$detail" "$clip" "$png"
    done <"$TSV" | _clips_write "$OUT" "logix-shaped build — $(cat "$(dirname "$NAUT")/SHA" 2>/dev/null || echo unknown)"
  fi
  echo
  for v in PASS FAIL XFAIL XPASS PASTE; do printf '%s %d  ' "$v" "$(awk -F'\t' -v v=$v '$3==v' "$TSV" | wc -l)"; done; echo
  fails=$(awk -F'\t' '$3=="FAIL" || $3=="XPASS"' "$TSV" | wc -l)
  exit $(( fails > 100 ? 100 : fails ))
fi

# ── in the container ────────────────────────────────────────────────────────
set -uo pipefail
source "$HOME/fixtures/prep.sh"
source "$HOME/fixtures/gestures.sh"
trap 'clip_stop; cleanup_capture' EXIT
printf 'CAP_W=%s\nCAP_H=%s\nREC_ZOOM=%s\nREC_FONT_SIZE=%s\nG_PACE=%s\n' \
  "$CAP_W" "$CAP_H" "$REC_ZOOM" "$REC_FONT_SIZE" "$G_PACE" >"$OUT_DIR/frame.env"
REF=$FIX/logix-shaped-ref
mkdir -p "$OUT_DIR/checks"

TSV=$OUT_DIR/build.tsv
: >"$TSV"
N=0
_record() { # <name> <verdict> <detail> <base>
  printf '%02d\t%s\t%s\t%s\n' "$N" "$1" "$2" "${3:-ok}" >>"$TSV"
  case $2 in
    PASS|XFAIL|PASTE) printf '  \033[32m%-5s\033[0m %02d %s %s\n' "$2" "$N" "$1" "${3:+— $3}" ;;
    *)                printf '  \033[31m%-5s\033[0m %02d %s %s\n' "$2" "$N" "$1" "${3:+— $3}" ;;
  esac
}
# row <name> <expect PASS|XFAIL> <verb> [args…] — run a verb, snap, record.
# NOT in a subshell: verbs set state later verbs read (G_FILE).
row() {
  local name=$1 expect=$2 rc verdict err base; shift 2
  N=$((N + 1))
  base=$(printf '%02d-%s' "$N" "$name")
  clip_start "$base"
  "$@" >/dev/null 2>"$HOME/.row-err"; rc=$?
  err=$(sed 's/\x1b\[[0-9;]*m//g' "$HOME/.row-err" | tr '\n' ' ' | sed 's/  */ /g; s/^ //; s/ $//')
  if [[ $expect == XFAIL ]]; then (( rc == 0 )) && verdict=XPASS || verdict=XFAIL
  else (( rc == 0 )) && verdict=PASS || verdict=FAIL; fi
  park "$((CAP_W - 30))" "$((CAP_H - 60))" 0.6
  snap "$base" >/dev/null
  clip_stop
  _record "$name" "$verdict" "${err:-ok}"
  # a failed or refused gesture can leave a toast or an open editor behind
  if [[ $verdict != PASS ]]; then g_key Escape; vs_cmd "Notifications: Clear All Notifications" 0.6; fi
}
# paste_row <name> <function> [args…] — text no gesture can author, written
# straight to disk (saving the open diagram first, then reverting it to
# what is on disk), recorded as a PASTE row.
paste_row() {
  local name=$1 rc base; shift
  N=$((N + 1))
  base=$(printf '%02d-%s' "$N" "$name")
  [[ -n ${G_FILE:-} ]] && g_save
  "$@" 2>"$HOME/.row-err"; rc=$?
  [[ -n ${G_FILE:-} ]] && { vs_cmd "Revert File" 2; sleep 1; }
  snap "$base" >/dev/null
  _record "$name" "$( (( rc == 0 )) && echo PASTE || echo FAIL)" "$(tr '\n' ' ' <"$HOME/.row-err")"
}
# chk <beat> [XFAIL] — `naut check` on the project at a beat boundary.
# XFAIL: the beat is known to leave it unclean (the finding says why).
chk() {
  local name="check:$1" out rc verdict
  N=$((N + 1))
  [[ -n ${G_FILE:-} ]] && g_save
  out=$(cd "$PROJ" && naut check . 2>&1); rc=$?
  printf '%s\n' "$out" >"$OUT_DIR/checks/$(printf '%02d' "$N")-$1.txt"
  if [[ ${2:-} == XFAIL ]]; then (( rc == 0 )) && verdict=XPASS || verdict=XFAIL
  else (( rc == 0 )) && verdict=PASS || verdict=FAIL; fi
  _record "$name" "$verdict" "$(grep -v '^$' <<<"$out" | grep -v 'warning:' | tail -2 | tr '\n' ' ')"
}

# ── build-local verbs ───────────────────────────────────────────────────────
# Same contract as the verbs in verbs/gestures.sh: save, then read the file
# back. The build's generally useful verbs moved there (ld_delete_rung,
# ld_vars_delete, ld_vars_declare, ld_rung_comment, ld_edge_retag,
# ld_edge_drawn, ld_copy_rung, ld_vars_lists_instance,
# ld_vars_escape_closes, ld_declare_offer_type); what stays are the probes
# of habits nautilus does not have yet.

# lx_desc_on_element <rung> <tag> <desc> — Studio 5000 draws a tag's
# description above the instruction. Does the ladder element show (or
# title) the nautilus.yaml desc anywhere? XFAIL until it does.
lx_desc_on_element() {
  local rung=$1 tag=$2 desc=$3
  js_true "($(ld_node_el "$rung" '*' "$tag"))?.closest('g.node')?.textContent.includes($(_q "$desc"))" \
    || { g_err "the $tag element on $rung shows no description (want \"$desc\")"; return 1; }
}
# lx_real_coil_checks <rung> <REAL tag> — the MOV/CPT habit: a coil that
# writes a REAL. The gesture itself lands (a retag is text); the verdict is
# `naut check` on the result. XFAIL: coils assign BOOL only.
lx_real_coil_checks() {
  local rung=$1 tag=$2
  ld_add_coil "$rung" "$tag" || return 1
  g_save
  (cd "$PROJ" && naut check . >"$HOME/.real-coil" 2>&1) || { g_err "naut check: $(grep -v warning "$HOME/.real-coil" | head -1)"; return 1; }
}

# ── pastes ──────────────────────────────────────────────────────────────────
_paste_tags() {
  mkdir -p "$PROJ/tags"
  cp "$REF/tags/io.yaml" "$REF/tags/plant.yaml" "$PROJ/tags/"
  cp "$REF/nautilus.yaml" "$PROJ/nautilus.yaml"
}
# The FB itself: its doc comment, FUNCTION_BLOCK and an empty LD body — no
# gesture creates a POU yet (#179). Its pins are gestured (the variables
# panel and the declare offer, #214); tFail needs no declaration: the FB
# picker inserts `tFail:TON(…)`, which declares it.
_paste_fb_shell() {
  mkdir -p "$PROJ/lib"
  python3 - "$REF/lib/motor.ld" "$PROJ/lib/motor.ld" <<'PY'
import sys
src = open(sys.argv[1]).read()
i = src.index("FUNCTION_BLOCK MotorStarter\n") + len("FUNCTION_BLOCK MotorStarter\n")
open(sys.argv[2], "w").write(src[:i] + "LD\nEND_LD\nEND_FUNCTION_BLOCK\n")
PY
}
_paste_st_fb() { cp "$REF/lib/speed.st" "$PROJ/lib/speed.st"; }
_paste_tests() { cp "$REF/conveyor_test.yaml" "$PROJ/conveyor_test.yaml"; }

# ════════════════════════════════════════════════════════════════════════════
# B0 — the project: `naut new --template minimal --language ld` (what an AB
# programmer picks: ladder), committed so the diff views have a HEAD.
PROJ=$HOME/conveyor
rm -rf "$PROJ"
(cd "$HOME" && naut new conveyor --template minimal --language ld --no-input >/dev/null) || die "naut new failed"
git -C "$PROJ" init -q 2>/dev/null
git -C "$PROJ" config user.name "Demo"; git -C "$PROJ" config user.email "demo@example.com"
git -C "$PROJ" add -A; git -C "$PROJ" commit -qm "scaffold: naut new conveyor --template minimal --language ld"
point_extension_at "$PROJ" "$PORT"
chk 00-scaffold

ext_open
hide_sidebar
sleep 3

# B1 — clear the template: its one rung and its three tags' declarations.
row ed_open_diagram-program PASS ed_open_diagram program.ld
row ld_delete_rung-high PASS ld_delete_rung high
for v in Sensor Setpoint Alarm; do row "ld_vars_delete-$v" PASS ld_vars_delete "$v"; done

# B2 — the tag database (YAML: no tag-grid gesture) and the manifest.
paste_row tags-yaml _paste_tags
chk 02-tags

# B3 — the AOI: MotorStarter as a ladder FUNCTION_BLOCK in lib/motor.ld.
paste_row motor-fb-shell _paste_fb_shell
chk 03-fb-shell
row ed_open_diagram-motor PASS ed_open_diagram lib/motor.ld
#   the AOI's Parameters tab: the inputs, in order, from the variables
#   panel (the first BOOL input, Start, is the pin the caller's rung powers)
for p in Start Stop Permit Aux Fault Reset; do row "ld_vars_declare-$p" PASS ld_vars_declare "$p" BOOL VAR_INPUT MotorStarter; done
chk 03-fb-inputs
#   run: [ Start | Run ] /Stop Permit /Faulted ( Run ) — the seal-in; the
#   outputs are declared from the amber offer as the rungs name them
row ld_add_rung-run PASS ld_add_rung run
row ld_rung_comment-run PASS ld_rung_comment run "seal-in: Start energizes Run, Run holds itself in"
row ld_add_contact-Start PASS ld_add_contact run Start
row ld_add_branch-sealin PASS ld_add_branch run Start Run
row ld_add_contact-Stop-nc PASS ld_add_contact run Stop nc
row ld_add_contact-Permit PASS ld_add_contact run Permit
row ld_add_contact-Faulted-nc PASS ld_add_contact run Faulted nc
row ld_add_coil-Run PASS ld_add_coil run Run
row ld_declare-Run PASS ld_declare Run VAR_OUTPUT
row ld_declare-Faulted PASS ld_declare Faulted VAR_OUTPUT
chk 03-run
#   fts: Run /Aux tFail:TON(PT := T#3S) ( S FailToStart )
row ld_add_rung-fts PASS ld_add_rung fts
row ld_rung_comment-fts PASS ld_rung_comment fts "commanded but the aux never made: fail to start"
row ld_add_contact-Run PASS ld_add_contact fts Run
row ld_add_contact-Aux-nc PASS ld_add_contact fts Aux nc
row ld_add_block-TON PASS ld_add_block fts TON tFail "PT := T#3S"
row ld_add_coil-FailToStart-set PASS ld_add_coil fts FailToStart set
row ld_declare-FailToStart PASS ld_declare FailToStart VAR_OUTPUT
chk 03-fts
#   flt: [ Fault | FailToStart ] ( S Faulted )
row ld_add_rung-flt PASS ld_add_rung flt
row ld_add_contact-Fault PASS ld_add_contact flt Fault
row ld_add_branch-flt PASS ld_add_branch flt Fault FailToStart
row ld_add_coil-Faulted-set PASS ld_add_coil flt Faulted set
chk 03-flt
#   rst: Reset /Fault ( R Faulted ) ( R FailToStart )
row ld_add_rung-rst PASS ld_add_rung rst
row ld_rung_comment-rst PASS ld_rung_comment rst "reset only once the overload has cleared"
row ld_add_contact-Reset PASS ld_add_contact rst Reset
row ld_add_contact-Fault-nc PASS ld_add_contact rst Fault nc
row ld_add_coil-Faulted-reset PASS ld_add_coil rst Faulted reset
row ld_add_coil-FailToStart-reset PASS ld_add_coil rst FailToStart reset
chk 03-rst

# B4 — the CPT rung's math, as an ST block with EN/ENO (pasted: ST is typed).
paste_row speed-st-fb _paste_st_fb
chk 04-st-fb

# B5 — the main routine (program.ld).
row ed_open_diagram-program-2 PASS ed_open_diagram program.ld
#   m1perm: /EStop AirOk ( M1_Permit ) — built AirOk first, then the E-stop
#   dragged in front of it.
row ld_add_rung-m1perm PASS ld_add_rung m1perm
row ld_add_contact-AirOk PASS ld_add_contact m1perm AirOk
row ld_declare-AirOk PASS ld_declare AirOk VAR_EXTERNAL
row ld_add_contact-EStop-nc PASS ld_add_contact m1perm EStop nc
row ld_declare-EStop PASS ld_declare EStop VAR_EXTERNAL
row ld_move_element-EStop PASS ld_move_element m1perm EStop m1perm AirOk
row ld_add_coil-M1_Permit PASS ld_add_coil m1perm M1_Permit
row ld_declare-M1_Permit PASS ld_declare M1_Permit VAR_EXTERNAL
chk 05-m1perm
#   m1: +M1_StartPB m1:MotorStarter(…) ( M1_Run ) — the JSR habit, as a
#   call; the ONS typed as +Tag on the start button's contact
row ld_add_rung-m1 PASS ld_add_rung m1
row ld_add_contact-M1_StartPB PASS ld_add_contact m1 M1_StartPB
row ld_declare-M1_StartPB PASS ld_declare M1_StartPB VAR_EXTERNAL
row ld_edge_retag-M1_StartPB PASS ld_edge_retag m1 M1_StartPB
row ld_edge_drawn-M1_StartPB PASS ld_edge_drawn m1 M1_StartPB
row lx_desc_on_element-M1_StartPB XFAIL lx_desc_on_element m1 M1_StartPB "M1 start pushbutton"
row ld_add_block-MotorStarter-m1 PASS ld_add_block m1 MotorStarter m1 \
  "Stop := M1_StopPB, Permit := M1_Permit, Aux := M1_Aux, Fault := M1_OL, Reset := FaultReset, FailToStart => Alm_M1FTS"
for v in M1_StopPB M1_Aux M1_OL FaultReset Alm_M1FTS; do row "ld_declare-$v" PASS ld_declare "$v" VAR_EXTERNAL; done
row ld_vars_lists_instance-m1 PASS ld_vars_lists_instance m1 MotorStarter
row ld_vars_escape_closes PASS ld_vars_escape_closes
row ld_add_coil-M1_Run PASS ld_add_coil m1 M1_Run
row ld_declare-M1_Run PASS ld_declare M1_Run VAR_EXTERNAL
chk 05-m1
#   m2perm: /EStop [ M1_Run [ M1_Aux | M1_AuxBypass ] | Maint ] ( M2_Permit )
row ld_add_rung-m2perm PASS ld_add_rung m2perm
row ld_add_contact-EStop-nc-2 PASS ld_add_contact m2perm EStop nc
row ld_add_contact-M1_Run PASS ld_add_contact m2perm M1_Run
row ld_add_branch-Maint PASS ld_add_branch m2perm M1_Run Maint
row ld_declare-Maint PASS ld_declare Maint VAR_EXTERNAL
row ld_add_contact_after-M1_Aux PASS ld_add_contact_after m2perm contact M1_Run M1_Aux
row ld_add_branch-nested PASS ld_add_branch m2perm M1_Aux M1_AuxBypass
row ld_declare-M1_AuxBypass PASS ld_declare M1_AuxBypass VAR_EXTERNAL
row ld_add_coil-M2_Permit PASS ld_add_coil m2perm M2_Permit
row ld_declare-M2_Permit PASS ld_declare M2_Permit VAR_EXTERNAL
row ld_assert_rung-m2perm PASS ld_assert_rung m2perm '/EStop *\[ *M1_Run *\[ *M1_Aux *\| *M1_AuxBypass *\] *\| *Maint *\] *\( *M2_Permit *\)'
chk 05-m2perm
#   m2: rung m1 copied below m2perm (select m1, Ctrl+C, select m2perm,
#   Ctrl+V — the copy is m2, its instance m2), then M1 → M2
row ld_copy_rung-m1 PASS ld_copy_rung m1 m2perm m2
row ld_retag-M2_StartPB PASS ld_retag m2 edge M1_StartPB M2_StartPB
row ld_declare-M2_StartPB PASS ld_declare M2_StartPB VAR_EXTERNAL
row ld_edit_fb_args-m2 PASS ld_edit_fb_args m2 m2 \
  "Stop := M2_StopPB, Permit := M2_Permit, Aux := M2_Aux, Fault := M2_OL, Reset := FaultReset, FailToStart => Alm_M2FTS"
for v in M2_StopPB M2_Aux M2_OL Alm_M2FTS; do row "ld_declare-$v" PASS ld_declare "$v" VAR_EXTERNAL; done
row ld_retag-M2_Run PASS ld_retag m2 coil M1_Run M2_Run
row ld_declare-M2_Run PASS ld_declare M2_Run VAR_EXTERNAL
chk 05-m2
#   starts: M1_Run cStarts:CTU(R := CountReset, PV := 9999, CV => M1_Starts)
row ld_add_rung-starts PASS ld_add_rung starts
row ld_add_contact-M1_Run-2 PASS ld_add_contact starts M1_Run
row ld_add_block-CTU PASS ld_add_block starts CTU "" "R := CountReset, PV := 9999, CV => M1_Starts"
row ld_rename_block-cStarts PASS ld_rename_block starts c1 cStarts
row ld_declare-CountReset PASS ld_declare CountReset VAR_EXTERNAL
# the amber offer types the counter's tag from the CV pin it captures (INT),
# not from its integer seed (the manifest's REAL)
row ld_declare_offer_type-M1_Starts PASS ld_declare_offer_type M1_Starts INT
row ld_declare-M1_Starts PASS ld_declare M1_Starts VAR_EXTERNAL
row ld_delete_last_coil-starts PASS ld_delete_last_coil starts
chk 05-starts
#   speed: M2_Run spd:SpeedCalc(…, Hz => M2_SpeedRef) — the CPT/MOV rung.
#   First the habit: a coil that writes the REAL (XFAIL: coils are BOOL).
row ld_add_rung-speed PASS ld_add_rung speed
row ld_add_contact-M2_Run PASS ld_add_contact speed M2_Run
row ld_add_block-SpeedCalc PASS ld_add_block speed SpeedCalc spd \
  "Pct := M2_SpeedPct, MinHz := MinHz, MaxHz := MaxHz, Hz => M2_SpeedRef"
for v in M2_SpeedPct MinHz MaxHz M2_SpeedRef; do row "ld_declare-$v" PASS ld_declare "$v" VAR_EXTERNAL; done
row lx_real_coil_checks-M2_SpeedRef XFAIL lx_real_coil_checks speed M2_SpeedRef
row ld_delete_last_coil-speed PASS ld_delete_last_coil speed M2_SpeedRef
chk 05-speed
#   the alarm word: one BOOL per bit, and the summary
row ld_add_rung-almestop PASS ld_add_rung almestop
row ld_add_contact-EStop-3 PASS ld_add_contact almestop EStop
row ld_add_coil-Alm_EStop PASS ld_add_coil almestop Alm_EStop
row ld_declare-Alm_EStop PASS ld_declare Alm_EStop VAR_EXTERNAL
row ld_add_rung-almm1 PASS ld_add_rung almm1
row ld_add_contact-m1.Faulted PASS ld_add_contact almm1 m1.Faulted
row ld_add_coil-Alm_M1Fault PASS ld_add_coil almm1 Alm_M1Fault
row ld_declare-Alm_M1Fault PASS ld_declare Alm_M1Fault VAR_EXTERNAL
row ld_add_rung-almm2 PASS ld_add_rung almm2
row ld_add_contact-m2.Faulted PASS ld_add_contact almm2 m2.Faulted
row ld_add_coil-Alm_M2Fault PASS ld_add_coil almm2 Alm_M2Fault
row ld_declare-Alm_M2Fault PASS ld_declare Alm_M2Fault VAR_EXTERNAL
row ld_add_rung-almany PASS ld_add_rung almany
row ld_add_contact-Alm_EStop PASS ld_add_contact almany Alm_EStop
row ld_add_branch-almany PASS ld_add_branch almany Alm_EStop Alm_M1Fault
row ld_add_leg-almany PASS ld_add_leg almany
row ld_retag_placeholder-Alm_M2Fault PASS ld_retag_placeholder almany contact Alm_M2Fault
row ld_add_coil-Alm_Any PASS ld_add_coil almany Alm_Any
row ld_declare-Alm_Any PASS ld_declare Alm_Any VAR_EXTERNAL
chk 05-alarms
row diagram_zoom-fit PASS diagram_zoom fit

# B6 — the acceptance tests (YAML), and the verdicts.
paste_row tests-yaml _paste_tests
chk 06-final
N=$((N + 1))
mkdir -p "$OUT_DIR/built"
(cd "$PROJ" && naut test . >"$OUT_DIR/built/naut-test.txt" 2>&1); rc=$?
_record "naut test" "$( (( rc == 0 )) && echo PASS || echo FAIL)" "$(tail -1 "$OUT_DIR/built/naut-test.txt")"
N=$((N + 1))
for f in program.ld lib/motor.ld lib/speed.st nautilus.yaml tags/io.yaml tags/plant.yaml conveyor_test.yaml; do
  mkdir -p "$OUT_DIR/built/$(dirname "$f")"; cp "$PROJ/$f" "$OUT_DIR/built/$f"
done
python3 "$FIX/compare.py" "$OUT_DIR/built" "$REF" >"$OUT_DIR/built/compare.txt" 2>&1; rc=$?
_record "matches reference" "$( (( rc == 0 )) && echo PASS || echo FAIL)" "$(tail -1 "$OUT_DIR/built/compare.txt")"
printf '%s · %s\n' "$(naut version 2>&1 | head -1)" "$(code --list-extensions --show-versions 2>/dev/null | grep -i '^joyauto.vscode-iec@')" >"$OUT_DIR/built/MANIFEST.txt"

echo
awk -F"\t" '{ printf "%-3s %-36s %-6s %s\n", $1, $2, $3, $4 }' "$TSV"
