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
#   lessons=true|false  website/lessons/ changed (the interactive lessons):
#                       cmd/naut's TestLessonsAreRealExercises runs the
#                       Check on every lesson's start, solution and
#                       variants; a light PR skips go test, so it runs
#                       as its own step
#   extension=true|false the VS Code extension, what it bundles or what its
#                       tests read changed: tools/vscode-iec/, the hmi/ and
#                       hmi-3d/ kits it links (file:), every example diagram
#                       (the gesture harness wants a committed layout
#                       fixture per tracked examples/**/*.ld|fbd|sfc), the
#                       lift station's own components (componentCompiler
#                       and userComponentBuild tests compile them), or
#                       .github/. The extension's own jobs need it, light
#                       PR or not; a Go-only PR leaves them out (the
#                       CLI-discovery e2e still runs on Linux: it drives naut).
#   e2e-all=true|false  the CLI-discovery e2e runs on macOS and Windows too,
#                       not Linux only: when what that e2e reads changed,
#                       tools/vscode-iec/ and the hmi/ and hmi-3d/ kits its
#                       webview-ui links. Not the example diagrams or the
#                       lift station's components: they set extension for
#                       the gesture harness and component tests, and the
#                       e2e writes its own fixtures (#916). A .github/ path
#                       sets it only when the e2e's
#                       definition changed: the vscode-iec-e2e job, the
#                       changes job (it builds the e2e's OS list) or the
#                       workflow's top-level keys (on, env, concurrency…).
#                       Those legs wait longest for a runner (and GitHub
#                       sometimes never gives one); every push to main runs
#                       all three regardless. To compare, set CI_SCOPE_BASE_CI
#                       to the base's ci.yml (CI_SCOPE_HEAD_CI defaults to
#                       .github/workflows/ci.yml). Fails closed: no base, yq
#                       missing or a file that does not parse counts as
#                       changed.
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
#                        (TestInstallDocsDownloadBeforeExtracting reads it).
#                        website/lessons/** stays light but sets lessons=
#                        (TestLessonsAreRealExercises reads it)
#   playground/app/**    the playground's VS Code app shell
#   docs/**/*.md         design notes -- but not docs/claims/ (tools/evidence)
#                        or docs/structure-warnings.md (lang/lint reads it)
#
# .github/ is never light: a workflow change runs everything.
set -u
full=false
any=false
hmibuild=false
lessons=false
extension=false
e2eall=false
roots=()
# e2e_unchanged: the e2e's definition is the same at the base and the head,
# compared parsed (so comments and layout do not count).
e2e_unchanged() {
  local base=${CI_SCOPE_BASE_CI:-} head=${CI_SCOPE_HEAD_CI:-.github/workflows/ci.yml} q a b
  [ -n "$base" ] && [ -f "$base" ] && [ -f "$head" ] && command -v yq >/dev/null || return 1
  # with_entries, not del(.jobs): yq's del empties .jobs for the rest of the
  # expression too, and the comparison would see only the top-level keys.
  q='{"top": with_entries(select(.key != "jobs")), "changes": .jobs.changes, "e2e": .jobs["vscode-iec-e2e"]}'
  a=$(yq -o=json "$q" "$base" 2>/dev/null) && b=$(yq -o=json "$q" "$head" 2>/dev/null) || return 1
  # A head without the e2e job is a change worth all three legs, never a pass.
  [ "$(yq -o=json '.jobs["vscode-iec-e2e"] | type' "$head" 2>/dev/null)" = '"!!map"' ] || return 1
  [ "$a" = "$b" ]
}
add_root() {
  local r
  for r in "${roots[@]+"${roots[@]}"}"; do [ "$r" = "$1" ] && return; done
  roots+=("$1")
}
while IFS= read -r path; do
  [ -n "$path" ] || continue
  any=true
  case "$path" in
    tools/vscode-iec/*|hmi/*|hmi-3d/*) extension=true; e2eall=true ;;
    .github/*)
      extension=true
      [ "$e2eall" = true ] || e2e_unchanged || e2eall=true ;;
    examples/*.ld|examples/*.fbd|examples/*.sfc) extension=true ;;
    examples/lift-station/hmi/src/lib/*) extension=true ;;
  esac
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
    website/lessons/*)
      lessons=true ;;
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
if [ "$any" != true ]; then full=true; extension=true; e2eall=true; fi
echo "full=$full"
echo "examples=${roots[*]+"${roots[*]}"}"
echo "hmibuild=$hmibuild"
echo "lessons=$lessons"
echo "extension=$extension"
echo "e2e-all=$e2eall"
