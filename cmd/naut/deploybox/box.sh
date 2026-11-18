# box.sh: the half of `naut deploy` that runs on the box, fed to
# `sh -s -- <command> [args]` over SSH. POSIX sh: the box needs systemd,
# coreutils and tar, nothing else, and it runs as root. With
# NAUT_SUPERVISOR=cron (#768) it needs crontab instead of systemd and runs
# as the SSH user, who must be able to write NAUT_ROOT. See
# cmd/naut/deploy.go.
#
# Messages go to stderr; stdout is reserved for data (a backup's tar).
set -eu
# Where releases and retained values live. The defaults are the systemd
# layout (deploy.go's deployRoot is ROOT's default). Setting them is for a
# mode without systemd (#768): nautilus.service and uninstall -purge keep
# the systemd paths.
SUPERVISOR=${NAUT_SUPERVISOR:-systemd}
ROOT=${NAUT_ROOT:-/opt/nautilus}
STATE=${NAUT_STATE:-/var/lib/nautilus}
UNIT=nautilus.service
UNITDIR=${NAUT_UNITDIR:-/etc/systemd/system} # set only by deploy_box_test.go
KEEP=3 # releases kept besides current and previous
say() { echo "$*" >&2; }
fail() { echo "naut deploy (on box): $*" >&2; exit 1; }
case $SUPERVISOR in
systemd) ;;
cron)
	# The supervisor is a keepalive script run from the SSH user's crontab;
	# its state sits under ROOT, which that user can write.
	[ -n "${NAUT_ROOT:-}" ] || fail "cron mode needs NAUT_ROOT"
	STATE=${NAUT_STATE:-$ROOT/state}
	;;
*) fail "unknown supervisor $SUPERVISOR" ;;
esac

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
	if [ "$SUPERVISOR" = cron ]; then
		writekeepalive
		return
	fi
	tmp=$UNITDIR/$UNIT.new
	if grep -q 'WATCHDOG''=1' "$ROOT/current/controller"; then
		cp "$ROOT/nautilus.service.in" "$tmp"
	else
		sed -e 's/^Type=notify/Type=simple/' -e '/^NotifyAccess=/d' -e '/^WatchdogSec=/d' \
			"$ROOT/nautilus.service.in" >"$tmp"
		say "note: this release does not notify systemd; running it as Type=simple without a watchdog"
	fi
	if cmp -s "$tmp" "$UNITDIR/$UNIT"; then
		rm -f "$tmp"
	else
		mv "$tmp" "$UNITDIR/$UNIT"
		systemctl daemon-reload
	fi
}

# hold (cron mode): while this run stops a controller, moves links and
# starts the next one, the keepalive must not start a release of its own.
# Each run has its own file, removed when it exits, so a status read in the
# middle of an update cannot lift the update's hold; the keepalive waits
# while any is fresh, and ignores one older than 10 minutes (a deploy cut
# off, by a power loss say). Taken only by the commands that stop or start.
hold() {
	[ "$SUPERVISOR" = cron ] || return 0
	mkdir -p "$ROOT"
	HOLD=$ROOT/hold.$$
	touch "$HOLD"
	trap 'rm -f "$HOLD"' EXIT
}

