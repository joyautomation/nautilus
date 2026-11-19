package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/internal/project"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/opcua"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/virtdev"
)

// securedProject writes an OPC UA device row that connects through a
// driver: section, Basic256Sha256 SignAndEncrypt with its own pki-dir,
// at endpoint.
func securedProject(t *testing.T, dir, endpoint string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, project.ManifestName), []byte(`name: iolm
tasks: [{ program: main.st, scan: 20ms }]
drivers:
  - { name: iolm-secure, type: opcua-client, security: Basic256Sha256, security-mode: SignAndEncrypt, pki-dir: "`+filepath.Join(dir, "client-pki")+`" }
devices:
  - { name: PF1, profile: pepperl-fuchs-ice3, endpoint: "`+endpoint+`", points: [Port1_PdiU32, Port1_PdoU32], driver: iolm-secure }
tags:
  - { name: Seen, role: state, type: UDINT }
  - { name: Up,   role: state, type: BOOL }
  - { name: Cmd,  role: setpoint, type: UDINT, init: 0 }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.st"), []byte(`PROGRAM Main
Seen := PF1.Port1_PdiU32;
Up := PF1__Online;
PF1_Port1_PdoU32 := Cmd;
END_PROGRAM
`), 0o644); err != nil {
		t.Fatal(err)
	}
}

// An OPC UA row that names a driver: section connects as the section
// says (#975): against a virtual device offering the secure endpoints, the
// project's driver opens a Basic256Sha256 SignAndEncrypt session, pins the
// server, reads the device's struct and writes a command; and naut check
// says nothing about security None.
func TestDevicesOPCUARowThroughASecuredSection(t *testing.T) {
	dir := t.TempDir()
	securedProject(t, dir, "10.0.0.21")
	if code := runDevicesAdd([]string{"pepperl-fuchs-ice3", dir}, io.Discard); code != 0 {
		t.Fatalf("naut devices add exited %d", code)
	}
	if err := os.WriteFile(filepath.Join(dir, virtdev.FileName), []byte("devices:\n  - { name: PF1, opcua: { pki: server-pki } }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var code int
	out := captureStdout(t, func() int { code = runCheck([]string{dir}); return code })
	if code != 0 {
		t.Fatalf("naut check exited %d:\n%s", code, out)
	}
	if strings.Contains(out, "security None") || strings.Contains(out, "pki-dir") {
		t.Errorf("naut check warns of the security of a row that names a secured section:\n%s", out)
	}
	var b strings.Builder
	if code := runDevicesRender([]string{dir}, &b); code != 0 || !strings.Contains(b.String(), "security Basic256Sha256 SignAndEncrypt from drivers: section iolm-secure") {
		t.Errorf("naut devices render does not name the section's security (exit %d):\n%s", code, b.String())
	}

	// The device trusts the project's client certificate, as an operator
	// does on a master configured secure.
	cert, _, err := opcua.EnsureCertificate(filepath.Join(dir, "client-pki"), opcua.DefaultApplicationURI)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(cert)
	if err != nil {
		t.Fatal(err)
	}
	trusted := filepath.Join(dir, "server-pki", "trusted", "certs")
	if err := os.MkdirAll(trusted, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(trusted, "naut.pem"), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	set, err := loadVirtualDevices(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	run, err := virtdev.Start(ctx, set, virtdev.StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer run.Stop()
	if err := run.Runtime.Tags().SetPath("PF1_Port1_PdiU32", int64(4242)); err != nil {
		t.Fatal(err)
	}

	securedProject(t, dir, run.Endpoints[0].Listen)
	proj, err := project.Load(os.DirFS(dir), "")
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	drv := proj.Runtime.Driver
	drv.(interface{ Start(context.Context) }).Start(ctx)
	done := make(chan struct{})
	rctx, stop := context.WithCancel(ctx)
	go func() { rt.Run(rctx); close(done) }()
	defer func() {
		stop()
		<-done
		drv.(interface{ Stop() }).Stop()
	}()
	waitFor(t, "the program reads the device over the secured session", func() bool {
		v, _ := rt.Tags().ReadPath("Seen")
		iv, isIR := v.(ir.Value)
		return rt.Tags().Bool("Up") && ((isIR && iv.I == 4242) || v == int64(4242))
	})
	od := drv.(interface{ Unwrap() nio.Driver }).Unwrap().(*opcua.Driver)
	if h := od.Health(); h.Policy != "Basic256Sha256" || h.Mode != "SignAndEncrypt" {
		t.Errorf("session %s/%s, want the section's Basic256Sha256/SignAndEncrypt", h.Policy, h.Mode)
	}
	if _, err := os.Stat(opcua.PinFile(filepath.Join(dir, "client-pki"), od.Endpoint())); err != nil {
		t.Errorf("the server was not pinned: %v", err)
	}
	if err := rt.Tags().SetPath("Cmd", int64(777)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the command reaches the device over the secured session", func() bool {
		v, ok := run.Runtime.Tags().ReadPath("PF1_Port1_PdoU32")
		iv, isIR := v.(ir.Value)
		return ok && ((isIR && iv.I == 777) || v == int64(777))
	})
}

// A secured row refuses what a secured session must: a device that does
// not trust the project's client certificate opens no session (it parks
// the certificate for an operator to approve), and a server whose
// certificate is not the one pinned on first use is refused by the client.
func TestDevicesOPCUASecuredSectionRefusals(t *testing.T) {
	for _, tc := range []struct {
		name        string
		trust, pin  bool
		lastError   string
		parkedOnDev bool
	}{
		{name: "an untrusted client certificate", parkedOnDev: true},
		{name: "a server other than the pinned one", trust: true, pin: true, lastError: "server certificate changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			client := filepath.Join(dir, "client-pki")
			securedProject(t, dir, "10.0.0.21")
			if code := runDevicesAdd([]string{"pepperl-fuchs-ice3", dir}, io.Discard); code != 0 {
				t.Fatalf("naut devices add exited %d", code)
			}
			if err := os.WriteFile(filepath.Join(dir, virtdev.FileName), []byte("devices:\n  - { name: PF1, opcua: { pki: server-pki } }\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if tc.trust {
				cert, _, err := opcua.EnsureCertificate(client, opcua.DefaultApplicationURI)
				if err != nil {
					t.Fatal(err)
				}
				raw, _ := os.ReadFile(cert)
				trusted := filepath.Join(dir, "server-pki", "trusted", "certs")
				if err := os.MkdirAll(trusted, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(trusted, "naut.pem"), raw, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			set, err := loadVirtualDevices(dir, "")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			run, err := virtdev.Start(ctx, set, virtdev.StartOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer run.Stop()
			listen := run.Endpoints[0].Listen
			securedProject(t, dir, listen)
			if tc.pin {
				pin := opcua.PinFile(client, listen)
				if err := os.MkdirAll(filepath.Dir(pin), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(pin, []byte(strings.Repeat("0", 64)+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			proj, err := project.Load(os.DirFS(dir), "")
			if err != nil {
				t.Fatal(err)
			}
			drv := proj.Runtime.Driver
			drv.(interface{ Start(context.Context) }).Start(ctx)
			defer drv.(interface{ Stop() }).Stop()
			od := drv.(interface{ Unwrap() nio.Driver }).Unwrap().(*opcua.Driver)
			waitFor(t, "the session is refused", func() bool {
				h := od.Health()
				return h.LastError != "" && strings.Contains(h.LastError, tc.lastError)
			})
			if tc.parkedOnDev {
				waitFor(t, "the device parks the certificate for approval", func() bool {
					ents, _ := os.ReadDir(filepath.Join(dir, "server-pki", "rejected", "certs"))
					return len(ents) > 0
				})
			}
			if od.Online() || od.Health().State == "connected" {
				t.Errorf("a refused session is up: %+v", od.Health())
			}
			if vals, err := drv.ReadInputs(); err == nil && vals["PF1__Online"] == true {
				t.Error("PF1__Online is true over a refused session")
			}
		})
	}
}
