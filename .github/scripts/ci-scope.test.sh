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
# light <roots> <hmibuild> [lessons]
light() { printf 'full=false\nexamples=%s\nhmibuild=%s\nlessons=%s\nextension=false\ne2e-all=false' "$1" "$2" "${3:-false}"; }
# full <roots> <hmibuild> [extension] [e2e-all, default: as extension] [lessons]
full() { printf 'full=true\nexamples=%s\nhmibuild=%s\nlessons=%s\nextension=%s\ne2e-all=%s' "$1" "$2" "${5:-false}" "${3:-false}" "${4:-${3:-false}}"; }

# The #747 shape: one Svelte line in an example HMI.
check "$(light examples/plcnext-bench false)" examples/plcnext-bench/hmi/src/routes/+page.svelte
check "$(light examples/lift-station true)" examples/lift-station/hmi/src/routes/3d/+page.svelte
check "$(light 'examples/epic-bench examples/spatial-rig' false)" \
  examples/epic-bench/hmi/package.json examples/spatial-rig/hmi/src/app.css examples/epic-bench/hmi/src/x.ts
# An HMI with no nautilus.yaml beside it: light, nothing to naut check.
check "$(light '' false)" examples/node-3d/hmi/src/App.svelte
check "$(light '' false)" website/src/content/docs/guides/real-time.md website/astro.config.mjs
check "$(light '' false)" playground/app/main.js
# The interactive lessons: light, but their Go test runs as its own step.
check "$(light '' false true)" website/lessons/b1-blink/solution/main.st
check "$(light '' false true)" website/lessons/b1-blink/lesson.md website/src/content/docs/guides/real-time.md
# ...and lessons beside anything not light: full (the Go job runs the test).
check "$(full '' false false false true)" website/lessons/b1-blink/lesson.md runtime/scan.go
check "$(light '' false)" docs/design/context-map.md
# Light paths mixed with anything else: full.
check "$(full examples/plcnext-bench false)" examples/plcnext-bench/hmi/src/routes/+page.svelte runtime/scan.go
check "$(full '' false)" website/src/content/docs/getting-started.md
check "$(full '' false)" docs/claims/getting-started.yaml
check "$(full '' false)" docs/structure-warnings.md
check "$(full '' false)" docs/design/diagram.png
check "$(full '' false true false)" examples/lift-station/hmi/lib/motor.ld
check "$(full '' false)" examples/lift-station/nautilus.yaml
check "$(full '' false)" examples/batch-skid/screens/line.mimic.json
check "$(full '' false)" README.md
check "$(full '' false true)" .github/workflows/ci.yml
check "$(full '' false)" go.mod
check "$(full '' false)" playground/scripts/build.sh
check "$(full '' false true)" hmi/src/lib/Tank.svelte
# The extension and the kits it bundles; a Go change alone leaves them out.
check "$(full '' false true)" tools/vscode-iec/src/extension.ts
check "$(full '' false true)" hmi-3d/src/lib/Scene.svelte
check "$(full '' false true)" tools/vscode-iec/package.json cmd/naut/main.go
check "$(full '' false)" cmd/naut/main.go internal/lsp/server.go
# What the extension's tests read outside its folder: every example diagram
# (gesture harness layout fixtures) and the lift station's own components.
# The CLI-discovery e2e reads none of them (it writes its own fixtures), so
# they leave its macOS and Windows legs out (#916)...
check "$(full '' false true false)" examples/new-project/main.ld
check "$(full '' false true false)" examples/batch-skid/dosing.fbd examples/batch-skid/phases.sfc
printf -v want 'full=false\nexamples=examples/lift-station\nhmibuild=true\nlessons=false\nextension=true\ne2e-all=false'
check "$want" examples/lift-station/hmi/src/lib/Rain.svelte
# ...unless what the e2e does read changed alongside, in either order.
check "$(full '' false true)" examples/new-project/main.ld tools/vscode-iec/src/e2e/suite.ts
check "$(full '' false true)" hmi/src/lib/Tank.svelte examples/batch-skid/dosing.fbd
printf -v want 'full=true\nexamples=examples/lift-station\nhmibuild=true\nlessons=false\nextension=true\ne2e-all=true'
check "$want" examples/lift-station/hmi/src/lib/Rain.svelte hmi-3d/src/lib/Scene.svelte
check "$(light examples/lift-station true)" examples/lift-station/hmi/src/routes/+page.svelte
# Nothing changed (or the diff failed): full, extension included.
check "$(full '' false true)"

# e2e-all: a .github/ change runs the e2e's macOS and Windows legs only when
# the e2e's own definition changed, judged against a base ci.yml. The cases
# edit a copy of the real one (yq), so they follow it as it grows.
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
head_ci=.github/workflows/ci.yml
variant() { # variant <name> <yq expression>: a base ci.yml, <name>.yml
  yq "$2" "$head_ci" >"$tmp/$1.yml" || { echo "FAIL yq could not build $1"; fail=1; }
}
cp "$head_ci" "$tmp/same.yml"
variant other-job '.jobs.go.timeout-minutes = 99'
variant e2e-timeout '.jobs["vscode-iec-e2e"].timeout-minutes = 99'
variant e2e-step '.jobs["vscode-iec-e2e"].steps += [{"run": "true"}]'
variant changes-job '.jobs.changes.outputs.e2e-os = "[\"ubuntu-latest\"]"'
variant top-env '.env.EXTRA = "1"'
variant comment '. head_comment = "a comment only"'
printf 'jobs: [not, a, map\n' >"$tmp/garbled.yml"
base() { # base <variant> <want e2e-all> <paths...>: scope against that base
  local v=$1 want=$2; shift 2
  local got
  got=$(printf '%s\n' "$@" | CI_SCOPE_BASE_CI="$tmp/$v.yml" "$here/ci-scope.sh" | sed -n 's/^e2e-all=//p')
  if [ "$got" != "$want" ]; then
    printf 'FAIL base=%s %s\n  want e2e-all=%s, got %s\n' "$v" "$*" "$want" "$got"
    fail=1
  fi
}
# The e2e's definition unchanged: Linux only, though the extension jobs run.
base same false .github/workflows/ci.yml
base same false .github/scripts/ci-ok.sh .github/workflows/race-nightly.yml
base other-job false .github/workflows/ci.yml
base comment false .github/workflows/ci.yml
# The e2e's definition changed: all three legs.
base e2e-timeout true .github/workflows/ci.yml
base e2e-step true .github/workflows/ci.yml
base changes-job true .github/workflows/ci.yml
base top-env true .github/workflows/ci.yml
# Fail closed: a base that is missing or does not parse is a change.
base missing true .github/workflows/ci.yml
base garbled true .github/workflows/ci.yml
# The extension itself changed: all three, whatever the base says.
base same true .github/workflows/ci.yml tools/vscode-iec/src/extension.ts
base same true hmi/src/lib/Tank.svelte .github/workflows/ci.yml
# A head without the e2e job never counts as unchanged.
yq 'del(.jobs["vscode-iec-e2e"])' "$head_ci" >"$tmp/no-e2e.yml"
got=$(printf '.github/workflows/ci.yml\n' | CI_SCOPE_BASE_CI="$tmp/no-e2e.yml" CI_SCOPE_HEAD_CI="$tmp/no-e2e.yml" "$here/ci-scope.sh" | sed -n 's/^e2e-all=//p')
[ "$got" = true ] || { echo "FAIL head without the e2e job: e2e-all=$got, want true"; fail=1; }
if [ $fail = 0 ]; then echo "ci-scope: all cases pass"; fi
exit $fail
