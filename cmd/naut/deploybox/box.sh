# box.sh: the half of `naut deploy` that runs on the box, as root, fed to
# `sh -s -- <command> [args]` over SSH. POSIX sh: the box needs systemd,
# coreutils and tar, nothing else. See cmd/naut/deploy.go.
#
# Messages go to stderr; stdout is reserved for data (a backup's tar).
set -eu
# Where releases and retained values live. The defaults are the systemd
# layout (deploy.go's deployRoot is ROOT's default). Setting them is for a
# mode without systemd (#768): nautilus.service and uninstall -purge keep
# the systemd paths.
ROOT=${NAUT_ROOT:-/opt/nautilus}
STATE=${NAUT_STATE:-/var/lib/nautilus}
UNIT=nautilus.service
KEEP=3 # releases kept besides current and previous
say() { echo "$*" >&2; }
fail() { echo "naut deploy (on box): $*" >&2; exit 1; }

# setlink <link> <release id>: point a link at a release, atomically where
# mv -T exists.
setlink() {
	ln -s "releases/$2" "$ROOT/$1.new"
	mv -T "$ROOT/$1.new" "$ROOT/$1" 2>/dev/null || { rm -f "$ROOT/$1"; mv "$ROOT/$1.new" "$ROOT/$1"; }
}
target() { if [ -L "$ROOT/$1" ]; then basename "$(readlink "$ROOT/$1")"; fi; }

# writeunit: the unit for the current release. A controller that predates
# systemd notification never sends READY=1, so it gets Type=simple and no
# watchdog rather than a start that times out. The pattern is split in
# two: this script is embedded in every naut, and so in every controller,
# and must not match itself.
writeunit() {
	tmp=/etc/systemd/system/$UNIT.new
	if grep -q 'WATCHDOG''=1' "$ROOT/current/controller"; then
		cp "$ROOT/nautilus.service.in" "$tmp"
	else
		sed -e 's/^Type=notify/Type=simple/' -e '/^NotifyAccess=/d' -e '/^WatchdogSec=/d' \
			"$ROOT/nautilus.service.in" >"$tmp"
		say "note: this release does not notify systemd; running it as Type=simple without a watchdog"
	fi
	if cmp -s "$tmp" "/etc/systemd/system/$UNIT"; then
		rm -f "$tmp"
	else
		mv "$tmp" "/etc/systemd/system/$UNIT"
		systemctl daemon-reload
	fi
}

# started: the unit is active and still the same process a few seconds
# later. With Type=notify, `systemctl restart` already waited for the first
# scan; the wait catches a controller that exits just after.
started() {
	systemctl is-active -q "$UNIT" || return 1
	pid=$(systemctl show -p MainPID --value "$UNIT")
	sleep 3
	systemctl is-active -q "$UNIT" && [ "$(systemctl show -p MainPID --value "$UNIT")" = "$pid" ]
}

restart() {
	systemctl reset-failed "$UNIT" 2>/dev/null || true
	systemctl restart "$UNIT" && started
}

why() {
	say "--- last journal lines ---"
	journalctl -u "$UNIT" -n 15 --no-pager -o cat >&2 || true
}

# place <id>: move an uploaded controller into its release directory. A
# release already on the box (an update back to an older one) is reused.
place() {
	if [ -f "$ROOT/incoming/$1" ]; then
		mkdir -p "$ROOT/releases/$1"
		chmod 0755 "$ROOT/incoming/$1"
		mv "$ROOT/incoming/$1" "$ROOT/releases/$1/controller"
	fi
	[ -x "$ROOT/releases/$1/controller" ] || fail "release $1 is not on the box"
	if [ -f "$ROOT/incoming/nautilus.service" ]; then
		mv "$ROOT/incoming/nautilus.service" "$ROOT/nautilus.service.in"
	fi
	# On disk before any link points at it: power lost just after an
	# update must not boot a link to a half-written binary.
	sync
}

prune() { # sh has no locals: names of its own, so the caller's cur and prev stand
	keep1=$(target current)
	keep2=$(target previous)
	n=0
	for d in $(ls -1t "$ROOT/releases"); do
		[ "$d" = "$keep1" ] || [ "$d" = "$keep2" ] && continue
		n=$((n + 1))
		[ $n -le $KEEP ] && continue
		rm -rf "${ROOT:?}/releases/$d"
		say "pruned release $d"
	done
}

# snapshot <release>: a copy of the retained values before an update,
# newest 10 kept. snap names it, empty when there were none. Taken with the
# controller stopped, after its last save on the way out.
snapshot() {
	snap=
	[ -f "$STATE/retain.json" ] || return 0
	mkdir -p "$STATE/backups"
	snap=$STATE/backups/retain-$(date +%Y%m%dT%H%M%S)-$1.json
	cp "$STATE/retain.json" "$snap"
	ls -1t "$STATE/backups"/retain-*.json | tail -n +11 | while read -r f; do rm -f "$f"; done
}

# unsnapshot: put the retained values back as snapshot found them. A
# release that failed may have saved over them before it died; it is
# stopped first, since systemd keeps restarting it.
unsnapshot() {
	systemctl stop "$UNIT" 2>/dev/null || true
	rm -f "$STATE/retain.json.tmp"
	if [ -n "$snap" ]; then
		cp "$snap" "$STATE/retain.json"
		chown "$(stat -c %u:%g "$STATE/")" "$STATE/retain.json"
	else
		rm -f "$STATE/retain.json"
	fi
}

