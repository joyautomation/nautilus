#!/usr/bin/env bash
# Table test for ci-scope.sh; run from the repo root (it reads the real
# examples/ tree for project roots). CI's changes job runs it first.
set -u
here=$(dirname "$0")
fail=0
check() { # check <want output, \n-separated> <paths...>
  local want=$1; shift
  local got
  got=$(printf '%s\n' "$@" | "$here/ci-scope.sh")
  if [ "$got" != "$want" ]; then
    printf 'FAIL %s\n  want: %s\n  got:  %s\n' "$*" "${want//$'\n'/ | }" "${got//$'\n'/ | }"
    fail=1
  fi
}
light() { printf 'full=false\nexamples=%s\nhmibuild=%s' "$1" "$2"; }
full() { printf 'full=true\nexamples=%s\nhmibuild=%s' "$1" "$2"; }

# The #747 shape: one Svelte line in an example HMI.
check "$(light examples/plcnext-bench false)" examples/plcnext-bench/hmi/src/routes/+page.svelte
check "$(light examples/lift-station true)" examples/lift-station/hmi/src/lib/Pump.svelte
check "$(light 'examples/epic-bench examples/spatial-rig' false)" \
  examples/epic-bench/hmi/package.json examples/spatial-rig/hmi/src/app.css examples/epic-bench/hmi/src/x.ts
# An HMI with no nautilus.yaml beside it: light, nothing to naut check.
check "$(light '' false)" examples/node-3d/hmi/src/App.svelte
check "$(light '' false)" website/src/content/docs/guides/real-time.md website/astro.config.mjs
check "$(light '' false)" playground/app/main.js
check "$(light '' false)" docs/design/context-map.md
# Light paths mixed with anything else: full.
check "$(full examples/plcnext-bench false)" examples/plcnext-bench/hmi/src/routes/+page.svelte runtime/scan.go
check "$(full '' false)" website/src/content/docs/getting-started.md
check "$(full '' false)" docs/claims/getting-started.yaml
check "$(full '' false)" docs/structure-warnings.md
check "$(full '' false)" docs/design/diagram.png
check "$(full '' false)" examples/lift-station/hmi/lib/motor.ld
check "$(full '' false)" examples/lift-station/nautilus.yaml
check "$(full '' false)" examples/batch-skid/screens/line.mimic.json
check "$(full '' false)" README.md
check "$(full '' false)" .github/workflows/ci.yml
check "$(full '' false)" go.mod
check "$(full '' false)" playground/scripts/build.sh
check "$(full '' false)" hmi/src/lib/Tank.svelte
# Nothing changed (or the diff failed): full.
check "$(full '' false)"
if [ $fail = 0 ]; then echo "ci-scope: all cases pass"; fi
exit $fail
