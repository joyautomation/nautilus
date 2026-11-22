package main

import (
	"bytes"
	"encoding/json"
	"errors"
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
  naut checkout verify <sealed.json> [--chain checkout.jsonl] [--audit audit.jsonl|dir] [--fingerprint <key fingerprint>]

init writes checkout.yaml: one row for each scalar input and output tag the
project declares, with the device, address, equipment and area the project
knows (an alias's hardware address; the equipment of an alarm on the tag;
the first auth area whose tag patterns match), the kind (DI, DO, AI, AO) and
the method (observe an input; force an output). Edit the rows (the device
and equipment the project cannot know, what a pass looks like) and commit
the file. Struct and array tags are not listed: add their member rows by
hand ("P101.Speed").

verify checks a sealed record (the file GET /api/checkout/runs/{id}/sealed
gives): the signature is the payload's, the payload names the key the record
carries, and the export is the one the payload hashes. With --chain (the
controller's checkout.jsonl, or a copy of it) it also checks the results file's
own chain, that the seal is in it where the record says, and that the export
holds exactly the results the file holds for the run. With --fingerprint (the
key's fingerprint, written down from the controller's Checkout page or
GET /api/checkout earlier) it checks the key is that one. With --audit (the
controller's audit journal, or a copy kept off the box) it finds the
checkout.seal entry that carries this record's hash: the audit journal is a
second, independently kept place the seal is written, so a record that matches
a copy of the journal made elsewhere was sealed when it says. It exits 1 on any
failure. The record is tamper-evident and signed by the controller's key: it
proves the record came from the holder of that key and was not altered since
(whoever holds the controller's state directory, the results file and the key,
can forge both: keep copies elsewhere, and give --chain and --audit copies made
elsewhere). The
people it names were identified by the controller's sign-in, not by a personal
certificate; it is not an electronic signature in a legal or regulatory sense.

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
	case "verify":
		return checkoutVerify(args[1:], stdout, stderr)
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

// checkoutVerify is naut checkout verify.
func checkoutVerify(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("naut checkout verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	chain := fs.String("chain", "", "the controller's checkout.jsonl (or a copy of it)")
	fp := fs.String("fingerprint", "", "the signing key's fingerprint, to compare")
	auditPath := fs.String("audit", "", "the controller's audit journal (a .jsonl file, or its directory, or a copy of either)")
	// the file may come before or after the flags
	var files []string
	rest := args
	for len(rest) > 0 {
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		rest = fs.Args()
		if len(rest) > 0 {
			files = append(files, rest[0])
			rest = rest[1:]
		}
	}
	if len(files) != 1 {
		fmt.Fprintln(stderr, "naut checkout verify: one sealed record file (try naut checkout --help)")
		return 2
	}
	rec, err := os.ReadFile(files[0])
	if err != nil {
		fmt.Fprintln(stderr, "naut checkout verify:", err)
		return 1
	}
	var cdata []byte
	if *chain != "" {
		if cdata, err = os.ReadFile(*chain); err != nil {
			fmt.Fprintln(stderr, "naut checkout verify:", err)
			return 1
		}
		if cdata == nil {
			cdata = []byte{}
		}
	}
	rep, err := checkout.Verify(rec, cdata)
	if err != nil {
		fmt.Fprintln(stderr, "naut checkout verify: FAILED:", err)
		return 1
	}
	if *fp != "" && *fp != rep.Fingerprint {
		fmt.Fprintf(stderr, "naut checkout verify: FAILED: the record is signed by key %s, not %s\n", rep.Fingerprint, *fp)
		return 1
	}
	fmt.Fprintf(stdout, "OK: run %s, signed off by %s (%s), %d result(s), signed by key %s\n", rep.Run, rep.Signer, rep.Mode, rep.Results, rep.Fingerprint)
	if rep.Statement != "" {
		fmt.Fprintln(stdout, "note:", rep.Statement)
	}
	switch {
	case rep.ChainChecked:
		fmt.Fprintln(stdout, "checked: the signature, the export's hash, the results file's chain, the seal's place in it, and that the export holds the file's results for the run")
	default:
		fmt.Fprintln(stdout, "checked: the signature and the export's hash (give --chain checkout.jsonl to check the results file too)")
	}
	if *auditPath != "" {
		sha, err := sealedPayloadSha(rec)
		if err != nil {
			fmt.Fprintln(stderr, "naut checkout verify: FAILED:", err)
			return 1
		}
		hit, err := findSealAudit(*auditPath, sha)
		if err != nil {
			fmt.Fprintln(stderr, "naut checkout verify: FAILED:", err)
			return 1
		}
		fmt.Fprintf(stdout, "audit: matches the controller's audit entry %s (seq %d, %s, %s)\n", hit.ID, hit.Seq, hit.User, hit.TS)
	}
	if *fp == "" {
		fmt.Fprintln(stdout, "not checked: that this key is the controller's: compare the fingerprint with one you recorded from the controller (--fingerprint)")
	}
	fmt.Fprintln(stdout, checkout.SealNotice)
	return 0
}

// sealedPayloadSha is the hash of a record's payload, as the seal entry and the
// audit entry carry it.
func sealedPayloadSha(record []byte) (string, error) {
	var sr checkout.SealedRecord
	if err := json.Unmarshal(record, &sr); err != nil || len(sr.Payload) == 0 {
		return "", errors.New("not a sealed record")
	}
	return checkout.PayloadSha256(sr.Payload), nil
}

// sealAuditHit is the audit entry that anchors a seal.
type sealAuditHit struct {
	ID   string `json:"id"`
	Seq  uint64 `json:"seq"`
	TS   string `json:"ts"`
	User string `json:"user"`
}

// findSealAudit reads an audit journal (a .jsonl file, or a directory of them)
// for the accepted checkout.seal entry whose after.sealSha256 is sha.
func findSealAudit(where, sha string) (sealAuditHit, error) {
	var files []string
	if fi, err := os.Stat(where); err != nil {
		return sealAuditHit{}, err
	} else if fi.IsDir() {
		m, _ := filepath.Glob(filepath.Join(where, "*.jsonl"))
		sort.Strings(m)
		files = m
	} else {
		files = []string{where}
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return sealAuditHit{}, err
		}
		for _, line := range bytes.Split(b, []byte("\n")) {
			var e struct {
				sealAuditHit
				Action string `json:"action"`
				Result string `json:"result"`
				After  struct {
					SealSha string `json:"sealSha256"`
				} `json:"after"`
			}
			if len(line) == 0 || json.Unmarshal(line, &e) != nil {
				continue
			}
			if e.Action == "checkout.seal" && e.Result == "ok" && e.After.SealSha == sha {
				return e.sealAuditHit, nil
			}
		}
	}
	return sealAuditHit{}, errors.New("the audit journal has no accepted checkout.seal entry for this record: it is another controller's journal, an older copy that ends before the seal, or the seal was never recorded there")
}
