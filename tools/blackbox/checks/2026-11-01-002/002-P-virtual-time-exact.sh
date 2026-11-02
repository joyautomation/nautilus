#!/usr/bin/env bash
# pass: virtual time is exact. LevelPct (an input) given at t=0 arrives with scan 1 (t=0.1 s); the
# 30 s TON is still FALSE at t=30.0 s and TRUE at t=30.1 s; a pump running 8 h accumulates
# 8.0000 h (±0.0001). Writes its own test file into a copy of SUT_PROJECT (evidence/pumps).
set -u
P=${SUT_PROJECT:?SUT_PROJECT}
T=$(mktemp -d); trap 'rm -rf "$T"' EXIT; cp -r "$P"/. "$T"/; cd "$T"; rm -f retain.json *_test.yaml
cat > edge_test.yaml <<'Y'
tests:
  - name: the 30 s alarm lands on the scan at 30.1 s
    given: { LevelPct: 95.0 }
    steps:
      - advance: 29.9s
        expect: { HiLevelAlm: false }
      - advance: 0.1s
        expect: { HiLevelAlm: false }
      - advance: 0.1s
        expect: { HiLevelAlm: true }
  - name: run hours after 8 h exactly
    given: { LevelPct: 65.0 }
    steps:
      - advance: 8h
        expect: { P1_Stats.Hours: { near: 8.0, tol: 0.0001 } }
Y
out=$(naut test -v 2>&1); rc=$?; echo "$out"; exit $rc
