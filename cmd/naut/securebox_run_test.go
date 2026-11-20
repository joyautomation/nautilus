package main

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/ua"
	"github.com/joyautomation/nautilus/internal/gopcuatest"
	opcuaserver "github.com/joyautomation/nautilus/opcua/server"
)

// How hard is it to secure a controller? (#708 DX, James: "we dont want it
// to be difficult to manage or get things running".) From what naut new
// writes to an operator signed in over HTTPS, a personal access token, and
// an OPC UA client certificate mapped to a role, with these six commands
// and no hand edit of nautilus.yaml:
//
//	naut auth init --user alice:operator
//	naut users add alice
//	naut users token alice
//	naut run
//	naut opcua certs
//	naut opcua trust <thumbprint> --as scada:operator
//
// (and a restart of naut run for the grant). The one prerequisite, outside
// the count: the project has an opcua: section, which naut new does not
// write. The test runs exactly those commands with the built CLI and checks
// each promise: HTTPS with the certificate whose fingerprint auth init
// printed, alice's sign-in, the token, the refused client's certificate
// mapped by its full thumbprint, and its write landing after the restart.
func TestSecureAControllerFromNautNew(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the CLI")
	}
	bin := buildNautCLI(t, t.TempDir(), nil)
	work, state := t.TempDir(), t.TempDir()
	env := []string{"XDG_STATE_HOME=" + state, "STATE_DIRECTORY=", "INVOCATION_ID=", "NAUTILUS_ADDR=127.0.0.1:0"}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "NAUTILUS_") && !strings.HasPrefix(kv, "XDG_STATE_HOME=") &&
			!strings.HasPrefix(kv, "STATE_DIRECTORY=") && !strings.HasPrefix(kv, "INVOCATION_ID=") {
			env = append(env, kv)
		}
	}
	naut := func(stdin string, args ...string) (string, string) {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Dir = filepath.Join(work, "plant")
		if args[0] == "new" {
			cmd.Dir = work
		}
		cmd.Env = env
		cmd.Stdin = strings.NewReader(stdin)
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		if err := cmd.Run(); err != nil {
			t.Fatalf("naut %s: %v\n%s%s", strings.Join(args, " "), err, out.String(), errb.String())
		}
		return out.String(), errb.String()
	}
	naut("", "new", "--no-input", "plant")
	dir := filepath.Join(work, "plant")
	manifest := filepath.Join(dir, "nautilus.yaml")
	// The prerequisite: an OPC UA server whose setpoint a client may write.
	f, _ := os.OpenFile(manifest, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("\nopcua:\n  endpoint: opc.tcp://127.0.0.1:0\n  writable: [TempSP]\n")
	f.Close()
	before, _ := os.ReadFile(manifest)

	// 1. auth init: the block, server.tls: self-signed, the fingerprint.
	_, initErr := naut("", "auth", "init", "--user", "alice:operator")
	fp := regexp.MustCompile(`([0-9A-F]{2}(:[0-9A-F]{2}){31})`).FindString(initErr)
	if fp == "" {
		t.Fatalf("auth init printed no fingerprint:\n%s", initErr)
	}
	// 2-3. alice's password, her token.
	naut("correct-horse-battery\n", "users", "add", "alice")
	pat, _ := naut("", "users", "token", "alice")
	pat = strings.TrimSpace(pat)

	// 4. naut run: HTTPS with the certificate auth init told of.
	start := func() (api, endpoint string, stop func()) {
		cmd := exec.Command(bin, "run")
		cmd.Dir, cmd.Env = dir, env
		stdout, stderr := &lockedBuffer{}, &lockedBuffer{}
		cmd.Stdout, cmd.Stderr = stdout, stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		exited := make(chan struct{})
		go func() { cmd.Wait(); close(exited) }()
		stop = func() { cmd.Process.Kill(); <-exited }
		t.Cleanup(stop)
		api = apiAddrFromBanner(t, stdout.String, exited, stderr.String)
		endpoint = opcuaURL(t, stdout.String, exited, stderr.String)
		if !strings.Contains(stdout.String(), "tag API on https://") || !strings.Contains(stderr.String(), fp) {
			t.Errorf("naut run does not serve HTTPS with the certificate auth init told of (%s):\n%s\n%s", fp, stdout.String(), stderr.String())
		}
		return api, endpoint, stop
	}
	api, endpoint, stop := start()
	leaf := leafOf(t, filepath.Join(state, "nautilus", "plant", "tls", "self-signed.pem"))
	if certFingerprint(leaf.Raw) != fp {
		t.Fatal("the state directory's certificate is not the one auth init told of")
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	base := "https://" + api
	me := func(header, value string) string {
		t.Helper()
		req, _ := http.NewRequest("GET", base+"/api/me", nil)
		if header != "" {
			req.Header.Set(header, value)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		var v struct {
			Principal string `json:"principal"`
		}
		json.Unmarshal(b, &v)
		return v.Principal
	}
	// alice signs in over HTTPS (the browser's route, as JSON).
	req, _ := http.NewRequest("POST", base+"/api/signin", strings.NewReader(`{"name":"alice","password":"correct-horse-battery"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", base)
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	var cookie string
	for _, c := range res.Cookies() {
		if c.Name == "__Host-nautilus_session" {
			cookie = c.Name + "=" + c.Value
		}
	}
	if res.StatusCode != http.StatusOK || cookie == "" {
		t.Fatalf("alice's sign-in over HTTPS: %d, cookies %v", res.StatusCode, res.Cookies())
	}
	if got := me("Cookie", cookie); got != "user:alice" {
		t.Errorf("signed in, /api/me says %q", got)
	}
	if got := me("Authorization", "Bearer "+pat); got != "user:alice" {
		t.Errorf("with the token, /api/me says %q", got)
	}
	if got := me("", ""); got == "user:alice" {
		t.Error("no credential reads as alice")
	}

	// The SCADA client connects with its certificate and is refused.
	der, key := opcuaClientCert(t, "urn:test:scada")
	tp := opcuaserver.Thumbprint(der)
	if _, err := connectSecure(t, endpoint, der, key); err == nil {
		t.Fatal("an untrusted certificate connected")
	}
	stop()
	// 5. naut opcua certs lists it. 6. trust --as maps it.
	if out, _ := naut("", "opcua", "certs"); !strings.Contains(out, tp) {
		t.Fatalf("naut opcua certs does not list the refused client %s:\n%s", tp, out)
	}
	if out, _ := naut("", "opcua", "trust", tp[:16], "--as", "scada:operator"); !strings.Contains(out, restartNote) {
		t.Errorf("trust --as:\n%s", out)
	}
	// No hand edit: the manifest is what naut new wrote (plus the
	// prerequisite), with lines inserted and nothing else changed.
	after, _ := os.ReadFile(manifest)
	if left := removeLines(string(after), string(before)); left != "" {
		t.Errorf("the commands changed more than they inserted (left %q):\n%s", left, after)
	}
	// The restart: the mapped certificate writes as opcua-cert:scada.
	_, endpoint, _ = start()
	c, err := connectSecure(t, endpoint, der, key)
	if err != nil {
		t.Fatalf("the trusted certificate: %v", err)
	}
	wr, err := c.Write(context.Background(), &ua.WriteRequest{NodesToWrite: []*ua.WriteValue{{
		NodeID: ua.NewStringNodeID(1, "TempSP"), AttributeID: ua.AttributeIDValue,
		Value: &ua.DataValue{EncodingMask: ua.DataValueValue, Value: ua.MustVariant(71.5)},
	}}})
	if err != nil || wr.Results[0] != ua.StatusGood {
		t.Errorf("the mapped certificate's write: %v %v", err, wr)
	}
}

// connectSecure opens a Basic256Sha256 SignAndEncrypt session with the
// certificate, anonymous.
func connectSecure(t *testing.T, endpoint string, der []byte, key *rsa.PrivateKey) (*opcua.Client, error) {
	t.Helper()
	ctx := context.Background()
	eps, err := opcua.GetEndpoints(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	var ep *ua.EndpointDescription
	for _, e := range eps {
		if e.SecurityPolicyURI == ua.SecurityPolicyURIBasic256Sha256 && e.SecurityMode == ua.MessageSecurityModeSignAndEncrypt {
			ep = e
		}
	}
	if ep == nil {
		t.Fatal("no Basic256Sha256 SignAndEncrypt endpoint")
	}
	c, err := opcua.NewClient(endpoint, opcua.AuthAnonymous(), opcua.SecurityFromEndpoint(ep, ua.UserTokenTypeAnonymous),
		opcua.Certificate(der), opcua.PrivateKey(key), opcua.ApplicationURI("urn:test:scada"),
		opcua.AutoReconnect(false), opcua.DialTimeout(5*time.Second), opcua.RequestTimeout(10*time.Second))
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
