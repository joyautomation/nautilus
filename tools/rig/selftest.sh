#!/usr/bin/env bash
# selftest.sh — every verb in verbs/gestures.sh, once, on a fresh scaffold,
# each read back from the saved file. Not a take: it proves the vocabulary
# still works against the extension build under test before a beat or a
# check relies on it.
#
#     tools/rig/selftest.sh                       # build THIS checkout, run
#     NAUT=…/naut VSIX=…/vscode-iec.vsix tools/rig/selftest.sh
#     G_PACE=fast tools/rig/selftest.sh           # teleport the pointer (nightly)
#     RIG_CLIPS=0 tools/rig/selftest.sh           # no per-verb clips (quicker)
#     RIG_NAME=nautilus-verbs-$(hostname -s) RIG_KEEP=1 tools/rig/selftest.sh
#
# On the host it brings up the rig container (lib/container.sh), puts naut on
# PATH and the VSIX in, pushes lib.sh, the verbs (as ~/fixtures) and itself,
# runs itself in there with RIG_IN_CONTAINER=1, and pulls ~/out back to
# tools/rig/out/selftest/ (RIG_OUT= moves tools/rig/out):
#
#   verbs-selftest.tsv     <n> <verb> PASS|FAIL|XFAIL|XPASS <detail>
#   verbs-NN-<verb>.png    the window right after each verb — READ THEM: a
#                          verb that passes by text on a wrong picture fails
#   verbs-NN-<verb>.mp4    the verb as it happened (RIG_CLIPS=0: no clips)
#   clips.html, clips.md   the review index: verb · verdict · clip · PNG
#   verbs-files/           the edited files, as saved, and `naut check` on them
#
# Exit status: the number of FAIL rows (capped at 100); 2 if it never got as
# far as a table.
#
# XFAIL rows exercise a gesture the extension does not support yet (see the
# content repo's ex01-lift-station/GESTURE-FINDINGS.md); XPASS means it
# started working — go and look.
#
# G_PACE=fast for a quicker run; the default (human) is the pace the content
# repo's build beats film at, so that is what a pre-filming self-test proves.

if [[ -z ${RIG_IN_CONTAINER:-} ]]; then
  set -uo pipefail
  RIG_DIR=$(cd "$(dirname "$0")" && pwd)
  export RIG_NAME=${RIG_NAME:-nautilus-verbs-rig}
  source "$RIG_DIR/lib/container.sh"
  source "$RIG_DIR/lib/clips-index.sh"
  OUT=${RIG_OUT:-$RIG_DIR/out}/selftest

  if [[ -z ${NAUT:-} || -z ${VSIX:-} ]]; then
    echo "▸ building the candidate (NAUT/VSIX not given)"
    eval "$("$RIG_DIR/smoke/build.sh")" || exit 2
  fi
  [[ -x $NAUT && -f $VSIX ]] || { echo "need NAUT=<naut binary> and VSIX=<vscode-iec.vsix>" >&2; exit 2; }

  echo "▸ $RIG_NAME: fresh container"
  trap '[[ -n ${RIG_KEEP:-} ]] || rig_down' EXIT
  rig_up || exit 2
  # naut in /usr/local/bin (with `nautilus` beside it), as record-vscode.sh
  # does it: prep.sh wants it on PATH, and so does the extension.
  incus file push "$NAUT" "$RIG_NAME/usr/local/bin/.naut.new" >/dev/null
  _rig chmod +x /usr/local/bin/.naut.new
  _rig mv -f /usr/local/bin/.naut.new /usr/local/bin/naut
  _rig ln -sf /usr/local/bin/naut /usr/local/bin/nautilus
  rig_push "$RIG_DIR/lib/lib.sh" "/home/$RIG_USER/lib.sh"
  rig_push_tree "$RIG_DIR/verbs" "/home/$RIG_USER/fixtures"
  rig_push "$RIG_DIR/selftest.sh" "/home/$RIG_USER/selftest.sh"
  _rig rm -f /tmp/ext.vsix; rig_push "$VSIX" /tmp/ext.vsix; _rig chmod 644 /tmp/ext.vsix
  _rig chown -R "$RIG_USER:$RIG_USER" "/home/$RIG_USER"
  echo "▸ installing $(basename "$VSIX")"
  rig_run "code --install-extension /tmp/ext.vsix --force 2>&1 | tail -1"

  echo "▸ self-test (G_PACE=${G_PACE:-human})"
  # NB: nothing in this command line may contain "vscode-rec" — lib.sh's
  # launch_vscode pkills by that string and would kill this shell.
  rig_run "rm -rf ~/out; mkdir -p ~/out && cd ~ && DISPLAY=$RIG_DISPLAY RIG_IN_CONTAINER=1 RIG_CLIPS=${RIG_CLIPS:-1} G_PACE=${G_PACE:-human} timeout 3600 bash ~/selftest.sh" \
    || echo "  (selftest exited non-zero)"

  PULL=$(mktemp -d)
  incus file pull -r "$RIG_NAME/home/$RIG_USER/out" "$PULL/" >/dev/null 2>&1
  rm -rf "$OUT"; mkdir -p "$(dirname "$OUT")"; mv "$PULL/out" "$OUT" 2>/dev/null; rm -rf "$PULL"
  TSV=$OUT/verbs-selftest.tsv
  [[ -s $TSV ]] || { echo "no $TSV — the self-test never reached its table" >&2; exit 2; }
  selftest_clips_index "$OUT"
  echo; echo "════ results — $TSV"
  awk -F'\t' '{ printf "%-3s %-30s %-6s %s\n", $1, $2, $3, $4 }' "$TSV"
  echo
  for v in PASS FAIL XFAIL XPASS; do printf '%s %d  ' "$v" "$(awk -F'\t' -v v=$v '$3==v' "$TSV" | wc -l)"; done; echo
  fails=$(awk -F'\t' '$3=="FAIL"' "$TSV" | wc -l)
  exit $(( fails > 100 ? 100 : fails ))
