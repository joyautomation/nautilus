package tiadtest

import (
	"testing"

	"github.com/joyautomation/nautilus/siemens/tiad"
)

// contractWebServer: tiad turns the CPU's web server on only over access
// control EnabledWithAccessControl (#789 stage 5 (A) step 2). With access
// control off, a FW 3.1 CPU's web server lets anyone in as "Anonymous"
// with full rights, so an apply that would turn it on over a kept CPU
// whose access control is anything else is refused before any write.
// Turning it off, or leaving it out (as found), is never refused.
func contractWebServer(t *testing.T, start Starter) {
	t.Run("NewCPU", func(t *testing.T) {
		// A CPU tiad makes has access control on: the server comes on,
		// and the apply says so.
		a := start(t, Config{Targets: []string{benchCPU}})
		c := a.Client()
		installGSD(t, c)
		s, err := c.CreateProject(ctx, "web", BenchStation(), BenchSecrets)
		if err != nil {
			t.Fatal(err)
		}
		r, err := s.ApplyStation(ctx, BenchStation(), BenchSecrets)
		if err != nil || !noteWith(r.Notes, WebOnNote) {
			t.Fatalf("the web server on a CPU tiad made: %+v %v", r, err)
		}
		off := BenchStation()
		off.WebServer = &tiad.WebServer{Enabled: false}
		if r, err := s.ApplyStation(ctx, off, BenchSecrets); err != nil || noteWith(r.Notes, WebOnNote) {
			t.Fatalf("the web server off: %+v %v", r, err)
		}
	})

	for _, state := range []string{AccessDisabled, "EnabledWithAccessControlViaAccessLevel"} {
		t.Run("Kept"+state, func(t *testing.T) {
			a := start(t, Config{Targets: []string{benchCPU}})
			if state != AccessDisabled {
				a.SetAccess(t, AccessScript{UploadAccessControl: state})
			}
			c := a.Client()
			installGSD(t, c)
			s, err := c.CreateProject(ctx, "web", BenchStation(), BenchSecrets)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.UploadWith(ctx, tiad.UploadRequest{Secrets: BenchLogin}); err != nil {
				t.Fatal(err)
			}
			on := adoptedStation()
			on.WebServer = &tiad.WebServer{Enabled: true}
			_, err = s.ApplyStation(ctx, on, BenchSecrets)
			if want := webRefusal(state); tiad.Kind(err) != tiad.KindBadRequest || err.Error() != "tiad: bad_request: "+want {
				t.Fatalf("the web server on over access control %s: %v, want %q", state, err, want)
			}
			// Refused before any write: the session is as it was, and
			// leaving the server out or turning it off is never refused.
			for _, w := range []*tiad.WebServer{nil, {Enabled: false}} {
				st := adoptedStation()
				st.WebServer = w
				if r, err := s.ApplyStation(ctx, st, BenchSecrets); err != nil || noteWith(r.Notes, WebOnNote) {
					t.Fatalf("webServer %+v over access control %s: %+v %v", w, state, r, err)
				}
			}
			if _, err := s.Compile(ctx, true, true); err != nil {
				t.Fatalf("the adopted CPU compiles after the refusal: %v", err)
			}
		})
	}

	t.Run("KeptEnabled", func(t *testing.T) {
		a := start(t, Config{Targets: []string{benchCPU}})
		a.SetAccess(t, AccessScript{UploadAccessControl: AccessEnabled})
		c := a.Client()
		installGSD(t, c)
		s, err := c.CreateProject(ctx, "web", BenchStation(), BenchSecrets)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.UploadWith(ctx, tiad.UploadRequest{Secrets: BenchLogin}); err != nil {
			t.Fatal(err)
		}
		on := adoptedStation()
		on.WebServer = &tiad.WebServer{Enabled: true}
		if r, err := s.ApplyStation(ctx, on, BenchSecrets); err != nil || !noteWith(r.Notes, WebOnNote) {
			t.Fatalf("the web server on over a kept CPU's access control on: %+v %v", r, err)
		}
	})
}
