#!/usr/bin/env bash
# 18 — the Logix deploy target's rules as live diagnostics (`naut lsp`,
# internal/lsp/logix.go), in a real editor:
#
#   A project whose nautilus.yaml has `target: logix` gets extra Error
#   diagnostics, source "nautilus (logix target)", code = the rule ID, each
#   on a whole line, once the file compiles. logix-tp's Main.st is clean
#   nautilus ST with a TP instance, which the v1 subset leaves out:
#   line 7 `pulse : TP;` -> logix/type, line 9 `pulse(IN := ...)` -> logix/fb
#   (host-side `naut check` says the same two lines; asserted first).
#
#   rows: the editor squiggles those lines; the status bar counts 2 errors;
#   the hover on the squiggled line carries the message; the Problems panel
#   lists logix/type and logix/fb with the source; with the `target:` section
#   removed from nautilus.yaml and the buffer touched, the diagnostics go.
#
# Read from the workbench DOM (.squiggly-error, the hover widget, the
# Problems tree rows, the status bar). No pixels. Edits are made and undone,
# never saved.
set -euo pipefail
CHECK=18-logix-target
source "$HOME/smoke/lib.sh"
source "$HOME/fixtures/gestures.sh"
rm -rf "$PROFILE"

ext_fixture logix-tp
F=$PROJ/Main.st
want_check=$(cd "$PROJ" && naut check 2>&1 || true)
if [[ $want_check == *"[logix/type]"* && $want_check == *"[logix/fb]"* ]]; then
  pass "host-side naut check reports logix/type and logix/fb"
else fail "naut check on the fixture lacks the logix rules: $want_check"; exit 1; fi

wb() { cdp page "$1" 2>/dev/null | python3 -c 'import sys, json; v = json.load(sys.stdin); print(v if isinstance(v, str) else json.dumps(v))'; }
squiggles() { local n; n=$(wb 'String(document.querySelectorAll(".monaco-editor .squiggly-error").length)'); [[ $n =~ ^[0-9]+$ ]] && echo "$n" || echo -1; }
errors() {
  local t; t=$(wb '(() => { const e = document.getElementById("status.problems"); return e ? (e.querySelector("a")?.getAttribute("aria-label") || "") + " | " + e.innerText.replace(/\s+/g, " ") : ""; })()')
  local n; n=$(sed 's/.*| //' <<<"$t" | grep -oE '[0-9]+' | head -1); echo "${n:--1}"
}
hover_text() {
  wb '[...document.querySelectorAll(".monaco-hover")].filter(e => !e.classList.contains("hidden") && e.getBoundingClientRect().height > 0 && e.innerText.trim()).map(e => e.innerText.trim().replace(/\s*\n\s*/g, " / ")).join(" ¦ ")'
}
# problem_rows — the Problems panel's rows, one per line (text content).
problem_rows() {
  wb '[...document.querySelectorAll(".markers-panel .monaco-list-row, .panel .markers-panel .monaco-tl-row")].map(r => r.innerText.replace(/\s*\n\s*/g, " ").trim()).filter(Boolean).join("\n")'
}
sq_is_2() { (( $(squiggles) == 2 )); }
sq_is_0() { (( $(squiggles) == 0 )); }
has_fb_row() { problem_rows | grep -q logix/fb; }
goto() { xdotool key --clearmodifiers ctrl+g; sleep 0.6; xdotool type "$1:$2"; sleep 0.3; xdotool key Return; sleep 0.5; }
show_hover() {
  local i h
  for i in 1 2 3 4 5 6; do
    xdotool key --clearmodifiers Escape; sleep 0.3
    xdotool key --clearmodifiers ctrl+k ctrl+i; sleep 1.5
    h=$(hover_text); [[ -n $h ]] && { echo "$h"; return 0; }
    sleep 1.5
  done
  return 1
}

smoke_open "$PROJ" Main.st
key Escape; hide_sidebar
sleep 3

# ── squiggles and the count ────────────────────────────────────────────────
t0=$SECONDS
wait_for 40 sq_is_2 || true
png=$(shot squiggles)
n=$(squiggles); e=$(errors)
if (( n == 2 )); then pass "the editor squiggles the two Logix lines ($n .squiggly-error, after ~$((SECONDS - t0)) s)" "$png"
else fail "expected 2 squiggles in Main.st, found $n" "$png"; fi
if (( e == 2 )); then pass "the status bar counts 2 errors" "$png"
else fail "status bar error count is $e, want 2" "$png"; fi

# ── hover on the squiggled FB call (line 9) ────────────────────────────────
goto 9 3
if h=$(show_hover); then
  png=$(shot hover)
  if [[ $h == *"Logix v1 subset"* && $h == *logix/fb* ]]; then pass "hover on line 9 carries the rule message — \"${h:0:160}\"" "$png"
  elif [[ $h == *"Logix v1 subset"* ]]; then pass "hover on line 9 carries the rule message (no code in the hover) — \"${h:0:160}\"" "$png"
  else fail "hover on line 9 lacks the Logix message: \"$h\"" "$png"; fi
else png=$(shot hover); fail "no hover on the squiggled line 9" "$png"; fi
xdotool key --clearmodifiers Escape

# ── the Problems panel ─────────────────────────────────────────────────────
xdotool key --clearmodifiers ctrl+shift+m; sleep 2.5
wait_for 8 has_fb_row || true
rows=$(problem_rows)
png=$(shot problems)
if grep -q 'logix/fb' <<<"$rows" && grep -q 'logix/type' <<<"$rows"; then pass "Problems lists logix/type and logix/fb — $(sed 's/ *$//' <<<"$rows" | paste -sd'|' | sed 's/|/; /g' | cut -c1-300)" "$png"
else fail "Problems panel lacks the rule codes: \"$(paste -sd'|' <<<"$rows")\"" "$png"; fi
if grep -q 'nautilus (logix target)' <<<"$rows"; then pass "Problems rows name the source 'nautilus (logix target)'" "$png"
else fail "Problems rows lack the source 'nautilus (logix target)'" "$png"; fi
xdotool key --clearmodifiers ctrl+shift+m; sleep 1

# ── drop the target: section, touch the buffer: the diagnostics go ─────────
python3 - "$PROJ/nautilus.yaml" <<'PY'
import sys
p = sys.argv[1]; s = open(p).read()
open(p, "w").write(s[:s.index("target:")])
PY
goto 11 1
xdotool key --clearmodifiers ctrl+End; sleep 0.3
xdotool type " "; sleep 0.6; xdotool key --clearmodifiers ctrl+z; sleep 0.6
if wait_for 20 sq_is_0; then
  png=$(shot target-removed); pass "with target: removed from nautilus.yaml the squiggles are gone" "$png"
else
  png=$(shot target-removed); warn "squiggles still $(squiggles) 20 s after removing target: and touching the buffer (re-analysis may need a manifest reload)" "$png"
fi