fi

# ── in the container ────────────────────────────────────────────────────────
set -uo pipefail
source "$HOME/fixtures/prep.sh"
source "$HOME/fixtures/gestures.sh"
trap 'clip_stop; cleanup_capture' EXIT

TSV=$OUT_DIR/verbs-selftest.tsv
: >"$TSV"
N=0
# vt <name> <expect PASS|XFAIL> <verb> [args…] — run, snap, record.
vt() {
  local name=$1 expect=$2 rc verdict err base; shift 2
  N=$((N + 1))
  base=$(printf 'verbs-%02d-%s' "$N" "$name")
  # One review clip per verb, the verb through the snap (RIG_CLIPS=0: none).
  clip_start "$base"
  # NOT in a subshell: verbs set state later verbs read (G_FILE).
  "$@" >/dev/null 2>"$HOME/.vt-err"; rc=$?
  err=$(sed 's/\x1b\[[0-9;]*m//g' "$HOME/.vt-err" | tr '\n' ' ' | sed 's/  */ /g; s/^ //; s/ $//')
  if [[ $expect == XFAIL ]]; then
    (( rc == 0 )) && verdict=XPASS || verdict=XFAIL
  else
    (( rc == 0 )) && verdict=PASS || verdict=FAIL
  fi
  park "$((CAP_W - 30))" "$((CAP_H - 60))" 0.6
  snap "$base" >/dev/null
  clip_stop
  printf '%02d\t%s\t%s\t%s\n' "$N" "$name" "$verdict" "${err:-ok}" >>"$TSV"
  case $verdict in
    PASS|XFAIL) printf '  \033[32m%-5s\033[0m %02d %s %s\n' "$verdict" "$N" "$name" "${err:+— $err}" ;;
    *)          printf '  \033[31m%-5s\033[0m %02d %s %s\n' "$verdict" "$N" "$name" "${err:+— $err}" ;;
  esac
}

# ── the project: the Demo template + a blank chart and a blank mimic ────────
ext_scaffold my-plant
: >"$PROJ/station.sfc"
printf '{\n\t"name": "Lift station",\n\t"canvas": { "width": 940, "height": 420 },\n\t"equipment": [],\n\t"pipes": []\n}\n' >"$PROJ/lift.mimic.json"
git -C "$PROJ" add -A; git -C "$PROJ" commit -qm "blank chart and mimic"

ext_open
hide_sidebar
sleep 3

# ── SFC: a blank station.sfc to a small chart ───────────────────────────────
vt ed_open_diagram-sfc PASS ed_open_diagram station.sfc
vt sfc_init PASS sfc_init
# "+ step" with Start selected chains Fill under it (step + transition, one edit)
vt sfc_add_step PASS sfc_add_step Start Fill "LevelPct < 20.0"
# "+ transition" → "other… (new step)": the STEP and the TRANSITION both land
vt sfc_add_transition_new_step PASS sfc_add_transition_new_step Fill Drain "LevelPct > 80.0"
vt sfc_add_transition-loop PASS sfc_add_transition Drain Start TRUE
vt sfc_add_transition_condition PASS sfc_add_transition_condition "Drain->Start" "LevelPct < 5.0"
vt sfc_add_action PASS sfc_add_action Fill N PumpRun
vt sfc_add_alt_branch PASS sfc_add_alt_branch Fill Overflow "LevelPct > 95.0"
vt sfc_add_parallel_branch PASS sfc_add_parallel_branch "Start->Fill" Mix
vt sfc_rename_step PASS sfc_rename_step Drain Empty
# "+ join": Mix (the Start divergence's second leg) joins Fill->Empty's
# FROM — a simultaneous convergence, FROM (Fill, Mix) TO Empty
vt sfc_join_step PASS sfc_join_step "Fill->Empty" Mix

