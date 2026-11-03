#!/usr/bin/env bash
# F-07: `given: { HiLevelDelay: T#8h }` on a `type: TIME` setpoint must set an 8 h delay (or be refused
# as a type error). Today it stores the STRING "T#8h" and the TON with PT := HiLevelDelay turns on at
# the first scan. Passes when the alarm is still FALSE 31 s in, or naut test rejects the given with a
# message naming the tag. SUT_PROJECT = evidence/pumps.
set -u
P=${SUT_PROJECT:?SUT_PROJECT}
T=$(mktemp -d); trap 'rm -rf "$T"' EXIT; cp -r "$P"/. "$T"/; cd "$T"; rm -f retain.json *_test.yaml
cat > t_test.yaml <<'Y'
tests:
  - name: T#8h given to a TIME setpoint delays the alarm
    given: { HiLevelDelay: T#8h, LevelPct: 95.0 }
    steps:
      - advance: 31s
        expect: { HiLevelAlm: false }
Y
out=$(naut test -v 2>&1); rc=$?; echo "$out"
if [ $rc -eq 0 ]; then echo "PASS alarm still FALSE at 31 s"; exit 0; fi
if echo "$out" | grep -q "HiLevelAlm = true"; then echo "FAIL the T#8h given did not delay the TON"; exit 1; fi
if echo "$out" | grep -qi "HiLevelDelay" && ! echo "$out" | grep -q "FAIL "; then echo "PASS given refused naming the tag"; exit 0; fi
echo "FAIL unexpected output"; exit 1
