#!/usr/bin/env bash
# F-05: a per-pump run-hours total (P1_Stats, role: state, written by the logic) survives a controller
# restart when the tag is marked for retention. The operator ruled retention opt-in, CODESYS-style
# (runs/RECONCILIATION-2026-11-03.md, ruling 4); /guides/tag-model/ documents it: a `state` tag is
# "Kept across a restart … only with `retain: true`" and needs a `retain:` section
# (`- { name: RunHours, role: state, type: REAL, retain: true }`).
# The check works on a copy of SUT_PROJECT: it makes sure there is a `retain:` section, marks
# P1_Stats `retain: true` and leaves P2_Stats unmarked. It runs the lead pump, sets both Hours to 5
# through POST /api/tags, stops with SIGTERM, starts again, and expects P1_Stats.Hours still >= 5 and
# P2_Stats back at its seed (Hours < 5), as the page says for an unmarked state tag.
# Rewritten 2026-11-03 per ruling 4 (the parent expected every state tag kept with no marking).
# Wall clock ~3.5 s (one 3 s sleep, past one 2 s retain write). Port 18081. SUT_PROJECT = evidence/pumps (which has retain: {}).
set -u
P=${SUT_PROJECT:?SUT_PROJECT}
T=$(mktemp -d); pid=""; trap '[ -n "$pid" ] && kill $pid 2>/dev/null; rm -rf "$T"' EXIT
cp -r "$P"/. "$T"/; cd "$T"; rm -f retain.json
grep -q '^retain:' nautilus.yaml || printf '\nretain: {}\n' >> nautilus.yaml
python3 - <<'EOF'
import re
s = open("nautilus.yaml").read()
s, n = re.subn(r"(\{\s*name:\s*P1_Stats\s*,)", r"\1 retain: true,", s)
assert n == 1, "P1_Stats tag line not found"
open("nautilus.yaml", "w").write(s)
EOF
grep -nE 'P[12]_Stats|^retain:' nautilus.yaml
U=http://127.0.0.1:18081
start() { NAUTILUS_ADDR=127.0.0.1:18081 naut run . > run.log 2>&1 & pid=$!
          for i in $(seq 1 30); do curl -sf $U/api/state >/dev/null && return; sleep 0.2; done; }
stats() { curl -s $U/api/state | python3 -c 'import json,sys; t=json.load(sys.stdin)["tags"]; print(json.dumps({"P1_Stats": t["P1_Stats"], "P2_Stats": t["P2_Stats"]}))'; }
start
curl -s -X POST $U/api/forces -d '{"name":"LevelPct","value":65}' >/dev/null
for n in P1_Stats.Hours P2_Stats.Hours; do
  echo "POST /api/tags $n=5: $(curl -s -o /dev/null -w '%{http_code}' -X POST $U/api/tags -d "{\"name\":\"$n\",\"value\":5}")"; done
sleep 3; echo "before restart: $(stats)"
kill $pid; wait $pid 2>/dev/null; pid=""; echo "retain.json: $(cat retain.json 2>/dev/null)"
start; after=$(stats); echo "after restart:  $after"
python3 - "$after" <<'EOF'
import json, sys
a = json.loads(sys.argv[1]); f = 0
def ok(c, m):
    global f; print(("PASS  " if c else "FAIL  ") + m); f += 0 if c else 1
ok(a["P1_Stats"]["Hours"] >= 5, f"P1_Stats (retain: true) kept its run hours: {a['P1_Stats']}")
ok(a["P2_Stats"]["Hours"] < 5, f"P2_Stats (not marked) re-seeded, as /guides/tag-model/ says: {a['P2_Stats']}")
sys.exit(1 if f else 0)
EOF
rc=$?; [ $rc -eq 0 ] && { echo PASS; exit 0; }; echo "FAIL retention does not follow retain: true"; exit 1
