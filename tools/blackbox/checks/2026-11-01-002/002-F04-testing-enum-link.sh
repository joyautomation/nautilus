#!/usr/bin/env bash
# F-04: /reference/testing/ links "enumeration" to the relative href `functions.md#enumerations`,
# which resolves to /reference/testing/functions.md and is a 404. Passes when every such link on the
# page resolves to 200. Needs network access to the website.
set -u
SITE=${SUT_WEBSITE:-https://nautilus.joyautomation.com}
html=$(curl -sL "$SITE/reference/testing/") || { echo "FAIL fetch"; exit 1; }
fail=0
for h in $(echo "$html" | grep -o 'href="[^"#/:][^"#:]*\.md[^"]*"' | sed 's/href="//;s/"$//' | sort -u); do
  u="$SITE/reference/testing/${h%%#*}"; c=$(curl -s -o /dev/null -w '%{http_code}' "$u")
  echo "$h -> $u -> $c"; [ "$c" = 200 ] || fail=1
done
[ $fail = 0 ] && echo PASS || echo "FAIL broken .md link"; exit $fail
