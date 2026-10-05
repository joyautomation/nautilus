#!/usr/bin/env bash
# demo.sh — the verb self-test FILMED: every verb in verbs/gestures.sh, once,
# at episode pace and the series frame, one clip per verb. These are the
# demonstration clips the docs site's proof pages show (the "demo" set; the
# nightly's fast-pace clips are the fallback). Same verbs, same assertions,
# same container as selftest.sh — this only fixes how it is filmed:
#
#     G_PACE=human  CAP_W=2560 CAP_H=1440  REC_ZOOM=2  REC_FONT_SIZE=14
#     RIG_CLIPS=1   RIG_SET=demo
#
#     tools/rig/demo.sh                               # build THIS checkout, film
#     NAUT=…/naut VSIX=…/vscode-iec.vsix tools/rig/demo.sh
#     RIG_NAME=nautilus-demo-$(hostname -s) RIG_KEEP=1 tools/rig/demo.sh
#     RIG_OUT=/somewhere tools/rig/demo.sh            # → /somewhere/demo/
#
# Output: tools/rig/out/demo/ (RIG_OUT moves tools/rig/out, as for every
# entry point): selftest/ exactly as selftest.sh writes it (verbs-NN-<verb>
# .mp4 and .png, verbs-selftest.tsv, clips.html) and manifest.json — set
# "demo", pace "human", frame 2560x1440 at zoom 2 (lib/manifest.sh). A build
# (no NAUT/VSIX given) still goes to out/build/, not under demo/.
#
# ~20–30 min: human pace moves the pointer the way a take does. The display
# is sized from CAP_W/CAP_H (lib/container.sh: Xvfb is CAP+40 each way).
# Exit status: selftest.sh's (the number of FAIL rows; 2 if no table).
set -uo pipefail
RIG_DIR=$(cd "$(dirname "$0")" && pwd)
OUT_ROOT=${RIG_OUT:-$RIG_DIR/out}
export SMOKE_BUILD=${SMOKE_BUILD:-$OUT_ROOT/build}
export RIG_OUT=$OUT_ROOT/demo
export RIG_NAME=${RIG_NAME:-nautilus-demo-rig}
export G_PACE=human CAP_W=2560 CAP_H=1440 REC_ZOOM=2 REC_FONT_SIZE=14 RIG_CLIPS=1 RIG_SET=demo
exec "$RIG_DIR/selftest.sh" "$@"
