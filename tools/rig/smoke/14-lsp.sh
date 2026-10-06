#!/usr/bin/env bash
# 14 — language intelligence (INVENTORY X18, X20, X40, X41), in a real editor:
#
#   X18  `naut lsp` on plant.st: hover (a manifest tag, a VAR_EXTERNAL that
#        is not in the manifest, a local FB instance), completion (a partial
#        tag name; `inst.` lists the TON's pins — typed, and by Ctrl+Space
#        after the dot of a line that parses), go to definition (F12 on a
#        VAR_EXTERNAL tag and on a local), diagnostics (an undeclared name
#        squiggles and the status bar's error count rises; the hover on the
#        name explains it; undo clears it).
#
#        Two known product bugs are WARN rows, not FAILs: typed `inst.` lists
#        nothing (#139), and an undeclared name's problem sits on the
#        statement's first token, not on the name (#141). When either is
#        fixed its row turns PASS by itself.
#   X40  signature help: typing `LIMIT(` opens the parameter-hints widget
#        with LIMIT's three parameters, the first highlighted; typing
#        `0.0, ` moves the highlight to the second; `settle(IN := ` shows the
#        TON's pins (inputs, outputs after =>) with IN highlighted, and
#        `Full, PT := ` moves it to PT. Read from the widget's DOM
#        (.parameter-hints-widget, its .parameter.active span); Esc and
#        undo leave the buffer clean.
#   X42  find all references (Shift+F12 → the references peek): on the tag
#        Level, every program that binds it and the manifest — per file, the
#        same count as a host-side `grep -ow Level` of that file; on the
#        PROGRAM's local settle, only the PROGRAM's lines — not those of the
#        FB above it that declares its own settle.
#   X44/X45  (own block, near the end) a ladder diagram's contact: its
#        tooltip and second line carry the tag's description; Shift+F12 on
#        it opens the References view on the tag.
#   X20  the YAML schemas (package.json yamlValidation → Red Hat YAML): a
#        bogus key under a task / a test step is flagged; completion in a new
#        task offers program / scan / name, in a new test step given /
#        advance / expect. Last block: tags/*.yaml and alarms/*.yaml (a
#        misspelt key is an error; a new entry completes with the schema's keys).

#        advance / expect.
#   X40  document symbols (naut lsp textDocument/documentSymbol): the
#        Outline view lists plant.st's PROGRAM, its VAR sections and their
#        declarations; with the cursor on a declaration, a rung or a step the
#        breadcrumb bar shows the enclosing POU / section; Go to Symbol in
#        Editor (Ctrl+Shift+O) lists them. The same on interlock.ld (rungs)
#        and batch.sfc (steps, transitions, actions). The rig profile turns
#        breadcrumbs off; X40 turns them on (live) after the X18/X20 rows.
#
# Everything is read from the WORKBENCH page's DOM over CDP (gestures.sh
# `cdp page`): the hover widget's text, the suggest widget's rows (label +
# kind icon), the status bar's "Ln X, Col Y" and problems counts, the
# editor's .squiggly-error decorations. No pixels. Every edit is typed
# into the buffer and undone again, never saved; the check ends by asserting
# every buffer is clean and the project is byte-identical to its commit.
#
# tank-batch's plant.st has VAR_EXTERNAL tags but no FB instance, so the
# check gives it one (settle : TON, read as settle.Q), an FB ahead of
# the PROGRAM with its own local settle (for X41's scoping row), and a
# batch_test.yaml with a step, committed on top of the fixture before VS
# Code opens.

# check gives it one (settle : TON, read as settle.Q), a batch_test.yaml
# with a step and a ladder library (interlock.ld, for X40's rungs),
# committed on top of the fixture before VS Code opens.
set -euo pipefail
CHECK=14-lsp
source "$HOME/smoke/lib.sh"
source "$HOME/fixtures/gestures.sh"
rm -rf "$PROFILE"

ext_fixture tank-batch
F=$PROJ/plant.st
python3 - "$F" <<'PY'
import sys
p = sys.argv[1]; s = open(p).read()
s = s.replace("END_VAR\n", "END_VAR\nVAR\n    settle : TON;\n    Full   : BOOL;\nEND_VAR\n", 1)
s = s.replace("END_PROGRAM", "settle(IN := Level > 90.0, PT := T#2s);\nFull := settle.Q;\nEND_PROGRAM")
s = ("FUNCTION_BLOCK Debounce\nVAR_INPUT\n  Raw : BOOL;\nEND_VAR\nVAR_OUTPUT\n  Clean : BOOL;\nEND_VAR\n"
      "VAR\n  settle : TON;\nEND_VAR\nsettle(IN := Raw, PT := T#1s);\nClean := settle.Q;\nEND_FUNCTION_BLOCK\n\n") + s
open(p, "w").write(s)
PY
cat >"$PROJ/batch_test.yaml" <<'YAML'
tests:
  - name: the plant fills while the fill valve is open
    suspend: [main]
    steps:
      - given: { Level: 10.0, FillValve: true }
        advance: 2s
        expect: { Level: { gt: 10.0 } }
YAML
# tag + alarm files for the X20 tag/alarm rows at the end (not composed into
# nautilus.yaml: the schemas bind by path, tags/*.yaml and alarms/*.yaml).
mkdir -p "$PROJ/tags" "$PROJ/alarms"
cat >"$PROJ/tags/spare-tags.yaml" <<'YAML'
- name: SpareTemp
  role: state
  init: 0.0
- name: SpareFlag
  role: input
YAML
cat >"$PROJ/alarms/spare-alarms.yaml" <<'YAML'
- id: spare-high
  tag: SpareFlag
  priority: high
- id: spare-low
  tag: SpareFlag
  priority: low
YAML
git -C "$PROJ" add -A; git -C "$PROJ" commit -qm "smoke 14: an FB instance and a test step"

cat >"$PROJ/interlock.ld" <<'LADDER'
(* A start/stop seal-in, written as rungs: a PROGRAM-less .ld is a
   project library. *)
FUNCTION_BLOCK SealIn
VAR_INPUT
    Start : BOOL;
    Stop  : BOOL;
END_VAR
VAR_OUTPUT
    Run  : BOOL;
    Lamp : BOOL;
END_VAR
LD
  RUNG seal (* start/stop with seal-in *)
    [ Start | Run ] /Stop ( Run )

  RUNG lamp (* the run lamp follows the seal *)
    Run ( Lamp )
END_LD
END_FUNCTION_BLOCK
LADDER
git -C "$PROJ" add -A; git -C "$PROJ" commit -qm "smoke 14: an FB instance, a test step and a ladder library"
out=$(cd "$PROJ" && naut check 2>&1 | tail -1 || true)
[[ $out == *", 0 with errors"* ]] || { fail "the check's own fixture does not compile: $out"; exit 1; }