# ── Ladder: a new rung on the Demo's interlocks.ld ──────────────────────────
vt ed_open_diagram-ld PASS ed_open_diagram interlocks.ld
vt ld_add_rung PASS ld_add_rung pump_run
vt diagram_zoom PASS diagram_zoom in 1
vt ld_add_contact-nc PASS ld_add_contact pump_run LevelPct nc
vt ld_add_contact PASS ld_add_contact pump_run HornAck
vt ld_add_coil PASS ld_add_coil pump_run PumpRun
vt ld_add_coil-reset PASS ld_add_coil pump_run Horn reset
vt ld_add_block-TON PASS ld_add_block pump_run TON t2 "PT := T#5S"
vt ld_add_block-libraryFB PASS ld_add_block pump_run RateOfChange roc2
vt ld_rename_block PASS ld_rename_block pump_run t2 t_run
vt ld_declare PASS ld_declare LevelPct VAR_EXTERNAL
vt ld_add_branch PASS ld_add_branch pump_run HornAck TempLowAlm
# A rung that calls a block needs no coil: its "( _ )" deletes outright
vt ld_add_rung-starter PASS ld_add_rung starter
vt ld_add_block-starter PASS ld_add_block starter TON t3 "PT := T#2S"
vt ld_delete_last_coil PASS ld_delete_last_coil starter
# Drags (L07): a contact to another rung, in front of an element there;
# the same contact to the end of its new rung's series; a coil (with its
# R mode) to another rung's coil zone — pump_run keeps its PumpRun coil.
vt ld_move_element PASS ld_move_element pump_run LevelPct horn HornAck
vt ld_move_element-same PASS ld_move_element horn LevelPct horn
vt ld_move_element-coil PASS ld_move_element pump_run Horn ackclear

# ── FBD: the Demo's program.fbd ─────────────────────────────────────────────
vt ed_open_diagram-fbd PASS ed_open_diagram program.fbd
vt fbd_add_block PASS fbd_add_block AND both
vt fbd_zoom_to PASS fbd_zoom_to both 2
vt fbd_wire PASS fbd_wire low.OUT both.IN1
vt fbd_add_tag_ref PASS fbd_add_tag_ref PumpRun both.IN2
vt fbd_add_comment PASS fbd_add_comment "Both conditions, for the lift station"
# A function block with named pins, through the "+ add → function block"
# picker: every PID input lands open (`lic : PID(AUTO := _, PV := _, …)`)
# and the file still parses.
vt fbd_add_block-PID PASS fbd_add_block PID lic
# A drag pins the node (F11): its `(* @layout *)` entry, in flow units.
vt fbd_move_node PASS fbd_move_node both -120 -80

# ── Mimic: a blank lift.mimic.json ──────────────────────────────────────────
vt ed_open_diagram-mimic PASS ed_open_diagram lift.mimic.json
vt mimic_drop-tank PASS mimic_drop Tank 400 60 WW101
vt mimic_drop-pump PASS mimic_drop Pump 150 250 P101
vt mimic_bind PASS mimic_bind WW101 levelPct LevelPct
vt mimic_pipe_direct PASS mimic_pipe_direct P101.out WW101.left
vt mimic_pipe PASS mimic_pipe P101.out WW101.left

# ── Component ports ─────────────────────────────────────────────────────────
vt component_edit_ports PASS component_edit_ports Pump
vt component_add_port PASS component_add_port seal
# Drag a dot (P01). seal (from + Add port) has no dir; Pump's default `out`
# carries dir "up", which the move must keep, as the mimic editor's ports
# mode does.
vt component_move_port PASS component_move_port seal 60 50
vt component_move_port-dir PASS component_move_port out -50 40

# ── the files, as saved, and what the compiler makes of them ────────────────
mkdir -p "$OUT_DIR/verbs-files"
cp "$PROJ"/station.sfc "$PROJ"/interlocks.ld "$PROJ"/program.fbd "$PROJ"/lift.mimic.json "$OUT_DIR/verbs-files/" 2>/dev/null
find "$PROJ" -name '*.component.json' -exec cp {} "$OUT_DIR/verbs-files/" \;
(cd "$PROJ" && naut check >"$OUT_DIR/verbs-files/naut-check.txt" 2>&1)
printf '%s\t%s\t%s\t%s\n' "--" "naut check" "NOTE" "$(tail -1 "$OUT_DIR/verbs-files/naut-check.txt")" >>"$TSV"

echo
awk -F"\t" '{ printf "%-3s %-30s %-6s %s\n", $1, $2, $3, $4 }' "$TSV"
