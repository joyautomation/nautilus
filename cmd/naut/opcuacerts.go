package main

// `naut opcua certs` and `naut opcua trust` manage the OPC UA SERVER's
// certificate store (the opcua: section's pki directory): list the server's
// own certificate and the client certificates it trusts or has refused, and
// approve a refused one. A client whose certificate is not trusted is
// refused with Bad_CertificateUntrusted and its certificate filed under
// rejected/; `naut opcua trust <thumbprint>` moves it to trusted/, and the
// client's next attempt connects, with no restart.

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"gopkg.in/yaml.v3"

	opcuaserver "github.com/joyautomation/nautilus/opcua/server"
)

// pkiFlag registers --pki on fs: the store directory, by default the one
// the project in the working directory names (opcua.pki), else ./pki,
// where `naut run` puts it.
func pkiFlag(fs *flag.FlagSet) *string {
	return fs.String("pki", "", "certificate store directory (default: opcua.pki in ./nautilus.yaml, else ./pki)")
}

func resolvePKI(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	raw, err := os.ReadFile("nautilus.yaml")
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
	dir := resolvePKI(*pkiDir)
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
		fmt.Printf("  subject     %s\n", own.Subject)
		fmt.Printf("  uri         %s\n", own.ApplicationURI)
		fmt.Printf("  expires     %s\n", expiry(own.NotAfter, now))
		fmt.Printf("  hosts       %s\n", strings.Join(append(append([]string(nil), own.DNSNames...), own.IPAddresses...), ", "))
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
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", c.Thumbprint, c.ApplicationURI, c.Subject, expiry(c.NotAfter, now))
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
	fs := flag.NewFlagSet("opcua trust", flag.ContinueOnError)
	pkiDir := pkiFlag(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: naut opcua trust [--pki DIR] <thumbprint>   (naut opcua certs lists the rejected ones)")
		return 2
	}
	c, err := opcuaserver.PKI{Dir: resolvePKI(*pkiDir)}.Trust(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "naut opcua trust: %v\n", err)
		return 1
	}
	fmt.Printf("trusted %s\n  %s\n  %s\nThe client can connect now; the server reads the trust list on every connection.\n", c.Thumbprint, c.ApplicationURI, c.Subject)
	return 0
}
