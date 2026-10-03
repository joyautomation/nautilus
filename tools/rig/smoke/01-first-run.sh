#!/usr/bin/env bash
# 01 — first run: clean activation, the walkthrough opens once (and only
# outside a nautilus project), and the missing-CLI prompt's "Install naut"
# downloads the latest release into globalStorage/bin, after which the
# language server runs and a broken .st gets a diagnostic.
#
# Needs the container's network (api.github.com); without it the install
# half is a SKIP and naut goes back on PATH for the rest.
set -euo pipefail
CHECK=01-first-run
source "$HOME/smoke/lib.sh"

rm -rf "$PROFILE" "$HOME/scratch"; mkdir -p "$HOME/scratch"
printf 'PROGRAM Broken\nVAR x : INT; END_VAR\nx := x + ;\nEND_PROGRAM\n' >"$HOME/scratch/broken.st"

# No naut anywhere the extension looks: off PATH (run.sh keeps it out of
# /usr/local/bin, ~/go/bin and ~/.local/bin too).
no_naut
if command -v naut >/dev/null; then fail "naut is still resolvable: $(command -v naut)"; exit 1; fi
MANAGED=$PROFILE/User/globalStorage/joyauto.vscode-iec/bin/naut

# The language client's trace is the programmatic way to see diagnostics.
EXTRA_SETTINGS='"nautilus-st.trace.server": "verbose"' smoke_open "$HOME/scratch" broken.st
sleep 8

# ── walkthrough: opened once, into a workspace with no nautilus.yaml ────────
png=$(shot walkthrough)
check "walkthrough auto-opened on first activation (no nautilus.yaml)" "$png" \
  wait_for 10 bash -c "[[ \$(xdotool getwindowname $WIN) == Welcome* ]]"
python3 - "$PROFILE/User/globalStorage/state.vscdb" <<'PY' && pass "globalState nautilus.walkthroughShown = true" || fail "globalState walkthroughShown flag not set"
import sqlite3, sys, json
v = sqlite3.connect(sys.argv[1]).execute("select value from ItemTable where key='joyauto.vscode-iec'").fetchone()
sys.exit(0 if v and json.loads(v[0]).get("nautilus.walkthroughShown") else 1)
PY

# ── clean activation ───────────────────────────────────────────────────────
if exthost_log | grep -q "_doActivateExtension joyauto.vscode-iec"; then
  pass "extension activated (onLanguage:iec-st)"
else
  fail "extension never activated"
fi
errs=$(exthost_log | grep -i '\[error\]' | grep -v DeprecationWarning || true)
if [[ -z $errs ]]; then pass "exthost.log: no errors on activation"; else fail "exthost.log errors: $(head -3 <<<"$errs" | tr '\n' ' ')"; fi
if ext_log | grep -q 'not found. Looked in'; then pass "nautilus log: CLI not found, search path logged"; else fail "nautilus log has no not-found entry"; fi

# ── the missing-CLI prompt ─────────────────────────────────────────────────
# At startup the warning lands in the notification CENTER with no toast (the
# bell gets its dot) — see the report; so open the centre to reach it.
vs_cmd "Notifications: Show Notifications" 1.5
png=$(shot missing-cli-prompt)
if (( $(px_count "$png" $NOTIF_BOX "$AMBER") > 25 )); then
  pass "missing-CLI warning is up, offering Install naut / Locate naut… / Install steps (in the centre: startup toasts auto-hide before the window settles)" "$png"
else
  fail "no missing-CLI warning in the notification centre" "$png"
fi

if ! getent hosts api.github.com >/dev/null; then
  skip "Install naut: no network in the container — naut put back on PATH"
  export PATH=$SMOKE_BIN:$PATH
  exit 0
fi

# "Install naut" is the toast's primary button: the first of three, at the
# centre's bottom-right. Measured at 1920x1200, side bar visible.
click_at 1598 1131 1
if ! wait_for 120 test -x "$MANAGED"; then
  png=$(shot install-failed)
  fail "Install naut: nothing landed in globalStorage/bin in 120 s" "$png"
  exit 1
fi
sleep 4
png=$(shot installed)
pass "Install naut → $(basename "$(dirname "$(dirname "$MANAGED")")")/bin/naut ($("$MANAGED" version))" "$png"
check "language server running from the managed copy" "" \
  wait_for 20 pgrep -f "$MANAGED lsp" >/dev/null

# ── diagnostics on the broken file ─────────────────────────────────────────
open_file broken.st 3
lsp_log() { cat "$(ls -td "$PROFILE"/logs/*/ | head -1)"window*/exthost/output_logging_*/*"nautilus Structured Text.log" 2>/dev/null; }
has_diag() { lsp_log | python3 -c '
import sys, re
t = sys.stdin.read()
# a publishDiagnostics for broken.st with a non-empty diagnostics array
for m in re.finditer(r"publishDiagnostics.*?Params: (\{.*?\n\})", t, re.S):
    if "broken.st" in m.group(1) and re.search(r"\"diagnostics\": \[\s*\{", m.group(1)):
        sys.exit(0)
sys.exit(1)'; }
sleep 3
png=$(shot diagnostics)
if wait_for 20 has_diag; then
  pass "publishDiagnostics for broken.st carries a diagnostic (squiggle on line 3)" "$png"
else
  fail "no diagnostic published for broken.st" "$png"
fi

# ── once, not on reload ────────────────────────────────────────────────────
# Close the walkthrough (reload restores open editors) and reload the window.
vs_cmd "View: Close All Editors" 1
activations() { exthost_log | grep -c "_doActivateExtension joyauto.vscode-iec" || true; }
before=$(activations)
vs_cmd "Developer: Reload Window" 12
# The scratch folder has no nautilus.yaml, so the extension re-activates only
# once an .st is open again — count after that.
open_file broken.st 4
sleep 3
after=$(activations)
(( after > before )) && pass "window reloaded (extension activated again: $before → $after)" || fail "reload did not happen ($before → $after activations)"
png=$(shot after-reload)
if [[ $(title) == Welcome* ]] || xdotool search --name '^Welcome' >/dev/null 2>&1; then
  fail "walkthrough opened again after reload" "$png"
else
  pass "walkthrough did not reopen after reload" "$png"
fi

# ── never into an existing project ─────────────────────────────────────────
export PATH=$SMOKE_BIN:$PATH
PROFILE=$HOME/.vscode-rec-01b
rm -rf "$PROFILE"
ext_scaffold my-plant
smoke_open "$PROJ" program.fbd
sleep 8
png=$(shot project-no-walkthrough)
if [[ $(title) == program.fbd* ]]; then
  pass "fresh profile on a nautilus project: no walkthrough" "$png"
else
  fail "fresh profile on a nautilus project opened: $(title)" "$png"
fi