# writekeepalive (cron mode): the script the crontab runs, @reboot and
# every minute. It starts the current release unless it is running: through
# naut-launch when that sits in ROOT (rt/plcnext-io: it carries the
# capability for priority:), pinned with taskset when ROOT/cpu holds a CPU,
# in STATE (where retain.json goes) with ROOT/env's variables. Output goes
# to STATE/nautilus.log, one generation of a megabyte kept.
writekeepalive() {
	cat >"$ROOT/keepalive.sh.new" <<EOF
#!/bin/sh
# Written by naut deploy. Run by cron: starts $ROOT/current unless it is
# running or a deploy holds it; "start" ignores the hold (naut deploy's own).
ROOT='$ROOT'
STATE='$STATE'
EOF
	cat >>"$ROOT/keepalive.sh.new" <<'EOF'
if [ "${1:-}" != start ]; then
	for h in "$ROOT"/hold.*; do
		if [ -e "$h" ] && [ -z "$(find "$h" -mmin +10 2>/dev/null)" ]; then exit 0; fi
	done
fi
[ -x "$ROOT/current/controller" ] || exit 0
# Running is the pid file's process with one of ROOT's releases as its
# argv[0] (naut-launch passes the resolved path). Not /proc/<pid>/exe: a
# controller that gained a capability is not dumpable, and its exe link is
# unreadable even to its owner.
real=$(cd "$ROOT" && pwd -P)
if [ -f "$STATE/nautilus.pid" ]; then
	case $(tr '\0' '\n' 2>/dev/null <"/proc/$(cat "$STATE/nautilus.pid")/cmdline" | head -n 1) in
	"$real"/releases/*/controller | "$ROOT"/current/controller) exit 0 ;;
	esac
fi
mkdir -p "$STATE"
cd "$STATE" || exit 1
log=$STATE/nautilus.log
if [ -f "$log" ] && [ "$(wc -c <"$log")" -gt 1048576 ]; then mv "$log" "$log.1"; fi
run=$ROOT/current/controller
[ -x "$ROOT/naut-launch" ] && run=$ROOT/naut-launch
pin=
if [ -f "$ROOT/cpu" ] && command -v taskset >/dev/null; then pin="taskset -c $(cat "$ROOT/cpu")"; fi
if [ -f "$ROOT/env" ]; then set -a; . "$ROOT/env"; set +a; fi
echo "naut keepalive: starting $(basename "$(readlink "$ROOT/current")") at $(date)" >>"$log"
setsid nohup $pin "$run" </dev/null >>"$log" 2>&1 &
echo $! >"$STATE/nautilus.pid"
EOF
	chmod 0755 "$ROOT/keepalive.sh.new"
	mv "$ROOT/keepalive.sh.new" "$ROOT/keepalive.sh"
}

# cronpid (cron mode): the controller's pid when one of ROOT's releases is
# running under the keepalive's pid file, else nothing. Known by argv[0],
# as in the keepalive: /proc/<pid>/exe is unreadable once naut-launch has
# given the controller a capability.
cronpid() {
	[ -f "$STATE/nautilus.pid" ] || return 0
	p=$(cat "$STATE/nautilus.pid")
	case $(tr '\0' '\n' 2>/dev/null <"/proc/$p/cmdline" | head -n 1) in
	"$(cd "$ROOT" && pwd -P)"/releases/*/controller | "$ROOT"/current/controller) echo "$p" ;;
	esac
}

# sv_running: the controller is up.
sv_running() {
	if [ "$SUPERVISOR" = cron ]; then
		[ -n "$(cronpid)" ]
	else
		systemctl is-active -q "$UNIT"
	fi
}

# sv_stop: the controller is down. Cron mode allows it the unit's 15 s
# (TimeoutStopSec) to save and exit before it is killed.
sv_stop() {
	if [ "$SUPERVISOR" != cron ]; then
		systemctl stop "$UNIT"
		return
	fi
	p=$(cronpid)
	[ -n "$p" ] || return 0
	kill "$p" 2>/dev/null || true
	n=0
	while [ $n -lt 15 ] && kill -0 "$p" 2>/dev/null; do
		sleep 1
		n=$((n + 1))
	done
	# Only if it is still there: a pid freed a moment ago may be reused.
	if kill -0 "$p" 2>/dev/null; then kill -9 "$p" 2>/dev/null || true; fi
	rm -f "$STATE/nautilus.pid"
}

# started: the controller is up and still the same process a few seconds
# later. With Type=notify, `systemctl restart` already waited for the first
# scan; the wait catches a controller that exits just after. Cron mode, like
# Type=simple, cannot see the first scan.
started() {
	if [ "$SUPERVISOR" = cron ]; then
		sleep 1
		p0=$(cronpid)
		[ -n "$p0" ] || return 1
		sleep 3
		[ "$(cronpid)" = "$p0" ]
		return
	fi
	systemctl is-active -q "$UNIT" || return 1
	pid=$(systemctl show -p MainPID --value "$UNIT")
	sleep 3
	systemctl is-active -q "$UNIT" && [ "$(systemctl show -p MainPID --value "$UNIT")" = "$pid" ]
}

