# Releasing nautilus

Three artifacts, four version lines (the extension has two channels), one
rule: **the repo is the source of truth, and a registry may never run ahead
of it.** CI's `version-sync` job enforces the rule on every push; the
publish workflows make registries catch up.

| Artifact | Version lives in | Ships when | Where |
|---|---|---|---|
| CLI + Go libraries | git tag `v*` | you push the tag | GitHub Release: GoReleaser binaries + a pre-release VSIX |
| VS Code extension — **pre-release** (odd minor, `0.11.x`) | `tools/vscode-iec/package.json` on main | a version bump lands on main (`publish.yml`) | Marketplace + Open VSX, pre-release channel |
| VS Code extension — **stable** (even minor, `0.10.x`) | git tag `vscode-v<X.Y.Z>` | you push the tag (`vscode-stable.yml`) | Marketplace + Open VSX, stable channel; GitHub Release with the VSIX |
| `@joyautomation/nautilus-hmi` | `hmi/package.json` | a version bump lands on main | npm |

There are no manual `vsce publish` or `npm publish` runs anymore — publishing
by hand puts the registry ahead of the repo, which is exactly the drift the
guard rails exist to prevent.

## Ship the HMI kit, or an extension pre-release

Bump the `version` in the package's `package.json` (extension: update its
CHANGELOG too) in the same PR as the change, and merge. On the push to main,
`publish.yml` compares each package's repo version against its registry:

- versions equal → nothing to do (the workflow is idempotent on every push),
- repo ahead → publish,
- registry ahead → fail loudly: someone published out of band.

For the extension, main **is the pre-release channel**: every bump on main
is published with `--pre-release`, to both the Marketplace and Open VSX, from
one VSIX. Bump the patch (`0.11.3` → `0.11.4`) for each change.

## Extension channels

VS Code's convention, which we follow: **stable = even minor, pre-release =
odd minor, one above it.** Users on the stable channel get the highest stable
version; users who choose *Switch to Pre-Release Version* get the highest
version of either kind, so the pre-release line must always sit above stable.

| | Stable | Pre-release |
|---|---|---|
| minor | even (`0.10`, `0.12`, …) | odd (`0.11`, `0.13`, …) |
| source of truth | the `vscode-v<X.Y.Z>` tag | `tools/vscode-iec/package.json` on main |
| workflow | `vscode-stable.yml` | `publish.yml` |

The stable version is **never committed**: `vscode-stable.yml` stamps the
tag's version into package.json (`npm version --no-git-tag-version`) on the
tagged commit and builds from there, the way `v*` tags version the CLI. The
tagged commit itself carries a pre-release version from main — that is the
exact code being promoted.

### Promote to stable

1. **Pick the pre-release.** One that has been out for a few days with no
   follow-up fix. Note its commit on main (the one that bumped to it).
2. **CLI first.** If that build needs a newer `naut` (the CHANGELOG says
   "needs the matching `naut` CLI"), make sure that CLI version is already
   released — its `v*` tag pushed and the GitHub Release out.
3. **Stable CHANGELOG entry.** Add `## [0.X.0] - <date>` (X even) at the
   top of `tools/vscode-iec/CHANGELOG.md`, summarizing everything in the
   pre-releases it contains since the last stable. The tagged commit must
   contain it: the workflow publishes that section as the GitHub Release
   notes (and the Marketplace shows the file), and fails if it is missing.
   - Land it on main as a CHANGELOG-only PR (no version bump needed; nothing
     publishes). If no extension code landed between the chosen pre-release
     and this PR, the merge commit is the same code — tag it.
   - If extension code *has* landed since, branch from the chosen commit,
     commit the entry there, and tag that commit (also cherry-pick the entry
     to main).
4. **Tag and push:**
   ```sh
   git tag vscode-v0.10.0 <commit>
   git push origin vscode-v0.10.0
   ```
