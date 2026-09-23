#!/usr/bin/env bash
# Reproduces: a Sparkplug edge node that was silent long enough for the broker to time out its keepalive
# reconnects, rebirths, and then never publishes NDATA again. See
# docs/handover/2026-09-19-sparkplug-edge-findings.md.
#
#   scripts/repro-sparkplug-silent-link.sh            # builds ./cmd/naut, expects a broker on localhost:1883
#   BROKER_HOST=10.0.0.5 BROKER_PORT=1883 scripts/repro-sparkplug-silent-link.sh
#   MOSQ="docker compose -f ../ignition/docker-compose.yml exec -T broker" scripts/repro-sparkplug-silent-link.sh
#
# Needs mosquitto_sub on PATH, or MOSQ set to a prefix that runs it somewhere the broker is "localhost"
# (when MOSQ is set, the subscriber connects to localhost:1883 from inside that container).
# Takes about 70 s: it has to outwait a 30 s keepalive.
#
# Freezing the process with SIGSTOP is a faithful stand-in for a hung controller or a dead radio link: the
# socket stays open and nothing arrives, which is the one way a link dies that closing a socket can't imitate.
set -uo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
host="${BROKER_HOST:-localhost}"; port="${BROKER_PORT:-1883}"
MOSQ="${MOSQ:-}"
if [ -n "$MOSQ" ]; then sub_host=localhost; sub_port=1883; else sub_host="$host"; sub_port="$port"; fi

work="$(mktemp -d)"; pid=""
cleanup() { [ -n "$pid" ] && { kill -CONT "$pid" 2>/dev/null; kill "$pid" 2>/dev/null; }; }
trap cleanup EXIT

echo "building nautilus from $root"
(cd "$root" && go build -o "$work/nautilus" ./cmd/naut) || exit 1

mkdir -p "$work/edge"
cat > "$work/edge/device.st" <<'EOF'
PROGRAM Device
VAR_EXTERNAL
    LevelSP : REAL;
    LevelFt : REAL;
END_VAR
LevelFt := LevelSP;
EOF
cat > "$work/edge/nautilus.yaml" <<EOF
name: silent-link-repro
server:
  addr: "localhost:18191"
tasks:
  - program: device.st
    scan: 100ms
tags:
  - { name: LevelSP, role: setpoint, init: 10.0 }
  - { name: LevelFt, role: state,    init: 10.0 }
driver:
  type: memory
sparkplug:
  broker: tcp://$host:$port
  group-id: Repro
  edge-node: silent-link
  default-class: { deadband: 0, max-interval: 10s }
EOF

cd "$work/edge"; "$work/nautilus" run > "$work/log" 2>&1 & pid=$!; cd - >/dev/null
sleep 3

$MOSQ timeout 120 mosquitto_sub -h "$sub_host" -p "$sub_port" -t 'spBv1.0/Repro/+/silent-link' -F '%U %t' \
    > "$work/wire.log" 2>/dev/null &

status() {
    curl -s localhost:18191/api/state | python3 -c "
import json,sys
d=[x for x in json.load(sys.stdin)['drivers'] if x.get('kind')=='sparkplug'][0]
m={x['label']:x.get('text',x['value']) for x in d['metrics']}
print('state=%s born=%s messages=%s seq=%s | %s' % (d['state'], d['extra'].get('born'), m.get('messages'), m.get('seq'), d['message']))"
}
set_level() { curl -s -X POST localhost:18191/api/tags -d "{\"name\":\"LevelSP\",\"value\":$1}"; }

set_level 55; sleep 1
echo "before: $(status)"

kill -STOP "$pid"; echo "frozen at $(date +%T); waiting 50 s for the broker to time out the 30 s keepalive"
sleep 50
thaw=$(date +%s.%N); kill -CONT "$pid"; echo "thawed at $(date +%T)"
sleep 4
for v in 56 57 58; do set_level $v; sleep 1; done
sleep 1

echo "after:  $(status)"
echo "edge's own LevelFt: $(curl -s localhost:18191/api/state | python3 -c "import json,sys; print(json.load(sys.stdin)['tags']['LevelFt'])")"
echo "--- messages on the wire after the thaw (three tag changes were made):"
awk -v t="$thaw" '$1 >= t {print $2}' "$work/wire.log" | sed -E 's|spBv1.0/[^/]+/||' | sort | uniq -c
ndata=$(awk -v t="$thaw" '$1 >= t && $2 ~ /NDATA/' "$work/wire.log" | wc -l)

echo "--- nautilus log:"; grep -v dashboard "$work/log" | tail -5

# Where is the publish loop? SIGQUIT makes the Go runtime dump every goroutine's stack.
kill -QUIT "$pid"; sleep 1; pid=""
echo "--- the sparkplug node's goroutines:"
awk 'BEGIN{RS=""} /sparkplug\.\(\*Node\)/ {print; print ""}' "$work/log" | grep -E '^goroutine|sparkplug\.|paho.*Wait|^\s+/.*nautilus/sparkplug' | head -20

echo
if [ "$ndata" -ge 3 ]; then echo "PASS: $ndata NDATA after the reconnect"; else echo "FAIL: $ndata NDATA after the reconnect (want 3)"; exit 1; fi
