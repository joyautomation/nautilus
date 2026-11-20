package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/ua"
	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/auth/users"
	"github.com/joyautomation/nautilus/internal/gopcuatest"
	opcuaserver "github.com/joyautomation/nautilus/opcua/server"
)

// `naut run` with an auth: block and an OPC UA server (#708 PR 8a): an
// OPC UA client signs in as a local user from the users file with a
// UserName token on a trusted SignAndEncrypt channel, writes only on the
// user's areas, and a wrong password is refused; a trusted certificate
// with no user writes only when auth.opcua-certificates maps it (then in
// its areas, as opcua-cert:<name>), else is refused naming UserName; the
// journal file holds the sign-ins and the writes, source opcua.
func TestNautRunSignsOPCUAUsersIn(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the CLI")
	}
	bin := buildNautCLI(t, t.TempDir(), nil)
	dir := t.TempDir()
	// A second certificate, mapped to a role by its thumbprint.
	mappedDER, mappedKey := opcuaClientCert(t, "urn:test:mapped")
	manifest := "name: guarded\ntasks:\n  - program: main.st\n    scan: 20ms\n" +
		"tags:\n  - { name: RCV_SP, role: setpoint, type: REAL, init: 1 }\n  - { name: RST_SP, role: setpoint, type: REAL, init: 1 }\n" +
		"server:\n  addr: \"127.0.0.1:0\"\n" +
		"opcua:\n  endpoint: opc.tcp://127.0.0.1:0\n  writable: [\"*_SP\"]\n" +
		"auth:\n  areas:\n    receiving: {tags: [RCV_*]}\n" +
		"  users:\n    akhan: [{role: operator, areas: [receiving]}]\n" +
		"  opcua-certificates:\n    mapped: {thumbprint: " + opcuaserver.Thumbprint(mappedDER) + ", grants: [{role: operator, areas: [receiving]}]}\n"
	os.WriteFile(filepath.Join(dir, "nautilus.yaml"), []byte(manifest), 0o644)
	os.WriteFile(filepath.Join(dir, "main.st"), []byte("PROGRAM Main\nVAR_EXTERNAL\n RCV_SP : REAL; RST_SP : REAL;\nEND_VAR\nEND_PROGRAM\n"), 0o644)
	// The client's certificate, trusted before the start.
	der, key := opcuaClientCert(t, "urn:test:scada")
	trusted := filepath.Join(dir, "pki", "trusted", "certs")
	os.MkdirAll(trusted, 0o700)
	os.WriteFile(filepath.Join(trusted, "scada.der"), der, 0o644)
	os.WriteFile(filepath.Join(trusted, "mapped.der"), mappedDER, 0o644)
	state := t.TempDir()
	os.Chmod(state, 0o700)
	uf := users.New()
	uf.Add("akhan", "hunter22")
	if err := uf.Save(filepath.Join(state, users.FileName), "opsadmin"); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "run")
	cmd.Dir = dir
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "NAUTILUS_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "STATE_DIRECTORY="+state, "XDG_STATE_HOME=")
	stdout, stderr := &lockedBuffer{}, &lockedBuffer{}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	t.Cleanup(func() {
		cmd.Process.Kill()
		<-exited
	})
	endpoint := opcuaURL(t, stdout.String, exited, stderr.String)

	ctx := context.Background()
	eps, err := opcua.GetEndpoints(ctx, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	var ep *ua.EndpointDescription
	for _, e := range eps {
		if e.SecurityPolicyURI == ua.SecurityPolicyURIBasic256Sha256 && e.SecurityMode == ua.MessageSecurityModeSignAndEncrypt {
			ep = e
		}
		if e.SecurityPolicyURI == ua.SecurityPolicyURINone {
			for _, p := range e.UserIdentityTokens {
				if p.TokenType == ua.UserTokenTypeUserName {
					t.Errorf("the None endpoint offers a UserName token: %+v", p)
				}
			}
		}
	}
	if ep == nil {
		t.Fatalf("no Basic256Sha256 endpoint; stderr:\n%s", stderr.String())
	}
	connectAs := func(cert []byte, k *rsa.PrivateKey, uri, name, password string) (*opcua.Client, error) {
		auth := []opcua.Option{opcua.AuthAnonymous(), opcua.SecurityFromEndpoint(ep, ua.UserTokenTypeAnonymous)}
		if name != "" {
			auth = []opcua.Option{opcua.AuthUsername(name, password), opcua.SecurityFromEndpoint(ep, ua.UserTokenTypeUserName)}
		}
		c, err := opcua.NewClient(endpoint, append(auth,
			opcua.Certificate(cert), opcua.PrivateKey(k), opcua.ApplicationURI(uri),
			opcua.AutoReconnect(false), opcua.DialTimeout(5*time.Second), opcua.RequestTimeout(10*time.Second))...)
		if err != nil {
			return nil, err
		}
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if err := gopcuatest.Connect(cctx, c); err != nil {
			return nil, err
		}
		t.Cleanup(func() { c.Close(context.Background()) })
		return c, nil
	}
	connect := func(name, password string) (*opcua.Client, error) {
		return connectAs(der, key, "urn:test:scada", name, password)
	}
	writeBoth := func(c *opcua.Client) []ua.StatusCode {
		t.Helper()
		resp, err := c.Write(ctx, &ua.WriteRequest{NodesToWrite: []*ua.WriteValue{
			{NodeID: ua.NewStringNodeID(1, "RCV_SP"), AttributeID: ua.AttributeIDValue, Value: &ua.DataValue{EncodingMask: ua.DataValueValue, Value: ua.MustVariant(7.5)}},
			{NodeID: ua.NewStringNodeID(1, "RST_SP"), AttributeID: ua.AttributeIDValue, Value: &ua.DataValue{EncodingMask: ua.DataValueValue, Value: ua.MustVariant(7.5)}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		return resp.Results
	}
	if _, err := connect("akhan", "wrong"); !errors.Is(err, ua.StatusBadUserAccessDenied) {
		t.Errorf("a wrong password: %v, want BadUserAccessDenied", err)
	}
	c, err := connect("akhan", "hunter22")
	if err != nil {
		t.Fatalf("akhan: %v; stderr:\n%s", err, stderr.String())
	}
	if got := writeBoth(c); got[0] != ua.StatusGood || got[1] != ua.StatusBadUserAccessDenied {
		t.Errorf("akhan writes RCV_SP, RST_SP: %v, want Good, BadUserAccessDenied", got)
	}
	// The same trusted certificate with no user: not mapped, so read-only.
	unmapped, err := connect("", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := writeBoth(unmapped); got[0] != ua.StatusBadUserAccessDenied || got[1] != ua.StatusBadUserAccessDenied {
		t.Errorf("an unmapped certificate writes: %v, want BadUserAccessDenied twice", got)
	}
	// The mapped certificate, no user: writes in its area as opcua-cert:mapped.
	mapped, err := connectAs(mappedDER, mappedKey, "urn:test:mapped", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := writeBoth(mapped); got[0] != ua.StatusGood || got[1] != ua.StatusBadUserAccessDenied {
		t.Errorf("the mapped certificate writes RCV_SP, RST_SP: %v, want Good, BadUserAccessDenied", got)
	}
	if !strings.Contains(stderr.String(), "Sparkplug writes are NOT covered") || strings.Contains(stderr.String(), "OPC UA server and Sparkplug") {
		t.Errorf("stderr's auth: notice:\n%s", stderr.String())
	}

	// The journal file: fsynced before each answer, so it is there now.
	want := map[string]bool{"auth.signin refused": false, "auth.signin ok user:akhan": false, "tag.write ok user:akhan RCV_SP": false, "tag.write refused user:akhan RST_SP": false,
		"tag.write ok opcua-cert:mapped RCV_SP": false, "tag.write refused opcua-cert:mapped RST_SP": false, "tag.write refused anonymous, naming UserName": false}
	ents, _ := os.ReadDir(filepath.Join(dir, "audit"))
	for _, de := range ents {
		if !audit.IsJournalFile(de.Name()) {
			continue
		}
		raw, _ := os.ReadFile(filepath.Join(dir, "audit", de.Name()))
		for _, line := range strings.Split(string(raw), "\n") {
			var e audit.Entry
			if json.Unmarshal([]byte(line), &e) != nil || e.Source != audit.SourceOPCUA {
				continue
			}
			switch {
			case e.Action == "auth.signin" && e.Result == audit.ResultRefused && e.Target == "user:akhan":
				want["auth.signin refused"] = true
			case e.Action == "auth.signin" && e.Result == audit.ResultOK && e.User == "user:akhan":
				want["auth.signin ok user:akhan"] = true
			case e.Action == "tag.write" && e.Result == audit.ResultOK && e.User == "user:akhan" && e.Target == "RCV_SP":
				want["tag.write ok user:akhan RCV_SP"] = true
			case e.Action == "tag.write" && e.Result == audit.ResultRefused && e.User == "user:akhan" && e.Target == "RST_SP":
				want["tag.write refused user:akhan RST_SP"] = true
			case e.Action == "tag.write" && e.Result == audit.ResultOK && e.User == "opcua-cert:mapped" && e.Target == "RCV_SP":
				want["tag.write ok opcua-cert:mapped RCV_SP"] = true
			case e.Action == "tag.write" && e.Result == audit.ResultRefused && e.User == "opcua-cert:mapped" && e.Target == "RST_SP":
				want["tag.write refused opcua-cert:mapped RST_SP"] = true
			case e.Action == "tag.write" && e.Result == audit.ResultRefused && e.User == "anonymous" && strings.Contains(fmt.Sprint(e.Detail["reason"]), "UserName"):
				want["tag.write refused anonymous, naming UserName"] = true
			}
		}
	}
	for k, ok := range want {
		if !ok {
			t.Errorf("no journal entry %q", k)
		}
	}
}

// opcuaClientCert makes a self-signed client application certificate for
// uri.
func opcuaClientCert(t *testing.T, uri string) ([]byte, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(uri)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "test client"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageContentCommitment | x509.KeyUsageKeyEncipherment | x509.KeyUsageDataEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		URIs:                  []*url.URL{u},
		DNSNames:              []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return der, key
}
