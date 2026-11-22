package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/joyautomation/nautilus/checkout"
	"github.com/joyautomation/nautilus/internal/project"
)

const checkoutUsage = `naut checkout — the I/O checkout and commissioning sign-off (#719)

Usage:
  naut checkout init [--project dir] [--force]

init writes checkout.yaml: one row for each scalar input and output tag the
project declares, with the device, address, equipment and area the project
knows (an alias's hardware address; the equipment of an alarm on the tag;
the first auth area whose tag patterns match), the kind (DI, DO, AI, AO) and
the method (observe an input; force an output). Edit the rows (the device
and equipment the project cannot know, what a pass looks like) and commit
the file. Struct and array tags are not listed: add their member rows by
hand ("P101.Speed").

The checkout is off until the manifest has a checkout: section:

  checkout: {}

The checkout is optional: a project without one has no checkout page, no
routes and no prompts.
`

// runCheckout is naut checkout in a project checkout.
func runCheckout(args []string) int {
	return checkoutMain(args, os.Stdout, os.Stderr)
}

func checkoutMain(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stdout, checkoutUsage)
		return 0
	}
	switch args[0] {
	case "init":
		fs := flag.NewFlagSet("naut checkout init", flag.ContinueOnError)
		fs.SetOutput(stderr)
		dir := fs.String("project", ".", "the project directory")
		force := fs.Bool("force", false, "overwrite an existing checkout.yaml")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		return checkoutInit(*dir, *force, stdout, stderr)
	}
	fmt.Fprintf(stderr, "naut checkout: unknown command %q (try naut checkout --help)\n", args[0])
	return 2
}

func checkoutInit(dir string, force bool, stdout, stderr io.Writer) int {
	m, err := project.ReadManifestForCheckoutInit(os.DirFS(dir))
	if err != nil {
		fmt.Fprintln(stderr, "naut checkout:", err)
		return 1
	}
	file := project.DefaultCheckoutFile
	if m.Checkout != nil && m.Checkout.File != "" {
		file = m.Checkout.File
	}
	dest := filepath.Join(dir, filepath.FromSlash(file))
	if _, err := os.Stat(dest); err == nil && !force {
		fmt.Fprintf(stderr, "naut checkout: %s already exists (it holds the engineer's edits): --force replaces it\n", file)
		return 1
	}
	sh, skipped := deriveSheet(m)
	if len(sh.Points) == 0 {
		fmt.Fprintln(stderr, "naut checkout: the project declares no scalar input or output tags to check")
		return 1
	}
	var buf bytes.Buffer
	buf.WriteString("# The I/O checkout sheet (#719), written by `naut checkout init`.\n# Fill in device and equipment where the project could not, say what a pass looks like in expect,\n# and commit. Enable it with `checkout: {}` in the manifest.\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(sh); err != nil {
		fmt.Fprintln(stderr, "naut checkout:", err)
		return 1
	}
	if err := os.WriteFile(dest, buf.Bytes(), 0o644); err != nil {
		fmt.Fprintln(stderr, "naut checkout:", err)
		return 1
	}
	fmt.Fprintf(stdout, "wrote %s: %d points\n", file, len(sh.Points))
	if len(skipped) > 0 {
		sort.Strings(skipped)
		fmt.Fprintf(stderr, "naut checkout: %d struct, array or untyped tags are not listed (add member rows by hand): %s\n", len(skipped), strings.Join(skipped, ", "))
	}
	if m.Checkout == nil {
		fmt.Fprintln(stdout, "add `checkout: {}` to the manifest to turn the checkout on")
	}
	return 0
}

var analogTypes = map[string]bool{"REAL": true, "LREAL": true, "INT": true, "DINT": true, "SINT": true, "LINT": true, "UINT": true, "UDINT": true, "USINT": true, "ULINT": true, "WORD": true, "DWORD": true, "BYTE": true}

// deriveSheet is the sheet the project's I/O tags make, and the names of the
// tags it could not (a struct, an array, or one with no type to read).
func deriveSheet(m *project.Manifest) (*checkout.Sheet, []string) {
	// the equipment of the alarms on each tag
	equip := map[string]string{}
	if m.Alarms != nil {
		for _, d := range m.Alarms.Defs {
			if d.Tag != "" && d.Equipment != "" {
				equip[strings.ToLower(d.Tag)] = d.Equipment
			}
		}
	}
	var areas []string
	if m.Auth != nil {
		for n := range m.Auth.Areas {
			areas = append(areas, n)
		}
		sort.Strings(areas)
	}
	areaOf := func(tag string) string {
		l := strings.ToLower(tag)
		for _, a := range areas {
			for _, pat := range m.Auth.Areas[a].Tags {
				if ok, _ := path.Match(strings.ToLower(pat), l); ok {
					return a
				}
			}
		}
		return ""
	}
	sh := &checkout.Sheet{Title: m.Name + " I/O checkout"}
	var skipped []string
	for _, t := range m.Tags {
		role := strings.ToLower(t.Role)
		if role != "input" && role != "output" {
			continue
		}
		var digital, ok bool
		switch typ := strings.ToUpper(t.Type); {
		case typ == "BOOL":
			digital, ok = true, true
		case analogTypes[typ]:
			ok = true
		case typ == "":
			switch t.Init.(type) {
			case bool:
				digital, ok = true, true
			case int, int64, uint64, float64:
				ok = true
			}
		}
		if !ok {
			skipped = append(skipped, t.Name)
			continue
		}
		kind, method := checkout.AI, checkout.Observe
		switch {
		case role == "input" && digital:
			kind = checkout.DI
		case role == "output" && digital:
			kind, method = checkout.DO, checkout.Force
		case role == "output":
			kind, method = checkout.AO, checkout.Force
		}
		sh.Points = append(sh.Points, checkout.Point{ID: t.Name, Tag: t.Name, Address: t.Alias, Equipment: equip[strings.ToLower(t.Name)],
			Area: areaOf(t.Name), Kind: kind, Method: method, Desc: t.Desc})
	}
	return sh, skipped
}