# ── workbench DOM ──────────────────────────────────────────────────────────
# wb <js → string> — evaluate in the workbench page, print the string raw.
wb() { cdp page "$1" 2>/dev/null | python3 -c 'import sys, json; v = json.load(sys.stdin); print(v if isinstance(v, str) else json.dumps(v))'; }
# hover_text — the visible editor hover's text ("" when none), one line.
hover_text() {
  wb '[...document.querySelectorAll(".monaco-hover")].filter(e => !e.classList.contains("hidden") && e.getBoundingClientRect().height > 0 && e.innerText.trim()).map(e => e.innerText.trim().replace(/\s*\n\s*/g, " / ")).join(" ¦ ")'
}
# suggest_rows — the open suggest widget's rows, one per line: "label [kind] detail".
suggest_rows() {
  wb '[...document.querySelectorAll(".suggest-widget.visible .monaco-list-row")].map(r => { const ic = r.querySelector(".suggest-icon"); const k = ic && (ic.className.match(/codicon-symbol-([a-z-]+)/) || [])[1]; return (r.querySelector(".label-name")?.innerText || r.getAttribute("aria-label") || "").trim() + " [" + (k || "?") + "] " + (r.querySelector(".details-label")?.innerText || "").trim(); }).join("\n")'
}
# suggest_has <label> [kind] — a row with exactly this label (and kind icon).
suggest_has() { suggest_rows | grep -qE "^$1 \[${2:-[a-z?-]+}\]"; }
# cursor_line — the status bar's "Ln X, Col Y", as X.
cursor_line() { wb 'document.getElementById("status.editor.selection")?.innerText || ""' | grep -oP 'Ln \K[0-9]+'; }
# problems — the status bar's problem counts as "errors warnings".
problems() {
  local t; t=$(wb '(() => { const e = document.getElementById("status.problems"); return e ? (e.querySelector("a")?.getAttribute("aria-label") || "") + " | " + e.innerText.replace(/\s+/g, " ") : ""; })()')
  local n; n=$(sed 's/.*| //' <<<"$t" | grep -oE '[0-9]+' | head -2 | paste -sd' ')
  echo "${n:-? ?}"
}
# errors / squiggles — a number always (-1 when the DOM did not answer), so
# the arithmetic below never meets a "?".
errors() { local p; p=$(problems); p=${p%% *}; [[ $p =~ ^[0-9]+$ ]] && echo "$p" || echo -1; }
squiggles() { local n; n=$(wb 'String(document.querySelectorAll(".monaco-editor .squiggly-error").length)'); [[ $n =~ ^[0-9]+$ ]] && echo "$n" || echo -1; }
has_squiggle() { (( $(squiggles) > 0 )); }
no_squiggle() { (( $(squiggles) == 0 )); }
has_rows() { [[ -n $(suggest_rows) ]]; }
# oneline — rows joined "a; b; c" for a results row.
oneline() { sed 's/ *$//' | paste -sd'|' | sed 's/|/; /g'; }
# dirty — the active editor has unsaved changes (the window title's "●").
dirty() { [[ $(title) == ●* ]]; }

# ── gestures ───────────────────────────────────────────────────────────────
# goto <line> <col> — Ctrl+G "line:col", as text_replace does.
goto() { xdotool key --clearmodifiers ctrl+g; sleep 0.6; xdotool type "$1:$2"; sleep 0.3; xdotool key Return; sleep 0.5; }
# line_of <file> <ERE> — the first matching line number.
line_of() { grep -nE -m1 -- "$2" "$1" | cut -d: -f1; }
# goto_word <file> <ERE for the line> <word> — the cursor inside <word>
# (one char in), on the first line matching the ERE.
goto_word() {
  local l c
  l=$(line_of "$1" "$2"); [[ -n $l ]] || { echo "  goto_word: no line /$2/ in $1" >&2; return 1; }
  c=$(awk -v l="$l" -v s="$3" 'NR==l{print index($0,s)}' "$1")
  goto "$l" $((c + 1))
}
# show_hover — editor.action.showHover (Ctrl+K Ctrl+I), until the widget has
# text (the language server may still be starting on the first one).
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
# undo_clean — Escape, then Ctrl+Z until the buffer is back at its saved
# version (the title loses its ●). Status 1 if 30 undos did not get there.
undo_clean() {
  local i
  xdotool key --clearmodifiers Escape; sleep 0.3
  for i in $(seq 30); do dirty || return 0; xdotool key --clearmodifiers ctrl+z; sleep 0.25; done
  sleep 0.5; ! dirty
}
# new_line_below <file> <ERE> — End of the matching line, Return: a fresh
# line, auto-indented, under it.
new_line_below() { local l; l=$(line_of "$1" "$2"); goto "$l" 999; xdotool key End Return; sleep 0.4; }
# new_line_above <file> <ERE> — a fresh EMPTY line above the matching one
# (Return at column 1, then Up): no auto-indent to fight.
new_line_above() { local l; l=$(line_of "$1" "$2"); goto "$l" 1; xdotool key Return Up; sleep 0.4; }
# complete_with <typed text> — type it, open the suggest widget (Ctrl+Space
# if typing did not), and wait for rows. Prints the rows.
complete_with() {
  xdotool type --delay 60 -- "$1"; sleep 1.2
  wait_for 3 has_rows || { xdotool key --clearmodifiers ctrl+space; sleep 1.5; }
  wait_for 6 has_rows || true
  suggest_rows
}

smoke_open "$PROJ" plant.st
key Escape; hide_sidebar
sleep 3

# ══ X18 hover ══════════════════════════════════════════════════════════════
# Level is a manifest tag: the hover is the manifest's view of it — the type
# compiled from VAR_EXTERNAL and the tag's desc/unit (manifest.go hoverDoc).
goto_word "$F" '^    Level := LIMIT\(0\.0, Level \+ 6' Level
if h=$(show_hover); then
  png=$(shot hover-tag)
  if [[ $h == *"Level : REAL"* && $h == *"Tank level"* ]]; then
    pass "X18 hover on tag Level: type and manifest desc — \"$h\"" "$png"
  else fail "X18 hover on tag Level does not show 'Level : REAL' + 'Tank level': \"$h\"" "$png"; fi
else png=$(shot hover-tag); fail "X18 no hover on tag Level (Ctrl+K Ctrl+I, 6 tries)" "$png"; fi

# PlantDtS is VAR_EXTERNAL but no manifest tag (the dt-tag): the declared
# type and section, from the program.
goto_word "$F" 'LIMIT\(0\.0, Level \+ 6\.0 \* PlantDtS' PlantDtS
if h=$(show_hover); then
  png=$(shot hover-external)
  if [[ $h == *"PlantDtS : REAL"* && $h == *VAR_EXTERNAL* ]]; then
    pass "X18 hover on PlantDtS: declared type and section — \"$h\"" "$png"
  else fail "X18 hover on PlantDtS lacks 'REAL' + 'VAR_EXTERNAL': \"$h\"" "$png"; fi
