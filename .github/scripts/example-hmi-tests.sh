#!/usr/bin/env bash
# Runs `npm test` in each example project's HMI app that has a "test"
# script in its package.json (npm ci first, from the committed lockfile).
#
#   example-hmi-tests.sh                     every examples/*/hmi
#   example-hmi-tests.sh examples/a ...      those project roots' hmi/ only
#
# CI's examples job calls it with the roots the scope picked (all of them
# on a full run). EXAMPLES_DIR (default: examples) is where "every" looks;
# the table test points it at fixtures. Exits non-zero when any test fails,
# after running them all; an HMI without a test script is skipped.
set -u
if [ $# -eq 0 ]; then
  roots=()
  for d in "${EXAMPLES_DIR:-examples}"/*/; do roots+=("${d%/}"); done
else
  roots=("$@")
fi
failed=()
ran=0
for root in "${roots[@]}"; do
  app=$root/hmi
  [ -f "$app/package.json" ] || continue
  node -e 'process.exit(require(process.argv[1]).scripts?.test ? 0 : 1)' "$PWD/$app/package.json" 2>/dev/null || continue
  ran=$((ran + 1))
  echo "::group::$app: npm test"
  if (cd "$app" && npm ci --no-audit --no-fund --loglevel=error && npm test); then
    echo "::endgroup::"
  else
    echo "::endgroup::"
    echo "::error::npm test failed in $app"
    failed+=("$app")
  fi
done
echo "example HMI tests: $ran run, ${#failed[@]} failed${failed[*]:+: ${failed[*]}}"
[ ${#failed[@]} -eq 0 ]
