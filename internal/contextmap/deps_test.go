// Package contextmap holds the executable half of docs/design/context-map.md:
// the import rules that keep nautilus's bounded contexts pointing the way the
// map says they do. There is no non-test code here on purpose — the map is a
// document, this is its check.
package contextmap

import (
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const module = "github.com/joyautomation/nautilus"

// rule is one kind of edge the map forbids: a package matching from may not
// import a package matching to. Entries are module-relative path prefixes; a
// bare name matches the package and its subtree. why quotes the map.
type rule struct {
	from []string
	to   []string
	why  string
}

// rules mirror docs/design/context-map.md §2. Change the map first, then
// the rule, then the code. u is the set of every package in the module, so
// a rule can say "anything except".
func rules(u universe) []rule {
	return []rule{
		{
			from: []string{"io", "lang/ir", "hist", "oplog", "audit"},
			to:   u.allBut(),
			why:  "substrates and seams import no other nautilus package",
		},
		{
			from: []string{"quorum"},
			to:   u.allBut("quorum"),
			why:  "quorum is a substrate: the vote protocol, its driver (quorum/node) and its simulator import only each other",
		},
		{
			from: u.allBut("quorum", "leader", "cmd"),
			to:   []string{"quorum"},
			why:  "only leader (which adapts it to leader.Elector) and the CLI (naut witness) know the vote protocol",
		},
		{
			from: []string{"retain"},
			to:   u.allBut("internal/k8sapi"),
			why:  "retain is a seam: a store interface plus a file and a ConfigMap adapter",
		},
		{
			from: []string{"lang"},
			to:   u.allBut("lang", "internal/tagfile"),
			why:  "the languages know the IR, each other and the tag-file shared kernel, nothing about the runtime or any bus",
		},
		{
			from: []string{"runtime"},
			to:   u.allBut("lang", "io", "retain", "internal/stproject"),
			why:  "the runtime hosts programs over the io seam; it never knows a driver, the server, alarms or the manifest",
		},
		{
			from: []string{"alarm"},
			to:   u.allBut("lang/ir", "retain"),
			why:  "alarms are a context of their own, wired to a tag store by the manifest, not by importing one",
		},
		{
			from: []string{"modbus", "eip", "optommp", "plcnext", "profinet", "hw", "snmp", "redfish", "prom", "replay", "sparkplug/host"},
			to:   []string{"runtime", "server", "alarm", "acceptance", "internal/project", "internal/lsp", "cmd", "devbind", "devprofile", "iolink"},
			why:  "drivers sit behind io.Driver and the tag file; the protocol side of the map never reaches back into the runtime side, nor knows the device library or the IO-Link layer above it",
		},
		{
			from: []string{"siemens/station", "siemens/tiad", "siemens/webapi", "siemens/writer", "siemens/importer"},
			to:   []string{"runtime", "server", "alarm", "acceptance", "internal/project", "internal/lsp", "cmd"},
			why:  "the Siemens target's agent client, Web API client, writer and importer are an anti-corruption layer; only siemens/facade, an application layer, composes the runtime side",
		},
		{
			from: []string{"server"},
			to:   []string{"modbus", "eip", "optommp", "plcnext", "profinet", "hw", "snmp", "redfish", "prom", "replay", "sparkplug", "logix", "siemens", "internal/project"},
			why:  "the operator surface serves a runtime and alarms; it knows no protocol and is composed by the manifest, not the reverse",
		},
		{
			from: []string{"devprofile"},
			to:   u.allBut(),
			why:  "the device profile format is a published language: the manifest, virtual devices and the CLI read it, and it reads none of them",
		},
		{
			from: []string{"iolink/iodd"},
			to:   u.allBut(),
			why:  "the IODD model and process data codec are a published language like the profile format: the device library, the IO-Link adapter and the CLI read it, and it reads none of them",
		},
		{
			from: []string{"iolink"},
			to:   []string{"runtime", "server", "alarm", "acceptance", "internal/project", "internal/lsp", "virtdev", "cmd"},
			why:  "the IO-Link layer derives a port device and adapts its master's driver, as devbind does; the manifest composes it, so it never reaches into the runtime side",
		},
		{
			from: []string{"devbind"},
			to:   []string{"runtime", "server", "alarm", "acceptance", "internal/project", "internal/lsp", "virtdev", "cmd"},
			why:  "devbind derives a device row's type, tags and driver manifests; the manifest composes it, so it never reaches into the runtime side",
		},
		{
			from: u.allBut("cmd", "virtdev"),
			to:   []string{"virtdev"},
			why:  "virtual devices are a test and commissioning context; only the CLI and verification compose them",
		},
		{
			from: u.allBut("cmd", "internal/lsp"),
			to:   []string{"internal/project"},
			why:  "internal/project is the composition root; only the CLI and the editor-facing services may import it",
		},
		{
			from: u.allBut(),
			to:   []string{"cmd"},
			why:  "nothing imports the CLI",
		},
	}
}

// excluded are packages the map treats as verification or dogfood: they
// compose whatever they test and are not held to a context's imports.
var excluded = []string{"examples", "testdata", "tools", "lang/conformance", "acceptance", "internal/contextmap"}

type universe []string

func (u universe) allBut(keep ...string) []string {
	var out []string
	for _, p := range u {
		if !matchesAny(p, keep) {
			out = append(out, p)
		}
	}
	return out
}

func matches(pkg, prefix string) bool {
	return pkg == prefix || strings.HasPrefix(pkg, prefix+"/")
}

func matchesAny(pkg string, prefixes []string) bool {
	for _, p := range prefixes {
		if matches(pkg, p) {
			return true
		}
	}
	return false
}

// graph runs go list from the module root and returns package -> internal
// imports, both module-relative. Test imports are not included; a test may
// import whatever it needs.
func graph(t *testing.T) map[string][]string {
	t.Helper()
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatalf("go env GOMOD: %v", err)
	}
	cmd := exec.Command("go", "list", "-f", "{{.ImportPath}} {{join .Imports \",\"}}", "./...")
	cmd.Dir = filepath.Dir(strings.TrimSpace(string(out)))
	out, err = cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	g := map[string][]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		pkg, imports, _ := strings.Cut(line, " ")
		pkg = strings.TrimPrefix(pkg, module+"/")
		if matchesAny(pkg, excluded) {
			continue
		}
		var deps []string
		for _, imp := range strings.Split(imports, ",") {
			if strings.HasPrefix(imp, module+"/") {
				deps = append(deps, strings.TrimPrefix(imp, module+"/"))
			}
		}
		g[pkg] = deps
	}
	return g
}