else png=$(shot hover-external); fail "X18 no hover on PlantDtS" "$png"; fi

goto_word "$F" '^Full := settle\.Q' settle
if h=$(show_hover); then
  png=$(shot hover-local)
  if [[ $h == *"settle : TON"* && $h == *VAR* ]]; then
    pass "X18 hover on local FB instance settle — \"$h\"" "$png"
  else fail "X18 hover on settle lacks 'settle : TON' + 'VAR': \"$h\"" "$png"; fi
else png=$(shot hover-local); fail "X18 no hover on settle" "$png"; fi
xdotool key --clearmodifiers Escape

# ══ X18 completion ═════════════════════════════════════════════════════════
new_line_below "$F" '^END_IF;'
rows=$(complete_with Dra)
png=$(shot complete-tag)
if grep -qE '^DrainValve \[variable\]' <<<"$rows"; then
  pass "X18 completion: 'Dra' lists DrainValve (a variable, from the server) — $(head -3 <<<"$rows" | oneline)" "$png"
elif grep -qE '^DrainValve ' <<<"$rows"; then
  warn "X18 completion: 'Dra' lists DrainValve, but not as a server variable item (word-based?) — $(oneline <<<"$rows")" "$png"
else fail "X18 completion: 'Dra' does not list DrainValve — rows: $(oneline <<<"${rows:-<none>}")" "$png"; fi
undo_clean || fail "X18 completion: undo did not bring plant.st back to clean"

# Typed: `settle.` on a fresh line. The buffer does not parse at that
# moment, and naut lsp drops every symbol on a parse error, so the server
# answers null and VS Code shows only its word-based fallback — #139.
new_line_below "$F" '^END_IF;'
rows=$(complete_with settle.)
png=$(shot complete-members-typed)
miss=""; for pin in Q ET IN PT; do grep -qE "^$pin \[" <<<"$rows" || miss="$miss $pin"; done
if [[ -z $miss ]]; then
  pass "X18 completion: typing 'settle.' lists the TON's pins — $(oneline <<<"$rows")" "$png"
else warn "X18 completion: typing 'settle.' lists no pins (missing$miss) — the line does not parse yet and the server drops its symbols (#139); rows: $(head -4 <<<"${rows:-<none>}" | oneline)…" "$png"; fi
if undo_clean; then
  pass "X18 completion: the typed text is undone, plant.st clean"
else fail "X18 completion: plant.st still dirty after undo"; fi

# Invoked (Ctrl+Space) right after the dot in `Full := settle.Q`, where the
# buffer parses: the member list the server builds for a TON instance.
l=$(line_of "$F" '^Full := settle\.Q')
goto "$l" "$(( $(awk -v l="$l" 'NR==l{print index($0,"settle.")}' "$F") + 7 ))"
xdotool key --clearmodifiers ctrl+space; sleep 1.5
wait_for 6 has_rows || true
rows=$(suggest_rows)
png=$(shot complete-members)
miss=""; for pin in Q ET IN PT; do grep -qE "^$pin \[" <<<"$rows" || miss="$miss $pin"; done
if [[ -z $miss ]]; then
  pass "X18 completion: Ctrl+Space after 'settle.' lists the TON's pins — $(oneline <<<"$rows")" "$png"
else fail "X18 completion: Ctrl+Space after 'settle.' is missing$miss — rows: $(oneline <<<"${rows:-<none>}")" "$png"; fi
xdotool key --clearmodifiers Escape; sleep 0.3
if ! dirty; then
  png=$(shot complete-reverted)
  pass "X18 completion: plant.st clean after the completion probes" "$png"
else png=$(shot complete-reverted); fail "X18 completion: plant.st dirty after the completion probes" "$png"; fi

# ══ X18 go to definition ═══════════════════════════════════════════════════
want=$(line_of "$F" '^    Level +: REAL;')
goto_word "$F" '^    Level := LIMIT\(0\.0, Level - 5' Level
from=$(cursor_line)
xdotool key --clearmodifiers F12; sleep 2
got=$(cursor_line); png=$(shot definition-tag)
if [[ $got == "$want" ]]; then
  pass "X18 F12 on tag Level (Ln $from) lands on its VAR_EXTERNAL declaration, Ln $got" "$png"
else fail "X18 F12 on tag Level (Ln $from): cursor on Ln ${got:-?}, declaration is Ln $want" "$png"; fi

want=$(line_of "$F" '^    settle : TON;')
goto_word "$F" '^Full := settle\.Q' settle
from=$(cursor_line)
xdotool key --clearmodifiers F12; sleep 2
got=$(cursor_line); png=$(shot definition-local)
if [[ $got == "$want" ]]; then
  pass "X18 F12 on local settle (Ln $from) lands on its VAR line, Ln $got" "$png"
else fail "X18 F12 on local settle (Ln $from): cursor on Ln ${got:-?}, declaration is Ln $want" "$png"; fi

# ══ X41 find all references ════════════════════════════════════════════════
# refs_files — the references peek's file rows, "name=count" per line.
refs_files() {
  wb '[...document.querySelectorAll(".reference-zone-widget .reference-file")].map(e => (e.querySelector(".label-name")?.innerText || e.innerText.split("\n")[0]).trim() + "=" + (e.querySelector(".monaco-count-badge")?.innerText || "?").trim()).join("\n")'
}
# refs_lines — the peek tree's rows (a reference row's text is its line), one per line.
refs_lines() {
  wb '[...document.querySelectorAll(".reference-zone-widget .monaco-list-row")].map(e => e.innerText.replace(/\s+/g, " ").trim()).join("\n")'
}
# refs_title — the peek's title bar: "plant.st ~/tank-batch - References (N)".
refs_title() { wb '(document.querySelector(".reference-zone-widget .peekview-title")?.innerText || "").replace(/\s+/g, " ").trim()'; }
# The tree has a row per file only when the results span files; a
# single-file result lists its reference rows alone.
refs_open() { [[ $(refs_title) == *"References ("* ]]; }
# peek_refs — Shift+F12 (Go to References) at the cursor, until the peek
# opens (a keystroke can land before the editor has focus back, as
# show_hover retries Ctrl+K Ctrl+I).
peek_refs() {
  local i
  for i in 1 2 3 4; do
    xdotool key --clearmodifiers Escape; sleep 0.3
    xdotool key --clearmodifiers shift+F12
    wait_for 6 refs_open && break
  done
  sleep 1
}
# grep_counts <word> <file>… — "file=N" per file with N > 0: whole-word,
# case-sensitive occurrences, the way a person would count them by hand.
grep_counts() { local w=$1 f n; shift; for f in "$@"; do n=$(grep -ow -- "$w" "$PROJ/$f" | wc -l); (( n > 0 )) && echo "$f=$n"; done; }

