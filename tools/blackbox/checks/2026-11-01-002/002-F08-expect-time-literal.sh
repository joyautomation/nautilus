#!/usr/bin/env bash
# F-08: `expect: { HiLevelDelay: T#30s }` against the TIME setpoint seeded `init: T#30s` should pass.
# Today it fails `HiLevelDelay = 30000, want "T#30s"`. SUT_PROJECT = evidence/pumps.
set -u
P=${SUT_PROJECT:?SUT_PROJECT}
T=$(mktemp -d); trap 'rm -rf "$T"' EXIT; cp -r "$P"/. "$T"/; cd "$T"; rm -f retain.json *_test.yaml
cat > t_test.yaml <<'Y'
tests:
  - name: a TIME tag compares to a TIME literal
    steps:
      - expect: { HiLevelDelay: T#30s }
Y
out=$(naut test -v 2>&1); rc=$?; echo "$out"; exit $rc