restart() {
	if [ "$SUPERVISOR" = cron ]; then
		sv_stop
		"$ROOT/keepalive.sh" start && started
		return
	fi
	systemctl reset-failed "$UNIT" 2>/dev/null || true
	systemctl restart "$UNIT" && started
}

# sv_enable / sv_disable: the controller starts at boot, or no longer.
sv_enable() {
	if [ "$SUPERVISOR" = cron ]; then
		{
			crontab -l 2>/dev/null | grep -v '# naut deploy$' || true
			echo "@reboot $ROOT/keepalive.sh # naut deploy"
			echo "* * * * * $ROOT/keepalive.sh # naut deploy"
		} | crontab -
		return
	fi
	systemctl enable -q "$UNIT"
}
sv_disable() {
	if [ "$SUPERVISOR" = cron ]; then
		crontab -l 2>/dev/null | grep -v '# naut deploy$' | crontab - || true
		return
	fi
	systemctl disable -q "$UNIT" 2>/dev/null || true
}

why() {
	if [ "$SUPERVISOR" = cron ]; then
		say "--- last log lines ($STATE/nautilus.log) ---"
		tail -n 15 "$STATE/nautilus.log" >&2 2>/dev/null || true
		return
	fi
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
	sv_stop 2>/dev/null || true
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
install | update | rollback | restore | uninstall) hold ;;
esac
case $cmd in
install)
	id=$1
	[ -z "$(target current)" ] || fail "already installed (current is $(target current)); use update"
	if [ "$SUPERVISOR" = cron ]; then
		command -v crontab >/dev/null || fail "no crontab: cron mode needs the SSH user's crontab"
		mkdir -p "$STATE"
	else
		command -v systemctl >/dev/null || fail "no systemctl: this box does not run systemd (naut deploy -supervisor cron)"
	fi
	place "$id"
	snapshot install # retained values kept from an earlier install, if any
	setlink current "$id"
	writeunit
	sync
	sv_enable
	if restart; then
		if [ "$SUPERVISOR" = cron ]; then
			# Later commands find the mode and root here (deploy.go).
			mkdir -p "$HOME/.config"
			printf 'supervisor=cron\nroot=%s\n' "$ROOT" >"$HOME/.config/naut-deploy"
			say "installed $id; started by $ROOT/keepalive.sh from the crontab"
		else
			say "installed $id; nautilus.service is running"
		fi
	else
		why
		# Leave nothing installed, so install can simply run again, and
		# the retained values as they were.
		unsnapshot
		sv_disable
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
		if sv_running; then
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
	sv_stop
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
	if [ "$SUPERVISOR" = cron ]; then
		p=$(cronpid)
		if [ -n "$p" ]; then echo "running:  pid $p (cron keepalive, $ROOT)"; else echo "running:  no (the keepalive starts it within a minute)"; fi
		echo "last log: $(tail -n 1 "$STATE/nautilus.log" 2>/dev/null)"
	else
		systemctl show -p ActiveState -p SubState -p Type -p NRestarts -p StatusText -p ExecMainStartTimestamp "$UNIT"
	fi
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
	sv_stop
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
	if [ "$SUPERVISOR" = cron ]; then
		# Only what deploy made: ROOT itself stays, and so does a
		# naut-launch in it (its capability takes root to set again).
		sv_disable
		sv_stop
		rm -rf "${ROOT:?}/releases" "$ROOT/incoming" "$ROOT/current" "$ROOT/previous" "$ROOT/keepalive.sh" "$ROOT/nautilus.service.in"
		rm -f "$HOME/.config/naut-deploy"
		if [ "${1:-}" = -purge ]; then
			rm -rf "$STATE"
			say "uninstalled; retained values deleted"
		else
			say "uninstalled; retained values kept in $STATE"
		fi
		exit 0
	fi
	systemctl disable -q --now "$UNIT" 2>/dev/null || true
	rm -f "$UNITDIR/$UNIT"
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
