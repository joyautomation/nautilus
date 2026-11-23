package tiadtest

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/siemens/tiad"
)

// webStation is the bench station with the web server on and users.
func webStation(users ...tiad.WebUser) tiad.Station {
	st := BenchStation()
	st.WebServer = &tiad.WebServer{Enabled: true, Users: users}
	return st
}

func webSecrets(namePw ...string) tiad.Secrets {
	sec := BenchSecrets
	sec.WebUsers = map[string]string{}
	for i := 0; i+1 < len(namePw); i += 2 {
		sec.WebUsers[namePw[i]] = namePw[i+1]
	}
	return sec
}

var (
	webNaut = tiad.WebUser{Name: "naut", Rights: []string{"read", "write", "mode"}}
	webOp   = tiad.WebUser{Name: "op", Rights: []string{"read"}}
)

// contractWebUsers: web users are the project's local users, each with a
// role of its own holding exactly its rights; their passwords are settled
// before any write; stale ones go by tiad's marker only; an upload
// removes the ones its session made; what tiad did not make, and an
// active anonymous user, refuse before any write (#789 stage 5 (A) step
// 2B).
func contractWebUsers(t *testing.T, start Starter) {
	t.Run("MadeFoundRemoved", func(t *testing.T) {
		a := start(t, Config{Targets: []string{benchCPU}})
		a.SetAccess(t, AccessScript{UploadAccessControl: AccessEnabled})
		c := a.Client()
		installGSD(t, c)
		sec := webSecrets("naut", BenchWebPassword, "op", "Op-web-2026")
		s, err := c.CreateProject(ctx, "web", webStation(webNaut), sec)
		if err != nil {
			t.Fatal(err)
		}
		r, err := s.ApplyStation(ctx, webStation(webNaut, webOp), sec)
		if err != nil || !noteWith(r.Notes, WebFoundNote(webNaut)) || !noteWith(r.Notes, WebUnchangedNote("naut")) || !noteWith(r.Notes, WebMadeNote(webOp)) {
			t.Fatalf("a found and a new web user: %+v %v", r, err)
		}
		if want := `made the web user "op" (role "nautilus web op", runtime rights WebReadTags), its password from secrets.webUsers["op"]`; !noteWith(r.Notes, want) {
			t.Fatalf("the made note names role and rights: %q", r.Notes)
		}
		// op dropped: its role and user go; naut stays.
		r, err = s.ApplyStation(ctx, webStation(webNaut), sec)
		if err != nil || !noteWith(r.Notes, WebRemovedNote("op", true)) || noteWith(r.Notes, WebRemovedNote("naut", true)) {
			t.Fatalf("a web user dropped: %+v %v", r, err)
		}
		if _, err := s.Compile(ctx, true, true); err != nil {
			t.Fatalf("compiles with its web users: %v", err)
		}
		// Off: every tiad web user goes; nautilus stays.
		off := BenchStation()
		off.WebServer = &tiad.WebServer{Enabled: false}
		r, err = s.ApplyStation(ctx, off, sec)
		if err != nil || !noteWith(r.Notes, WebRemovedNote("naut", true)) {
			t.Fatalf("the web server off: %+v %v", r, err)
		}
		if r, err := s.ApplyStation(ctx, BenchStation(), sec); err != nil || !noteWith(r.Notes, WebMadeNote(webNaut)) || !noteWith(r.Notes, UnchangedUserNote) {
			t.Fatalf("on again: %+v %v", r, err)
		}
		// The session made naut (twice) and op: an upload removes what is
		// left of it, and nautilus's user and role.
		up, err := s.UploadWith(ctx, tiad.UploadRequest{Secrets: BenchLogin})
		want := []string{`user "nautilus"`, `role "nautilus full access"`, `user "naut"`, `role "nautilus web naut"`}
		if err != nil || !reflect.DeepEqual(up.RemovedAccess, want) {
			t.Fatalf("an upload: %+v %v, want removedAccess %q", up, err, want)
		}
	})

	t.Run("Passwords", func(t *testing.T) {
		a := start(t, Config{Targets: []string{benchCPU}})
		c := a.Client()
		installGSD(t, c)
		both := webStation(webNaut, webOp)
		sec := webSecrets("naut", BenchWebPassword, "op", "Op-web-2026")
		s, err := c.CreateProject(ctx, "web", both, sec)
		if err != nil {
			t.Fatal(err)
		}
		rotated := webSecrets("naut", "Bench-web-2027", "op", "Op-web-2026")
		if r, err := s.ApplyStation(ctx, both, rotated); err != nil || !noteWith(r.Notes, WebSetNote("naut")) || !noteWith(r.Notes, WebUnchangedNote("op")) {
			t.Fatalf("a rotated web password: %+v %v", r, err)
		}
		if _, err := s.Compile(ctx, true, true); err != nil {
			t.Fatal(err)
		}
		// naut's set, then op's refused as one it had: a refusal, naut's
		// set and recorded, the project no longer compiled.
		if r, err := s.ApplyStation(ctx, both, webSecrets("naut", "Bench-web-2028", "op", "Op-web-2027")); err != nil || !noteWith(r.Notes, WebSetNote("op")) {
			t.Fatalf("both rotated: %+v %v", r, err)
		}
		if _, err := s.Compile(ctx, true, true); err != nil {
			t.Fatal(err)
		}
		_, err = s.ApplyStation(ctx, both, webSecrets("naut", "Bench-web-2029", "op", "Op-web-2026"))
		if tiad.Kind(err) != tiad.KindBadRequest || err.Error() != "tiad: bad_request: "+WebReused("op") {
			t.Fatalf("op back to one it had: %v, want %q", err, WebReused("op"))
		}
		if _, err := s.Download(ctx, tiad.DownloadRequest{Decisions: tiad.Decisions{ResetData: tiad.Yes()}, Secrets: BenchDownloadSecrets(), Address: benchCPU, Software: true, Announced: true}); tiad.Kind(err) != tiad.KindNotCompiled {
			t.Fatalf("a download after a partial password change: %v, want not_compiled", err)
		}
		if r, err := s.ApplyStation(ctx, both, webSecrets("naut", "Bench-web-2029", "op", "Op-web-2027")); err != nil || !noteWith(r.Notes, WebUnchangedNote("naut")) {
			t.Fatalf("naut's password was set before the refusal: %+v %v", r, err)
		}
	})

	t.Run("PasswordIsAChange", func(t *testing.T) {
		// A web password set with no station change is still a change the
		// CPU does not hold: compare says so after a download.
		a, s := compiledBench(t, start)
		a.SetQuestions(t, []tiad.Question{qStop, {ID: tiad.QuestionStartAfter, Message: "Start the CPU after loading"}})
		req := AcceptHardware(tiad.DownloadRequest{Address: benchCPU, Hardware: true, Software: true, Announced: true,
			Decisions: tiad.Decisions{StopCPU: tiad.Yes(), ResetData: tiad.Yes(), StartAfter: tiad.Yes()}, Secrets: BenchDownloadSecrets()})
		if res, err := s.Download(ctx, req); err != nil || !res.Downloaded {
			t.Fatalf("download: %+v %v", res, err)
		}
		if cmp, err := s.CompareWith(ctx, BenchCompare); err != nil || !cmp.Equal {
			t.Fatalf("compare after the download: %+v %v", cmp, err)
		}
		r, err := s.ApplyStation(ctx, BenchStation(), webSecrets("naut", "Bench-web-2027"))
		if err != nil || len(r.Changes) != 0 || !noteWith(r.Notes, WebSetNote("naut")) {
			t.Fatalf("a rotated web password: %+v %v", r, err)
		}
		if _, err := s.Compile(ctx, true, true); err != nil {
			t.Fatal(err)
		}
		if cmp, err := s.CompareWith(ctx, BenchCompare); err != nil || cmp.Equal {
			t.Fatalf("compare after a web password was set: %+v %v, want different", cmp, err)
		}
	})

	t.Run("RefusedBeforeAnyWrite", func(t *testing.T) {
		a := start(t, Config{Targets: []string{benchCPU}})
		c := a.Client()
		installGSD(t, c)
		s, err := c.CreateProject(ctx, "webrec", webStation(webNaut), BenchSecrets)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Save(ctx, false); err != nil {
			t.Fatal(err)
		}
		closeFirst(t, s)
		for _, k := range []struct{ records, want string }{
			{"strip-users", WebForeignUser("naut")},
			{"strip-roles", WebForeignRole("naut")},
		} {
			a.SetAccess(t, AccessScript{Records: k.records})
			s, err := c.Open(ctx, "projects/webrec/webrec.ap21")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.ApplyStation(ctx, webStation(webNaut), BenchSecrets); tiad.Kind(err) != tiad.KindBadRequest || err.Error() != "tiad: bad_request: "+k.want {
				t.Fatalf("%s: %v, want %q", k.records, err, k.want)
			}
			// Off never refuses, and never removes what tiad did not make.
			off := BenchStation()
			off.WebServer = &tiad.WebServer{Enabled: false}
			off.CPU.AccessControl = tiad.AccessKeep
			r, err := s.ApplyStation(ctx, off, BenchSecrets)
			if err != nil {
				t.Fatalf("%s, off: %v", k.records, err)
			}
			if k.records == "strip-roles" && noteWith(r.Notes, "removed") {
				t.Fatalf("a role tiad did not make was removed: %q", r.Notes)
			}
			if k.records == "strip-users" && (noteWith(r.Notes, `the web user "naut" and`) || !noteWith(r.Notes, WebRemovedNote("naut", false))) {
				t.Fatalf("a user without a record: its marked role goes, the user stays: %q", r.Notes)
			}
			closeFirst(t, s)
		}

		// Another agent key's record: a web user has no record-only route,
		// so its unchanged password is refused, and a new one is set.
		a.SetAccess(t, AccessScript{Records: "rekey"})
		s, err = c.Open(ctx, "projects/webrec/webrec.ap21")
		if err != nil {
			t.Fatal(err)
		}
		keep := webStation(webNaut)
		keep.CPU.AccessControl = tiad.AccessKeep
		if _, err := s.ApplyStation(ctx, keep, BenchSecrets); tiad.Kind(err) != tiad.KindBadRequest || err.Error() != "tiad: bad_request: "+WebReusedOtherKey("naut") {
			t.Fatalf("rekey, the unchanged web password: %v", err)
		}
		if r, err := s.ApplyStation(ctx, keep, webSecrets("naut", "Bench-web-2027")); err != nil || !noteWith(r.Notes, WebSetNote("naut")) {
			t.Fatalf("rekey, a new web password: %+v %v", r, err)
		}
		closeFirst(t, s)
		a.SetAccess(t, AccessScript{})

		// An active anonymous user: the server never comes on; left out,
		// a server found on says so, and plain HTTP too.
		a.SetAccess(t, AccessScript{UploadAccessControl: AccessEnabled, UploadWebServer: true, UploadAnonymous: true})
		s, err = c.CreateProject(ctx, "anon", BenchStation(), BenchSecrets)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.UploadWith(ctx, tiad.UploadRequest{Secrets: BenchLogin}); err != nil {
			t.Fatal(err)
		}
		on := adoptedStation()
		on.WebServer = &tiad.WebServer{Enabled: true, Users: []tiad.WebUser{webNaut}}
		if _, err := s.ApplyStation(ctx, on, BenchSecrets); tiad.Kind(err) != tiad.KindBadRequest || err.Error() != "tiad: bad_request: "+WebAnonymousActive {
			t.Fatalf("on over an active anonymous user: %v", err)
		}
		r, err := s.ApplyStation(ctx, adoptedStation(), BenchSecrets)
		if err != nil || !noteWith(r.Notes, WebPlainHTTPNote) || !noteWith(r.Notes, WebAnonymousUserNote) {
			t.Fatalf("left out over an active anonymous user and plain HTTP: %+v %v", r, err)
		}
	})

	t.Run("Checks", func(t *testing.T) {
		a := start(t, Config{Targets: []string{benchCPU}})
		c := a.Client()
		installGSD(t, c)
		for name, k := range map[string]struct {
			st   tiad.Station
			sec  tiad.Secrets
			want string
		}{
			"reserved": {webStation(tiad.WebUser{Name: "Nautilus", Rights: []string{"read"}}), webSecrets("Nautilus", "Some-web-2026"),
				`webServer.users[0].name: "Nautilus" is reserved`},
			"case twice": {webStation(webOp, tiad.WebUser{Name: "OP", Rights: []string{"read"}}), webSecrets("op", "Op-web-2026", "OP", "Op-web-2027"),
				`webServer.users: "OP" twice (names are compared ignoring case)`},
			"bad name": {webStation(tiad.WebUser{Name: "op 1", Rights: []string{"read"}}), webSecrets("op 1", "Op-web-2026"),
				`a web user name is 1 to 32 characters`},
			"weak": {webStation(webOp), webSecrets("op", "weakweak"),
				`secrets.webUsers["op"] breaks TIA's password rules`},
			"same as another": {webStation(webOp, webNaut), webSecrets("op", "Op-web-2026", "naut", "Op-web-2026"),
				`secrets.webUsers["naut"] is the same as secrets.webUsers["op"]: each password is its own secret`},
			"missing": {webStation(webOp), webSecrets(),
				`web user op has no password in secrets.webUsers`},
		} {
			if _, err := c.CreateProject(ctx, "chk", k.st, k.sec); tiad.Kind(err) != tiad.KindBadRequest || !strings.Contains(err.Error(), k.want) {
				t.Errorf("%s: %v, want %q", name, err, k.want)
			}
		}
		// The agents' own wording, past the client's check: exactly the
		// same words on both sides.
		for _, k := range []struct {
			st   tiad.Station
			sec  tiad.Secrets
			want string
		}{
			{webStation(tiad.WebUser{Name: "Anonymous", Rights: []string{"read"}}), webSecrets("Anonymous", "Some-web-2026"),
				`station: webServer.users[0].name: "Anonymous" is reserved (tiad's Full access user is "nautilus", TIA's anonymous user is "Anonymous")`},
			{webStation(webOp, webNaut), webSecrets("op", "Op-web-2026", "naut", "Op-web-2026"),
				`secrets.webUsers["naut"] is the same as secrets.webUsers["op"]: each password is its own secret`},
		} {
			status, b := rawJSON(t, c, "POST", "/v1/projects", map[string]any{"name": "raw", "station": k.st, "secrets": k.sec})
			if msg := envelopeMessage(b); status != http.StatusBadRequest || msg != k.want {
				t.Errorf("HTTP %d %q, want %q", status, msg, k.want)
			}
		}
		// The project's own policy, before any write.
		strict := PasswordPolicy{MinLength: 16, MinDigits: 1, UpperAndLower: true}
		a.SetAccess(t, AccessScript{Policy: &strict})
		sec := webSecrets("op", "Op-web-2026")
		sec.FullAccessPassword = "Bench-access-2026-long"
		if _, err := c.CreateProject(ctx, "strict", webStation(webOp), sec); tiad.Kind(err) != tiad.KindBadRequest ||
			!strings.Contains(err.Error(), `secrets.webUsers["op"] breaks the project's password policy for local users`) {
			t.Fatalf("a web password the project's policy refuses: %v", err)
		}
	})
}
