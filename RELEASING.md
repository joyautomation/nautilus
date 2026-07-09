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

| Artifact | Channel | Credential | When absent |
|---|---|---|---|
| CLI binaries (`cmd/nautilus`) | GitHub Release (GoReleaser) | none — uses `GITHUB_TOKEN` | **still publishes** |
| VSIX | attached to the GitHub Release | none | **still attached** |
| VS Code extension | VS Code Marketplace | secret `VSCE_PAT` | step skips |
| VS Code extension | Open VSX | secret `OVSX_PAT` | step skips |
| `@joyautomation/nautilus-hmi` | npm | OIDC trusted publisher + var `PUBLISH_HMI=true` | step skips |

So on the very first tag — with **nothing configured** — you get
cross-platform CLI binaries and the packaged `.vsix` on a GitHub Release.
Turn on each registry independently when you want it.

npm uses **OIDC trusted publishing**, not a stored token: the job mints a
short-lived credential GitHub↔npm and publishes with provenance. There's no
`NPM_TOKEN` to rotate. Because there's no secret to gate on, the HMI publish
is switched by a repository **variable** `PUBLISH_HMI` instead.

## Before the first *public* release

- **Make the repo public.** Go distribution is decentralized: once public +
  tagged, `go install github.com/joyautomation/nautilus/cmd/nautilus@latest`
  and `go get` of the libraries resolve through the module proxy with no
  further step. (Private works too, but only for you.)
- ~~Settle the license.~~ Done — Apache-2.0 at the repo root and in the
  `tools/vscode-iec` and `hmi` packages.
- **Add the registry secrets** below for whichever channels you want live.

## Registry setup (Settings → Secrets and variables → Actions)

- **`VSCE_PAT`** (secret) — VS Code Marketplace. Azure DevOps org, publisher
  `joyautomation` (already in `package.json`), Personal Access Token with
  Marketplace → Manage scope.
- **`OVSX_PAT`** (secret) — Open VSX. Eclipse Foundation account + signed
  Publisher Agreement, `joyautomation` namespace, access token.
- **npm — OIDC, no secret.** On npmjs.com, open the package's Settings →
  Trusted Publisher and point it at repo `joyautomation/nautilus`, workflow
  `release.yml` (leave environment blank). Then set repository **variable**
  `PUBLISH_HMI` = `true` (Variables tab) to arm the publish step.
  - First-publish bootstrap: npm can only attach a trusted publisher to a
    package that exists. If `@joyautomation/nautilus-hmi` has never been
    published, do one manual `npm publish` (from `hmi/`, logged in locally),
    then configure the trusted publisher — every CI publish after that is
    tokenless.

## Validating changes to the pipeline

`ci.yml` runs `goreleaser check` on every push/PR, so a broken
`.goreleaser.yaml` fails a normal CI run rather than a tagged release. To dry
-run a full build locally without publishing: `goreleaser release --snapshot
--clean`.
