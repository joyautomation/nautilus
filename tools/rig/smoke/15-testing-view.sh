#!/usr/bin/env bash
# 15 — *_test.yaml suites in the Testing view (acceptanceTests.ts, the
# `nautilusAcceptance` TestController; INVENTORY X19).
#
#   Discovery: the tree lists the suite and every test in it, the same names
#     and count `naut test -list` prints for the project.
#   Run all (the view's Run Tests button): every row turns Passed.
#   Gutter: a run glyph beside each `- name:` line; clicking one re-runs
#     only that test (`naut test -json -run ^(<that name>)$`, a 1/1 summary).
#   Failure inline: a wrong expectation, saved and run → that test Failed in
#     the tree, a failure widget in the yaml on the line naut reports, the tag
#     value in it (or at least in the peek). Revert → green, and the file is
#     byte-identical to where it started.
#   Watcher: a test added to the yaml and saved appears in the tree with no
#     manual refresh (the FileSystemWatcher on **/*_test.yaml).
#
# The project is `naut new`'s scaffold, whose my-plant_test.yaml has four
# tests that run in milliseconds of wall time (`naut test` is virtual time).
# Everything is read from the WORKBENCH page's DOM through cdp.js (tree rows'
# aria-labels, the glyph margin, the failure content widget, the peek) — no
# pixels — and the CLI is wrapped in a shim that logs every `naut test` the
# extension runs, so "only that test re-ran" is read from the command line.
set -euo pipefail
CHECK=15-testing-view
source "$HOME/smoke/lib.sh"
source "$HOME/fixtures/gestures.sh"
G_PACE=${G_PACE:-fast}
rm -rf "$PROFILE"

ext_scaffold my-plant
SUITE=my-plant_test.yaml
F=$PROJ/$SUITE
sum0=$(sum "$F")

# The shim: first on PATH (the extension resolves naut from PATH first,
# cliResolve.ts), logs each `naut test …` command line, then runs the CLI
# under test. NB: no "vscode-rec" anywhere in here (launch_vscode pkills it).
NLOG=$HOME/naut-test.log
mkdir -p "$HOME/shim"
cat >"$HOME/shim/naut" <<SH
#!/bin/sh
[ "\$1" = test ] && printf '%s\n' "\$*" >>"$NLOG"
exec $SMOKE_BIN/naut "\$@"
SH
chmod +x "$HOME/shim/naut"; : >"$NLOG"
export PATH=$HOME/shim:$PATH

