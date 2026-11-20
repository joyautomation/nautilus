#!/usr/bin/env bash
# Table test for keycloak-nightly-issue.sh; run from the repo root. CI's
# changes job runs it. gh is a stub: `issue list` answers with $STUB_OPEN
# (the open issue's number, or nothing), every other call is recorded with
# its --body-file's contents.
set -u
here=$(dirname "$0")
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail=0

cat >"$tmp/gh" <<'EOF'
#!/usr/bin/env bash
if [ "$1 $2" = "issue list" ]; then
  echo "${STUB_OPEN:-}"
  exit 0
fi
echo "$*" >>"$STUB_DIR/calls"
while [ $# -gt 0 ]; do
  if [ "$1" = --body-file ]; then cat "$2" >>"$STUB_DIR/body"; fi
  shift
done
EOF
chmod +x "$tmp/gh"
printf 'ok\n--- FAIL: TestKeycloakLogin (0.4s)\n    oidc_keycloak_test.go:50: want 200, got 401\nFAIL\n' >"$tmp/red.log"

# run <name> <open issue or ""> <outcome> <log> ; leaves calls/body in $tmp/<name>/
run() {
  local name=$1 open=$2 outcome=$3 log=$4
  mkdir -p "$tmp/$name"
  STUB_DIR="$tmp/$name" STUB_OPEN="$open" GH="$tmp/gh" GITHUB_REPOSITORY=o/r \
    RUN_URL=https://example/run/1 GITHUB_SHA=0123456789abcdef RUN_OUTCOME="$outcome" RUN_LOG="$log" \
    "$here/keycloak-nightly-issue.sh" >"$tmp/$name/out" 2>&1 || { echo "FAIL $name: script exited non-zero"; fail=1; }
}
expect() { # expect <name> <substring of calls> <substring of body or "">
  local name=$1
  grep -q -- "$2" "$tmp/$name/calls" 2>/dev/null || { echo "FAIL $name: calls lack '$2': $(cat "$tmp/$name/calls" 2>/dev/null)"; fail=1; }
  [ -z "$3" ] || grep -q -- "$3" "$tmp/$name/body" 2>/dev/null || { echo "FAIL $name: body lacks '$3'"; fail=1; }
}
expect_none() { [ ! -s "$tmp/$1/calls" ] || { echo "FAIL $1: unexpected calls: $(cat "$tmp/$1/calls")"; fail=1; }; }

run red-new "" failure "$tmp/red.log"
expect red-new "issue create" "got 401"
expect red-new "issue create" "0123456"
run red-open 7 failure "$tmp/red.log"
expect red-open "issue comment 7" "TestKeycloakLogin"
run cancelled "" cancelled ""
expect cancelled "issue create" "timed out"
run skipped "" skipped "$tmp/none"
expect skipped "issue create" "did not run"
run green-open 7 success "$tmp/red.log"
expect green-open "issue close 7" ""
run green-none "" success ""
expect_none green-none

[ $fail = 0 ] && echo "keycloak-nightly-issue: all cases pass"
exit $fail
