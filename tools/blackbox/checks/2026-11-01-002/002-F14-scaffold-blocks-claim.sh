#!/usr/bin/env bash
# F-14: /guides/blocks-and-tasks/ says "`naut new` scaffolds this shape: the PI controller ships in
# blocks.st, instantiated from program.st." Passes when the default scaffold has a blocks.st that
# declares FUNCTION_BLOCK PI and a program.st that instantiates it. (Either side may change.)
set -u
T=$(mktemp -d); trap 'rm -rf "$T"' EXIT; cd "$T"
naut new p </dev/null >/dev/null 2>&1; cd p; ls
grep -n "FUNCTION_BLOCK" blocks.st 2>/dev/null
grep -qiE "^FUNCTION_BLOCK +PI\b" blocks.st 2>/dev/null && [ -f program.st ] && grep -qiE ":\s*PI\b" program.st \
  && { echo PASS; exit 0; }
echo "FAIL scaffold does not match the page"; exit 1
