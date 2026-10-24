#!/bin/sh
# naut-keepalive.sh — start `naut run` for the bench project unless it is
# already running. The controller has no systemd and admin cannot write
# /etc/init.d, but admin may keep a crontab (vixie cron, started at boot
# before the plcnext runtime), so:
#
#   @reboot   /opt/plcnext/apps/nautilus/plcnext-bench/controller/naut-keepalive.sh
#   * * * * * /opt/plcnext/apps/nautilus/plcnext-bench/controller/naut-keepalive.sh
#
# starts it at boot and restarts it within a minute of a crash. naut does
# not need the runtime to be up first: the driver retries the socket and
# the shim's page with backoff and handshakes the bus when both appear.
# Deliberately not an ACF <Process>: a declared ACF process that exits
# during start-up takes the whole runtime down (rt/plcnext-io/README.md).
APP=/opt/plcnext/apps/nautilus
PROJECT=$APP/plcnext-bench
PIDFILE=$APP/naut.pid
LOG=$APP/naut-run.log

if [ -f "$PIDFILE" ]; then
	pid=$(cat "$PIDFILE")
	if kill -0 "$pid" 2>/dev/null && grep -q naut "/proc/$pid/cmdline" 2>/dev/null; then
		exit 0
	fi
fi
# One generation of log, a megabyte each.
if [ -f "$LOG" ] && [ "$(wc -c < "$LOG")" -gt 1048576 ]; then
	mv "$LOG" "$LOG.1"
fi
cd "$PROJECT" || exit 1
echo "naut-keepalive: starting naut run in $PROJECT at $(date)" >> "$LOG"
setsid nohup "$APP/naut" run . >> "$LOG" 2>&1 < /dev/null &
echo $! > "$PIDFILE"