# ── helpers (workbench DOM, via cdp.js page) ────────────────────────────────
pyj() { python3 -c "import json,sys; d=json.loads(sys.argv[1]); print($2)" "$1"; }
# The Testing view's rows: aria-label is "<label> (<State>)[, in <duration>]".
ROWS_JS='[...document.querySelectorAll(".test-explorer-tree .monaco-list-row")].map(r => { const a = r.getAttribute("aria-label") || ""; const m = a.match(/^(.*) \(([^()]*)\)(, in .*)?$/); return { name: m ? m[1] : a, state: m ? m[2] : "", dur: m && m[3] ? m[3].slice(5) : "", icon: ((r.querySelector(".computed-state") || {}).className || "").replace(/.*codicon-testing-([a-z]+)-icon.*/, "$1"), level: +r.getAttribute("aria-level"), expanded: r.getAttribute("aria-expanded") }; })'
rows() { cdp page "$ROWS_JS"; }
# leaves — the test rows (level 2), one "<state>\t<name>" per line.
leaves() { pyj "$(rows)" '"\n".join(r["state"] + "\t" + r["name"] for r in d if r["level"] == 2)'; }
state_of() { leaves | awk -F'\t' -v n="$1" '$2 == n {print $1}'; }
all_leaves() { local want=$1 n=$2; [[ $(leaves | awk -F'\t' -v w="$want" '$1 == w' | wc -l) -eq $n && $(leaves | wc -l) -eq $n ]]; }
# summary — the view's result line ("4/4": passed/total of the last run).
summary() { cdp page '(document.querySelector(".result-summary-container [custom-hover]") || {}).textContent || ""' | tr -d '"'; }
# click_page <js -> Element in the workbench page> — click its centre.
click_page() {
  local b x y w h; b=$(page_el_box "$1") || { g_err "not on the page: ${1:0:90}"; return 1; }
  read -r x y w h <<<"$b"; g_click $((x + w / 2)) $((y + h / 2)) "${2:-1}"
}
RUN_ALL_JS='[...document.querySelectorAll(".part.sidebar .action-label.codicon-testing-run-all-icon")].find(a => a.getBoundingClientRect().width > 0)'
# The editor's line numbers, to say which line a glyph or widget sits on.
LINES_JS='[...document.querySelectorAll(".editor-instance .margin-view-overlays .line-numbers")].map(e => { const r = e.getBoundingClientRect(); return { n: +e.textContent, t: r.top, b: r.bottom }; })'
_on_line='const lines = '"$LINES_JS"'; const lineOf = (el) => { const r = el.getBoundingClientRect(), cy = r.top + r.height / 2; const l = lines.find((l) => l.t <= cy && cy < l.b); return l ? l.n : 0; };'
# glyphs — the gutter's test glyphs in view, "<line>\t<icon>" per line.
glyphs() {
  pyj "$(cdp page "(() => { $_on_line return [...document.querySelectorAll('.editor-instance .glyph-margin-widgets .testing-run-glyph')].map((g) => [lineOf(g), g.className.replace(/.*codicon-testing-([a-z]+)-icon.*/, '\$1')]); })()")" '"\n".join("%d\t%s" % (l, i) for l, i in d)'
}
glyph_el() { printf '(() => { %s return [...document.querySelectorAll(".editor-instance .glyph-margin-widgets .testing-run-glyph")].find((g) => lineOf(g) === %d); })()' "$_on_line" "$1"; }
# widget — the inline failure (VS Code's test-error-content-widget): {line, text}.
widget() { cdp page "(() => { $_on_line const w = document.querySelector('.editor-instance .test-error-content-widget'); return w ? { line: lineOf(w), text: w.textContent.trim() } : null; })()"; }
WIDGET_JS='document.querySelector(".editor-instance .test-error-content-widget")'
# (the peek's body is a Monaco editor: its spaces are U+00A0 in the DOM)
peek_text() { pyj "$(cdp page '((document.querySelector(".editor-instance .test-output-peek") || {}).textContent || "").replace(/\u00a0/g, " ")')" d; }
peek_has() { [[ $(peek_text 2>/dev/null) == *"$1"* ]]; }
# runs — how many `naut test -json` / `-list` the extension has started.
runs() { grep -c -- "^test -json" "$NLOG" || true; }
lists() { grep -c -- "^test -list" "$NLOG" || true; }
# wait_run <runs before> — the extension started a new `naut test -json`;
# then give it a moment to land the results in the tree.
ran_since() { (( $(runs) > $1 )); }
listed_since() { (( $(lists) > $1 )); }
wait_run() { wait_for 20 ran_since "$1"; local rc=$?; sleep 2; return $rc; }
has_rows() { [[ $(cdp page 'document.querySelectorAll(".test-explorer-tree .monaco-list-row").length' 2>/dev/null) -ge 1 ]]; }
has_test() { leaves | cut -f2 | grep -qxF -- "$1"; }
widget_up() { [[ $(cdp page '!!document.querySelector(".editor-instance .test-error-content-widget")' 2>/dev/null) == true ]]; }
peek_up() { [[ $(cdp page '!!document.querySelector(".editor-instance .test-output-peek")' 2>/dev/null) == true ]]; }
# line_replace <file> <line> <old> <new> — lib.sh's text_replace on a given
# line (text_replace takes the FIRST match in the file).
line_replace() {
  local f=$1 l=$2 old=$3 new=$4 c i
  c=$(awk -v l="$l" -v s="$old" 'NR == l {print index($0, s)}' "$f")
  [[ ${c:-0} -gt 0 ]] || { g_err "line $l of $f has no '$old'"; return 1; }
  key ctrl+1
  xdotool key --clearmodifiers ctrl+g; sleep 0.6
  xdotool type "$l:$c"; sleep 0.3; xdotool key Return; sleep 0.4
  for ((i = 0; i < ${#old}; i++)); do xdotool key shift+Right; done
  xdotool type --delay 30 -- "$new"; sleep 0.6
  key Escape   # the completion popup the typed value opens
}
save_and_relist() { local l0; l0=$(lists); key ctrl+s; wait_for 10 listed_since "$l0" || true; sleep 1.5; }

# ── the CLI's own view of the suite (run in the container, no VS Code) ─────
LIST=$(cd "$PROJ" && "$SMOKE_BIN/naut" test -list .)
N=$(grep -c . <<<"$LIST")
mapfile -t NAMES < <(python3 -c 'import json,sys; [print(json.loads(l)["name"]) for l in sys.stdin if l.strip()]' <<<"$LIST")
mapfile -t LINES < <(python3 -c 'import json,sys; [print(json.loads(l)["line"]) for l in sys.stdin if l.strip()]' <<<"$LIST")
mapfile -t YAML_NAMES < <(sed -n 's/^  - name: //p' "$F")
info "naut test -list: $N tests in $SUITE (yaml has ${#YAML_NAMES[@]} '- name:' entries): $(IFS='|'; echo "${NAMES[*]}")"
(( N >= 3 )) || { fail "the scaffold's suite has $N tests (want ≥ 3)"; exit 1; }

EXTRA_SETTINGS='"editor.autoClosingBrackets": "never", "editor.autoClosingQuotes": "never", "editor.autoIndent": "none"' \
  smoke_open "$PROJ" "$SUITE"
key Escape; sleep 2
vs_cmd "Testing: Focus on Test Explorer View" 3

# ── discovery ───────────────────────────────────────────────────────────────
wait_for 30 has_rows || true
# The suite row starts collapsed: open it with its twistie, the way a person does.
if [[ $(pyj "$(rows)" 'next((r["expanded"] for r in d if r["level"] == 1), "")') == false ]]; then
  click_page '[...document.querySelectorAll(".test-explorer-tree .monaco-list-row")].find(r => r.getAttribute("aria-level") === "1").querySelector(".monaco-tl-twistie")' 1.5
fi
png=$(shot discovered)
R=$(rows)
suite_ok=$(pyj "$R" '"yes" if any(r["level"] == 1 and r["name"] == "'"$SUITE"'" for r in d) else "no"')
tree_names=$(pyj "$R" '"\n".join(r["name"] for r in d if r["level"] == 2)' | LC_ALL=C sort)
want_names=$(printf '%s\n' "${NAMES[@]}" | LC_ALL=C sort)
yaml_names=$(printf '%s\n' "${YAML_NAMES[@]}" | LC_ALL=C sort)
tn=$(grep -c . <<<"$tree_names" || true)
if [[ $suite_ok == yes && $tree_names == "$want_names" && $want_names == "$yaml_names" ]]; then
  pass "discovery: the tree lists $SUITE and all $tn tests, the same names as the yaml and as naut test -list ($N)" "$png"
else
  fail "discovery: suite row $suite_ok, tree $tn tests vs naut test -list $N / yaml ${#YAML_NAMES[@]} (tree: $(tr '\n' '|' <<<"$tree_names"))" "$png"
fi
info "tree rows before any run: $(pyj "$R" '"; ".join("%s (%s)" % (r["name"], r["state"]) for r in d)')"

# The gutter, before any run: each glyph in view sits on a `- name:` line.
G=$(glyphs)
bad=0; while IFS=$'\t' read -r l i; do [[ -z $l ]] && continue; sed -n "${l}p" "$F" | grep -q '^ *- name:' || bad=1; done <<<"$G"
if [[ -n $G && $bad == 0 ]] && grep -q "^${LINES[0]}"$'\t' <<<"$G"; then
  pass "gutter: run glyphs on lines $(cut -f1 <<<"$G" | paste -sd,) — each a '- name:' line (icons: $(cut -f2 <<<"$G" | sort -u | paste -sd,))" "$png"
else
  fail "gutter: glyphs [$(tr '\n' ' ' <<<"$G")] — want one on line ${LINES[0]} and only on '- name:' lines" "$png"
fi

# ── run all (the view's Run Tests button) ───────────────────────────────────
r0=$(runs)
click_page "$RUN_ALL_JS" 1; park "" "" 0.3
wait_run "$r0" || true
wait_for 20 all_leaves Passed "$N" || true
png=$(shot run-all)
S=$(summary)
if all_leaves Passed "$N" && [[ $S == "$N/$N" ]]; then
  pass "Run Tests: all $N rows Passed, summary $S (one naut test -json for all $N)" "$png"
else
  fail "Run Tests: rows [$(leaves | cut -f1 | sort | uniq -c | paste -sd' ')], summary '$S'" "$png"
fi
info "run-all command: $(grep '^test -json' "$NLOG" | tail -1)"
info "durations in the tree are VIRTUAL time ($(pyj "$(rows)" '"; ".join("%s: %s" % (r["name"][:28], r["dur"]) for r in d if r["level"] == 2)')) — the run itself took a few ms"

# ── gutter run: one test ────────────────────────────────────────────────────
T1=${NAMES[0]} L1=${LINES[0]}
key ctrl+1; key ctrl+Home; sleep 1
r0=$(runs)
if click_page "$(glyph_el "$L1")" 1; then
  wait_run "$r0" || true
  cmd=$(grep '^test -json' "$NLOG" | tail -1)
  S=$(summary)
  png=$(shot gutter-run)
  if (( $(runs) == r0 + 1 )) && [[ $cmd == "test -json -run ^($T1)\$ ." && $S == 1/1 && $(state_of "$T1") == Passed ]]; then
    pass "gutter click on line $L1 ran only '$T1' (naut $cmd; summary $S)" "$png"
  else
    fail "gutter click on line $L1: $(( $(runs) - r0 )) run(s), last '$cmd', summary '$S', state '$(state_of "$T1")'" "$png"
  fi
else
  png=$(shot gutter-run)
  fail "no gutter glyph to click on line $L1 (glyphs: $(glyphs | tr '\n' ' '))" "$png"
fi

# ── failure inline ──────────────────────────────────────────────────────────
# The first test's last step expects PumpRun false above the band; say true.
EL=$(grep -n 'expect: { PumpRun: false }' "$F" | head -1 | cut -d: -f1)
line_replace "$F" "$EL" "PumpRun: false" "PumpRun: true"
save_and_relist
grep -q 'PumpRun: true }' <(sed -n "${EL}p" "$F") || { fail "the edit on line $EL never reached disk"; exit 1; }
J=$(cd "$PROJ" && "$SMOKE_BIN/naut" test -json -run "^($T1)\$" . || true)
FL=$(pyj "$J" 'd["failure"]["line"]' 2>/dev/null || echo "?"); FD=$(pyj "$J" 'd["failure"]["detail"]' 2>/dev/null || echo "?")
info "after the edit (line $EL), naut test says: line $FL, '$FD'"
r0=$(runs)
click_page "$RUN_ALL_JS" 1; park "" "" 0.3
wait_run "$r0" || true
wait_for 15 widget_up || true
png=$(shot failed)
others=$(leaves | awk -F'\t' -v n="$T1" '$2 != n && $1 == "Passed"' | wc -l)
if [[ $(state_of "$T1") == Failed ]] && (( others == N - 1 )); then
  pass "the broken test is Failed in the tree, the other $others Passed (summary $(summary))" "$png"
else
  fail "after the edit: '$T1' is '$(state_of "$T1")', $others others Passed (want $((N - 1)))" "$png"
fi
W=$(widget)
if [[ $W == null || -z $W ]]; then
  fail "no inline failure message in the yaml editor" "$png"
else
  WL=$(pyj "$W" 'd["line"]'); WT=$(pyj "$W" 'd["text"]')
  [[ $WL == "$FL" ]] && pass "inline failure message on line $WL, the step naut reports ('$WT')" "$png" \
    || fail "inline failure message on line $WL (naut reports line $FL): '$WT'" "$png"
  if [[ $WT == *"$FD"* ]]; then
    pass "the inline message carries the tag value: '$FD'" "$png"
  else
    warn "the inline message reads '$WT' — the tag value ('$FD') is not in it, only in the peek; and it sits on the step's line $WL, not the expect on line $EL (README: 'a failure shows the step and tag value that broke, inline on the assertion'; #145)" "$png"
  fi
  click_page "$WIDGET_JS" 2
  wait_for 8 peek_up || true
  wait_for 8 peek_has "$FD" || true
  P=$(peek_text); png=$(shot peek)
  [[ $P == *"$FD"* ]] && pass "clicking it opens the peek with the actual value: '$FD'" "$png" \
    || fail "the peek does not show '$FD' (peek: ${P:0:160})" "$png"
  key Escape
fi

# ── revert → green, byte-identical ──────────────────────────────────────────
line_replace "$F" "$EL" "PumpRun: true" "PumpRun: false"
save_and_relist
r0=$(runs)
click_page "$RUN_ALL_JS" 1; park "" "" 0.3
wait_run "$r0" || true
wait_for 20 all_leaves Passed "$N" || true
png=$(shot reverted)
if all_leaves Passed "$N" && ! widget_up; then
  pass "reverted and re-run: all $N Passed again, the inline message gone (summary $(summary))" "$png"
else
  fail "after the revert: rows [$(leaves | cut -f1 | sort | uniq -c | paste -sd' ')], widget $(widget)" "$png"
fi
[[ $(sum "$F") == "$sum0" ]] && pass "$SUITE is byte-identical to the start ($sum0)" \
  || fail "$SUITE differs from the start after the revert: $(git -C "$PROJ" diff --stat -- "$SUITE" | tail -1)"

# ── watcher: a new test appears without a refresh ───────────────────────────
NEW="added by the smoke check"
l0=$(lists)
key ctrl+1; key ctrl+End
xdotool type --delay 20 -- "  - { name: $NEW, suspend: [sim], given: { LevelPct: 35.0 }, steps: [ { scans: 1, expect: { PumpRun: true } } ] }"
sleep 0.5; key Escape; key ctrl+s
wait_for 15 has_test "$NEW" || true
png=$(shot watcher)
NL=$(cd "$PROJ" && "$SMOKE_BIN/naut" test -list . | grep -c .)
if has_test "$NEW" && (( $(leaves | wc -l) == N + 1 )); then
  pass "watcher: saving a new test added '$NEW' to the tree ($((N + 1)) rows, naut test -list $NL) — no refresh ($(( $(lists) - l0 )) re-list(s) on save)" "$png"
else
  fail "watcher: the tree has $(leaves | wc -l) tests after the save (naut test -list $NL), no '$NEW'" "$png"
fi