func TestContextMap(t *testing.T) {
	g := graph(t)
	var u universe
	for pkg := range g {
		u = append(u, pkg)
	}
	sort.Strings(u)
	for _, r := range rules(u) {
		for _, pkg := range u {
			if !matchesAny(pkg, r.from) {
				continue
			}
			for _, d := range g[pkg] {
				if matchesAny(d, r.to) {
					t.Errorf("%s imports %s: %s (docs/design/context-map.md §2)", pkg, d, r.why)
				}
			}
		}
	}
}

// stdlibOnly are packages that import nothing outside the standard library,
// not even a third-party module: the audit journal's integrity rests on it
// (docs/design/identity-audit.md §4).
var stdlibOnly = []string{"audit"}

func TestStdlibOnlyPackages(t *testing.T) {
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatalf("go env GOMOD: %v", err)
	}
	root := filepath.Dir(strings.TrimSpace(string(out)))
	for _, pkg := range stdlibOnly {
		cmd := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}} {{.Standard}}", "./"+pkg)
		cmd.Dir = root
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("go list -deps ./%s: %v", pkg, err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			imp, std, _ := strings.Cut(line, " ")
			if std != "true" && imp != module+"/"+pkg {
				t.Errorf("%s depends on %s: it imports only the standard library (docs/design/context-map.md §2)", pkg, imp)
			}
		}
	}
}