goto_word "$F" '^    Level := LIMIT\(0\.0, Level \+ 6' Level
peek_refs
got=$(refs_files | sort); t=$(refs_title); png=$(shot references-tag)
want=$(grep_counts Level plant.st batch.sfc nautilus.yaml | sort)
n=$(awk -F= '{s += $2} END {print s + 0}' <<<"$want")
nf=$(grep -c = <<<"$got" || true)
if [[ -n $got && $got == "$want" && $t == *"References ($n)"* ]] && (( nf >= 2 )); then
  pass "X42 Shift+F12 on tag Level: \"$t\", $nf files, each file's count = grep -ow Level — $(oneline <<<"$got")" "$png"
else fail "X42 Shift+F12 on tag Level: the peek (\"${t:-no peek}\") lists $(oneline <<<"${got:-<no file rows>}"); grep -ow counts $(oneline <<<"$want") = $n" "$png"; fi
xdotool key --clearmodifiers Escape; sleep 0.5

goto_word "$F" '^Full := settle\.Q' settle
peek_refs
got=$(refs_files); rows=$(refs_lines); t=$(refs_title); png=$(shot references-local)
n=$(grep -c . <<<"$rows" || true)
inprog=$(awk '/^PROGRAM /,/^END_PROGRAM/' "$F" | grep -ow settle | wc -l)
infile=$(grep -ow settle "$F" | wc -l)
if [[ $t == "plant.st"*"References ($inprog)"* && -z $got && $n == "$inprog" ]] && ! grep -q 'Raw' <<<"$rows" && (( infile > inprog )); then
  pass "X42 Shift+F12 on local settle: \"$t\", every one in PROGRAM Plant (grep there: $inprog), none of the FB Debounce's own settle ($infile in the file) — $(oneline <<<"$rows")" "$png"
else fail "X42 Shift+F12 on local settle: the peek (\"${t:-no peek}\") lists $n rows [$(oneline <<<"$rows")]${got:+ in files $(oneline <<<"$got")}; want $inprog, all in PROGRAM Plant, of the file's $infile" "$png"; fi
xdotool key --clearmodifiers Escape; sleep 0.5

# ══ X18 diagnostics ════════════════════════════════════════════════════════
e0=$(errors); s0=$(squiggles)
[[ $e0 == 0 && $s0 == 0 ]] || info "X18 diagnostics: baseline is not clean — $e0 error(s), $s0 squiggle(s)"
text_replace "$F" "IF Heater THEN" "IF Heatr THEN"
wait_for 10 has_squiggle || true
sleep 1; e1=$(errors); s1=$(squiggles); png=$(shot diagnostics-typo)
if (( ${s1:-0} > ${s0:-0} && ${e1:-0} > ${e0:-0} )); then
  pass "X18 diagnostics: undeclared 'Heatr' squiggles ($s1) and the status bar errors go $e0 → $e1" "$png"
else fail "X18 diagnostics: after the typo squiggles $s0 → ${s1:-?}, status-bar errors $e0 → ${e1:-?}" "$png"; fi
# The problem's hover: on the misspelt name itself, where a person points.
goto_word "$F" '^IF Heater THEN' Heater
h=$(show_hover || true); png=$(shot diagnostics-hover)
if grep -q 'undeclared identifier "Heatr"' <<<"$h"; then
  pass "X18 diagnostics: hovering Heatr shows the problem — \"$h\"" "$png"
else
  goto "$(line_of "$F" '^IF Heater THEN')" 1
  h1=$(show_hover || true); png=$(shot diagnostics-hover-line)
  if grep -q 'undeclared identifier "Heatr"' <<<"$h1"; then
    warn "X18 diagnostics: the problem is on 'IF' at column 1, not on 'Heatr' (#141) — hovering the name shows ${h:+\"$h\"}${h:-nothing}; at 1:1 \"$h1\"" "$png"
  else fail "X18 diagnostics: no hover names the undeclared 'Heatr' (on the name: \"$h\"; at column 1: \"$h1\")" "$png"; fi
fi
undo_clean || true
wait_for 10 no_squiggle || true
sleep 1; e2=$(errors); s2=$(squiggles); png=$(shot diagnostics-reverted)
if ! dirty && [[ $e2 == "$e0" && $s2 == "$s0" ]]; then
  pass "X18 diagnostics: undo clears them (errors $e2, squiggles $s2, buffer clean)" "$png"
else fail "X18 diagnostics: after undo errors ${e2:-?} (was $e0), squiggles ${s2:-?}, $(dirty && echo dirty || echo clean)" "$png"; fi

# ══ X40 signature help ═════════════════════════════════════════════════════
# sig_label / sig_active — the visible parameter-hints widget's signature
# text, and the highlighted parameter's text ("" when none).
sig_widget='[...document.querySelectorAll(".parameter-hints-widget")].find(e => e.classList.contains("visible") && e.getBoundingClientRect().height > 0)'
sig_label() { wb "(() => { const w = $sig_widget; return w ? (w.querySelector(\".signature .code\") || w.querySelector(\".signature\") || w).innerText.trim().replace(/\\s*\\n\\s*/g, \" \") : \"\"; })()"; }
sig_active() { wb "(() => { const w = $sig_widget; return w ? (w.querySelector(\".parameter.active\")?.innerText || \"\").trim() : \"\"; })()"; }
has_sig() { [[ -n $(sig_label) ]]; }
active_is() { [[ $(sig_active) == "$1" ]]; }

new_line_below "$F" '^END_IF;'
xdotool type --delay 60 -- 'TempC := LIMIT('
wait_for 8 has_sig || true
sleep 0.5; lbl=$(sig_label); act=$(sig_active); png=$(shot signature-open)
miss=""; for p in "MN : ANY_NUM" "IN : ANY_NUM" "MX : ANY_NUM"; do [[ $lbl == *"LIMIT("*"$p"* ]] || miss="$miss '$p'"; done
if [[ -z $miss && $act == "MN : ANY_NUM" ]]; then
  pass "X40 signature help: typing 'LIMIT(' shows \"$lbl\", first parameter highlighted ($act)" "$png"
else fail "X40 signature help: after 'LIMIT(' the widget shows \"${lbl:-<none>}\" (missing$miss), highlighted \"${act:-<none>}\" — want MN" "$png"; fi

xdotool type --delay 60 -- '0.0, '
wait_for 6 active_is "IN : ANY_NUM" || true
sleep 0.3; lbl=$(sig_label); act=$(sig_active); png=$(shot signature-comma)
if [[ $act == "IN : ANY_NUM" && $lbl == *"LIMIT("* ]]; then
  pass "X40 signature help: typing '0.0, ' moves the highlight to the second parameter ($act)" "$png"
