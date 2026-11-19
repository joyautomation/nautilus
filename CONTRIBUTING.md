# Contributing to nautilus

Thanks for helping. This file covers how a change gets from an idea to
`main`: where to start, how to build and test, the rules every PR is
held to, which Go packages are a stable API, and how review works.

By taking part you agree to the [Code of Conduct](CODE_OF_CONDUCT.md).
Report a security problem privately as [SECURITY.md](SECURITY.md)
describes, never in a public issue.

## Start with an issue

- Find or open an issue first. For anything bigger than a small fix, say
  in the issue what you plan to change before writing code, so the
  approach is agreed before the review.
- Keep PRs small and about one thing. Link the issue in the PR
  (`Fixes #123`, or `Part of #123` when the issue needs several PRs).

## Build and test

You need Go (the version in `go.mod`, currently 1.25) and, for the
TypeScript parts, Node.js with npm.

```sh
go build ./...
go vet ./...
go test ./...                    # everything; add -race for concurrency changes
go test ./internal/contextmap/   # the context-map import rules (below)
```

| Part | Where | Check it with |
| --- | --- | --- |
| runtime, languages, drivers, `naut` CLI | Go packages at the root, `cmd/naut` | `go test ./...` |
| operator-screen (HMI) kit | `hmi/` | `npm ci && npm run check && npm test` |
| VS Code extension | `tools/vscode-iec/` | `npm ci && npm test` |
| docs site | `website/` | `npm ci && npm run build` |

CI also runs staticcheck. Its findings are a report, not a gate, but a PR
should not add new ones in the packages it touches.

## The rules every PR is held to

**`ci-ok` is the only required check.** It passes when every CI job
passed, or was skipped because the change could not affect it. Nothing
merges without a green `ci-ok`: no auto-merge, and no admin bypass.

**Every change comes with a test that fails without it.** Before you
open the PR, mutation-check the test: undo the fix, or break the code
the test claims to cover, and watch the test go red, then put it back.
Say in the PR what you broke and which test caught it. A pure refactor
adds no test, but every existing test must pass unchanged.

**Respect the context map.** [`docs/design/context-map.md`](docs/design/context-map.md)
says which package may import which, and `internal/contextmap` enforces
it. If a change needs a new edge, change the map first, then the rule,
then the code, in that order, in the same PR, with the reason.

**This repository is public.** Never put any of the following in code,
tests, fixtures, commit messages, issues, comments or PRs:

- the name of a customer, a site, or a person at a vendor;
- a real plant's tag names or program names;
- IP addresses, hostnames or serial numbers of real equipment;
- credentials of any kind, including test ones that work somewhere.

Use synthetic names (`Tank1`, `PUMP_101`), documentation addresses
(`192.0.2.0/24`) and obviously fake secrets.

## Go API

nautilus is a CLI and a manifest format first. Most users never import
its Go packages. For those who do (the `sdk` templates of `naut new`, and
`examples/go-sdk`), only this much is a supported Go API:

- **Stable packages:** `io`, `runtime`, `server` and `acceptance`.
- **Stable interfaces** in packages that are otherwise internal:
  `retain.Store`, `hist.Sink`, `alarm.Journal` and `alarm.Notifier`, plus
  the types their methods take and return (`retain.State`,
  `alarm.Event`, `alarm.Filter`). `runtime.Coordinator`, the redundancy
  seam, is in `runtime`.

**Everything else is internal**, whatever its import path: every driver,
`lang/...`, `logix/...`, `eip/...`, `cmd/...`, `internal/...`, the rest of
`retain`, `hist` and `alarm`, and the shipped implementations of those
interfaces, such as `leader.Elector`. Any of it can change in any
release, without notice.

nautilus is pre-1.0, so even the stable set can change in a minor
release. When it does, the release notes say so.

`internal/contextmap` checks that the templates and examples import only
the stable packages.

## How PRs are reviewed

- A maintainer reviews every PR. Expect one review and one re-review. A
  minor point outside the PR's scope goes into a follow-up issue, not
  another round.
- Every change under `.github/` gets a maintainer's review before it
  merges, because CI runs the PR's own workflow files.
- Once a PR is approved and `ci-ok` is green, a maintainer squash-merges
  it.

## License

nautilus is licensed under [Apache-2.0](LICENSE). Contributions are
inbound = outbound: anything you intentionally submit is licensed under
Apache-2.0 too, as section 5 of the license says. There is no CLA and no
DCO sign-off.
