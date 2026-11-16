#!/usr/bin/env bash
# 24 — live values inside a ladder FUNCTION_BLOCK, in a real desktop VS Code.
#
# The rungs of a .ld FUNCTION_BLOCK name the block's pins and locals bare
# (`Sealed`, `Running`); on the controller they are members of an instance
# (`tank.Sealed`). The diagram reads them through the instance the body is
# monitoring, the text editor's CodeLens choice, auto-picked when the
# project declares exactly one.
#
#   a. The block's heading says which instance it monitors: "tank", picked
#      with no clicks (one instance in the project).
#   b. Before the start: the internal bit Sealed shows FALSE, and Running
#      shows the INSTANCE's FALSE, not the controller tag Running (held
#      TRUE, a decoy sharing the name).
#   c. StartPB → TRUE over the tag API: Sealed and Running light TRUE from
#      tank.*, and the seal holds once StartPB drops again.
#   d. T on the selected Sealed contact does nothing to the controller: a
#      block local is not a tag, and the decoy Running is not touched.
#
# Project: fixtures/ld-fb-live. Assertions read the webview DOM over CDP
# (the .liveval pill under each element, the [data-fb-monitor] chip) and
# /api/state, not pixels.
set -euo pipefail
CHECK=24-ld-fb-live
source "$HOME/smoke/lib.sh"
source "$HOME/fixtures/gestures.sh"
G_PACE=fast
rm -rf "$PROFILE"

export NAUTILUS_TEST_STATE=$OUT_DIR/$CHECK-state.json
rm -f "$NAUTILUS_TEST_STATE"

# ── helpers ────────────────────────────────────────────────────────────────
tagv() {
  api /api/state | python3 -c '
import sys, json
d = json.load(sys.stdin); print(json.dumps(d.get("tags", {}).get(sys.argv[1])))' "$1"
}
set_tag() { curl -sf --max-time 3 -X POST -H 'content-type: application/json' -d "{\"name\":\"$1\",\"value\":$2}" "localhost:$PORT/api/tags" >/dev/null; }
poll() { local s=$1 i; shift; for ((i = 0; i < s * 4; i++)); do "$@" && return 0; sleep 0.25; done; return 1; }
# The live pills of every element naming <ref> in rung <rung>, joined.
pills() { wv "[...doc.querySelectorAll($(_q "[data-rung=\"$1\"][data-xref=\"$2\"]"))].map(e => e.querySelector('.liveval')?.textContent.trim() ?? '∅').join(',')"; }
pills_are() { local p; p=$(pills "$1" "$2"); [[ -n $p && $(tr ',' '\n' <<<"$p" | sort -u) == "$3" ]]; }
chip() { wv "doc.querySelector('[data-fb-monitor=\"TankCycle\"]')?.textContent.trim() ?? ''"; }
chip_has() { [[ $(chip) == *"$1"* ]]; }
EL() { printf 'doc.querySelector(%s)' "$(_q "[data-rung=\"seal\"][data-xref=\"$1\"]")"; }

# ── project + controller ────────────────────────────────────────────────────
ext_fixture ld-fb-live
PORT=$(free_port 18102 18103 18104 18105)
point_extension_at "$PROJ" "$PORT"
start_controller "$PROJ" "$PORT"
sleep 2
[[ $(tagv Running) == true ]] && pass "naut run on :$PORT: decoy Running $(tagv Running), StartPB $(tagv StartPB)" \
  || { fail "controller on :$PORT: decoy Running $(tagv Running), want true"; exit 1; }

smoke_open "$PROJ" tankcycle.ld
key Escape; hide_sidebar
sleep 3
ed_open_diagram tankcycle.ld || { fail "tankcycle.ld did not open as a ladder diagram" "$(shot open)"; exit 1; }
tstate() { python3 -c 'import json,sys
try: print(json.load(open(sys.argv[1])).get("connected"))
except Exception: print(None)' "$NAUTILUS_TEST_STATE"; }
connected() { [[ $(tstate) == True ]]; }
poll 15 connected || info "test state never said connected ($(tstate))"
sleep 1

# ── a: the monitored instance, auto-picked ──────────────────────────────────
poll 15 chip_has "monitoring tank" && pass "a: the TankCycle heading reads '$(chip)'" "$(shot a-chip)" \
  || fail "a: TankCycle heading chip '$(chip)', want 'monitoring tank'" "$(shot a-chip)"

# ── b: internals and outputs read from the instance ─────────────────────────
if poll 10 pills_are seal Sealed FALSE; then
  pass "b: internal Sealed shows $(pills seal Sealed) (tank.Sealed)" "$(shot b-sealed)"
else
  fail "b: Sealed pills '$(pills seal Sealed)', want FALSE from tank.Sealed" "$(shot b-sealed)"
fi
pills_are run Running FALSE && pass "b: Running shows $(pills run Running), the instance's, not the decoy tag ($(tagv Running))" \
  || fail "b: Running pill '$(pills run Running)', want the instance's FALSE (decoy tag is $(tagv Running))" "$(shot b-running)"

# ── c: start → TRUE from tank.*, sealed in after release ────────────────────
set_tag StartPB true
if poll 5 pills_are seal Sealed TRUE && poll 3 pills_are run Running TRUE; then
  pass "c: StartPB TRUE → Sealed $(pills seal Sealed), Running $(pills run Running), Start $(pills seal Start)" "$(shot c-started)"
else
  fail "c: after StartPB TRUE: Sealed '$(pills seal Sealed)', Running '$(pills run Running)', Lamp $(tagv Lamp)" "$(shot c-started)"
fi
set_tag StartPB false
sleep 1
poll 5 pills_are seal Start FALSE && pills_are seal Sealed TRUE \
  && pass "c: StartPB back FALSE: Start $(pills seal Start), Sealed stays $(pills seal Sealed)" \
  || fail "c: after release: Start '$(pills seal Start)', Sealed '$(pills seal Sealed)'" "$(shot c-released)"

# ── d: T on a block local writes nothing ────────────────────────────────────
before=$(api /api/state | python3 -c 'import sys,json; print(json.dumps(json.load(sys.stdin)["tags"], sort_keys=True))')
if click_el "$(EL Sealed)"; then
  g_key t
  sleep 1.5
  after=$(api /api/state | python3 -c 'import sys,json; print(json.dumps(json.load(sys.stdin)["tags"], sort_keys=True))')
  [[ $before == "$after" ]] && pass "d: T on the Sealed contact changed no tag (Running still $(tagv Running))" "$(shot d-toggle)" \
    || fail "d: T on Sealed changed tags: $before → $after" "$(shot d-toggle)"
else
  fail "d: no Sealed contact to select" "$(shot d-no-contact)"
fi
