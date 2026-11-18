#!/usr/bin/env bash
# Table test for ci-ok.sh; run from the repo root. CI's changes job runs it.
set -u
here=$(dirname "$0")
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail=0

# A workflow with one job of each kind the gate has to judge.
cat >"$tmp/wf.yml" <<'EOF'
on: pull_request
jobs:
  changes:
    runs-on: ubuntu-latest
  always:
    needs: changes
    runs-on: ubuntu-latest
  heavy:
    needs: changes
    if: needs.changes.outputs.full != 'false'
    runs-on: ubuntu-latest
    steps:
      - if: github.event_name == 'push'
        run: true
  ext:
    needs: changes
    if: needs.changes.outputs.extension != 'false'
    runs-on: ubuntu-latest
  both:
    needs: changes
    if: needs.changes.outputs.full != 'false' && needs.changes.outputs.extension != 'false'
    runs-on: ubuntu-latest
  other-if:
    if: github.event_name == 'push'
    runs-on: ubuntu-latest
  info:
    needs: [changes, heavy]
    if: needs.changes.outputs.full != 'false'
    continue-on-error: true
    runs-on: ubuntu-latest
  ci-ok:
    if: always()
    needs: [changes, always, heavy, ext, both, other-if]
    runs-on: ubuntu-latest
EOF

# needs <full> <extension> [job=result ...]: every job of the fixture
# succeeds unless named; result "-" leaves the job out of needs; full or
# extension "" leaves that scope output out.
needs() {
  local full=$1 ext=$2; shift 2
  local j='{"changes":"success","always":"success","heavy":"success","ext":"success","both":"success","other-if":"success"}'
  local kv
  for kv in "$@"; do j=$(jq --arg k "${kv%%=*}" --arg v "${kv#*=}" '.[$k] = $v' <<<"$j"); done
  jq --arg full "$full" --arg ext "$ext" '
    with_entries(select(.value != "-"))
    | with_entries(.value = {result: .value, outputs: {}})
    | if has("changes") then .changes.outputs = ({full: $full, extension: $ext} | with_entries(select(.value != ""))) else . end' <<<"$j"
}
check() { # check pass|fail <label> <needs json>
  local want=$1 label=$2 got
  if printf '%s' "$3" | "$here/ci-ok.sh" "$tmp/wf.yml" >"$tmp/out" 2>&1; then got=pass; else got=fail; fi
  if [ "$got" != "$want" ]; then
    echo "FAIL $label: want $want, got $got"; sed 's/^/    /' "$tmp/out"; fail=1
  fi
}

K=skipped
check pass "full run, all green"                           "$(needs true true)"
check pass "light PR: every gated job skipped by the scope" "$(needs false false heavy=$K ext=$K both=$K)"
check pass "Go-only PR: extension jobs skipped"             "$(needs true false ext=$K both=$K)"
check pass "behind both gates, full=false alone skips it"   "$(needs false true heavy=$K both=$K)"
check pass "the informational job failing doesn't count"    "$(needs true true info=failure)"
check fail "a job failed"                                   "$(needs true true heavy=failure)"
check fail "a job was cancelled"                            "$(needs true true ext=cancelled)"
check fail "heavy skipped though the scope said full"       "$(needs true true heavy=$K)"
check fail "ext skipped though the scope said extension"    "$(needs true true ext=$K)"
check fail "both-gated job skipped though both said true"   "$(needs true true both=$K)"
check fail "an always-run job skipped"                      "$(needs false false heavy=$K ext=$K both=$K always=$K)"
check fail "a job skipped by a non-scope if"                "$(needs true true other-if=$K)"
check fail "the scope job failed (its outputs say light)"   "$(needs false false changes=failure heavy=$K ext=$K both=$K)"
check fail "the scope job was cancelled"                    "$(needs true true changes=cancelled)"
check fail "the scope job missing from needs"               "$(needs true true changes=-)"
check fail "no full output, heavy skipped"                  "$(needs '' true heavy=$K)"
check fail "no extension output, ext skipped"               "$(needs true '' ext=$K)"
check fail "full garbled (FALSE), heavy skipped"            "$(needs FALSE true heavy=$K)"
check fail "extension garbled (no), ext skipped"            "$(needs true no ext=$K)"
check fail "a job missing from ci-ok's needs"               "$(needs true true heavy=-)"
check fail "needs is not JSON"                              "not json"
grep -q "not a JSON object" "$tmp/out" || { echo "FAIL needs is not JSON: no clear message"; sed 's/^/    /' "$tmp/out"; fail=1; }
# A workflow the parser finds no jobs in (renamed key, re-indented) must
# never pass with nothing judged.
printf 'on: push\nworkflow-jobs:\n  go:\n    runs-on: x\n' >"$tmp/nojobs.yml"
if printf '%s' "$(needs true true)" | "$here/ci-ok.sh" "$tmp/nojobs.yml" >"$tmp/out" 2>&1; then
  echo "FAIL a workflow with no jobs found: want fail, got pass"; fail=1
fi
grep -q "found no jobs" "$tmp/out" || { echo "FAIL a workflow with no jobs found: no clear message"; sed 's/^/    /' "$tmp/out"; fail=1; }