cmd=$1
shift
case $cmd in
install)
	id=$1
	[ -z "$(target current)" ] || fail "already installed (current is $(target current)); use update"
	command -v systemctl >/dev/null || fail "no systemctl: this box does not run systemd"
	place "$id"
	snapshot install # retained values kept from an earlier install, if any
	setlink current "$id"
	writeunit
	sync
	systemctl enable -q "$UNIT"
	if restart; then
		say "installed $id; nautilus.service is running"
	else
		why
		# Leave nothing installed, so install can simply run again, and
		# the retained values as they were.
		unsnapshot
		systemctl disable -q "$UNIT" 2>/dev/null || true
		rm -f "$ROOT/current"
		fail "$id did not start; nothing is installed (the release stays under releases/)"
	fi
	;;
update)
	id=$1
	cur=$(target current)
	prev=$(target previous)
	[ -n "$cur" ] || fail "nothing installed; use install"
	if [ "$id" = "$cur" ]; then
		rm -f "$ROOT/incoming/$id"
		if systemctl is-active -q "$UNIT"; then
			say "$id is already current and running"
			exit 0
		fi
		# A retried deploy must not pass while the controller is down.
		say "$id is already current but not running; starting it"
		if restart; then
			say "started $id"
			exit 0
		fi
		why
		[ -n "$prev" ] || fail "$id did not start, and there is no previous release to go back to"
		say "$id did not start; going back to $prev"
		setlink current "$prev"
		setlink previous "$id"
		writeunit
		sync
		restart || { why; fail "$prev did not start either"; }
		say "rolled back to $prev"
		exit 3
	fi
	place "$id"
	systemctl stop "$UNIT"
	snapshot "$cur"
	setlink previous "$cur"
	setlink current "$id"
	writeunit
	sync
	if restart; then
		prune
		say "updated $cur -> $id (rollback returns to $cur)"
		exit 0
	fi
	why
	say "$id did not start; going back to $cur, with the retained values as they were"
	unsnapshot
	setlink current "$cur"
	if [ -n "$prev" ]; then setlink previous "$prev"; else rm -f "$ROOT/previous"; fi
	writeunit
	restart || { why; fail "$cur did not start again either"; }
	say "rolled back to $cur; $id stays on the box under releases/"
	exit 3
	;;
rollback)
	cur=$(target current)
	prev=$(target previous)
	[ -n "$prev" ] || fail "no previous release to roll back to"
	setlink current "$prev"
	setlink previous "$cur"
	writeunit
	sync
	if restart; then
		say "rolled back $cur -> $prev (rollback again returns to $cur)"
	else
		why
		fail "$prev did not start"
	fi
	;;
status)
	cur=$(target current)
	[ -n "$cur" ] || fail "nothing installed"
	echo "current:  $cur ($(NAUTILUS_CLI=1 "$ROOT/current/controller" version 2>/dev/null || echo '?'))"
	echo "previous: $(target previous)"
	echo "releases: $(ls -1t "$ROOT/releases" | tr '\n' ' ')"
	systemctl show -p ActiveState -p SubState -p Type -p NRestarts -p StatusText -p ExecMainStartTimestamp "$UNIT"
	;;
backup)
	[ -f "$STATE/retain.json" ] || fail "no $STATE/retain.json yet (does the project have a retain: block?)"
	tmp=$(mktemp -d)
	cp "$STATE/retain.json" "$tmp/retain.json"
	target current >"$tmp/release"
	tar -C "$tmp" -czf - retain.json release
	rm -rf "$tmp"
	;;
restore)
	f=$ROOT/incoming/restore.tgz
	[ -f "$f" ] || fail "no backup uploaded"
	tmp=$(mktemp -d)
	tar -C "$tmp" -xzf "$f"
	[ -s "$tmp/retain.json" ] || fail "the backup holds no retain.json"
	from=$(cat "$tmp/release" 2>/dev/null || echo '?')
	[ "$from" = "$(target current)" ] || say "note: the backup was taken on release $from; current is $(target current)"
	# Stopped first: the controller saves every 2 s and once more on
	# its way out, and either would overwrite the restored file.
	systemctl stop "$UNIT"
	cp "$tmp/retain.json" "$STATE/retain.json"
	chown "$(stat -c %u:%g "$STATE/")" "$STATE/retain.json"
	rm -f "$STATE/retain.json.tmp"
	rm -rf "$tmp" "$f"
	if restart; then
		say "restored retained values (taken on $from)"
	else
		why
		fail "the controller did not start after the restore"
	fi
	;;
uninstall)
	systemctl disable -q --now "$UNIT" 2>/dev/null || true
	rm -f "/etc/systemd/system/$UNIT"
	systemctl daemon-reload
	rm -rf "$ROOT"
	if [ "${1:-}" = -purge ]; then
		rm -rf "$STATE" /var/lib/private/nautilus
		say "uninstalled; retained values deleted"
	else
		say "uninstalled; retained values kept in $STATE"
	fi
	;;
*) fail "unknown command $cmd" ;;
esac
