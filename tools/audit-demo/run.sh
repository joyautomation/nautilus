#!/usr/bin/env bash
# The audit trail, shown end to end (#709 PR 10). See
# website/src/content/docs/guides/audit-trail.md for the walk-through.
#
#   tools/audit-demo/run.sh              pauses between beats (press Enter)
#   tools/audit-demo/run.sh --no-pause   runs straight through (CI runs it so)
#
# It builds a lift station from examples/lift-station in a scratch
# directory, gives it two people (alice, an operator, and bob, an
# engineer), a journal with six weeks of past history, and a copy of that
# journal in Postgres under a role that can only add to it. Then:
#
#   Act 1  operators: who did what. alice and bob sign in and work the
#          station; a substitute runs out and is put back by `system`.
#   Act 2  the auditor: was anything changed? verify says verified; an
#          edited line and a deleted file each say BROKEN while the
#          Postgres copy still holds the original; a lowered retention is
#          a WARNING on an otherwise good verdict.
#
# The past history is written by tools/audit-demo/seed with the real
# journal writer and its clock set to each past day: the controller the
# demo pretends was running. Everything from Act 1 on is live.
#
# Needs: go (unless NAUT and SEED are given), curl, jq, and psql or docker.
#
# Environment:
#   NAUT            a naut binary (default: built from this tree)
#   SEED            the seed binary (default: built from this tree)
#   DEMO_DIR        where to build it (default: a new temporary directory)
#   DEMO_ADDR       the controller's address (default 127.0.0.1:0, any free port)
#   DEMO_ADMIN_DSN  a Postgres superuser DSN; the demo makes the database
#                   naut_audit_demo and the role naut_audit_demo there.
#                   Default: a postgres:16 container on 127.0.0.1:55440.
#   DEMO_KEEP=1     leave the directory, the database and the container
set -euo pipefail

PAUSE=1
for a in "$@"; do
	case "$a" in
	--no-pause) PAUSE=0 ;;
	-h | --help)
		sed -n '2,/^set -euo/p' "$0" | sed '$d; s/^# \{0,1\}//'
		exit 0
		;;
	*)
		echo "run.sh: unknown argument $a (try --help)" >&2
		exit 2
		;;
	esac
done

REPO=$(cd "$(dirname "$0")/../.." && pwd)
for c in curl jq; do
	command -v "$c" >/dev/null || {
		echo "run.sh: needs $c" >&2
		exit 2
	}
done

# The demo removes its directory at the end, so it only ever uses one it
# made: a new temporary one, or a DEMO_DIR that does not exist yet.
if [ -z "${DEMO_DIR:-}" ]; then
	DEMO_DIR=$(mktemp -d -t naut-audit-demo.XXXXXX)