else fail "X40 signature help: after '0.0, ' highlighted \"${act:-<none>}\" in \"${lbl:-<none>}\" — want IN" "$png"; fi
undo_clean || fail "X40 signature help: undo did not bring plant.st back to clean"

new_line_below "$F" '^END_IF;'
xdotool type --delay 60 -- 'settle(IN := '
wait_for 8 has_sig || true
wait_for 4 active_is "IN : BOOL" || true
sleep 0.3; lbl=$(sig_label); act=$(sig_active); png=$(shot signature-fb)
if [[ $lbl == *"TON(IN : BOOL, PT : TIME) => Q : BOOL, ET : TIME"* && $act == "IN : BOOL" ]]; then
  pass "X40 signature help: 'settle(IN := ' names the TON's pins — \"$lbl\", IN highlighted" "$png"
else fail "X40 signature help: after 'settle(IN := ' the widget shows \"${lbl:-<none>}\", highlighted \"${act:-<none>}\" — want TON's pins, IN" "$png"; fi

xdotool type --delay 60 -- 'Full, PT := '
wait_for 6 active_is "PT : TIME" || true
sleep 0.3; act=$(sig_active); png=$(shot signature-fb-named)
if [[ $act == "PT : TIME" ]]; then
  pass "X40 signature help: 'Full, PT := ' moves the highlight to the named pin ($act)" "$png"
else fail "X40 signature help: after 'Full, PT := ' highlighted \"${act:-<none>}\" — want PT" "$png"; fi

xdotool key --clearmodifiers Escape; sleep 0.5
gone=$(has_sig && echo shown || echo hidden)
undo_clean || true
sleep 0.5; png=$(shot signature-reverted)
if [[ $gone == hidden ]] && ! dirty; then
  pass "X40 signature help: Esc closes the widget; undone, plant.st clean" "$png"
else fail "X40 signature help: after Esc the widget is $gone; plant.st $(dirty && echo dirty || echo clean) after undo" "$png"; fi

# ══ X20 nautilus.yaml schema ═══════════════════════════════════════════════
Y=$PROJ/nautilus.yaml
open_file nautilus.yaml 4
sleep 3
e0=$(errors); s0=$(squiggles)
[[ $e0 == 0 && $s0 == 0 ]] || info "X20 nautilus.yaml: baseline is not clean — $e0 error(s), $s0 squiggle(s)"
text_replace "$Y" "dt-tag: PlantDtS" "bogus-key: PlantDtS"
wait_for 12 has_squiggle || true
sleep 1; e1=$(errors); s1=$(squiggles)
goto_word "$Y" 'dt-tag: PlantDtS' dt-tag     # the disk line: same column
h=$(show_hover || true); png=$(shot yaml-bogus-key)
if (( ${s1:-0} > ${s0:-0} && ${e1:-0} > ${e0:-0} )) && grep -qi 'not allowed' <<<"$h"; then
  pass "X20 nautilus.yaml: a bogus key under a task is a schema error (errors $e0 → $e1) — \"$h\"" "$png"
elif (( ${s1:-0} > ${s0:-0} )); then
  pass "X20 nautilus.yaml: a bogus key under a task squiggles (errors $e0 → ${e1:-?}) — hover \"$h\"" "$png"
else fail "X20 nautilus.yaml: no schema error for a bogus task key (squiggles $s0 → ${s1:-?}, errors $e0 → ${e1:-?}, hover \"$h\")" "$png"; fi
undo_clean || fail "X20 nautilus.yaml: undo did not bring the buffer back to clean"

new_line_above "$Y" '^  - name: plant'
rows=$(complete_with '  - ')
png=$(shot yaml-task-completion)
miss=""; for k in program scan name; do grep -qE "^$k \[" <<<"$rows" || miss="$miss $k"; done
if [[ -z $miss ]]; then
  pass "X20 nautilus.yaml: completion in a new task offers program, scan, name — $(oneline <<<"$rows")" "$png"
else fail "X20 nautilus.yaml: new-task completion is missing$miss — rows: $(oneline <<<"${rows:-<none>}")" "$png"; fi
undo_clean || true
sleep 1; png=$(shot yaml-reverted)
if ! dirty && [[ $(errors) == "$e0" ]]; then
  pass "X20 nautilus.yaml: undone, buffer clean, errors back to $e0" "$png"
else fail "X20 nautilus.yaml: after undo $(dirty && echo dirty || echo clean), errors $(errors) (was $e0)" "$png"; fi

# ══ X20 *_test.yaml schema ═════════════════════════════════════════════════
T=$PROJ/batch_test.yaml
open_file batch_test.yaml 4
sleep 3
e0=$(errors); s0=$(squiggles)
[[ $e0 == 0 && $s0 == 0 ]] || info "X20 batch_test.yaml: baseline is not clean — $e0 error(s), $s0 squiggle(s)"
text_replace "$T" "advance: 2s" "advanse: 2s"
wait_for 12 has_squiggle || true
sleep 1; e1=$(errors); s1=$(squiggles)
goto_word "$T" 'advance: 2s' advance
h=$(show_hover || true); png=$(shot test-bogus-key)
if (( ${s1:-0} > ${s0:-0} && ${e1:-0} > ${e0:-0} )); then
  pass "X20 batch_test.yaml: a misspelt step key is an error (errors $e0 → $e1) — \"$h\"" "$png"
else fail "X20 batch_test.yaml: no error for a misspelt step key (squiggles $s0 → ${s1:-?}, errors $e0 → ${e1:-?}, hover \"$h\")" "$png"; fi
undo_clean || fail "X20 batch_test.yaml: undo did not bring the buffer back to clean"

new_line_above "$T" '^      - given:'
rows=$(complete_with '      - ')
png=$(shot test-step-completion)
miss=""; for k in given advance expect; do grep -qE "^$k \[" <<<"$rows" || miss="$miss $k"; done
if [[ -z $miss ]]; then
  pass "X20 batch_test.yaml: completion in a new step offers given, advance, expect — $(oneline <<<"$rows")" "$png"
else fail "X20 batch_test.yaml: new-step completion is missing$miss — rows: $(oneline <<<"${rows:-<none>}")" "$png"; fi
undo_clean || true
sleep 1; png=$(shot test-reverted)
if ! dirty && [[ $(errors) == "$e0" ]]; then
  pass "X20 batch_test.yaml: undone, buffer clean, errors back to $e0" "$png"
else fail "X20 batch_test.yaml: after undo $(dirty && echo dirty || echo clean), errors $(errors) (was $e0)" "$png"; fi

