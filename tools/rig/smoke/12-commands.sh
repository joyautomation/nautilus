#!/usr/bin/env bash
# 12 — the palette commands no other check runs (gesture inventory rows C03,
# C04, C05, C07), against a real `naut run`:
#
#   C03 nautilus: Show CLI Info — the toast names the naut in use and its
#       version, and the version is what `naut version` says for the binary
#       under test.
#   C04 nautilus: Restart Language Server — a NEW `naut lsp` process comes
#       up (the old one exits), the language client logs the restart, and a
#       typo made after the restart still gets a diagnostic: the server is
#       alive, not just respawned.
#   C05 nautilus: Connect to Controller… — typing the running controller's
#       URL into the quick pick connects (runtimeUrl + connected in the test
#       state); typing a dead port's URL drops to offline.
#   C07 Live Values view, title-bar Refresh — after a tag write through the
#       API, the tree shows the new value.
#
# Assertions read the extension's test-state snapshot (NAUTILUS_TEST_STATE,
# tools/vscode-iec/src/testState.ts) and the workbench DOM over CDP
# (gestures.sh's `cdp page`: toasts, the status bar, the tree), not pixels.
set -euo pipefail
CHECK=12-commands
source "$HOME/smoke/lib.sh"
source "$HOME/fixtures/gestures.sh"   # cdp page / page_el_box / g_click
G_PACE=${G_PACE:-fast}
rm -rf "$PROFILE"

# The extension writes its snapshot here on activation and on every change;
# `code` (launch_vscode) inherits the environment.
export NAUTILUS_TEST_STATE=$HOME/.smoke-$CHECK-state.json
rm -f "$NAUTILUS_TEST_STATE"

# ts <python expr over s, the snapshot> — print its value ("" if no snapshot).
ts() {
  python3 - "$NAUTILUS_TEST_STATE" "$1" <<'PY' 2>/dev/null || true
import json, sys
s = json.load(open(sys.argv[1]))
print(eval(sys.argv[2], {}, {"s": s}))
PY
}
ts_true() { [[ $(ts "bool($1)") == True ]]; }
live_item() { ts 'next((e["text"] for e in s["statusBar"] if e["name"] == "nautilus.live"), "")'; }
state_line() { ts '"runtimeUrl=%s connected=%s live=%r" % (s["runtimeUrl"], s["connected"], next((e["text"] for e in s["statusBar"] if e["name"] == "nautilus.live"), ""))'; }

# pg <js> — evaluate in the workbench page; a string result comes back plain.
pg() { cdp page "$1" 2>/dev/null | python3 -c 'import json,sys; v=json.loads(sys.stdin.read() or "null"); print("" if v is None else v)' 2>/dev/null || true; }
# toasts — every notification's message (toasts and the centre), one per line.
toasts() { pg '[...document.querySelectorAll(".notification-list-item-message")].map(e => e.textContent.trim()).join("\n")'; }
has_toast() { toasts | grep -q -- "$1"; }
# statusbar_text <regex> — the text of the status-bar item matching it.
statusbar_text() { pg "[...document.querySelectorAll('#workbench\\\\.parts\\\\.statusbar .statusbar-item')].map(e => e.textContent.trim()).find(t => /$1/.test(t)) || ''"; }
# problems — the status bar's problems item: "<errors> <warnings>".
problems() {
  pg '(() => { const e = document.getElementById("status.problems"); return e ? (e.getAttribute("aria-label") || "") + " | " + e.textContent.trim() : ""; })()'
}
errors() { problems | python3 -c 'import re,sys; t=sys.stdin.read().split("|")[-1]; n=re.findall(r"\d+", t); print(n[0] if n else -1)'; }

