#!/usr/bin/env bash
# F-16: TON's PT is a TIME (the Studio 5000 page: "`PT` is a `TIME`, written `T#5S`, not a millisecond
# `DINT`"). The operator ruled (runs/RECONCILIATION-2026-11-03.md, ruling 6) that an integer where a
# TIME is expected keeps compiling and `naut check` warns about it. So: with `PT := 30000`, naut check
# exits 0 and prints a gcc-style warning at the PT line that names PT, the integer and TIME; with
# `PT := T#30s` (the control) it prints no such warning.
# Rewritten 2026-11-03 per ruling 6 (the parent expected naut check to exit non-zero).
# The id keeps its old slug ("refused") because ids are stable; the behaviour checked is the warning.
# SUT_PROJECT = evidence/pumps.
set -u
P=${SUT_PROJECT:?SUT_PROJECT}
T=$(mktemp -d); trap 'rm -rf "$T"' EXIT; cp -r "$P"/. "$T"/; cd "$T"
fails=0
ok() { if eval "$2"; then echo "PASS  $1"; else echo "FAIL  $1"; fails=$((fails+1)); fi; }

sed -i 's/PT := HiLevelDelay/PT := 30000/' program.st
line=$(grep -n "PT := 30000" program.st | cut -d: -f1); echo "program.st:$line: $(sed -n ${line}p program.st)"
out=$(naut check 2>&1); rc=$?; echo "$out" | sed 's/^/      /'
w=$(echo "$out" | grep -E "^program\.st:${line}:[0-9]+: warning: " | grep -E '\bPT\b' | grep -F 30000 | grep -E '\bTIME\b')
ok "naut check exits 0 with PT := 30000 (it compiles)" "[ $rc -eq 0 ]"
ok "a warning at program.st:$line names PT, the integer 30000 and TIME" "[ -n \"\$w\" ]"

sed -i 's/PT := 30000/PT := T#30s/' program.st
out=$(naut check 2>&1); rc=$?; echo "      -- control, PT := T#30s:"; echo "$out" | sed 's/^/      /'
ok "control: naut check exits 0 with PT := T#30s" "[ $rc -eq 0 ]"
ok "control: no warning at program.st:$line" "! echo \"\$out\" | grep -qE \"^program\\.st:${line}:[0-9]+: warning: \""

[ $fails -eq 0 ] && { echo PASS; exit 0; }; echo "FAIL integer-as-TIME is not warned as ruled"; exit 1