elif [[ /$DEMO_DIR/ == */../* ]]; then
	echo "run.sh: DEMO_DIR may not contain .. (the demo deletes it afterwards)" >&2
	exit 2
elif [ -e "$DEMO_DIR" ]; then
	echo "run.sh: DEMO_DIR $DEMO_DIR exists; name one that does not (the demo deletes it afterwards)" >&2
	exit 2
else
	mkdir -p "$DEMO_DIR"
fi
DEMO_DIR=$(cd "$DEMO_DIR" && pwd)
P=$DEMO_DIR/project # the lift station
STATE=$DEMO_DIR/state # the controller's state: users file (never in the project)
DB=naut_audit_demo
ROLE=naut_audit_demo
CONTAINER=naut-audit-demo-pg
RUN_PID=

# ---------------------------------------------------------------- output

bold=$'\e[1m' dim=$'\e[2m' red=$'\e[31m' green=$'\e[32m' yellow=$'\e[33m' off=$'\e[0m'
if [ ! -t 1 ]; then bold= dim= red= green= yellow= off=; fi

beat() { # beat <title> <what to look at>: a pause point for the narration
	printf '\n%s== %s ==%s\n' "$bold" "$1" "$off"
	[ -n "${2:-}" ] && printf '%s%s%s\n' "$dim" "$2" "$off"
	if [ "$PAUSE" = 1 ]; then
		read -r -p "  (Enter to go on) " _ </dev/tty || true
	fi
	return 0
}
say() { printf '  %s\n' "$*"; }
fail() {
	printf '%sDEMO FAILED: %s%s\n' "$red" "$*" "$off" >&2
	exit 1
}

stop_controller() { # a clean stop: the copy catches up and anchors the head
	[ -n "$RUN_PID" ] || return 0
	kill -TERM "$RUN_PID" 2>/dev/null || true
	for _ in $(seq 60); do
		kill -0 "$RUN_PID" 2>/dev/null || break
		sleep 0.5
	done
	kill -KILL "$RUN_PID" 2>/dev/null || true
	wait "$RUN_PID" 2>/dev/null || true
	RUN_PID=
}
cleanup() {
	local code=$?
	set +e
	stop_controller
	if [ "${DEMO_KEEP:-}" != 1 ]; then
		if [ -n "${DB_MADE:-}" ]; then
			psql_ "$DEMO_ADMIN_DSN" -c "DROP DATABASE IF EXISTS $DB" >/dev/null 2>&1 ||
				echo "run.sh: could not drop the database $DB; drop it by hand" >&2
		fi
		if [ -n "${ROLE_MADE:-}" ]; then
			psql_ "$DEMO_ADMIN_DSN" -c "DROP ROLE IF EXISTS $ROLE" >/dev/null 2>&1 ||
				echo "run.sh: could not drop the role $ROLE; drop it by hand" >&2
		fi
		[ -n "${STARTED_CONTAINER:-}" ] && docker stop "$CONTAINER" >/dev/null 2>&1
		rm -rf "$DEMO_DIR"
	else
		echo "kept: $DEMO_DIR"
	fi
	exit $code
}
trap cleanup EXIT

# ---------------------------------------------------------------- tools

if [ -z "${NAUT:-}" ] || [ -z "${SEED:-}" ]; then
	mkdir -p "$DEMO_DIR/bin"
	(cd "$REPO" && go build -o "$DEMO_DIR/bin/naut" ./cmd/naut && go build -o "$DEMO_DIR/bin/seed" ./tools/audit-demo/seed)
	NAUT=${NAUT:-$DEMO_DIR/bin/naut}
	SEED=${SEED:-$DEMO_DIR/bin/seed}
fi

# ---------------------------------------------------------------- Postgres

if [ -z "${DEMO_ADMIN_DSN:-}" ]; then
	command -v docker >/dev/null || fail "set DEMO_ADMIN_DSN, or install docker for a throwaway Postgres"
	if ! docker ps --format '{{.Names}}' | grep -qx "$CONTAINER"; then
		docker run -d --rm --name "$CONTAINER" -e POSTGRES_PASSWORD=postgres \
			-p 127.0.0.1:55440:5432 postgres:16 >/dev/null
		STARTED_CONTAINER=1
	fi
	DEMO_ADMIN_DSN="postgres://postgres:postgres@127.0.0.1:55440/postgres?sslmode=disable"
	OWN_PG=1
fi
command -v psql >/dev/null || [ -n "${OWN_PG:-}" ] || fail "with DEMO_ADMIN_DSN set, the demo needs psql"
# The admin DSN must be a URL with a user and a database:
# postgres://user:password@host:port/db[?…].
[[ $DEMO_ADMIN_DSN =~ ^postgres(ql)?://[^@/]+@[^/?]+/[^?]* ]] ||
	fail "DEMO_ADMIN_DSN must look like postgres://user:password@host:port/database[?sslmode=…]"
dsn_for() { # dsn_for <user:password or ""> <database>: the admin DSN, changed
	local d=$DEMO_ADMIN_DSN
	if [ -n "$1" ]; then d=$(sed -E "s#^(postgres(ql)?://)[^@/]*@#\1$1@#" <<<"$d"); fi
	sed -E "s#^(postgres(ql)?://[^/]*)/[^?]*#\1/$2#" <<<"$d"
}
psql_() { # psql_ <dsn> <psql args...>: psql here, or the one in the demo's container
	local dsn=$1 pw bare
	shift
	# The password goes in PGPASSWORD, so it is never in a process listing.
	pw=$(sed -nE 's#^postgres(ql)?://[^:@/]*:([^@/]*)@.*#\2#p' <<<"$dsn")
	bare=$(sed -E 's#^(postgres(ql)?://[^:@/]*):[^@/]*@#\1@#' <<<"$dsn")
	if command -v psql >/dev/null; then
		PGPASSWORD=$pw psql "$bare" -v ON_ERROR_STOP=1 -X -q "$@"
	else
		docker exec -i -e PGPASSWORD="$pw" "$CONTAINER" psql "$(sed -E 's#@[^/]*/#@localhost:5432/#' <<<"$bare")" -v ON_ERROR_STOP=1 -X -q "$@"
	fi
}
up=
for _ in $(seq 60); do
	if psql_ "$DEMO_ADMIN_DSN" -c 'SELECT 1' >/dev/null 2>&1; then
		up=1
		break
	fi
	sleep 0.5
done
[ -n "$up" ] || fail "Postgres does not answer at DEMO_ADMIN_DSN: $(psql_ "$DEMO_ADMIN_DSN" -c 'SELECT 1' 2>&1 | head -1)"
# The demo marks what it makes, and only ever drops or reuses what it
# marked: a database or role of that name that someone else made is left
# alone, and the demo stops.
MARK='made by the nautilus audit demo (tools/audit-demo/run.sh)'
q() { # q <sql>: one value into $DEMO_DIR/q.out (not a subshell, so fail stops the demo)
	psql_ "$DEMO_ADMIN_DSN" -At -c "$1" >"$DEMO_DIR/q.out" 2>"$DEMO_DIR/psql.err" || fail "Postgres: $(cat "$DEMO_DIR/psql.err")"
}
q "SELECT coalesce(shobj_description(oid, 'pg_database'), '-') FROM pg_database WHERE datname = '$DB'"
db=$(cat "$DEMO_DIR/q.out")
q "SELECT coalesce(shobj_description(oid, 'pg_authid'), '-') FROM pg_roles WHERE rolname = '$ROLE'"
role=$(cat "$DEMO_DIR/q.out")
[ -z "$db" ] || [ "$db" = "$MARK" ] || fail "a database $DB exists that the demo did not make; it is left alone (use another Postgres)"
[ -z "$role" ] || [ "$role" = "$MARK" ] || fail "a role $ROLE exists that the demo did not make; it is left alone (use another Postgres)"
psql_ "$DEMO_ADMIN_DSN" -c "DROP DATABASE IF EXISTS $DB" -c "CREATE DATABASE $DB" -c "COMMENT ON DATABASE $DB IS '$MARK'" \
	>/dev/null 2>"$DEMO_DIR/psql.err" || fail "could not make the database $DB: $(cat "$DEMO_DIR/psql.err")"
DB_MADE=1
if [ -z "$role" ]; then
	psql_ "$DEMO_ADMIN_DSN" -c "CREATE ROLE $ROLE LOGIN PASSWORD 'demo-only'" -c "COMMENT ON ROLE $ROLE IS '$MARK'" \
		>/dev/null 2>"$DEMO_DIR/psql.err" || fail "could not make the role $ROLE: $(cat "$DEMO_DIR/psql.err")"
fi
ROLE_MADE=1
# The tables are the superuser's; the controller's role may only add rows
# (naut audit pg-schema prints exactly these grants).
"$NAUT" audit pg-schema --role "$ROLE" >"$DEMO_DIR/schema.sql" || fail "naut audit pg-schema failed"
psql_ "$(dsn_for "" "$DB")" <"$DEMO_DIR/schema.sql" >/dev/null 2>"$DEMO_DIR/psql.err" ||
	fail "the grants did not apply: $(grep -v NOTICE "$DEMO_DIR/psql.err")"
export DEMO_AUDIT_DB
DEMO_AUDIT_DB=$(dsn_for "$ROLE:demo-only" "$DB")

# ---------------------------------------------------------------- the station

rm -rf "$P" "$STATE"
mkdir -p "$P" "$STATE"
chmod 700 "$STATE"
(cd "$REPO/examples/lift-station" &&
	tar --exclude=./hmi/node_modules --exclude=./hmi/.svelte-kit --exclude=./retain.json --exclude=./audit -cf - .) |
	tar -xf - -C "$P"
cd "$P"

# One input on the discharge header, so a substitute can run out.
printf -- '- { name: PIT101_Pressure, role: input, type: REAL, init: 42.0, unit: psi, desc: "Discharge header pressure (PIT-101)" }\n' >tags/audit-demo.yaml
# after_line <line> <new line>: insert into nautilus.yaml (awk, not GNU sed)
after_line() { awk -v at="$1" -v add="$2" '{ print } $0 == at { print add }' nautilus.yaml >nautilus.yaml.new && mv nautilus.yaml.new nautilus.yaml; }
after_line "  - tags/station.yaml" "  - tags/audit-demo.yaml"
sed -i.bak -E 's|^  addr: .*|  addr: "'"${DEMO_ADDR:-127.0.0.1:0}"'"|' nautilus.yaml
rm -f nautilus.yaml.bak
cat >>nautilus.yaml <<'EOF'

substitutes:
  rules:
    - { match: PIT101_Pressure, substitute: 45.0, max: 30m }

audit:
  keep-days: 30         # the site's rule: a month on the box
  sink: postgres        # and a copy the controller can only add to
  dsn-env: DEMO_AUDIT_DB
  anchor-every: 2s      # 5m in the field; short so the demo needn't wait
EOF
"$NAUT" auth init --no-tls --user alice:operator --user bob:engineer >/dev/null 2>&1
after_line "auth:" "  state-dir: \"$STATE\""
grep -qF "  state-dir: \"$STATE\"" nautilus.yaml || fail "auth: block not found to put state-dir in"
echo 'alice-demo-pw' | "$NAUT" users add alice >/dev/null 2>&1
echo 'bob-demo-pw' | "$NAUT" users add bob >/dev/null 2>&1

"$SEED" -dir audit -days 42 -keep-days 30
# Pruned as it went, as naut run prunes at every start: 31 days are left
# (yesterday back to the one that turns 31 days old today).
[ "$(ls audit | wc -l | tr -d ' ')" = 31 ] || fail "the seeded journal has $(ls audit | wc -l | tr -d ' ') files, want 31"

# ---------------------------------------------------------------- the controller

start_controller() {
	: >"$DEMO_DIR/run.log"
	"$NAUT" run >>"$DEMO_DIR/run.log" 2>&1 &
	RUN_PID=$!
	URL=
	for _ in $(seq 120); do
		URL=$(grep -oE 'on http://[0-9.:]+' "$DEMO_DIR/run.log" | head -1 | sed 's/^on //') || true
		[ -n "$URL" ] && curl -fsS "$URL/readyz" >/dev/null 2>&1 && break
		kill -0 "$RUN_PID" 2>/dev/null || {
			cat "$DEMO_DIR/run.log" >&2
			fail "naut run stopped"
		}
		sleep 0.5
	done
	[ -n "$URL" ] && curl -fsS "$URL/readyz" >/dev/null 2>&1 || fail "naut run never answered /readyz (log: $DEMO_DIR/run.log)"
}

# as <who> <method> <path> [json]: a request from that person's browser
# session (signed in over loopback; a write carries the page's Origin).
as() {
	local who=$1 m=$2 path=$3 body=${4:-}
	local args=(-sS -o "$DEMO_DIR/body" -w '%{http_code}' -X "$m" -b "$DEMO_DIR/$who.jar" -c "$DEMO_DIR/$who.jar"
		-H "Origin: $URL" -H 'X-Nautilus-Client: hmi')
	# The body goes on stdin, so a password is never in a process listing.
	[ -n "$body" ] && args+=(-H 'Content-Type: application/json' --data-binary @-)
	printf '%s' "$body" | curl "${args[@]}" "$URL$path"
}
expect() { # expect <status> <who> <method> <path> [json]
	local want=$1
	shift
	local got
	got=$(as "$@") || got="no answer"
	case "$want:$got" in "$got:$got" | 2xx:2??) ;; *) fail "$1 $2 $3: $got, want $want: $(cat "$DEMO_DIR/body")" ;; esac
}
signin() { # signin <who> <password>
	rm -f "$DEMO_DIR/$1.jar"
	expect 2xx "$1" POST /api/signin "{\"name\":\"$1\",\"password\":\"$2\"}"
	say "$1 signs in"
}

