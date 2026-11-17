#!/usr/bin/env bash
# Decides how much of ci.yml a pull request needs (#752). Reads the changed
# paths, one per line, on stdin; prints GitHub step outputs:
#
#   full=true|false     false only when EVERY path is on the light list below
#   examples=<roots>    example project roots (with a nautilus.yaml) whose
#                       HMI changed: naut check + naut test run on them
#   hmibuild=true|false examples/lift-station/hmi changed: run
#                       internal/hmibuild's TestBuildsTheLiftStationApp,
#                       the one Go test that builds an example's HMI
#
# The light list is paths no Go test, embed or naut binary reads, found by
# grepping the tests and by changing one file per area with a warm test
# cache and watching which packages re-ran. Anything not on it, an empty
# list, or any surprise means the full suite. Keep it short and exact: a
# path wrongly listed here skips the Go tests on the PR that changes it.
#
#   examples/**/hmi/**   an example's own HMI app (Svelte, assets) -- but
#                        not .st/.ld/.fbd/.sfc there (the language walks
#                        in cmd/naut and internal/lsp lint those)
#   website/**           the docs site -- but not getting-started.md
#                        (TestInstallDocsDownloadBeforeExtracting reads it)
#   playground/app/**    the playground's VS Code app shell
#   docs/**/*.md         design notes -- but not docs/claims/ (tools/evidence)
#                        or docs/structure-warnings.md (lang/lint reads it)
#
# .github/ is never light: a workflow change runs everything.
set -u
full=false
any=false
hmibuild=false
roots=()
add_root() {
  local r
  for r in "${roots[@]+"${roots[@]}"}"; do [ "$r" = "$1" ] && return; done
  roots+=("$1")
}
while IFS= read -r path; do
  [ -n "$path" ] || continue
  any=true
  case "$path" in
    examples/*/hmi/*.st|examples/*/hmi/*.ld|examples/*/hmi/*.fbd|examples/*/hmi/*.sfc)
      full=true ;;
    examples/*/hmi/*)
      root=${path%%/hmi/*}
      [ -f "$root/nautilus.yaml" ] && add_root "$root"
      [ "$root" = examples/lift-station ] && hmibuild=true
      ;;
    website/src/content/docs/getting-started.md)
      full=true ;;
    website/*|playground/app/*)
      ;;
    docs/claims/*|docs/structure-warnings.md)
      full=true ;;
    docs/*.md)
      ;;
    *)
      full=true ;;
  esac
done
[ "$any" = true ] || full=true
echo "full=$full"
echo "examples=${roots[*]+"${roots[*]}"}"
echo "hmibuild=$hmibuild"
