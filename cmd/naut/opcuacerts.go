package main

// `naut opcua certs` and `naut opcua trust` manage the OPC UA SERVER's
// certificate store (the opcua: section's pki directory): list the server's
// own certificate and the client certificates it trusts or has refused, and
// approve a refused one. A client whose certificate is not trusted is
// refused with Bad_CertificateUntrusted and its certificate filed under
// rejected/; `naut opcua trust <thumbprint>` moves it to trusted/, and the
// client's next attempt connects, with no restart.

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"

	"github.com/joyautomation/nautilus/internal/project"
	opcuaserver "github.com/joyautomation/nautilus/opcua/server"
)

// pkiFlag registers --pki on fs: the store directory, by default the one
// the project in the working directory names (opcua.pki), else ./pki,
// where `naut run` puts it.
func pkiFlag(fs *flag.FlagSet) *string {
	return fs.String("pki", "", "certificate store directory (default: opcua.pki in ./nautilus.yaml, else ./pki)")
}

func resolvePKI(flagValue, manifest string) string {
	if flagValue != "" {
		return flagValue
	}
	if manifest == "" {
		manifest = "nautilus.yaml"
	}
	raw, err := os.ReadFile(filepath.FromSlash(manifest))
	if err == nil {
		var m struct {
			OPCUA *struct {
				PKI string `yaml:"pki"`
			} `yaml:"opcua"`
		}
		if yaml.Unmarshal(raw, &m) == nil && m.OPCUA != nil && m.OPCUA.PKI != "" {
			return m.OPCUA.PKI
		}
	}
	return opcuaserver.DefaultPKI
}

func runOPCUACerts(args []string) int {
	fs := flag.NewFlagSet("opcua certs", flag.ContinueOnError)
	pkiDir := pkiFlag(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "naut opcua certs: takes no arguments")
		return 2
	}
	dir := resolvePKI(*pkiDir, "")
	if _, err := os.Stat(dir); err != nil {
		fmt.Fprintf(os.Stderr, "naut opcua certs: no certificate store at %s (the server makes it on its first start; --pki names another)\n", dir)
		return 1
	}
	own, trusted, rejected, err := opcuaserver.PKI{Dir: dir}.Certs()
	if err != nil {
		fmt.Fprintf(os.Stderr, "naut opcua certs: %v\n", err)
		return 1
	}
	now := time.Now()
	if own != nil {
		fmt.Printf("Server certificate (%s)\n", own.File)
		fmt.Printf("  thumbprint  %s\n", own.Thumbprint)
		fmt.Printf("  subject     %s\n", printable(own.Subject))
		fmt.Printf("  uri         %s\n", printable(own.ApplicationURI))
		fmt.Printf("  expires     %s\n", expiry(own.NotAfter, now))
		fmt.Printf("  hosts       %s\n", printable(strings.Join(append(append([]string(nil), own.DNSNames...), own.IPAddresses...), ", ")))
	} else {
		fmt.Printf("Server certificate: none yet (the server makes one in %s on its first start)\n", filepath.Join(dir, "own"))
	}
	list := func(title, sub string, certs []opcuaserver.CertInfo, hint string) {
		fmt.Printf("\n%s (%s): %d\n", title, filepath.Join(dir, sub, "certs"), len(certs))
		if len(certs) == 0 {
			return
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  THUMBPRINT\tAPPLICATION URI\tSUBJECT\tEXPIRES")
		for _, c := range certs {
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", c.Thumbprint, printable(c.ApplicationURI), printable(c.Subject), expiry(c.NotAfter, now))
		}
		tw.Flush()
		if hint != "" {
			fmt.Println(hint)
		}
	}
	list("Trusted clients", "trusted", trusted, "")
	list("Rejected clients", "rejected", rejected, "Approve one with: naut opcua trust <thumbprint>")
	return 0
}

// printable is s with every control or non-printable character shown as
// "?": a certificate's subject and URI are chosen by whoever made it, and
// rejected/ holds certificates from anyone who reached the port, so they
// must not carry terminal escape sequences into the listing.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return '?'
	}, s)
}

func expiry(t, now time.Time) string {
	d := t.Sub(now)
	switch {
	case d < 0:
		return t.Format("2006-01-02") + " (EXPIRED)"
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%s (in %d days)", t.Format("2006-01-02"), int(d.Hours()/24)+1)
	}
	return t.Format("2006-01-02")
}

func runOPCUATrust(args []string) int {
	return opcuaTrustMain(args, os.Stdout, os.Stderr)
}

const opcuaTrustUsage = "usage: naut opcua trust [--pki DIR] <thumbprint> [--as <name>:<role>[@<area>,...]] [-m <manifest>]   (naut opcua certs lists the rejected ones)"

