#!/usr/bin/env bash
# The nautilus VS Code extension smoke suite, in a REAL VS Code, in a
# throwaway Incus container (Xvfb :99 + openbox — never the desk's display).
# Run it before every stable promotion; the nightly runs it too.
#
#     tools/rig/smoke/run.sh                         # build THIS checkout, run all
#     tools/rig/smoke/run.sh 03-preview-undo 07-ready-live   # just these
#     NAUT=… VSIX=… tools/rig/smoke/run.sh           # test builds you already have
#     NAUTILUS_REF=v0.12.0-rc1 tools/rig/smoke/run.sh  # build another ref (build.sh)
#     RIG_KEEP=1 tools/rig/smoke/run.sh …            # leave the container up after
#     RIG_CLIPS=0 tools/rig/smoke/run.sh             # no per-check clips (quicker)
#
# Output: tools/rig/out/smoke/ (RIG_OUT= moves tools/rig/out) — one PNG per
# piece of evidence plus results.tsv (check, verdict, what, png), one clip
# per check (<check>.mp4; RIG_CLIPS=0 skips them) and clips.html/clips.md,
# the review index (check · verdict · clip · PNGs). The table
# is printed at the end; the exit status is the number of FAILs (capped at 100).
#
# Each check is its own script (NN-*.sh, sourcing lib.sh) run inside the
# container as user dev; they are independent — each builds its own project
# and uses its own fresh VS Code profile — so any subset runs on its own.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
RIG_DIR=$(cd "$HERE/.." && pwd)                # tools/rig
REPO=$(cd "$RIG_DIR/../.." && pwd)             # the nautilus checkout
export RIG_NAME=${RIG_NAME:-nautilus-smoke-rig}
source "$RIG_DIR/lib/container.sh"
source "$RIG_DIR/lib/clips-index.sh"
OUT=${RIG_OUT:-$RIG_DIR/out}/smoke

if [[ -z ${NAUT:-} || -z ${VSIX:-} ]]; then
  echo "▸ building the candidate (NAUT/VSIX not given)"
  eval "$("$HERE/build.sh")" || exit 2
fi
[[ -x $NAUT && -f $VSIX ]] || { echo "need NAUT=<naut binary> and VSIX=<vscode-iec.vsix>" >&2; exit 2; }
NAUTILUS_SHA=${NAUTILUS_SHA:-$(cat "$(dirname "$NAUT")/SHA" 2>/dev/null || echo unknown)}