tab() { if command -v column >/dev/null; then column -t -s $'\t'; else cat; fi; }
journal() { # journal <who>: today's entries, as the journal holds them
	jq -r --arg since "$SINCE" '.entries | reverse | .[] | select(.ts >= $since) |
		def v: if type == "object" then (if has("value") then .value elif has("live") then .live else "…" end | tostring)
			elif type == "array" then "…" else tostring end;
		[(.ts[11:19]), .user, .action, (.target // ""),
		 (if .before != null or .after != null then "\(if .before == null then "" else (.before | v) end) → \(if .after == null then "" else (.after | v) end)" else "" end),
		 .result, (if .result == "refused" then (.detail.reason // "") else "" end)] | @tsv' \
		"$DEMO_DIR/body" | tab | sed 's/^/    /'
}

verify() { # verify <want exit>: naut audit verify against the Postgres anchors
	local code=0
	"$NAUT" audit verify --anchor postgres >"$DEMO_DIR/verify.out" 2>&1 || code=$?
	sed -E "s/^(verified.*)/$green\1$off/; s/^(BROKEN.*)/$red\1$off/; s/^(WARNING.*)/$yellow\1$off/; s/^/    /" "$DEMO_DIR/verify.out"
	say "${dim}exit $code$off"
	[ "$code" = "$1" ] || fail "verify exited $code, want $1"
}

# ================================================================ Act 1

beat "Act 1: the everyday trail" "Two people, two roles, one lift station. Every action lands in the journal under the person who took it."
SINCE=$(date -u +%Y-%m-%dT%H:%M:%S)
start_controller
say "naut run is up at $URL, with auth: on (alice is an operator, bob an engineer)"

signin alice alice-demo-pw
expect 2xx alice POST /api/tags '{"name":"InflowGpm","value":240}'
say "alice raises the bench inflow to 240 gpm"
expect 403 alice POST /api/forces '{"name":"LIT101_Level","value":85}'
say "alice tries to force the level: refused, an operator can't force (and the refusal is journalled too)"
expect 2xx alice POST /api/tags '{"name":"ForceLSHH","value":true}'
for _ in $(seq 40); do
	as alice GET /api/alarms >/dev/null
	jq -e '[.alarms[]? | select(.id == "LSHH101" and .cond)] | length > 0' "$DEMO_DIR/body" >/dev/null && break
	sleep 0.25
done
jq -e '[.alarms[]? | select(.id == "LSHH101" and .cond)] | length > 0' "$DEMO_DIR/body" >/dev/null || fail "the high-high alarm never came in"
expect 2xx alice POST /api/alarms/ack '{"ids":["LSHH101"]}'
expect 2xx alice POST /api/tags '{"name":"ForceLSHH","value":false}'
say "the high-high float trips; alice acknowledges the alarm"
expect 2xx alice POST /api/signout
say "alice signs out"

signin bob bob-demo-pw
expect 2xx bob POST /api/forces '{"name":"LIT101_Level","value":85}'
expect 2xx bob DELETE /api/forces/LIT101_Level
say "bob forces the wet-well level to 85% for a check, then clears the force"
# An online edit sends the running source, changed, with its hash as the base.
expect 2xx bob GET '/api/program?task=stats'
base=$(jq -r .hash "$DEMO_DIR/body")
jq -r .source "$DEMO_DIR/body" | sed 's|StationFlowGal + FIT101_Flow \* StatsDtS|StationFlowGal + MAX(FIT101_Flow, 0.0) * StatsDtS|' >"$DEMO_DIR/stats.st"
grep -q 'MAX(FIT101_Flow' "$DEMO_DIR/stats.st" || fail "the totalizer line was not found in the running source"
expect 2xx bob PUT '/api/program?task=stats' "$(jq -Rs --arg b "$base" '{source: ., baseHash: $b}' "$DEMO_DIR/stats.st")"
say "bob edits the totalizer online (never counts negative flow)"
expect 2xx bob POST /api/substitutes "{\"name\":\"PIT101_Pressure\",\"seconds\":2,\"requestId\":\"demo-$(date +%s%N)\"}"
say "bob substitutes 45 psi for the discharge pressure, for 2 seconds"
for _ in $(seq 40); do
	as bob GET "/api/audit?action=substitute.expire" >/dev/null
	jq -e '.entries | length > 0' "$DEMO_DIR/body" >/dev/null && break
	sleep 0.25
done
jq -e '.entries[0].user == "system"' "$DEMO_DIR/body" >/dev/null || fail "no substitute.expire by system"
say "...it runs out, and the controller puts the sensor back: that one is journalled as system"

as bob GET "/api/audit?limit=200" >/dev/null
beat "The journal, as bob (an engineer may read it) sees it" "Who, what, on which tag, and whether it was allowed."
journal
for want in "user:alice auth.signin" "user:alice tag.write" "user:alice force.set refused" "user:alice alarm.ack" "user:bob force.set" \
	"user:bob force.clear" "user:bob program.edit" "user:bob substitute.start" "system substitute.expire"; do
	read -r u a r <<<"$want"
	jq -e --arg u "$u" --arg a "$a" --arg r "${r:-ok}" --arg since "$SINCE" \
		'[.entries[] | select(.ts >= $since and .user == $u and .action == $a and .result == $r)] | length > 0' \
		"$DEMO_DIR/body" >/dev/null || fail "the journal has no $want"
done
stop_controller

# ================================================================ Act 2

beat "Act 2: was anything changed?" "naut audit verify checks the journal's hash chain, then every line against the Postgres copy and its anchors."
verify 0
pruned=$(cat audit/audit-*.jsonl | jq -r --arg since "$SINCE" 'select(.ts >= $since and .action == "audit.prune" and .user == "system") | .target')
[ -n "$pruned" ] || fail "no honest prune: the run should have pruned the day that turned 31 days old"
say "verified. The run pruned $pruned, the day that turned 31 days old: the prune is journalled with the retention it pruned by"
if grep -q 'pruned from the journal, kept in the copy' "$DEMO_DIR/verify.out"; then
	say "and the copy in Postgres keeps what it removed"
fi

F=$(ls audit/audit-*.jsonl | sed -n 10p)
cp -a audit "$DEMO_DIR/audit.bak"
beat "Someone with root on the box rewrites history" "One alarm acknowledgement in $(basename "$F") is changed from alice to bob."
line=$(grep -n '"user":"user:alice","source":"hmi","action":"alarm.ack"' "$F" | head -1 | cut -d: -f1)
[ -n "$line" ] || fail "no alarm acknowledgement by alice in $F"
seq=$(sed -n "${line}p" "$F" | jq -r .seq)
sed -i.bak "${line}s/\"user\":\"user:alice\"/\"user\":\"user:bob\"/" "$F" && rm -f "$F.bak"
verify 1
say "the copy in Postgres still holds the original:"
psql_ "$DEMO_AUDIT_DB" -At -c "SELECT '    seq ' || seq || ': ' || \"user\" || ' ' || action || ' ' || target FROM audit_entries WHERE seq = $seq"

rm -rf audit && cp -a "$DEMO_DIR/audit.bak" audit
beat "Or deletes a whole day" "$(basename "$F") is removed."
rm "$F"
verify 1

rm -rf audit && cp -a "$DEMO_DIR/audit.bak" audit
beat "Put back as it was" ""
verify 0

beat "A lowered retention" "Someone sets audit.keep-days from 30 to 7 and restarts: the old files go, and every verify says so from then on."
sed -i.bak 's/^  keep-days: 30 .*/  keep-days: 7/' nautilus.yaml && rm -f nautilus.yaml.bak
start_controller
# naut run holds the prune until Postgres has anchored the new retention,
# then removes every day older than 7: a week of files and today's are left.
for _ in $(seq 120); do
	[ "$(ls audit | wc -l | tr -d ' ')" = 8 ] && break
	sleep 0.5
done
[ "$(ls audit | wc -l | tr -d ' ')" = 8 ] || fail "the journal has $(ls audit | wc -l | tr -d ' ') files after lowering to 7 days, want 8"
sleep 1
stop_controller
verify 0
grep -q '^WARNING: retention LOWERED from 30d to 7d' "$DEMO_DIR/verify.out" || fail "no WARNING for the lowered retention"

beat "Done" "Each action under the person or process that took it, refusals too; history that can't be rewritten without it showing."
say "${green}the audit demo ran clean$off"