5. **Bump main to the next odd minor** (`0.11.x` → `0.13.0` after stable
   `0.12.0`; after the first stable, `0.10.0`, main is already on `0.11.x`
   and just continues). Pre-release must stay above stable, and
   `publish.yml` refuses to publish a pre-release below the highest stable.

**Stable hotfix:** branch from the tag (`git switch -c hotfix-0.10
vscode-v0.10.0`), fix, add a `## [0.10.1]` CHANGELOG entry, tag
`vscode-v0.10.1` on the fix, push the tag. main is unaffected; land the fix
on main separately as a normal pre-release bump. CI skips the pre-release
checks for PRs into a branch other than main.

## Cut a CLI release

```sh
git tag v0.3.2
git push origin v0.3.2
```

That triggers `release.yml`: GoReleaser builds cross-platform CLI binaries
onto a GitHub Release (the tag versions the CLI via ldflags), and a VSIX —
built at the extension's own package.json version, i.e. main's pre-release
line, and packaged as a pre-release — is attached for offline installs. The
tag versions the **Go module and CLI only**; it does not touch the extension
or HMI versions. (`release.yml` ignores `vscode-v*` tags, which GitHub's
`v*` filter would otherwise match.)

## Guard rails

The extension's channel logic lives in two scripts that every workflow
shares: `.github/scripts/vscode-registry-versions.mjs` reads the highest
pre-release and highest stable version on each registry (Marketplace:
`vsce show --json`, a version is a pre-release when its properties carry
`Microsoft.VisualStudio.Code.PreRelease = "true"`; Open VSX:
`/api/v2/-/query?includeAllVersions=true`, per-version `preRelease` — its
`latest` alias is *not* stable-only), and `.github/scripts/vscode-channel-guard.sh`
applies the rules below. Run the guard locally to see what CI would decide:
`.github/scripts/vscode-channel-guard.sh sync 0.11.0 0.0.0`.

- `version-sync` (in `ci.yml`) fails any push/PR where npm has a higher HMI
  version than `hmi/package.json`, or, per extension channel:
  - a registry's highest **pre-release** is above main's package.json, or
    main's package.json has an even minor;
  - a registry's highest **stable** is above the highest `vscode-v*` tag
    (no stable and no tags yet → passes).
- `publish.yml` re-checks before publishing a pre-release, and also refuses a
  version below the highest stable ("bump main to the next odd minor").
- `vscode-stable.yml` refuses a tag that is not `X.Y.Z`, has an odd minor, or
  is below the highest stable on either registry. A tag equal to a
  registry's stable means that registry already has it (a rerun after a
  failure) and it is skipped.
- A registry that cannot be read (the Marketplace gallery API times out for
  stretches) is a warning, not a failure; publishing then relies on
  `--skip-duplicate`. Open VSX publishes even when the Marketplace step
  failed; rerun the job to retry the Marketplace.
- Recovering from out-of-band drift: pre-release — bump main's package.json
  past the registry version and merge; stable — tag the version that was
  published (`vscode-v<that version>`) or publish past it from a new tag.

## Registry credentials (Settings → Secrets and variables → Actions)

- **`VSCE_PAT`** (secret) — VS Code Marketplace. Azure DevOps org, publisher
  `joyauto` (already in `package.json`), Personal Access Token with
  Marketplace → Manage scope.
- **`OVSX_PAT`** (secret) — Open VSX. Eclipse Foundation account + signed
  Publisher Agreement, `joyauto` namespace, access token.
- **npm — OIDC, no secret.** On npmjs.com, the package's Settings → Trusted
  Publisher must point at repo `joyautomation/nautilus`, workflow
  **`publish.yml`** (npm verifies the workflow *filename*; it was previously
  configured for `release.yml` — update it or CI publishes will be rejected).
  Repository **variable** `PUBLISH_HMI` = `true` arms the publish step.

## Validating changes to the pipeline

`ci.yml` runs `goreleaser check` on every push/PR, so a broken
`.goreleaser.yaml` fails a normal CI run rather than a tagged release. To dry
-run a full build locally without publishing: `goreleaser release --snapshot
--clean`.
