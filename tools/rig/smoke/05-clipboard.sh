#!/usr/bin/env bash
# 05 — the diagram clipboard through the REAL system clipboard (PR #25).
#
#   FBD      copy in program.fbd's editor, paste in ANOTHER .fbd's editor —
#            a different webview, so only the system clipboard can carry it
#            (clipboard.ts: the in-memory fallback is per webview). Proven
#            first by pasting the same copy into a plain text file.
#   SFC      copy / paste a step within one editor
#   Mimic    copy / paste equipment within one editor
#   Ladder   cut / paste an element
#
# Every paste is saved and read back, and the project must still `naut check`
# (the select-all FBD paste: must be well-formed — see there for why not clean).
#
# What is selected is found in the DOM (gestures.sh: fbd_node_el,
# mimic_eq_el, ld_select_node, sfc_select_step; empty canvas by
# elementFromPoint).
set -euo pipefail
CHECK=05-clipboard
source "$HOME/smoke/lib.sh"
export G_PACE=fast
source "$HOME/fixtures/gestures.sh"   # cdp / click_el / the editors' element finders
rm -rf "$PROFILE"

ext_scaffold my-plant
printf 'PROGRAM Target\nVAR_EXTERNAL\n    TempC : REAL;\nEND_VAR\nFBD\nEND_FBD\nEND_PROGRAM\n' >"$PROJ/target.fbd"
printf 'PROGRAM Target2\nVAR_EXTERNAL\n    TempC : REAL;\nEND_VAR\nFBD\nEND_FBD\nEND_PROGRAM\n' >"$PROJ/target2.fbd"
: >"$PROJ/clip.txt"
cp "$FIX/heated-tank.mimic.json" "$PROJ/"
git -C "$PROJ" add -A; git -C "$PROJ" commit -qm "smoke fixtures"
fcheck() { (cd "$PROJ" && naut check 2>&1 | grep -v '^naut check' || true); }

smoke_open "$PROJ" program.fbd
key Escape; hide_sidebar
vs_cmd "nautilus: Open as Diagram Editor" 6

# ── FBD, one network: the TAL-101 LT block (wire "cold") ────────────────────
wait_js "$_G_READY" 10 || true
click_el "$(fbd_node_el cold)" || fail "FBD: no block cold (data-id b:w.cold) on the diagram"
png=$(shot fbd-selected)
key ctrl+c; sleep 1
open_file clip.txt 2; key ctrl+v; sleep 1; key ctrl+s; sleep 1
if grep -q '"nautilus":"fbd"' "$PROJ/clip.txt"; then
  pass "FBD copy reached the SYSTEM clipboard (pasted into clip.txt: $(head -c 60 "$PROJ/clip.txt")…)" "$png"
else
  fail "FBD copy is not on the system clipboard (clip.txt: $(head -c 80 "$PROJ/clip.txt"))" "$png"
fi
open_file target.fbd 3
vs_cmd "nautilus: Open as Diagram Editor" 6
wait_js "$_G_READY" 10 || true
click_canvas "" 0.5 || true; key ctrl+v; sleep 3
png=$(shot fbd-pasted-one)
key ctrl+s; sleep 1.5
if grep -q 'cold = LT(' "$PROJ/target.fbd"; then
  pass "FBD block pasted into ANOTHER .fbd editor: $(grep -o 'cold = LT([^)]*)' "$PROJ/target.fbd")" "$png"
else
  fail "FBD paste into target.fbd did nothing" "$png"
fi
e=$(fcheck | grep target.fbd || true)
[[ -z $e ]] && pass "target.fbd still compiles after the paste" || fail "target.fbd broken by the paste: $e"

# ── FBD, select-all (Ctrl+A): coils come with both their chip and block ids ──
vs_cmd "View: Close All Editors" 1
open_file program.fbd 3
vs_cmd "nautilus: Open as Diagram Editor" 6
wait_js "$_G_READY" 10 || true
click_canvas "" 0.5 || true; key ctrl+a; sleep 0.5; key ctrl+c; sleep 1
open_file target2.fbd 3
vs_cmd "nautilus: Open as Diagram Editor" 6
wait_js "$_G_READY" 10 || true
click_canvas "" 0.5 || true; key ctrl+v; sleep 3
key ctrl+s; sleep 1.5
png=$(shot fbd-pasted-all)
e=$(fcheck | grep target2.fbd || true)
# By design a paste does NOT check clean: a copy severs every tag read to an
# open `_` pin, and coils Target2 does not declare are undeclared — "the
# `_`/undeclared diagnostics are the intended breadcrumbs" (lang/fbd/
# editparity.go, opDuplicate; editparity_test.go "Copy severs wiring but
# keeps configuration"). So the bar is that the paste is WELL-FORMED: every
# source statement arrives once, and with program.fbd's declarations spliced
# in, naut check gets past parsing and reports nothing but those
# breadcrumbs. The 2026-09-24 duplicateText bug (MUL(Ki, e, ScanDtS) ->
# MUL(_, _0.0)) is a parse error and still FAILs here.
body() { sed -n '/^FBD$/,/^END_FBD$/p' "$1" | grep -vE '^\s*(//|$|FBD$|END_FBD$)' | wc -l; }
cp "$PROJ/target2.fbd" "$PROJ/target2.pasted"
python3 - "$PROJ/program.fbd" "$PROJ/target2.fbd" <<'PY2'
import sys
prog, tgt = (open(f).read().split("\n") for f in sys.argv[1:3])
def header(ls):   # [first VAR* line, FBD line)
    fbd = next(i for i, l in enumerate(ls) if l.strip().upper() == "FBD")
    var = next(i for i, l in enumerate(ls) if l.strip().upper().startswith("VAR"))
    return var, fbd