# YAML a line-by-line reader gets wrong: a commented job key (whose
# continue-on-error a naive parser pins on the job above), a quoted key, a
# flow-style job, a folded if:. The gate has to see each job as YAML does.
cat >"$tmp/tricky.yml" <<'EOF2'
jobs:
  changes:
    runs-on: x
  go:
    runs-on: x
  lint: # the job after go
    continue-on-error: true
    runs-on: x
  'quoted':
    runs-on: x
  flow: {runs-on: x, if: "needs.changes.outputs.full != 'false'"}
  folded:
    if: >-
      needs.changes.outputs.full != 'false' &&
      github.event_name == 'pull_request'
    runs-on: x
  near-miss:
    if: needs.changes.outputs.full != 'true'
    runs-on: x
  either:
    if: needs.changes.outputs.full != 'false' || github.event_name == 'push'
    runs-on: x
  either-and:
    if: needs.changes.outputs.full != 'false' && github.event_name == 'pull_request' || github.event_name == 'push'
    runs-on: x
  flaky:
    continue-on-error: ${{ inputs.flaky == 'true' }}
    runs-on: x
  ci-ok:
    if: always()
    runs-on: x
EOF2
tricky() { # tricky pass|fail <label> <full> <job=result ...>
  local want=$1 label=$2 full=$3 got; shift 3
  local j='{"changes":"success","go":"success","lint":"success","quoted":"success","flow":"success","folded":"success","near-miss":"success","either":"success","either-and":"success","flaky":"success"}' kv
  for kv in "$@"; do j=$(jq --arg k "${kv%%=*}" --arg v "${kv#*=}" '.[$k] = $v' <<<"$j"); done
  j=$(jq --arg full "$full" 'with_entries(select(.value != "-")) | with_entries(.value = {result: .value, outputs: {}}) | .changes.outputs = {full: $full, extension: "true"}' <<<"$j")
  if printf '%s' "$j" | "$here/ci-ok.sh" "$tmp/tricky.yml" >"$tmp/out" 2>&1; then got=pass; else got=fail; fi
  if [ "$got" != "$want" ]; then echo "FAIL $label: want $want, got $got"; sed 's/^/    /' "$tmp/out"; fail=1; fi
}
tricky pass "tricky YAML, all green"                           true
tricky fail "go failed (a commented job after it is informational)" true go=failure
tricky pass "the commented job is the informational one"      true lint=failure
tricky fail "a quoted-key job missing from needs"             true quoted=-
tricky fail "a quoted-key job failed"                         true quoted=failure
tricky pass "a flow-style job skipped by the scope"           false flow=$K folded=$K
tricky fail "a flow-style job skipped though full"            true flow=$K
tricky pass "a folded && gate skipped by the scope"           false folded=$K flow=$K
tricky fail "an if: that only mentions outputs.full, skipped" false near-miss=$K
tricky fail "an || gate, skipped (can't pin it on the scope)" false either=$K
tricky fail "the gate as an && term beside an ||, skipped"    false either-and=$K
tricky fail "continue-on-error as an expression is judged"   true flaky=failure

# The exact case from review: a job key with a comment, right after a
# gated go, carrying continue-on-error. Read line by line, its fields land
# on go, which then reads informational and its failure passes.
cat >"$tmp/commented.yml" <<'EOF2'
jobs:
  changes:
    runs-on: x
  go:
    needs: changes
    if: needs.changes.outputs.full != 'false'
    runs-on: x
  lint: # flaky lint, informational
    continue-on-error: true
    runs-on: x
  ci-ok:
    if: always()
    runs-on: x
EOF2
c='{"changes":{"result":"success","outputs":{"full":"true","extension":"true"}},"go":{"result":"failure"},"lint":{"result":"success"}}'
if printf '%s' "$c" | "$here/ci-ok.sh" "$tmp/commented.yml" >"$tmp/out" 2>&1; then
  echo "FAIL go failed beside a commented informational job: want fail, got pass"; sed 's/^/    /' "$tmp/out"; fail=1
fi

# The real workflow.
wf=.github/workflows/ci.yml
# ci-ok must keep if: always(): with !cancelled() or success() a cancelled
# or failed run reports ci-ok as skipped, which GitHub counts as passing.
[ "$(yq '.jobs."ci-ok".if' "$wf")" = "always()" ] || { echo "FAIL $wf: ci-ok's if: is not always()"; fail=1; }
# yq and Python's YAML agree on the job set (when PyYAML is around).
if python3 -c 'import yaml' 2>/dev/null; then
  a=$(yq '.jobs | keys | .[]' "$wf" | sort)
  b=$(python3 -c 'import sys,yaml; print("\n".join(sorted(yaml.safe_load(open(sys.argv[1]))["jobs"])))' "$wf")
  [ "$a" = "$b" ] || { echo "FAIL $wf: yq and PyYAML see different jobs"; diff <(echo "$a") <(echo "$b") | sed 's/^/    /'; fail=1; }
fi
# ci-ok's needs list every job it has to judge: a full green run passes,
# and dropping any one of them fails.
list=$(yq '.jobs."ci-ok".needs[]' "$wf")
if [ -z "$list" ]; then
  echo "FAIL: no ci-ok job with needs in $wf"; fail=1
else
  all=$(printf '%s\n' "$list" | jq -R . | jq -s 'map({(.): {result: "success", outputs: (if . == "changes" then {full: "true", extension: "true"} else {} end)}}) | add')
  if ! printf '%s' "$all" | "$here/ci-ok.sh" "$wf" >"$tmp/out" 2>&1; then
    echo "FAIL $wf: a full green run does not pass ci-ok (a job missing from its needs?)"; sed 's/^/    /' "$tmp/out"; fail=1
  fi
  for drop in $list; do
    [ "$drop" != changes ] || continue
    if printf '%s' "$all" | jq --arg d "$drop" 'del(.[$d])' | "$here/ci-ok.sh" "$wf" >/dev/null 2>&1; then
      echo "FAIL $wf: ci-ok passes with $drop missing from its needs"; fail=1
    fi
  done
fi
if [ $fail = 0 ]; then echo "ci-ok: all cases pass"; fi
exit $fail