# ══ X20 tags/*.yaml and alarms/*.yaml schemas ═════════════════════════════
# BEGIN smoke-partial-rows: appended block — the tag-file and alarm-file
# schemas (package.json yamlValidation). Same shape as the rows above: a
# bogus key is a schema error, a new list entry completes with the schema's
# keys. Keys are the schema's (nautilus.schema.json definitions tag / alarm).
yaml_rows() { # <file> <label> <bogus-from> <bogus-to> <anchor ERE for the entry> <keys…>
  local f=$1 name=$2 from=$3 to=$4 anchor=$5; shift 5
  local base=${f##*/}
  open_file "$base" 4
  sleep 3
  e0=$(errors); s0=$(squiggles)
  [[ $e0 == 0 && $s0 == 0 ]] || info "X20 $name: baseline is not clean — $e0 error(s), $s0 squiggle(s)"
  text_replace "$f" "$from" "$to"
  wait_for 12 has_squiggle || true
  sleep 1; e1=$(errors); s1=$(squiggles)
  goto_word "$f" "$from" "${from%%:*}"
  h=$(show_hover || true); png=$(shot "$name-bogus-key")
  if (( ${s1:-0} > ${s0:-0} && ${e1:-0} > ${e0:-0} )); then
    pass "X20 $name: a misspelt key is a schema error (errors $e0 → $e1) — \"$h\"" "$png"
  else fail "X20 $name: no schema error for a misspelt key (squiggles $s0 → ${s1:-?}, errors $e0 → ${e1:-?}, hover \"$h\")" "$png"; fi
  undo_clean || fail "X20 $name: undo did not bring the buffer back to clean"

  new_line_above "$f" "$anchor"
  rows=$(complete_with '- ')
  png=$(shot "$name-completion")
  miss=""; for k in "$@"; do grep -qE "^$k \[" <<<"$rows" || miss="$miss $k"; done
  if [[ -z $miss ]]; then
    pass "X20 $name: completion in a new entry offers $* — $(oneline <<<"$rows")" "$png"
  else fail "X20 $name: new-entry completion is missing$miss — rows: $(oneline <<<"${rows:-<none>}")" "$png"; fi
  undo_clean || true
  sleep 1; png=$(shot "$name-reverted")
  if ! dirty && [[ $(errors) == "$e0" ]]; then
    pass "X20 $name: undone, buffer clean, errors back to $e0" "$png"
  else fail "X20 $name: after undo $(dirty && echo dirty || echo clean), errors $(errors) (was $e0)" "$png"; fi
}
yaml_rows "$PROJ/tags/spare-tags.yaml" tags-yaml "init: 0.0" "inits: 0.0" '^- name: SpareFlag' name role type init unit desc
yaml_rows "$PROJ/alarms/spare-alarms.yaml" alarms-yaml "priority: high" "priorty: high" \
  '^- id: spare-low' id name match class
# The suggest list is alphabetical and shows ~12 rows, so `tag` and `priority`
# sit below the fold: filter by typing their prefixes instead.
A=$PROJ/alarms/spare-alarms.yaml
for pre in "ta:tag" "prio:priority"; do
  new_line_above "$A" '^- id: spare-low'
  rows=$(complete_with "- ${pre%%:*}")
  png=$(shot "alarms-yaml-complete-${pre#*:}")
  if grep -qE "^${pre#*:} \[" <<<"$rows"; then
    pass "X20 alarms-yaml: completing '${pre%%:*}' in a new entry offers ${pre#*:} — $(oneline <<<"$rows")" "$png"
  else fail "X20 alarms-yaml: completing '${pre%%:*}' does not offer ${pre#*:} — rows: $(oneline <<<"${rows:-<none>}")" "$png"; fi
  undo_clean || fail "X20 alarms-yaml: undo did not bring the buffer back to clean after '${pre%%:*}'"
done
# END smoke-partial-rows

# ══ X40 document symbols ═══════════════════════════════════════════════════
# outline_rows — the Outline view's rows on screen (a virtual list) in list
# order, indented by tree level: "name [kind] detail", "<selected>" on the
# selected row.
outline_rows() {
  wb '(() => { const p = document.querySelector(".outline-pane"); if (!p) return ""; return [...p.querySelectorAll(".monaco-list-row")].sort((a, b) => (+a.dataset.index) - (+b.dataset.index)).map(r => "  ".repeat(Math.max(0, (+r.getAttribute("aria-level") || 1) - 1)) + (r.querySelector(".label-name")?.innerText || r.getAttribute("aria-label") || "").trim() + " [" + ((r.querySelector("[class*=codicon-symbol-]")?.className.match(/codicon-symbol-([a-z-]+)/) || [])[1] || "?") + "] " + (r.querySelector(".label-description")?.innerText || "").trim() + (r.classList.contains("selected") ? " <selected>" : "")).join("\n"); })()'
}
has_outline() { [[ -n $(outline_rows) ]]; }
# outline_all — every row of the Outline, not just the ones its virtual list
# has on screen: Home, then PageDown through the tree (the Outline has the
# focus), collecting rows by list index. The selection marks are dropped.
outline_all() {
  local i acc=""
  xdotool key --clearmodifiers Home; sleep 0.5
  for i in 1 2 3 4 5 6 7 8; do
    acc+=$(wb '(() => { const p = document.querySelector(".outline-pane"); if (!p) return ""; return [...p.querySelectorAll(".monaco-list-row")].map(r => r.dataset.index + "\t" + "  ".repeat(Math.max(0, (+r.getAttribute("aria-level") || 1) - 1)) + (r.querySelector(".label-name")?.innerText || r.getAttribute("aria-label") || "").trim() + " [" + ((r.querySelector("[class*=codicon-symbol-]")?.className.match(/codicon-symbol-([a-z-]+)/) || [])[1] || "?") + "] " + (r.querySelector(".label-description")?.innerText || "").trim()).join("\n"); })()')$'\n'
    xdotool key --clearmodifiers Next; sleep 0.4
  done
  sort -t$'\t' -k1,1n -u <<<"$acc" | grep . | cut -f2-
}
# crumbs — the active editor's breadcrumb bar, "a › b › c".
crumbs() {
  wb '(() => { const g = document.querySelector(".editor-group-container.active") || document; return [...g.querySelectorAll(".breadcrumbs-control .monaco-breadcrumb-item")].map(e => e.innerText.trim()).filter(Boolean).join(" › "); })()'
}
# qp_rows — the open quick pick's rows: "label | description".
qp_rows() {
  wb '[...document.querySelectorAll(".quick-input-widget .quick-input-list .monaco-list-row")].sort((a, b) => (+a.dataset.index) - (+b.dataset.index)).map(r => (r.querySelector(".label-name")?.innerText || r.getAttribute("aria-label") || "").trim() + " | " + (r.querySelector(".label-description")?.innerText || "").trim()).join("\n")'
}
has_qp() { [[ -n $(qp_rows) ]]; }
# has_all <text> <ERE>... — every ERE matches a line of the text; prints the
# ones that do not.
has_all() { local t=$1 re miss=""; shift; for re; do grep -qE -- "$re" <<<"$t" || miss="$miss /$re/"; done; [[ -z $miss ]] || { echo "$miss"; return 1; }; }
# outline_tree — focus the Outline view, read its whole tree, focus the
# editor again. Prints the rows.
outline_tree() {
  local rows
  vs_cmd "Focus on Outline View" 2
  wait_for 10 has_outline || true
  rows=$(outline_all)
  key ctrl+1
  echo "$rows"
}
# symbol_at <file> <ERE for the line> <word> — the cursor on it, then the
# breadcrumbs and the Outline's selection have caught up.
symbol_at() { goto_word "$@"; sleep 1.5; }
# goto_symbol [filter] — Ctrl+Shift+O, optionally narrowed; prints the rows.
goto_symbol() {
  xdotool key --clearmodifiers Escape; sleep 0.3
  xdotool key --clearmodifiers ctrl+shift+o; sleep 1.5
  [[ -n ${1:-} ]] && { xdotool type --delay 60 -- "$1"; sleep 1.2; }
  wait_for 6 has_qp || true
  qp_rows
}

# The rig profile turns breadcrumbs off; X40 needs them, and the Outline
# fully expanded. Settings apply live.
sed -i 's/"breadcrumbs.enabled": false/"breadcrumbs.enabled": true/; 0,/^{/s//{\n  "outline.collapseItems": "alwaysExpand",/' "$PROFILE/User/settings.json"
sleep 1
open_file plant.st 3
rows=$(outline_tree); png=$(shot outline-st)
if miss=$(has_all "$rows" '^Plant \[' '^  VAR_EXTERNAL \[' '^    Level \[[a-z?-]+\] REAL' '^  VAR \[' '^    settle \[[a-z?-]+\] TON'); then
  pass "X41 Outline on plant.st: the PROGRAM, its VAR sections and declarations — $(head -4 <<<"$rows" | oneline)…" "$png"
else fail "X41 Outline on plant.st is missing$miss — rows: $(oneline <<<"${rows:-<none>}")" "$png"; fi

symbol_at "$F" '^    settle : TON;' settle
c=$(crumbs); png=$(shot breadcrumbs-st)
if [[ $c == *"Plant › VAR › settle"* ]]; then
  pass "X41 breadcrumbs on plant.st's settle declaration: \"$c\"" "$png"
else fail "X41 breadcrumbs on plant.st's settle declaration: \"${c:-<none>}\", want …Plant › VAR › settle" "$png"; fi

rows=$(goto_symbol); png=$(shot goto-symbol-st)
if miss=$(has_all "$rows" '^Plant ' '^VAR_EXTERNAL ' '^Level ' '^settle '); then
  pass "X41 Go to Symbol in Editor on plant.st lists them — $(head -5 <<<"$rows" | oneline)…" "$png"
else fail "X41 Go to Symbol in Editor on plant.st is missing$miss — rows: $(oneline <<<"${rows:-<none>}")" "$png"; fi
xdotool key --clearmodifiers Escape; sleep 0.4

L=$PROJ/interlock.ld
open_file interlock.ld 4
rows=$(outline_tree); png=$(shot outline-ld)
if miss=$(has_all "$rows" '^SealIn \[' '^  VAR_INPUT \[' '^    Start \[[a-z?-]+\] BOOL' '^  seal \[[a-z?-]+\] start/stop with seal-in' '^  lamp \['); then
  pass "X41 Outline on interlock.ld: the FUNCTION_BLOCK, its sections and its rungs with their comments — $(oneline <<<"$rows")" "$png"
else fail "X41 Outline on interlock.ld is missing$miss — rows: $(oneline <<<"${rows:-<none>}")" "$png"; fi

symbol_at "$L" '^    Run \( Lamp \)' Lamp
c=$(crumbs); png=$(shot breadcrumbs-ld)
if [[ $c == *"SealIn › lamp"* ]]; then
  pass "X41 breadcrumbs inside interlock.ld's lamp rung: \"$c\"" "$png"
else fail "X41 breadcrumbs inside interlock.ld's lamp rung: \"${c:-<none>}\", want …SealIn › lamp" "$png"; fi

rows=$(goto_symbol); png=$(shot goto-symbol-ld)
if miss=$(has_all "$rows" '^SealIn ' '^seal ' '^lamp '); then
  pass "X41 Go to Symbol in Editor on interlock.ld lists the rungs — $(oneline <<<"$rows")" "$png"
else fail "X41 Go to Symbol in Editor on interlock.ld is missing$miss — rows: $(oneline <<<"${rows:-<none>}")" "$png"; fi
xdotool key --clearmodifiers Escape; sleep 0.4

S=$PROJ/batch.sfc
open_file batch.sfc 4
rows=$(outline_tree); png=$(shot outline-sfc)
if miss=$(has_all "$rows" '^TankBatch \[' '^  VAR_EXTERNAL \[' '^    BatchCount \[[a-z?-]+\] INT' '^  Idle \[[a-z?-]+\] INITIAL_STEP' '^  Fill \[[a-z?-]+\] STEP' '^  Fill → \(Heat, Mix\) \[[a-z?-]+\] t_full: ' '^  \(Heat, Mix\) → Drain \[' '^  CountBatch \[[a-z?-]+\] ACTION'); then
  pass "X41 Outline on batch.sfc: the PROGRAM, its sections, the steps (initial marked), the transitions as From → To, the actions — $(grep -E '^  [^ ]' <<<"$rows" | sed 's/^ *//' | oneline)" "$png"
else fail "X41 Outline on batch.sfc is missing$miss — rows: $(oneline <<<"${rows:-<none>}")" "$png"; fi

symbol_at "$S" '^  STEP Fill:' Fill
c=$(crumbs); png=$(shot breadcrumbs-sfc)
if [[ $c == *"TankBatch › Fill"* ]]; then
  pass "X41 breadcrumbs on batch.sfc's STEP Fill: \"$c\"" "$png"
else fail "X41 breadcrumbs on batch.sfc's STEP Fill: \"${c:-<none>}\", want …TankBatch › Fill" "$png"; fi

rows=$(goto_symbol Idle); png=$(shot goto-symbol-sfc)
if miss=$(has_all "$rows" '^Idle ' '^Idle → Fill ' '^Drain → Idle '); then
  pass "X41 Go to Symbol in Editor on batch.sfc, filtered 'Idle': the initial step and its transitions — $(oneline <<<"$rows")" "$png"
else fail "X41 Go to Symbol in Editor on batch.sfc, filtered 'Idle', is missing$miss — rows: $(oneline <<<"${rows:-<none>}")" "$png"; fi
xdotool key --clearmodifiers Escape; sleep 0.4

# ┌── X44 / X45: cross-reference and descriptions on a diagram element ──────┐
# │ (#218, #216 — parity-xref-desc). Self-contained: its own ladder program, │
# │ committed, opened as the diagram editor; reads the webview over cdp.js   │
# │ and the References view from the workbench DOM.                          │
# └──────────────────────────────────────────────────────────────────────────┘
#   X45  the Start contact's tooltip (<title>) ends with the manifest desc
#        and the contact draws it as a second line (text.nx-desc); the
#        local Latched carries its VAR line's comment the same way.
#   X44  click the Start contact, Shift+F12: the References view opens on
#        Start — per file the same count as `grep -ow Start`, over every
#        program that binds it (drain.ld itself, plant.st, batch.sfc) and
#        nautilus.yaml; the SealIn FB's own VAR_INPUT Start (interlock.ld)
#        is not one of them.
cat >"$PROJ/drain.ld" <<'LADDER'
PROGRAM DrainInterlock
VAR_EXTERNAL
    Start   : BOOL;
    Abort   : BOOL;
    RunLamp : BOOL;
END_VAR
VAR
    Latched : BOOL; (* abort seen since the last batch began *)
END_VAR
LD
  RUNG latch (* an abort latches until the next batch *)
    [ Abort | Latched ] /Start ( Latched )

  RUNG lamp
    /Latched ( RunLamp )
END_LD
END_PROGRAM
LADDER
git -C "$PROJ" add -A; git -C "$PROJ" commit -qm "smoke 14: a ladder program for X44/X45"
start_desc=$(grep -oP 'name: Start,.*desc: "\K[^"]+' "$PROJ/nautilus.yaml")
# xd_title <rung> <tag> — the element's tooltip; xd_line — its second line.
xd_title() { js "($(ld_node_el "$1" '*' "$2"))?.closest('g.node')?.querySelector(':scope > title')?.textContent ?? ''" | python3 -c 'import sys, json; print(json.load(sys.stdin))'; }
xd_line() { js "($(ld_node_el "$1" '*' "$2"))?.closest('g.node')?.querySelector('text.nx-desc')?.textContent ?? ''" | python3 -c 'import sys, json; print(json.load(sys.stdin))'; }
# refview_rows — the References view's tree, "level<TAB>text" per row ("" when
# no References view is showing). refview_files — "file=N" per file row, N its
# reference rows.
refview_js='(() => {
  const trees = [...document.querySelectorAll(".customview-tree")].filter((t) => t.getBoundingClientRect().height > 0);
  const tree = trees.find((t) => t.closest(".pane")?.querySelector(".pane-header")?.innerText.match(/references/i)) ?? trees.find((t) => (t.closest(".composite")?.querySelector(".composite.title, .title-label")?.innerText ?? "").match(/references/i));
  if (!tree) return "";
  return [...tree.querySelectorAll(".monaco-list-row")].map((r) => (r.getAttribute("aria-level") ?? "?") + "\t" + r.innerText.replace(/\s+/g, " ").trim()).join("\n");
})()'
refview_rows() { wb "$refview_js"; }
refview_files() {
  refview_rows | awk -F'\t' '$1 == 1 { split($2, w, " "); f = w[1]; n[f] = 0; order[++k] = f; next } $1 == 2 && f != "" { n[f]++ } END { for (i = 1; i <= k; i++) print order[i] "=" n[order[i]] }'
}
refview_open() { [[ -n $(refview_rows) ]]; }

