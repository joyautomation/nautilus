#!/usr/bin/env bash
# naut-release.sh: install, update and roll back a nautilus controller on a
# Linux box with systemd (an ARM64 IPC such as a Toradex Verdin, or any
# other), over SSH. See tools/deploy/README.md.
#
# On the box:
#   /opt/nautilus/releases/<id>/controller   one `naut build` binary per release
#   /opt/nautilus/current  -> releases/<id>  what nautilus.service runs
#   /opt/nautilus/previous -> releases/<id>  what rollback returns to
#   /var/lib/nautilus/retain.json            retained values, in no release
#   /var/lib/nautilus/backups/               a copy taken before every update
#
# The box needs systemd, a POSIX sh, coreutils and tar; root, or sudo
# without a password, for the SSH user. Nothing else is installed.
set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)

usage() {
	cat <<'EOF'
usage: naut-release.sh <command> ...

  build <project-dir> [-runtime <naut>|<vX.Y.Z>] [-arch arm64|amd64|armv7] [-o <file>]
        naut build the project into a controller. -runtime vX.Y.Z downloads
        that release's naut for -arch (default arm64) from GitHub.
  install  <ssh-target> <controller> [-id <id>]   first install, then start
  update   <ssh-target> <controller> [-id <id>]   make it current; roll back on its own
                                                  if it does not start
  rollback <ssh-target>                           swap current and previous, restart
  status   <ssh-target>
  backup   <ssh-target> [<file.tgz>]              retained values + the release they ran on
  restore  <ssh-target> <file.tgz>                stop, put them back, start
  uninstall <ssh-target> [-purge]                 remove the unit and releases;
                                                  -purge also deletes retained values

<ssh-target> is anything ssh takes (user@host, a Host from ~/.ssh/config).
NAUT_SSH_OPTS adds ssh options, e.g. "-p 2222 -i ~/.ssh/verdin".
The id defaults to the controller's file name plus 8 hex of its sha256.
EOF
}

die() { echo "naut-release: $*" >&2; exit 1; }

# The id is a directory name on the box and an argument to a remote shell:
# keep it to characters that need no quoting anywhere.
clean_id() { printf '%s' "$1" | tr -c 'A-Za-z0-9._+-' '_'; }

ctl_dir=$(mktemp -d "${TMPDIR:-/tmp}/naut-release.XXXXXX")
trap 'ssh -o ControlPath="$ctl_dir/cm" -O exit _ >/dev/null 2>&1 || true; rm -rf "$ctl_dir"' EXIT

ssh_t() {
	# One connection per run (ControlMaster): an update is several calls.
	# shellcheck disable=SC2086
	ssh -o BatchMode=yes -o ControlMaster=auto -o ControlPath="$ctl_dir/cm" \
		-o ControlPersist=60 ${NAUT_SSH_OPTS:-} "$@"
}

# remote <target> <command> [args...]: run the on-box half below as root.
remote() {
	local t=$1
	shift
	ssh_t "$t" "if [ \"\$(id -u)\" = 0 ]; then sh -s -- $*; else sudo -n sh -s -- $*; fi" <<<"$REMOTE_SH"
}

# upload <target> <local file> <name>: into /opt/nautilus/incoming/<name>.
upload() {
	ssh_t "$1" "if [ \"\$(id -u)\" = 0 ]; then s=; else s='sudo -n'; fi
		\$s mkdir -p /opt/nautilus/incoming && \$s sh -c 'cat > /opt/nautilus/incoming/$3.part && mv /opt/nautilus/incoming/$3.part /opt/nautilus/incoming/$3'" <"$2"
}

