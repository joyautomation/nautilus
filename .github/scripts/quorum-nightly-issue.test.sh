#!/usr/bin/env bash
# Table test for quorum-nightly-issue.sh; run from the repo root. CI's
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

# Real output, trimmed: a red seeds run (two failing seeds) and a mutants
# run with one mutation surviving.
cat >"$tmp/seeds-red.log" <<'EOF'
--- FAIL: TestSeeds (0.20s)
    seeds_test.go:300: seed 29: lease at 15.204795778s: B open with 1 running promise(s) of term 5
          repro: QUORUM_SEED=29 go test ./quorum/sim -run '^TestSeeds$' -v
          shrunk to 2 of 2 faults: Options{Seed: 29, Delay: 3582974, Jitter: 2478661, Fsync: 2484444, InPlace: true,
        	Faults: []Fault{
        		{Kind: Cut, At: 13451436066, For: 18301509488, From: B, To: W},
        	}}
    seeds_test.go:300: seed 106: lease at 21.038034677s: A open with 1 running promise(s) of term 5
          repro: QUORUM_SEED=106 go test ./quorum/sim -run '^TestSeeds$' -v
          shrunk to 2 of 3 faults: Options{Seed: 106, Delay: 464832, Jitter: 312645, Fsync: 467548, InPlace: false,
        	}}
    seeds_test.go:306: seeds 1–200000: 2 failing
FAIL
FAIL	github.com/joyautomation/nautilus/quorum/sim	131.203s
FAIL
EOF
# A suite cut off by its job's limit: nothing failed, the log just stops.
cat >"$tmp/seeds-cut.log" <<'EOF'
ok  	github.com/joyautomation/nautilus/quorum/sim	623.436s
=== RUN   TestThreeReplicaSimulation
EOF
cat >"$tmp/seeds-green.log" <<'EOF'
ok  	github.com/joyautomation/nautilus/quorum/sim	130.912s
EOF
cat >"$tmp/mutants-red.log" <<'EOF'
| mutation | result |
|---|---|
| (baseline: the tag on, no mutation) | green |
| no_rho | caught (8 tests red) |
| no_observe | SURVIVED: still green: TestObserveLiftsTheNextCampaign |
| retry_on_grid | caught (1 tests red) |
exit status 1
EOF
# Two legs' logs run together (#1096): one leg red, one green.
cat >"$tmp/mutants-legs.log" <<'EOF'
| mutation | result |
|---|---|
| (baseline: the tag on, no mutation; shard 0/2, 2 rows) | green |
| no_rho | caught (8 tests red) |
| no_observe | SURVIVED: still green: TestObserveLiftsTheNextCampaign |
| (shard 0/2: 2 rows judged) | done |
exit status 1
| mutation | result |
|---|---|
| (baseline: the tag on, no mutation; shard 1/2, 2 rows) | green |
| retry_on_grid | caught (1 tests red) |
| any_sender | BROKEN: package p failed with no test failing (a panic or a build error) |
| (shard 1/2: 2 rows judged) | done |
EOF
# Three legs: shard 1 timed out after one row, shard 2 never started.
cat >"$tmp/mutants-short.log" <<'EOF'
| mutation | result |
|---|---|
| (baseline: the tag on, no mutation; shard 0/3, 1 rows) | green |
| no_rho | caught (8 tests red) |
| (shard 0/3: 1 rows judged) | done |
| mutation | result |
|---|---|
| (baseline: the tag on, no mutation; shard 1/3, 2 rows) | green |
| retry_on_grid | caught (1 tests red) |
EOF
cat >"$tmp/mutants-broken.log" <<'EOF'
# github.com/joyautomation/nautilus/quorum
quorum/vote.go:12:2: undefined: rho
exit status 1
EOF
cat >"$tmp/mutants-green.log" <<'EOF'
| mutation | result |
|---|---|
| (baseline: the tag on, no mutation) | green |
| no_rho | caught (8 tests red) |
EOF

