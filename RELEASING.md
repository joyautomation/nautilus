# Releasing nautilus

Distribution is wired but dormant: the pipeline is in place, and cutting a
release is a single tag. Nothing publishes until you push a `v*` tag, and the
third-party publish steps stay skipped until their secrets exist.

## Cut a release

```sh
git tag v0.3.0
git push origin v0.3.0
```

That triggers `.github/workflows/release.yml`. The **tag drives every
version**: the CLI gets it via ldflags, and the extension / HMI package
versions are set from the tag at publish time — no manual version bumps.

## What ships, and what it needs

| Artifact | Channel | Secret required | Without the secret |
|---|---|---|---|
| CLI binaries (`cmd/nautilus`) | GitHub Release (GoReleaser) | none — uses `GITHUB_TOKEN` | **still publishes** |
| VSIX | attached to the GitHub Release | none | **still attached** |
| VS Code extension | VS Code Marketplace | `VSCE_PAT` | step skips |
| VS Code extension | Open VSX | `OVSX_PAT` | step skips |
| `@joyautomation/nautilus-hmi` | npm | `NPM_TOKEN` | step skips |

So on the very first tag — with **no secrets configured** — you get
cross-platform CLI binaries and the packaged `.vsix` on a GitHub Release.
Add the secrets when you want the public registries.

## Before the first *public* release

- **Make the repo public.** Go distribution is decentralized: once public +
  tagged, `go install github.com/joyautomation/nautilus/cmd/nautilus@latest`
  and `go get` of the libraries resolve through the module proxy with no
  further step. (Private works too, but only for you.)
- ~~Settle the license.~~ Done — Apache-2.0 at the repo root and in the
  `tools/vscode-iec` and `hmi` packages.
- **Add the registry secrets** below for whichever channels you want live.

## Registry accounts / secrets to create (Settings → Secrets → Actions)

- `VSCE_PAT` — VS Code Marketplace. Azure DevOps org, publisher `joyautomation`
  (already set in `package.json`), Personal Access Token with Marketplace →
  Manage scope.
- `OVSX_PAT` — Open VSX. Eclipse account, claim the `joyautomation` namespace,
  create a token.
- `NPM_TOKEN` — npm. Account with access to the `@joyautomation` org, an
  automation token.

## Validating changes to the pipeline

`ci.yml` runs `goreleaser check` on every push/PR, so a broken
`.goreleaser.yaml` fails a normal CI run rather than a tagged release. To dry
-run a full build locally without publishing: `goreleaser release --snapshot
--clean`.
