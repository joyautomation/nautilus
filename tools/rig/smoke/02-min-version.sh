#!/usr/bin/env bash
# 02 — min-version warning: an old naut (says 0.10.0; the extension needs
# package.json nautilusCli.minVersion) earliest on PATH. The warning offers
# "Update naut" and "Don't show again for this version"; Update explains how
# that copy was installed and offers the managed install; Don't-show-again
# persists per version (globalState) and silences the next session.
#
# The warning and its buttons are found in the workbench DOM (the
# notification centre's rows), not off the frame.
set -euo pipefail
CHECK=02-min-version
source "$HOME/smoke/lib.sh"
export G_PACE=fast
source "$HOME/fixtures/gestures.sh"   # cdp page / page_click_button
OLD_RE='naut 0\.10\.0 is older than this extension needs'

rm -rf "$PROFILE"; mkdir -p "$HOME/fake-bin"
cat >"$HOME/fake-bin/naut" <<SH
#!/bin/sh
# an old naut: it SAYS 0.10.0, and is the real CLI for everything else (lsp)
if [ "\$1" = version ]; then echo "nautilus 0.10.0"; exit 0; fi
exec $SMOKE_BIN/naut "\$@"
SH
chmod +x "$HOME/fake-bin/naut"
export PATH=$HOME/fake-bin:$PATH
[[ $(naut version) == "nautilus 0.10.0" ]] || { fail "fake naut not first on PATH"; exit 1; }
# The minimum under test, from the VSIX run.sh installed (not hard-coded: it
# moved 0.11.0 → 0.12.0 in 5f045a0 and the check went red on the rig).
MIN=$(python3 -c 'import json,sys,zipfile; print(json.load(zipfile.ZipFile(sys.argv[1]).open("extension/package.json"))["nautilusCli"]["minVersion"])' /tmp/ext.vsix)
[[ $MIN =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { fail "could not read nautilusCli.minVersion from the VSIX ($MIN)"; exit 1; }

ext_scaffold my-plant
smoke_open "$PROJ" sim.st
sleep 6
if ext_log | grep -q "fake-bin/naut .*version 0.10.0; this extension needs ≥ $MIN"; then
  pass "extension resolved the fake naut first and read its version (0.10.0 < $MIN)"
else
  fail "the old naut was not the one resolved: $(ext_log | grep 'nautilus CLI:' | tail -1)"
fi

# Startup toasts auto-hide (~15 s) before the window settles — the centre
# holds the same notification with the same buttons.
vs_cmd "Notifications: Show Notifications" 1.5
wait_for 10 notification "$OLD_RE" >/dev/null || true
n=$(notification "$OLD_RE" || true)
png=$(shot warning)
[[ $n == warning$'\t'*"($MIN)"*$'\t'"Update naut / Don't show again for this version" ]] \
  && pass "min-version warning present (Update naut / Don't show again for this version)" "$png" \
  || fail "no warning in the notification centre (notifications: $(notifications | tr '\t\n' '|;'))" "$png"

page_click_button '^Update naut$' 2 || fail "no 'Update naut' button on the min-version warning"
png=$(shot update-followup)
fu=$(notifications | awk -F'\t' -v re="$OLD_RE" '$2 !~ re { print $2 " [" $3 "]"; exit }')
info "Update naut → follow-up names how this naut was installed and offers the managed copy (DOM: ${fu:-no other notification})" "$png"

# "Don't show again for this version", in the next session.
vs_cmd "Notifications: Clear All Notifications" 1
vs_cmd "Developer: Reload Window" 10
vs_cmd "Notifications: Show Notifications" 1.5
wait_for 10 notification "$OLD_RE" >/dev/null || true
shot warning-again >/dev/null
page_click_button "^Don't show again for this version\$" 2 || fail "no 'Don't show again for this version' button after the reload"
if gstate | grep -qF "\"nautilus.cli.skipVersionWarning\":\"0.10.0<$MIN\""; then
  pass "Don't show again → globalState skipVersionWarning = 0.10.0<$MIN"
else
  fail "Don't show again did not persist: $(gstate)"
fi
vs_cmd "Developer: Reload Window" 10
vs_cmd "Notifications: Show Notifications" 1.5
# The extension checks the version once the window is up; give it the time
# the warning took to appear above before calling it silenced.
sleep 4
png=$(shot silenced)
n=$(notification "$OLD_RE" || true)
[[ -z $n ]] && pass "next session: no min-version warning" "$png" || fail "warning shown again after Don't show again ($n)" "$png"