checks=("$@")
[[ ${#checks[@]} -gt 0 ]] || mapfile -t checks < <(cd "$HERE" && ls [0-9][0-9]-*.sh | sed 's/\.sh$//')

echo "▸ $RIG_NAME: fresh container"
trap '[[ -n ${RIG_KEEP:-} ]] || rig_down' EXIT
rig_up || exit 2

# The CLI under test goes in at /opt/smoke-bin — NOT /usr/local/bin, which
# the extension searches on its own (cliResolve.ts fallbackDirs); check 01
# needs a machine where naut is nowhere, and lib.sh puts it on PATH for the rest.
_rig mkdir -p /opt/smoke-bin
incus file push -q "$NAUT" "$RIG_NAME/opt/smoke-bin/naut" 2>/dev/null || incus file push "$NAUT" "$RIG_NAME/opt/smoke-bin/naut" >/dev/null
_rig chmod 755 /opt/smoke-bin/naut
_rig bash -c 'rm -f /usr/local/bin/naut /usr/local/bin/nautilus'
rig_push "$RIG_DIR/lib/lib.sh" "/home/$RIG_USER/lib.sh"
# ~/fixtures: the verbs (prep.sh, gestures.sh, cdp.js), then the checks' own
# data over them. ~/smoke: the checks and lib.sh.
rig_push_tree "$RIG_DIR/verbs" "/home/$RIG_USER/fixtures"
rig_push_tree "$HERE/fixtures" "/home/$RIG_USER/fixtures"
rig_push_tree "$HERE" "/home/$RIG_USER/smoke"
_rig mkdir -p "/home/$RIG_USER/smoke-assets"
L5X=$REPO/lang/l5x/testdata/DemoProgram.L5X
[[ -f $L5X ]] && rig_push "$L5X" "/home/$RIG_USER/smoke-assets/DemoProgram.L5X"
_rig rm -f /tmp/ext.vsix; rig_push "$VSIX" /tmp/ext.vsix; _rig chmod 644 /tmp/ext.vsix
_rig chown -R "$RIG_USER:$RIG_USER" "/home/$RIG_USER"
rig_run "rm -rf ~/out/smoke; mkdir -p ~/out/smoke; chmod +x ~/smoke/*.sh"
echo "▸ installing $(basename "$VSIX")"
rig_run "code --install-extension /tmp/ext.vsix --force 2>&1 | tail -1"
rig_run "printf '# nautilus %s · %s · VS Code %s · %s\n' '$NAUTILUS_SHA' '$(basename "$VSIX")' \"\$(code --version | head -1)\" \"\$(date -Is)\" > ~/out/smoke/results.tsv"

for c in "${checks[@]}"; do
  echo; echo "▸ $c"
  # NB: nothing in this command line may contain "vscode-rec" — lib.sh's
  # launch_vscode pkills by that string and would kill this shell.
  rig_run "cd ~ && DISPLAY=$RIG_DISPLAY RIG_CLIPS=${RIG_CLIPS:-1} timeout 1200 bash ~/smoke/$c.sh 2>&1 | grep -v 'still: '" \
    || echo "  ($c exited non-zero)"
  # A check the timeout killed never ran its clip_stop: end its recording.
  rig_run "pkill -x naut 2>/dev/null; pkill -INT -x ffmpeg 2>/dev/null && sleep 1; true"
done

PULL=$(mktemp -d)
incus file pull -r "$RIG_NAME/home/$RIG_USER/out/smoke" "$PULL/" >/dev/null 2>&1
if [[ $# -gt 0 && -f $OUT/results.tsv ]]; then
  # A subset re-run: replace just those checks' rows and PNGs in the last
  # full run's results, so out/smoke stays one complete picture.
  for c in "${checks[@]}"; do rm -f "$OUT/$c"-*.png "$OUT/$c.mp4"; done
  { head -1 "$PULL/smoke/results.tsv" | sed 's/$/ (partial re-run: '"${checks[*]}"')/'
    awk -F'\t' -v re="^($(IFS='|'; echo "${checks[*]}"))$" 'NR>1 && $1 !~ re' "$OUT/results.tsv"
    tail -n +2 "$PULL/smoke/results.tsv"; } | awk 'NR==1{print;next}{print | "sort -s -t\"\t\" -k1,1"}' >"$OUT/results.new"
  cp "$PULL/smoke/"*.png "$PULL/smoke/"*.mp4 "$OUT/" 2>/dev/null; mv "$OUT/results.new" "$OUT/results.tsv"
else
  rm -rf "$OUT"; mkdir -p "$(dirname "$OUT")"; mv "$PULL/smoke" "$OUT"
fi
rm -rf "$PULL"
smoke_clips_index "$OUT"

echo; echo "════ results — $OUT/results.tsv"
head -1 "$OUT/results.tsv"
awk -F'\t' 'NR>1 && $2 != "NOTE" {printf "%-18s %-5s %s\n", $1, $2, $3}' "$OUT/results.tsv"
echo
for v in PASS FAIL WARN SKIP; do printf '%s %d  ' "$v" "$(awk -F'\t' -v v=$v 'NR>1 && $2==v' "$OUT/results.tsv" | wc -l)"; done; echo
fails=$(awk -F'\t' 'NR>1 && $2=="FAIL"' "$OUT/results.tsv" | wc -l)
exit $(( fails > 100 ? 100 : fails ))
