#!/usr/bin/env bash
# naut check every manifest of one example project, then naut test it (CI's
# examples job runs this once per project root, several at a time).
#
#   naut-example.sh <naut binary> <project root>
#
# Every sibling *.yaml with a top-level `tasks:` key is a manifest: the
# nautilus.yaml itself plus a field/line/site variant (field.yaml, line.yaml,
# ...); generated tag/device files are skipped. A project is tested when it
# has *_test.yaml suites. Exits non-zero when any check or test failed, after
# running all of them.
set -u
naut=$1
root=$2
fail=0
for manifest in "$root"/*.yaml; do
  [ -e "$manifest" ] || continue
  name=$(basename "$manifest")
  case "$name" in
    *_manifest.yaml|devices.yaml|sites.yaml) continue ;;
  esac
  grep -q '^tasks:' "$manifest" || continue
  if [ "$name" = nautilus.yaml ]; then
    echo "--- naut check $root"
    "$naut" check "$root" || fail=1
  else
    echo "--- naut check -m $name $root"
    "$naut" check -m "$name" "$root" || fail=1
  fi
done
if ls "$root"/*_test.yaml >/dev/null 2>&1; then
  echo "--- naut test $root"
  "$naut" test "$root" || fail=1
fi
exit $fail