if ed_open_diagram drain.ld; then
  # Descriptions arrive from naut lsp after the model: wait for the tooltip.
  wait_js "($(ld_node_el latch '*' Start))?.closest('g.node')?.querySelector(':scope > title')?.textContent.includes($(_q "$start_desc"))" 20 || true
  t=$(xd_title latch Start || true); l=$(xd_line latch Start || true)
  click_el "$(ld_node_el latch contact Start)" || true
  p=$(el_at "$(ld_node_el latch contact Start)" 0.5 0.5 || true)
  [[ -n $p ]] && { g_move $p || true; sleep 2.5; }   # the native tooltip, for the PNG
  png=$(shot xref-desc-contact)
  if [[ $t == *$'\n'"$start_desc" && -n $l && $start_desc == "${l%…}"* ]]; then
    pass "X45 ladder contact Start: tooltip ends with its nautilus.yaml desc \"$start_desc\"; second line \"$l\"" "$png"
  else fail "X45 ladder contact Start: tooltip \"${t//$'\n'/ ⏎ }\", second line \"$l\" — want the desc \"$start_desc\" in both" "$png"; fi
  t=$(xd_title latch Latched || true)
  if [[ $t == *"abort seen since the last batch began" ]]; then
    pass "X45 ladder contact Latched (a local): tooltip ends with its VAR line's comment"
  else fail "X45 ladder contact Latched: tooltip \"${t//$'\n'/ ⏎ }\" lacks its VAR comment"; fi

  want=$(grep_counts Start drain.ld plant.st batch.sfc nautilus.yaml | sort || true)
  for i in 1 2 3; do
    click_el "$(ld_node_el latch contact Start)" || true
    g_key shift+F12 || true
    wait_for 10 refview_open && break
  done
  sleep 1.5
  got=$(refview_files | sort || true); rows=$(refview_rows || true); png=$(shot xref-desc-references)
  n=$(awk -F= '{s += $2} END {print s + 0}' <<<"$want")
  if [[ -n $got && $got == "$want" ]] && ! grep -q '^interlock.ld=' <<<"$got"; then
    pass "X44 Shift+F12 on the ladder contact Start opens the References view: $n references, per file = grep -ow Start — $(oneline <<<"$got"); not interlock.ld's FB-local Start" "$png"
  else fail "X44 Shift+F12 on the ladder contact Start: the References view lists $(oneline <<<"${got:-<nothing>}") (rows: $(head -12 <<<"$rows" | tr '\t' ' ' | oneline)); want $(oneline <<<"$want")" "$png"; fi
  xdotool key --clearmodifiers ctrl+b; sleep 0.8   # the sidebar the view opened
else
  png=$(shot xref-desc-open)
  fail "X44/X45: drain.ld did not open as a ladder diagram" "$png"
fi
vs_cmd "View: Close All Editors" 1.2
# └── end X44 / X45 block ────────────────────────────────────────────────────┘

# ══ nothing left behind ════════════════════════════════════════════════════
d=$(wb 'String(document.querySelectorAll(".tabs-container .tab.dirty").length)')
st=$(git -C "$PROJ" status --porcelain)
if [[ $d == 0 && -z $st ]]; then
  pass "every buffer clean (0 dirty tabs) and the project matches its commit"
else fail "left behind: $d dirty tab(s); git status: ${st:-clean}"; fi