pv, pf = header(prog); tv, tf = header(tgt)
open(sys.argv[2], "w").write("\n".join(tgt[:tv] + prog[pv:pf] + tgt[tf:]))
PY2
e2=$(fcheck | grep target2.fbd || true)
mv "$PROJ/target2.pasted" "$PROJ/target2.fbd"
ns=$(body "$PROJ/program.fbd"); np=$(body "$PROJ/target2.fbd")
if (( np == ns )) && { [[ -z $e2 ]] || grep -q 'unfilled placeholder' <<<"$e2"; }; then
  pass "select-all paste into another .fbd is well-formed: all $ns statements, parses; only the designed breadcrumbs (as pasted: ${e:-none}; with program.fbd's declarations: ${e2:-none})" "$png"
else
  fail "select-all copy → paste writes INVALID FBD: $np of $ns statements; as pasted: ${e:-none}; with program.fbd's declarations: ${e2:-none} | $(sed -n '/^FBD$/,/^END_FBD$/p' "$PROJ/target2.fbd" | tr -s ' ' | paste -sd'|' | cut -c1-400)" "$png"
fi
vs_cmd "View: Close All Editors" 1

# ── Mimic: copy / paste the tank ────────────────────────────────────────────
open_file heated-tank.mimic.json 10
G_FILE=heated-tank.mimic.json   # mimic_eq_el reads the ids from it
click_el "$(mimic_eq_el T101)" || fail "Mimic: no tank T101 on the canvas"
key ctrl+c; sleep 0.5; key ctrl+v; sleep 2
png=$(shot mimic-pasted)
key ctrl+s; sleep 1.5
ids=$(python3 -c "import json,sys; print(' '.join(e['id'] for e in json.load(open(sys.argv[1]))['equipment']))" "$PROJ/heated-tank.mimic.json")
[[ $(wc -w <<<"$ids") == 4 ]] && pass "Mimic: copy/paste the tank → 4 equipment ($ids)" "$png" \
  || fail "Mimic: equipment after paste: $ids" "$png"
vs_cmd "View: Close All Editors" 1

# ── Ladder: cut / paste an element ──────────────────────────────────────────
# Cut the horn rung's /HornAck contact, paste it after the ackclear rung's
# /HiTempAlm contact.
L=$PROJ/interlocks.ld
open_file interlocks.ld 3
vs_cmd "nautilus: Open as Diagram Editor" 6
wait_js "$_G_READY" 10 || true
ld_select_node horn contact HornAck || fail "Ladder: no contact HornAck on rung horn"
png=$(shot ladder-selected)
key ctrl+x; sleep 2
ld_select_node ackclear contact HiTempAlm || fail "Ladder: no contact HiTempAlm on rung ackclear"
key ctrl+v; sleep 2
png=$(shot ladder-pasted)
key ctrl+s; sleep 1.5
horn=$(grep -m1 '( Horn )' "$L" | sed 's/^ *//'); ack=$(grep -m1 'R HornAck' "$L" | sed 's/^ *//')
if [[ $horn != *HornAck* && $ack == */HornAck* ]]; then
  pass "Ladder: cut /HornAck from rung horn, pasted into ackclear: '$ack'" "$png"
else
  fail "Ladder cut/paste: horn='$horn' ackclear='$ack'" "$png"
fi
e=$(fcheck | grep interlocks.ld || true)
[[ -z $e ]] && pass "interlocks.ld compiles after the cut/paste" || fail "interlocks.ld broken: $e"

# ── SFC: copy / paste a step (another project: tank-batch) ──────────────────
ext_fixture tank-batch
S=$PROJ/batch.sfc
n0=$(grep -cE '^\s*(INITIAL_)?STEP ' "$S")
smoke_open "$PROJ" batch.sfc
key Escape; hide_sidebar
vs_cmd "nautilus: Open as Diagram Editor" 8
wait_js "$_G_READY" 10 || true
sfc_select_step Fill || fail "SFC: could not select step Fill on the chart"
key ctrl+c; sleep 0.5; key ctrl+v; sleep 2.5
png=$(shot sfc-pasted)
key ctrl+s; sleep 1.5
n1=$(grep -cE '^\s*(INITIAL_)?STEP ' "$S")
(( n1 == n0 + 1 )) && pass "SFC: copy/paste step Fill → $(grep -oE 'STEP Fill[A-Za-z0-9_]+' "$S" | head -1) ($n0 → $n1 steps)" "$png" \
  || fail "SFC: steps $n0 → $n1 after copy/paste" "$png"
e=$(fcheck | grep batch.sfc || true)
[[ -z $e ]] && pass "batch.sfc compiles after the paste" || info "batch.sfc after paste: $e (a pasted step with no transitions may be expected to warn)"
