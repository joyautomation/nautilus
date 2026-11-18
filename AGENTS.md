# AGENTS.md

For AI coding agents working on nautilus itself. (`naut agents` writes a
different AGENTS.md into a user's automation project; that one comes from
`cmd/naut/templates/AGENTS.md.tmpl`.)

Read [CONTRIBUTING.md](CONTRIBUTING.md) first. It has the commands and
the rules, and this file doesn't repeat them. In short:

- **Build and test:** `go build ./... && go vet ./... && go test ./...`.
  The HMI kit, the VS Code extension and the docs site have their own npm
  checks; see the table in CONTRIBUTING.md.
- **`ci-ok` is the only gate.** Never merge without it green, never use
  auto-merge or an admin bypass.
- **A change needs a test that fails without it.** Mutation-check the
  test (break the code, see it go red, restore it) and say so in the PR.
- **Context map:** `docs/design/context-map.md` decides which package may
  import which; `go test ./internal/contextmap/` enforces it. Change the
  map before the code.
- **Public repo:** no customer, site or vendor-contact names, real tag
  names, IPs, serials or credentials anywhere, commit messages included.
- **Go API:** only `io`, `runtime`, `server`, `acceptance` and a few named
  seam interfaces are stable (CONTRIBUTING.md, "Go API"). The rest is
  internal. Templates and examples may import only the stable packages.
