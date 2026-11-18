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

# The real workflow: ci-ok's needs list every job it has to judge, so a full
# run with all of them green passes; dropping any one of them fails.
wf=.github/workflows/ci.yml
list=$(awk '/^  ci-ok:/{f=1} f && /^    needs:/{sub(/^    needs: *\[/,""); sub(/\].*/,""); print; exit}' "$wf" | tr -d ' ' | tr ',' '\n')
if [ -z "$list" ]; then
  echo "FAIL: no ci-ok job with a needs: [...] list in $wf"; fail=1
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
