#!/usr/bin/env bash
# F-14: /guides/blocks-and-tasks/ (as corrected; same text in /opt/sut/docs and on the live site,
# 2026-11-03) says what each `naut new` template scaffolds:
#   "The default `demo` template ships `blocks.st` with a `RateOfChange` block, which `program.fbd`
#    instantiates like a built-in TON; the PI loop in that project is written inline in the diagram.
#    The `sdk-demo` template (the tour as a Go project) ships the `PI` block above in `blocks.st`,
#    instantiated from `program.st`. The `minimal` and `sdk` templates start blank, with no library
#    file."
# A library file, per the same page, is a .st/.ld/.fbd file with no PROGRAM, in the root or under lib/.
# Passes when every clause holds. Needs only naut; scaffolds into a temp dir (no Go toolchain needed).
# Rewritten 2026-11-03 per runs/RECONCILIATION-2026-11-03.md ruling 5 (the parent asserted the
# page's old sentence: PI in blocks.st, used from program.st, in the default template).
set -u
T=$(mktemp -d); trap 'rm -rf "$T"' EXIT; cd "$T"
fails=0
ok() { if eval "$2"; then echo "PASS  $1"; else echo "FAIL  $1"; fails=$((fails+1)); fi; }
libs() { # library files of a project: .st/.ld/.fbd in the root or under lib/ with no PROGRAM
  ( cd "$1" && { ls -1 *.st *.ld *.fbd 2>/dev/null; find lib -type f \( -name '*.st' -o -name '*.ld' -o -name '*.fbd' \) 2>/dev/null; } \
    | while read -r f; do grep -qiE '^\s*PROGRAM\b' "$f" || echo "$f"; done ); }
for t in demo sdk-demo minimal sdk; do
  naut new "p-$t" --template "$t" --no-input </dev/null >/dev/null 2>&1; rc=$?
  ok "naut new --template $t exits 0" "[ $rc -eq 0 ] && [ -d p-$t ]"
  echo "      $t files: $(cd p-$t 2>/dev/null && ls | tr '\n' ' ')  library files: [$(libs p-$t | tr '\n' ' ')]"
done
# demo
ok "demo: blocks.st declares FUNCTION_BLOCK RateOfChange" "grep -qiE '^\s*FUNCTION_BLOCK\s+RateOfChange\b' p-demo/blocks.st"
ok "demo: program.fbd instantiates RateOfChange" "grep -qiE ':\s*RateOfChange\s*(\(|;)' p-demo/program.fbd"
ok "demo: no PI block in blocks.st and no PI instance in program.fbd (the PI loop is inline)" \
   "! grep -qiE '^\s*FUNCTION_BLOCK\s+PI\b' p-demo/blocks.st && ! grep -qiE ':\s*PI\s*(\(|;)' p-demo/program.fbd"
grep -niE 'FUNCTION_BLOCK|:\s*RateOfChange' p-demo/blocks.st p-demo/program.fbd | sed 's/^/      /'
# sdk-demo
ok "sdk-demo: blocks.st declares FUNCTION_BLOCK PI" "grep -qiE '^\s*FUNCTION_BLOCK\s+PI\b' p-sdk-demo/blocks.st"
ok "sdk-demo: program.st instantiates PI" "grep -qiE ':\s*PI\s*(;|\()' p-sdk-demo/program.st"
grep -niE 'FUNCTION_BLOCK|:\s*PI\b' p-sdk-demo/blocks.st p-sdk-demo/program.st | sed 's/^/      /'
# minimal, sdk
for t in minimal sdk; do ok "$t: no library file" "[ -z \"\$(libs p-$t)\" ]"; done
[ $fails -eq 0 ] && { echo PASS; exit 0; }; echo "FAIL $fails clause(s) of the page do not match the scaffold"; exit 1
