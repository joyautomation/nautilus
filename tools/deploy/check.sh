#!/usr/bin/env bash
# check.sh: install -> update -> rollback -> backup/restore -> a release
# that will not start, against a real box over SSH, with naut-release.sh.
# Every step asserts which release runs and that a retained setpoint
# survived. Ends by uninstalling everything it put there.
#
#   tools/deploy/check.sh <ssh-target> [-runtime <naut>|vX.Y.Z] [-arch arm64]
#
# -runtime is the naut the controllers are built on: a path, or a release
# version to download for -arch (default arm64). Without it the controllers
# are built on the local naut, which suits a box of the same architecture.
# NAUT names the naut that builds (default: naut on PATH); it needs
# `naut build -runtime` when -runtime is given.
#
# It refuses a box that already has a nautilus install.
set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REL=$HERE/naut-release.sh
API_PORT=18090 # check-project/nautilus.yaml's server.addr, on the box

[ $# -ge 1 ] || { sed -n '2,15p' "$0"; exit 2; }
target=$1
shift
build_args=()
while [ $# -gt 0 ]; do
	case $1 in
	-runtime | -arch) build_args+=("$1" "$2"); shift 2 ;;
	*) echo "check: unknown flag $1" >&2; exit 2 ;;
	esac
done

work=$(mktemp -d "${TMPDIR:-/tmp}/naut-deploy-check.XXXXXX")
sock=$work/ssh
cleanup() {
	ssh -S "$sock" -O exit "$target" >/dev/null 2>&1 || true
	rm -rf "$work"
}
trap cleanup EXIT

step() { printf '\n== %s\n' "$*"; }
pass() { printf 'PASS %s\n' "$*"; }
installed=no
fail() {
	printf 'FAIL %s\n' "$*"
	[ $installed = no ] || printf 'The check'"'"'s install is still on the box; remove it with\n  %s uninstall %s -purge\n' "$REL" "$target"
	exit 1
}
now() { date +%s.%N; }
since() { printf '%.1f s' "$(echo "$(now) - $1" | bc)"; }

# shellcheck disable=SC2086
ssh -fNM -S "$sock" -o BatchMode=yes ${NAUT_SSH_OPTS:-} "$target"
if "$REL" status "$target" >/dev/null 2>&1; then
	fail "$target already has a nautilus install; this check installs and uninstalls its own, so it will not run there"
fi

# The box's API, through the SSH connection: nothing listens off the box.
lport=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')
ssh -S "$sock" -O forward -L "127.0.0.1:$lport:127.0.0.1:$API_PORT" "$target" >/dev/null
api=http://127.0.0.1:$lport

tag() { # tag <name>: its value from /api/state, waiting for the API
	local i
	for i in $(seq 1 100); do
		if out=$(curl -fsS "$api/api/state" 2>/dev/null); then
			python3 -c 'import json,sys; v=json.load(sys.stdin)["tags"][sys.argv[1]]; print(json.dumps(v))' "$1" <<<"$out"
			return
		fi
		sleep 0.1
	done
	fail "the controller's API did not answer"
}
write() { curl -fsS -X POST -H 'Content-Type: application/json' -d "{\"name\":\"$1\",\"value\":$2}" "$api/api/tags" >/dev/null; }
expect() { # expect <what> <tag> <value>
	local got
	got=$(tag "$2")
	[ "$got" = "$3" ] || fail "$1: $2 is $got, want $3"
	pass "$1: $2 = $got"
}
links() { # links <current> <previous>
	local st
	st=$("$REL" status "$target")
	grep -q "^current:  $1 " <<<"$st" || fail "current is not $1: $st"
	grep -q "^previous: $2\$" <<<"$st" || fail "previous is not $2: $st"
	pass "current $1, previous $2"
}
saved() { sleep 3; } # retain saves every 2 s

step "build three releases"
for n in 1 2; do
	mkdir -p "$work/r$n"
	cp "$HERE"/check-project/* "$work/r$n/"
	sed -i "s/THIS_RELEASE : DINT := 1;/THIS_RELEASE : DINT := $n;/" "$work/r$n/main.st"
	"$REL" build "$work/r$n" "${build_args[@]}" -o "$work/release-$n" >/dev/null
done
# Release 3 starts but its first scan never finishes: only a Type=notify
# unit can tell (systemd waits for READY=1, which never comes).
mkdir -p "$work/r3"
cp "$HERE"/check-project/* "$work/r3/"
sed -i -e "s/THIS_RELEASE : DINT := 1;/THIS_RELEASE : DINT := 3;/" \
	-e 's/^Scans := Scans + 1;$/Scans := Scans + 1;\nFOR i := 1 TO 1000000 DO\n  FOR j := 1 TO 1000000 DO\n    Scans := Scans + 1;\n  END_FOR;\nEND_FOR;/' \
	-e 's/^VAR CONSTANT$/VAR\n  i, j : DINT;\nEND_VAR\nVAR CONSTANT/' \
	"$work/r3/main.st"
"$REL" build "$work/r3" "${build_args[@]}" -o "$work/release-hung" >/dev/null
printf '#!/bin/sh\necho "this release never starts" >&2\nexit 3\n' >"$work/release-broken"
chmod +x "$work/release-broken"
A=release-1-$(sha256sum "$work/release-1" | cut -c1-8)
B=release-2-$(sha256sum "$work/release-2" | cut -c1-8)
pass "built $A and $B"

step "install release 1"
t=$(now)
installed=yes
"$REL" install "$target" "$work/release-1"
pass "install took $(since "$t")"
unit=$("$REL" status "$target" | grep '^Type=')
pass "unit: $unit"
expect "release 1 runs" Release 1
expect "seed" SP 1.5
write SP 42.5
saved

step "update to release 2"
t=$(now)
"$REL" update "$target" "$work/release-2"
pass "update took $(since "$t")"
links "$B" "$A"
expect "release 2 runs" Release 2
expect "the setpoint survived the update" SP 42.5

step "roll back"
t=$(now)
"$REL" rollback "$target"
pass "rollback took $(since "$t")"
links "$A" "$B"
expect "release 1 runs again" Release 1
expect "the setpoint survived the rollback" SP 42.5

step "back up, change, restore"
"$REL" backup "$target" "$work/backup.tgz" >/dev/null
write SP 7
saved
expect "changed" SP 7
"$REL" restore "$target" "$work/backup.tgz"
expect "restored" SP 42.5

step "an update that does not start rolls itself back"
set +e
"$REL" update "$target" "$work/release-broken"
rc=$?
set -e
[ $rc = 3 ] || fail "update of a broken release exited $rc, want 3"
pass "the update failed (exit 3)"
links "$A" "$B"
expect "release 1 still runs" Release 1
expect "the setpoint is untouched" SP 42.5

if [ "$unit" = Type=notify ]; then
	step "an update whose first scan hangs rolls itself back (Type=notify)"
	t=$(now)
	set +e
	"$REL" update "$target" "$work/release-hung"
	rc=$?
	set -e
	[ $rc = 3 ] || fail "update of a release whose first scan hangs exited $rc, want 3"
	pass "the update failed (exit 3) after $(since "$t")"
	links "$A" "$B"
	expect "release 1 still runs" Release 1
	expect "the setpoint is untouched" SP 42.5
else
	pass "skipped: a release whose first scan hangs is caught only under Type=notify"
fi

step "roll forward with rollback"
"$REL" rollback "$target"
links "$B" "$A"
expect "release 2 runs" Release 2

step "uninstall"
"$REL" uninstall "$target" -purge
pass "uninstalled"
printf '\nALL PASS\n'
