#!/usr/bin/env bash
# staticcheck's JSON findings (-f json, one object per line) as a Markdown job
# summary: counts by check and by package, the possible bugs (SA*) first, then
# every finding. Report only (#752): CI's staticcheck job is not a gate.
#
#   staticcheck-summary.sh <findings.jsonl>
set -euo pipefail
f=$1
root="$PWD/" # findings carry absolute paths; the summary shows them from the repo root
n=$(jq -s 'length' "$f")
echo "## staticcheck: $n finding(s)"
echo
if [ "$n" = 0 ]; then
  echo "None. Once this stays at none, the job can become a gate."
  exit 0
fi
echo "Not a gate: findings are routed to the sessions that own the code. **SA** checks are possible bugs; **S** simplifications; **ST** style; **U1000** unused code."
echo
echo "| check | count |"
echo "|---|---|"
jq -rs 'group_by(.code) | map({c: .[0].code, n: length}) | sort_by(-.n) | .[] | "| `\(.c)` | \(.n) |"' "$f"
echo
echo "| package | count |"
echo "|---|---|"
jq -rs --arg root "$root" 'map(.location.file | ltrimstr($root) | split("/") | .[:-1] | join("/")) | group_by(.) | map({p: .[0], n: length}) | sort_by(-.n) | .[] | "| `\(.p)` | \(.n) |"' "$f"
echo
line() { jq -r --arg root "$root" "$1"' | "- `\(.location.file | ltrimstr($root)):\(.location.line)` **\(.code)** \(.message)"' "$f"; }
echo "### Possible bugs (SA)"
echo
line 'select(.code | startswith("SA"))'
echo
echo "### Everything else"
echo
line 'select(.code | startswith("SA") | not)'