// opcuaTrustMain is naut opcua trust: it approves a refused client's
// certificate and, with --as, maps it in the manifest's auth: block
// (auth.opcua-certificates, by its full thumbprint) in the same step
// (#708). Everything --as writes is checked before the certificate moves,
// so a refused mapping leaves both the store and the manifest as they were.
func opcuaTrustMain(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("opcua trust", flag.ContinueOnError)
	fs.SetOutput(stderr)
	pkiDir := pkiFlag(fs)
	as := fs.String("as", "", "also map the certificate in auth.opcua-certificates: <name>:<role>[@<area>,...]")
	manifest := fs.String("m", "", manifestFlagUsage)
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return 2
	}
	if len(pos) != 1 {
		fmt.Fprintln(stderr, opcuaTrustUsage)
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "naut opcua trust: %v\n", err)
		return 1
	}
	pki := opcuaserver.PKI{Dir: resolvePKI(*pkiDir, *manifest)}
	if *as == "" {
		c, err := pki.Trust(pos[0])
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "trusted %s\n  %s\n  %s\nThe client can connect now; the server reads the trust list on every connection.\n", c.Thumbprint, printable(c.ApplicationURI), printable(c.Subject))
		if m, err := project.ReadManifest(os.DirFS("."), *manifest); err == nil && m.Auth != nil {
			fmt.Fprintf(stdout, "Under auth: until it is mapped it gets anonymous access (auth.anonymous: nothing under anonymous: none) and cannot write: naut opcua trust %s --as <name>:<role> maps it (or its client signs in with a user name).\n", c.Thumbprint)
		}
		return 0
	}
	g, err := parseGrantSpec(*as)
	if err != nil {
		return fail(fmt.Errorf("--as %w", err))
	}
	c, rejected, err := findClientCert(pki, pos[0])
	if err != nil {
		return fail(err)
	}
	e, err := openManifestEdit(".", *manifest)
	if errors.Is(err, os.ErrNotExist) {
		return fail(fmt.Errorf("--as edits the project's nautilus.yaml, and there is none here, so nothing changed. On a box naut deploy set up: trust it here without --as, then in the project's checkout add\n  auth:\n    opcua-certificates:\n      %s: {thumbprint: %s, grants: %s}\nand deploy it", g.name, c.Thumbprint, g.yaml()))
	}
	if err != nil {
		return fail(err)
	}
	if err := mapOPCUACert(e, g, c.Thumbprint); err != nil {
		return fail(err)
	}
	// Exact: the result is the manifest as it was plus this one entry,
	// mapped to this certificate.
	want := project.AuthCert{Thumbprint: c.Thumbprint, Grants: project.AuthGrants{{Role: g.role, Areas: g.areas}}}
	took := func(m *project.Manifest) bool { return exactCertEdit(e.m, m, g.name, want) }
	// The manifest first (save checks the edit loads and is exactly this
	// mapping): a mapping for a certificate not yet trusted is inert, a
	// trusted certificate not yet mapped is not.
	if _, err := e.save(took); err != nil {
		return fail(fmt.Errorf("--as %s: nothing changed: %w", *as, err))
	}
	// A certificate the block maps by its subject: the thumbprint mapping
	// wins from the restart on.
	if old, ok := e.m.AuthPolicy().OPCUACertFor("", c.Subject); ok {
		fmt.Fprintf(stdout, "note: auth.opcua-certificates.%s maps this certificate by its subject; %s, by its thumbprint, supersedes it for this certificate (a thumbprint wins over a subject)\n", old, g.name)
	}
	if rejected {
		if c, err = pki.Trust(c.Thumbprint); err != nil {
			return fail(fmt.Errorf("the mapping is written, but the certificate is not trusted (the mapping does nothing until it is): %w; run naut opcua trust %s", err, c.Thumbprint))
		}
		fmt.Fprintf(stdout, "trusted %s\n  %s\n  %s\n", c.Thumbprint, printable(c.ApplicationURI), printable(c.Subject))
	} else {
		fmt.Fprintf(stdout, "%s is trusted already\n", c.Thumbprint)
	}
	fmt.Fprintf(stdout, "%s: auth.opcua-certificates.%s = %s (thumbprint %s)\n", e.path, g.name, g.yaml(), c.Thumbprint)
	fmt.Fprintln(stdout, "The client can connect now. Its reads and writes, as opcua-cert:"+g.name+", need the grant loaded:")
	fmt.Fprintln(stdout, restartNote)
	return 0
}

