#!/usr/bin/env bash
# F-06: `naut check` is the documented CI gate ("compiles every program and cross-checks it against
# the manifest"). On a project whose nautilus.yaml is empty it must exit non-zero, as test/run/build
# do. Today it exits 0 ("no .st, .fbd, .ld, or .sfc files found"); the others say only
# "nautilus.yaml: EOF". Passes when check exits non-zero AND test's message says more than "EOF".
set -u
T=$(mktemp -d); trap 'rm -rf "$T"' EXIT; cd "$T"; : > nautilus.yaml
c=$(naut check 2>&1); crc=$?; echo "check rc=$crc: $c"
t=$(naut test 2>&1); trc=$?; echo "test rc=$trc: $t"
fail=0
[ $crc -ne 0 ] || { echo "FAIL naut check passes an empty manifest"; fail=1; }
echo "$t" | grep -qx "naut test: nautilus.yaml: EOF" && { echo "FAIL message is a bare EOF"; fail=1; }
exit $fail