# check NAME OPEN MUTANTS_OUTCOME MUTANTS_LOG SEEDS_OUTCOME SEEDS_LOG WANT_CALL [WANT_BODY...]
#   OPEN: the open issue's number, or ''. WANT_CALL: a regex the one recorded
#   gh call must match ('' = no call). WANT_BODY: regexes the body must
#   match; a leading ! means it must NOT match.
check() {
  local name=$1 open=$2 mo=$3 ml=$4 so=$5 sl=$6 want=$7
  shift 7
  rm -f "$tmp/calls" "$tmp/body"
  local out
  if ! out=$(STUB_DIR=$tmp STUB_OPEN=$open GH="$tmp/gh" \
    GITHUB_REPOSITORY=o/r GITHUB_SHA=0123456789abcdef RUN_URL=https://example.test/run/1 \
    MUTANTS_OUTCOME=$mo MUTANTS_LOG=${ml:+$tmp/$ml} SEEDS_OUTCOME=$so SEEDS_LOG=${sl:+$tmp/$sl} \
    "$here/quorum-nightly-issue.sh" 2>&1); then
    echo "FAIL $name: the script failed: $out"
    fail=1
    return
  fi
  local calls n
  calls=$(cat "$tmp/calls" 2>/dev/null || true)
  n=$(printf '%s' "$calls" | grep -c . || true)
  if [ -z "$want" ]; then
    if [ "$n" != 0 ]; then
      echo "FAIL $name: want no gh call, got: $calls"
      fail=1
    fi
  elif [ "$n" != 1 ] || ! grep -qE -- "$want" <<<"$calls"; then
    echo "FAIL $name: want one call matching /$want/, got: $calls"
    fail=1
  fi
  local re body
  body=$(cat "$tmp/body" 2>/dev/null || true)
  for re in "$@"; do
    if [ "${re:0:1}" = '!' ]; then
      if grep -qE -- "${re:1}" <<<"$body"; then
        echo "FAIL $name: body matches /${re:1}/:"
        echo "$body"
        fail=1
      fi
    elif ! grep -qE -- "$re" <<<"$body"; then
      echo "FAIL $name: body lacks /$re/:"
      echo "$body"
      fail=1
    fi
  done
}

S="'\^TestSeeds\\\$'"
# Green: nothing to do, or close the open issue with the run.
check "green, no issue" '' success mutants-green.log success seeds-green.log ''
check "green closes the issue" 7 success mutants-green.log success seeds-green.log \
  '^issue close 7 --repo o/r --comment Green again: https://example.test/run/1$'
# Red seeds: the issue opens with the repro lines and the failing seeds.
check "seeds red opens the issue" '' success mutants-green.log failure seeds-red.log \
  '^issue create --repo o/r --title quorum: nightly simulator --body-file ' \
  'Main at \*\*01234567\*\*.*https://example.test/run/1' \
  'The seeded schedules .* failed' \
  "^QUORUM_SEED=29 go test ./quorum/sim -run $S -v$" \
  "^QUORUM_SEED=106 go test ./quorum/sim -run $S -v$" \
  'seed 29: lease at 15.204795778s: B open' \
  'seeds 1–200000: 2 failing' \
  '!quorum-mutants' '!Faults'
# Red mutants: a comment on the open issue, with the table.
check "mutants red comments" 7 failure mutants-red.log success seeds-green.log \
  '^issue comment 7 --repo o/r --body-file ' \
  'go run ./tools/quorum-mutants` failed' \
  '^\| no_observe \| SURVIVED: still green: TestObserveLiftsTheNextCampaign \|$' \
  '!The seeded schedules' '!exit status'
# The legs' tables: one header, only the rows not caught, and a count.
check "mutants legs list only what was not caught" 7 failure mutants-legs.log success seeds-green.log \
  '^issue comment 7 ' \
  '^\| no_observe \| SURVIVED: still green: TestObserveLiftsTheNextCampaign \|$' \
  '^\| any_sender \| BROKEN: ' \
  'The other 2 mutations were caught' \
  '!no_rho' '!retry_on_grid' '!shard 0/2' '!exit status' '!\| done \|'
if [ "$(grep -c '^| mutation | result |$' "$tmp/body" 2>/dev/null)" != 1 ]; then
  echo "FAIL mutants legs: want one table header"
  cat "$tmp/body"
  fail=1
fi
# A leg without its done row is named, not hidden behind the caught rows.
MUTANTS_LEGS=3 check "a leg that never finished is named" 7 cancelled mutants-short.log success seeds-green.log \
  '^issue comment 7 ' 'was cancelled' \
  '^\| \(shard 1/3\) \| never finished' '^\| \(shard 2/3\) \| never finished' \
  '!shard 0/3\)' '!no_rho' 'The other 2 mutations were caught'
# A build break: no table, so the log's tail.
check "mutants broken shows the log" '' failure mutants-broken.log success seeds-green.log \
  '^issue create ' 'undefined: rho'
# Both red: both sections.
check "both red" 7 failure mutants-red.log failure seeds-red.log \
  '^issue comment 7 ' 'SURVIVED' "^QUORUM_SEED=29 go test"
# A suite cut off with nothing failing: the log's tail, not an empty block.
check "seeds cut off shows the tail" 7 success mutants-green.log failure seeds-cut.log \
  '^issue comment 7 ' 'The seeded schedules .* failed' '=== RUN   TestThreeReplicaSimulation' '!QUORUM_SEED='
# A timed-out seeds run: red, said so, no repro lines to give.
check "seeds cancelled" '' success mutants-green.log cancelled '' \
  '^issue create ' 'was cancelled \(timed out\?\)' '!QUORUM_SEED='
# A step that never ran (the job failed before it) is not green.
check "seeds skipped" 7 success mutants-green.log skipped '' \
  '^issue comment 7 ' 'did not run'

[ "$fail" = 0 ] && echo "quorum-nightly-issue: all cases pass"
exit "$fail"