// findClientCert is the one client certificate, rejected (rejected true)
// or trusted, whose thumbprint starts with prefix, refusing a prefix that
// names several or that is shorter than Trust takes.
func findClientCert(pki opcuaserver.PKI, prefix string) (opcuaserver.CertInfo, bool, error) {
	p := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(prefix), ":", ""))
	if len(p) < 16 {
		return opcuaserver.CertInfo{}, false, fmt.Errorf("give at least 16 hex digits of the thumbprint, ideally all 40 (naut opcua certs lists them)")
	}
	_, trusted, rejected, err := pki.Certs()
	if err != nil {
		return opcuaserver.CertInfo{}, false, err
	}
	c, inRejected, err := pickClientCert(p, trusted, rejected)
	if err != nil {
		return c, false, fmt.Errorf("%s: %w", pki.Dir, err)
	}
	return c, inRejected, nil
}

// pickClientCert is the one certificate of trusted and rejected whose
// thumbprint starts with p.
func pickClientCert(p string, trusted, rejected []opcuaserver.CertInfo) (opcuaserver.CertInfo, bool, error) {
	// Across both sets: a prefix that names one rejected and one trusted
	// certificate is ambiguous, not a pick of the rejected one. The same
	// certificate in both is the trusted one.
	hits := map[string]opcuaserver.CertInfo{}
	inRejected := map[string]bool{}
	for _, c := range trusted {
		if strings.HasPrefix(c.Thumbprint, p) {
			hits[c.Thumbprint] = c
		}
	}
	for _, c := range rejected {
		if _, ok := hits[c.Thumbprint]; !ok && strings.HasPrefix(c.Thumbprint, p) {
			hits[c.Thumbprint], inRejected[c.Thumbprint] = c, true
		}
	}
	switch len(hits) {
	case 0:
	case 1:
		for tp, c := range hits {
			return c, inRejected[tp], nil
		}
	default:
		return opcuaserver.CertInfo{}, false, fmt.Errorf("%d certificates (rejected and trusted) have a thumbprint starting %s; give more digits", len(hits), p)
	}
	return opcuaserver.CertInfo{}, false, fmt.Errorf("no rejected or trusted certificate has a thumbprint starting %s (a client's certificate lands in rejected/ when it is refused)", p)
}

// mapOPCUACert adds name: {thumbprint, grants} to the manifest's
// auth.opcua-certificates, making the key when there is none. It never
// makes the auth: block itself (errNoAuth).
func mapOPCUACert(e *manifestEdit, g grantSpec, thumbprint string) error {
	ak, av := child(e.root, "auth")
	if ak == nil {
		return errNoAuth
	}
	entry := fmt.Sprintf("%s: {thumbprint: %s, grants: %s}", g.name, thumbprint, g.yaml())
	ck, cv := child(av, "opcua-certificates")
	if ck == nil {
		if !blockMapping(av) {
			return fmt.Errorf("auth: (line %d) is not a block a command can add to; add this under it by hand:\n  opcua-certificates:\n    %s", ak.Line, entry)
		}
		return e.insertUnder(ak, av, []string{"opcua-certificates:", "  " + entry})
	}
	for i := 0; i+1 < len(cv.Content); i += 2 {
		if strings.EqualFold(cv.Content[i].Value, g.name) {
			return fmt.Errorf("auth.opcua-certificates already maps %s (line %d): choose another name, or edit that one", g.name, cv.Content[i].Line)
		}
		if _, tv := child(cv.Content[i+1], "thumbprint"); tv != nil && strings.EqualFold(tv.Value, thumbprint) {
			return fmt.Errorf("auth.opcua-certificates.%s (line %d) maps this certificate already", cv.Content[i].Value, cv.Content[i].Line)
		}
	}
	if !blockMapping(cv) {
		return fmt.Errorf("auth.opcua-certificates (line %d) is not a block a command can add to; add this under it by hand:\n    %s", ck.Line, entry)
	}
	return e.insertUnder(ck, cv, []string{entry})
}

// exactCertEdit reports whether post is pre with exactly one entry added
// to auth.opcua-certificates: name, as want, which the compiled policy
// maps want's thumbprint to; every other part of the manifest the same.
func exactCertEdit(pre, post *project.Manifest, name string, want project.AuthCert) bool {
	if pre.Auth == nil || post.Auth == nil || post.AuthPolicy() == nil {
		return false
	}
	if n, ok := post.AuthPolicy().OPCUACertFor(want.Thumbprint, ""); !ok || n != name {
		return false
	}
	if got, ok := post.Auth.OPCUACerts[name]; !ok || !reflect.DeepEqual(got, want) {
		return false
	}
	rest := *post.Auth
	rest.OPCUACerts = maps.Clone(post.Auth.OPCUACerts)
	delete(rest.OPCUACerts, name)
	if len(rest.OPCUACerts) == 0 && len(pre.Auth.OPCUACerts) == 0 {
		rest.OPCUACerts = pre.Auth.OPCUACerts
	}
	return reflect.DeepEqual(&rest, pre.Auth) && sameBut(pre, post, func(m *project.Manifest) { m.Auth = nil })
}
