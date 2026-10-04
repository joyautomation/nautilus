#!/usr/bin/env bash
# 02 — min-version warning: an old naut (says 0.10.0; the extension needs
# package.json nautilusCli.minVersion) earliest on PATH. The warning offers
# "Update naut" and "Don't show again for this version"; Update explains how
# that copy was installed and offers the managed install; Don't-show-again
# persists per version (globalState) and silences the next session.
set -euo pipefail
CHECK=02-min-version
source "$HOME/smoke/lib.sh"

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
png=$(shot warning)
n=$(px_count "$png" $NOTIF_BOX "$AMBER")
(( n > 25 )) && pass "min-version warning present (Update naut / Don't show again for this version)" "$png" \
             || fail "no warning in the notification centre" "$png"

# "Update naut" — measured at 1920x1200: the primary button.
click_at 1577 1131 2
png=$(shot update-followup)
info "Update naut → follow-up names how this naut was installed and offers the managed copy" "$png"

# "Don't show again for this version", in the next session.
vs_cmd "Notifications: Clear All Notifications" 1
vs_cmd "Developer: Reload Window" 10
vs_cmd "Notifications: Show Notifications" 1.5
shot warning-again >/dev/null
click_at 1768 1131 2
if gstate | grep -qF "\"nautilus.cli.skipVersionWarning\":\"0.10.0<$MIN\""; then
  pass "Don't show again → globalState skipVersionWarning = 0.10.0<$MIN"
else
  fail "Don't show again did not persist: $(gstate)"
fi
vs_cmd "Developer: Reload Window" 10
vs_cmd "Notifications: Show Notifications" 1.5
png=$(shot silenced)
n=$(px_count "$png" $NOTIF_BOX "$AMBER")
(( n < 10 )) && pass "next session: no min-version warning" "$png" || fail "warning shown again after Don't show again" "$png"