lsp_pids() { pgrep -f "^$SMOKE_BIN/naut lsp" | sort | paste -sd, || true; }
# lsp_log — the language client's own output channel ("nautilus Structured
# Text"), from the current profile's newest session.
lsp_log() { cat "$(ls -td "$PROFILE"/logs/*/ | head -1)"window*/exthost/output_logging_*/*"nautilus Structured Text"*.log 2>/dev/null || true; }

clear_toasts() { vs_cmd "Notifications: Clear All Notifications" 1; }

# ── setup ───────────────────────────────────────────────────────────────────
ext_scaffold my-plant
PORT=$(free_port 18080 18081 18082 18083)
DEAD=$(free_port 18190 18191 18192 18193)   # nothing listens here
LIVE_URL=http://localhost:$PORT DEAD_URL=http://localhost:$DEAD
# Start pointed at nothing, so C05's connect is a real change of runtimeUrl.
point_extension_at "$PROJ" "$DEAD"
start_controller "$PROJ" "$PORT"
sleep 3
api /api/state >/dev/null && pass "naut run on :$PORT, /api/state answers (VS Code starts pointed at dead :$DEAD)" \
  || { fail "controller not answering on :$PORT"; exit 1; }

smoke_open "$PROJ" sim.st
key Escape
wait_for 20 test -s "$NAUTILUS_TEST_STATE" || { fail "no test-state snapshot at $NAUTILUS_TEST_STATE (NAUTILUS_TEST_STATE not honoured?)"; exit 1; }
wait_for 10 ts_true 's["cliVersion"]' || true
clear_toasts

# ── C03 Show CLI Info ───────────────────────────────────────────────────────
WANT_VER=$(naut version | sed -nE 's/^[[:space:]]*(nautilus|naut)[[:space:]]+([^[:space:]]+).*/\2/p' | head -1)
WANT_PATH=$SMOKE_BIN/naut
vs_cmd "nautilus: Show CLI Info" 1
wait_for 8 has_toast 'naut .* at ' || true
msg=$(toasts | grep -m1 'naut .* at ' || true)
png=$(shot cli-info)
if [[ -n $WANT_VER && $msg == *"naut $WANT_VER at $WANT_PATH"* ]]; then
  pass "C03 Show CLI Info names $WANT_PATH and version '$WANT_VER' (= naut version): \"$msg\"" "$png"
else
  fail "C03 Show CLI Info: want \"naut $WANT_VER at $WANT_PATH\", toast says \"${msg:-<no toast>}\"" "$png"
fi
sv=$(ts 's["cliVersion"]'); sp=$(ts 's["cliPath"]')
[[ $sv == "$WANT_VER" && $sp == "$WANT_PATH" ]] && info "C03 test state agrees: cliPath=$sp cliVersion=$sv" \
  || warn "C03 test state disagrees with naut version: cliPath=$sp cliVersion=$sv (want $WANT_PATH / $WANT_VER)"
clear_toasts

# ── C04 Restart Language Server ─────────────────────────────────────────────
# The extension itself logs nothing on a restart (extension.ts just stops the
# client and starts a new one); what lands in the logs is the language
# client's own channel, "nautilus Structured Text", where the old client
# records its server's exit — vscode-languageclient logs every exit at
# [Error], a clean one included ("Server process exited with code 0.").
EXITED='Server process exited with code 0.'
wait_for 15 test -n "$(lsp_pids)" || true
pid0=$(lsp_pids)
exits0=$(lsp_log | grep -cF -- "$EXITED" || true)
e0=$(errors)
info "C04 before restart: naut lsp pid(s) $pid0; problems '$(problems)'; '$EXITED' logged $exits0 time(s)"
vs_cmd "nautilus: Restart Language Server" 4
new_lsp() { local p; p=$(lsp_pids); [[ -n $p && $p != "$pid0" ]] && ! grep -qw -- "${pid0:-x}" <<<"$p"; }
wait_for 15 new_lsp || true
pid1=$(lsp_pids)
exits1=$(lsp_log | grep -cF -- "$EXITED" || true)
line=$(lsp_log | grep -F -- "$EXITED" | tail -1)
png=$(shot lsp-restarted)
if [[ -n $pid0 && -n $pid1 && $pid1 != "$pid0" ]] && ! grep -qw -- "$pid0" <<<"$pid1" && (( exits1 > exits0 )); then
  pass "C04 Restart Language Server: naut lsp pid $pid0 → $pid1 (old one gone); the client logged \"$line\"" "$png"
else
  fail "C04 Restart Language Server: naut lsp pid(s) before '$pid0', after '$pid1'; '$EXITED' logged $exits0 → $exits1 time(s)" "$png"
fi
# Each restart builds a new LanguageClient, which makes a new output channel
# of the same name and never disposes the old one: count them in the Output
# view's channel picker.
vs_cmd "Output: Show Output Channels" 2
chans=$(pg '[...document.querySelectorAll(".quick-input-list .monaco-list-row")].map(r => r.getAttribute("aria-label")).join(" ; ")')
png=$(shot output-channels)
key Escape
n=$(grep -o 'nautilus Structured Text' <<<"$chans" | wc -l)
if (( n > 1 )); then
  warn "C04 after one restart the Output picker lists $n 'nautilus Structured Text' channels (the old client's is never disposed; one more per restart)" "$png"
else
  info "C04 Output picker: $n 'nautilus Structured Text' channel(s) ($chans)" "$png"
fi

# A typo made AFTER the restart: only a live server can flag it.
text_replace "$PROJ/sim.st" "LevelPct := LIMIT" "LevelPct := LIMIT_TYPO"
has_errors() { (( $(errors) > 0 )); }
wait_for 15 has_errors || true
e1=$(errors)
png=$(shot lsp-diagnostic)
if (( e1 > 0 )); then
  pass "C04 after the restart a typo (LIMIT_TYPO) still gets a diagnostic: problems '$(problems)' (was $e0)" "$png"
else
  fail "C04 after the restart a typo (LIMIT_TYPO) gets no diagnostic: problems '$(problems)'" "$png"
fi
vs_cmd "File: Revert File" 2
clear_toasts

# ── C05 Connect to Controller ───────────────────────────────────────────────
# connect <url> — the palette command, the URL typed into its quick pick.
connect() {
  vs_cmd "nautilus: Connect to Controller" 1.5
  xdotool type --delay 30 "$1"; sleep 0.8
  xdotool key Return; sleep 1
}
info "C05 before: $(state_line)"
connect "$LIVE_URL"
wait_for 10 ts_true "s['runtimeUrl'] == '$LIVE_URL' and s['connected']" || true
png=$(shot connect-live)
if ts_true "s['runtimeUrl'] == '$LIVE_URL' and s['connected']"; then
  pass "C05 Connect to $LIVE_URL: test state $(state_line)" "$png"
else
  fail "C05 Connect to $LIVE_URL: not connected within 10 s — $(state_line)" "$png"
fi
info "C05 notification: $(ts 's["lastNotification"]')"

# ── C07 Live Values view: Refresh ───────────────────────────────────────────
vs_cmd "nautilus: Focus on Live Values View" 3
# tree_row <tag> — the Live Values tree row for <tag>, as the DOM shows it.
tree_row() {
  pg "(() => { const v = document.querySelector('[id=\"workbench.view.extension.nautilus\"]') || document; const r = [...v.querySelectorAll('.monaco-list-row')].find(r => r.querySelector('.label-name')?.textContent.trim() === '$1'); return r ? (r.getAttribute('aria-label') || '') + ' | ' + r.textContent.trim() : ''; })()"
}
TAG=PumpStopLevel NEWV=77.25
wait_for 10 test -n "$(tree_row $TAG)" || true
before=$(tree_row $TAG)
info "C07 before the write: '$before'"
api_post /api/tags "{\"name\":\"$TAG\",\"value\":$NEWV}" >/dev/null && info "C07 POST /api/tags $TAG=$NEWV: $(api /api/state | python3 -c "import json,sys; print(json.load(sys.stdin)['tags'].get('$TAG'))" 2>/dev/null)" \
  || fail "C07 POST /api/tags $TAG=$NEWV refused"
REFRESH="[...document.querySelectorAll('.action-label')].find(a => /Refresh Live Values/.test(a.getAttribute('aria-label') || a.title || '') && a.offsetParent !== null)"
box=$(page_el_box "$REFRESH" || true)
if [[ -n $box ]]; then
  read -r x y w h <<<"$box"
  g_click $((x + w / 2)) $((y + h / 2)) 1.5
  how="the view's title-bar Refresh button"
else
  warn "C07 no visible Refresh button in the Live Values title bar — ran nautilus.liveValues.refresh through the palette instead"
  vs_cmd "nautilus: Refresh Live Values" 2
  how="the palette"
fi
row_has() { [[ $(tree_row "$1") == *"$2"* ]]; }
wait_for 5 row_has $TAG $NEWV || true
after=$(tree_row $TAG)
png=$(shot live-values-refresh)
if [[ $after == *"$NEWV"* ]]; then
  pass "C07 Refresh ($how): Live Values shows $TAG = $NEWV ('$after')" "$png"
else
  fail "C07 Refresh ($how): Live Values row for $TAG is '$after' (want $NEWV)" "$png"
fi

# The tree also re-renders from the live stream (liveValuesView.ts coalesces
# frames to twice a second), and Refresh only re-fires the same change event,
# so a correct value after the click shows the click ran and the view is
# current, not that the click alone brought the value in.
info "C07 note: the tree also follows the stream (~500 ms), so this row proves the button runs and the view is current, not that Refresh alone fetched $NEWV"
# The title bar's other button, Connect: a command with no icon renders as
# its whole title, in text.
cb=$(pg "(() => { const a = [...document.querySelectorAll('.action-label')].find(a => /Connect to Controller/.test(a.getAttribute('aria-label') || '') && a.offsetParent !== null); return a ? Math.round(a.getBoundingClientRect().width) + 'px ' + JSON.stringify(a.textContent.trim()) + ' codicon=' + /codicon/.test(a.className) : ''; })()")
if [[ $cb == *codicon=false* ]]; then
  warn "C05/C07 the Live Values title bar's Connect button has no icon: it renders as text, $cb, and squeezes the view title (package.json nautilus.connect has no \"icon\")" "$png"
else
  info "C05/C07 the Live Values title bar's Connect button: ${cb:-not found}" "$png"
fi

# ── C05 again: a dead port ──────────────────────────────────────────────────
key Escape
open_file sim.st 2      # the live status item shows while an ST editor is open
connect "$DEAD_URL"
wait_for 10 ts_true "s['runtimeUrl'] == '$DEAD_URL' and not s['connected']" || true
sleep 1
sb=$(statusbar_text 'nautilus: (live|offline)')
png=$(shot connect-dead)
if ts_true "s['runtimeUrl'] == '$DEAD_URL' and not s['connected']" && [[ $(live_item) == *offline* && $sb == *offline* ]]; then
  pass "C05 Connect to dead $DEAD_URL: connected false, status bar '$sb' — $(state_line)" "$png"
else
  fail "C05 Connect to dead $DEAD_URL: $(state_line); status bar DOM '$sb'" "$png"
fi
info "C05 notification: $(ts 's["lastNotification"]')"
cp "$NAUTILUS_TEST_STATE" "$OUT_DIR/$CHECK-state.json" 2>/dev/null || true
