#!/bin/sh
# bench-matrix.sh — one mode of the in-process tier's timing benchmark:
# `naut plcnext bench` under three loads, each for the same window, the
# results appended as JSON lines on the controller. Run it from a machine
# that reaches the controller over ssh, once per mode (the shim and naut
# best-effort, pinned, then with SCHED_FIFO — rt/plcnext-io/README.md),
# and compare the lines; each line records the CPUs the shim and naut ran on.
#
#   bench-matrix.sh normal 5m
#   SSH="ssh admin@192.168.82.216" bench-matrix.sh fifo50 5m
#
# Loads:  quiet  nothing added
#         cpu    two busy loops on the controller at normal priority, one
#                per core: what SCHED_FIFO is meant to shut out
#         net    a stream of zeros into the controller over ssh (network
#                receive, sshd's cipher): someone copying a big file onto it
set -eu
MODE=${1:?usage: bench-matrix.sh <mode label> [window, default 5m]}
WINDOW=${2:-5m}
SSH=${SSH:-ssh admin@192.168.82.216}
APP=/opt/plcnext/apps/nautilus
OUT=$APP/bench.jsonl
secs=$(echo "$WINDOW" | awk '/m$/ {print $0*60; next} /s$/ {print $0+0; next} {print $0+0}')

bench() { $SSH "$APP/naut plcnext bench --for $WINDOW --label $MODE/$1 --out $OUT"; }

echo "== $MODE/quiet"
bench quiet

echo "== $MODE/cpu"
$SSH "for i in 1 2; do timeout $((secs + 5)) sh -c 'while :; do :; done' </dev/null >/dev/null 2>&1 & done"
bench cpu
sleep 6

echo "== $MODE/net"
head -c 20000000000 /dev/zero | timeout $((secs + 5)) $SSH 'cat > /dev/null' &
loader=$!
sleep 2
bench net
wait $loader 2>/dev/null || true
echo "results appended to $OUT on the controller"