# The on-box half: POSIX sh, run as root. Messages go to stderr; stdout is
# reserved for data (a backup's tar).
REMOTE_SH=$(
	cat <<'REMOTE'
set -eu
ROOT=/opt/nautilus
STATE=/var/lib/nautilus
UNIT=nautilus.service
KEEP=3 # releases kept besides current and previous
say() { echo "$*" >&2; }
fail() { echo "naut-release (on box): $*" >&2; exit 1; }

# setlink <link> <release id>: point a link at a release, atomically where
# mv -T exists.
setlink() {
	ln -s "releases/$2" "$ROOT/$1.new"
	mv -T "$ROOT/$1.new" "$ROOT/$1" 2>/dev/null || { rm -f "$ROOT/$1"; mv "$ROOT/$1.new" "$ROOT/$1"; }
}
target() { if [ -L "$ROOT/$1" ]; then basename "$(readlink "$ROOT/$1")"; fi; }

# writeunit: the unit for the current release. A controller that predates
# systemd notification never sends READY=1, so it gets Type=simple and no
# watchdog rather than a start that times out.
writeunit() {
	tmp=/etc/systemd/system/$UNIT.new
	if grep -q 'WATCHDOG=1' "$ROOT/current/controller"; then
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

snapshot() { # a copy of the retained values before an update, newest 10 kept
	[ -f "$STATE/retain.json" ] || return 0
	mkdir -p "$STATE/backups"
	cp "$STATE/retain.json" "$STATE/backups/retain-$(date +%Y%m%dT%H%M%S)-$1.json"
	ls -1t "$STATE/backups"/retain-*.json | tail -n +11 | while read -r f; do rm -f "$f"; done
}

cmd=$1
shift
case $cmd in
install)
	id=$1
	[ -z "$(target current)" ] || fail "already installed (current is $(target current)); use update"
	command -v systemctl >/dev/null || fail "no systemctl: this box does not run systemd"
	place "$id"
	setlink current "$id"
	writeunit
	sync
	systemctl enable -q "$UNIT"
	if restart; then
		say "installed $id; nautilus.service is running"
	else
		why
		fail "$id did not start"
	fi
	;;
update)
	id=$1
	cur=$(target current)
	prev=$(target previous)
	[ -n "$cur" ] || fail "nothing installed; use install"
	if [ "$id" = "$cur" ]; then
		say "$id is already current"
		exit 0
	fi
	place "$id"
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
	say "$id did not start; going back to $cur"
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
REMOTE
)

# fetch_runtime <vX.Y.Z> <arch>: that release's naut, cached.
fetch_runtime() {
	local v=${1#v} arch=$2 cache
	cache=${XDG_CACHE_HOME:-$HOME/.cache}/nautilus/naut_${v}_linux_${arch}
	if [ ! -x "$cache/naut" ]; then
		mkdir -p "$cache"
		curl -fsSL "https://github.com/joyautomation/nautilus/releases/download/v${v}/nautilus_${v}_linux_${arch}.tar.gz" |
			tar -xz -C "$cache" naut || die "could not download naut v$v for linux/$arch"
	fi
	echo "$cache/naut"
}

default_id() { echo "$(clean_id "$(basename "$1")")-$(sha256sum "$1" | cut -c1-8)"; }

cmd=${1:-}
[ -n "$cmd" ] || { usage; exit 2; }
shift
case $cmd in
build)
	[ $# -ge 1 ] || die "build <project-dir> [-runtime ...] [-arch ...] [-o file]"
	proj=$1
	shift
	runtime="" arch=arm64 out=""
	while [ $# -gt 0 ]; do
		case $1 in
		-runtime) runtime=$2; shift 2 ;;
		-arch) arch=$2; shift 2 ;;
		-o) out=$2; shift 2 ;;
		*) die "build: unknown flag $1" ;;
		esac
	done
	case $runtime in v[0-9]*) runtime=$(fetch_runtime "$runtime" "$arch") ;; esac
	if [ -z "$out" ]; then
		name=$(basename "$(cd "$proj" && pwd)")
		rev=$(git -C "$proj" rev-parse --short HEAD 2>/dev/null || echo nogit)
		[ -z "$(git -C "$proj" status --porcelain 2>/dev/null)" ] || rev=$rev-dirty
		out=$name-$rev
	fi
	args=(build -o "$out")
	[ -z "$runtime" ] || args+=(-runtime "$runtime")
	"${NAUT:-naut}" "${args[@]}" "$proj"
	echo "$out"
	;;
install | update)
	[ $# -ge 2 ] || die "$cmd <ssh-target> <controller> [-id <id>]"
	t=$1 ctl=$2
	shift 2
	id=$(default_id "$ctl")
	[ "${1:-}" != -id ] || id=$(clean_id "$2")
	[ -f "$ctl" ] || die "no controller at $ctl"
	upload "$t" "$ctl" "$id"
	upload "$t" "$HERE/nautilus.service" nautilus.service
	remote "$t" "$cmd" "$id"
	;;
rollback | status)
	[ $# -eq 1 ] || die "$cmd <ssh-target>"
	remote "$1" "$cmd"
	;;
backup)
	[ $# -ge 1 ] || die "backup <ssh-target> [file.tgz]"
	out=${2:-nautilus-retain-$(clean_id "$1")-$(date +%Y%m%dT%H%M%S).tgz}
	remote "$1" backup >"$out.part" || { rm -f "$out.part"; exit 1; }
	mv "$out.part" "$out"
	echo "$out"
	;;
restore)
	[ $# -eq 2 ] || die "restore <ssh-target> <file.tgz>"
	[ -f "$2" ] || die "no backup at $2"
	upload "$1" "$2" restore.tgz
	remote "$1" restore
	;;
uninstall)
	[ $# -ge 1 ] || die "uninstall <ssh-target> [-purge]"
	remote "$1" uninstall "${2:-}"
	;;
-h | -help | --help | help) usage ;;
*) usage; exit 2 ;;
esac
