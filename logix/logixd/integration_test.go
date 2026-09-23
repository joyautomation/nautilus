package logixd_test

// Integration tests against a live logixd agent.
//
//	NAUTILUS_LOGIXD_URL=http://host:8188 \
//	NAUTILUS_LOGIXD_TOKEN=... \
//	go test ./logix/logixd/ -run TestAgent -v
//
// They are in two tiers, and the split is the point.
//
// TIER 1 needs no Rockwell licence at all: the agent's own contract —
// authentication, the file sandbox, error classification, the shape of the
// probe report. These run anywhere logixd runs, which means they run in CI
// on a machine whose activation has lapsed, and they are what catches a
// regression in the agent itself.
//
// TIER 2 needs a usable SDK, so each test asks the agent's own probe first
// and SKIPS with the gate that failed. That is deliberate: an unlicensed
// machine must report "skipped: no activation", never a red build that
// looks like broken code. The moment an activation lands, they run with no
// edit.
//
// Tier 2 tests that touch a CONTROLLER additionally need
// NAUTILUS_LOGIXD_COMM_PATH, and they stay read-mostly: the one write is an
// export-then-import of a routine's own rungs, which is a semantic no-op.
// Nothing here downloads.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/logix/logixd"
)

func agent(t *testing.T) *logixd.Client {
	t.Helper()
	url := os.Getenv("NAUTILUS_LOGIXD_URL")
	if url == "" {
		t.Skip("set NAUTILUS_LOGIXD_URL to run integration tests against a logixd agent")
	}
	return logixd.New(url, os.Getenv("NAUTILUS_LOGIXD_TOKEN"))
}

func ctx(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return c
}

// requireSDK skips unless the agent reports the SDK usable, naming the gate
// that failed so an unlicensed run says why rather than just "skip".
func requireSDK(t *testing.T, c *logixd.Client) {
	t.Helper()
	p, err := c.Probe(ctx(t, 10*time.Minute))
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if p.Usable {
		return
	}
	var failed []string
	for _, g := range p.Gates {
		if !g.OK {
			failed = append(failed, fmt.Sprintf("%s (%s)", g.Name, g.Detail))
		}
	}
	t.Skipf("SDK not usable on the agent; failing gates: %s", strings.Join(failed, "; "))
}

// seedProject uploads a real .ACD into the agent's work directory and
// returns its agent-relative path.
//
// The tests used to build their subject with CreateNewProject. They do not
// any more: that call is intermittent on the reference host — it fails in
// the SDK's OWN shipped example, on a freshly restarted service, between
// two successful OpenLogixProject calls. Seeding from a real project is
// both more reliable and a better test, because an empty controller
// exercises almost nothing.
func seedProject(t *testing.T, c *logixd.Client) string {
	t.Helper()
	local := os.Getenv("NAUTILUS_LOGIXD_SEED_ACD")
	if local == "" {
		t.Skip("set NAUTILUS_LOGIXD_SEED_ACD to a .ACD to seed SDK tests from")
	}
	raw, err := os.ReadFile(local)
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}
	rel := path.Join(runID(t), filepath.Base(local))
	if err := c.PutFile(ctx(t, 10*time.Minute), rel, raw); err != nil {
		t.Fatalf("uploading the seed project: %v", err)
	}
	return rel
}

// runID namespaces a test's files inside the agent's work directory.
func runID(t *testing.T) string {
	return "it-" + strings.ReplaceAll(t.Name(), "/", "-") + "-" +
		time.Now().UTC().Format("150405.000")
}

// --- tier 1: the agent's own contract -------------------------------------

func TestAgentHealth(t *testing.T) {
	c := agent(t)
	h, err := c.Health(ctx(t, 30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if h.Service != "logixd" {
		t.Errorf("service = %q", h.Service)
	}
	if h.SDKClient == "" {
		t.Error("the agent should report which SDK client it is linked against")
	}
	t.Logf("logixd %s, SDK client %s, %d sessions", h.Version, h.SDKClient, h.Sessions)
}

// An agent that can stop a controller must not answer unauthenticated
// callers. This is the one test that fails CLOSED matters most.
func TestAgentRejectsMissingToken(t *testing.T) {
	c := agent(t)
	if c.Token == "" {
		t.Skip("agent is running without a token (loopback bind); nothing to reject")
	}
	anon := logixd.New(c.BaseURL, "none")
	_, err := anon.Health(ctx(t, 30*time.Second))
	if err == nil {
		t.Fatal("a bad token was accepted")
	}
	var e *logixd.Error
	if !errors.As(err, &e) || e.Status != http.StatusUnauthorized {
		t.Fatalf("want 401, got %v", err)
	}
}

func TestAgentFileRoundTrip(t *testing.T) {
	c := agent(t)
	cx := ctx(t, 2*time.Minute)
	rel := path.Join(runID(t), "hello.L5X")
	body := []byte("<?xml version=\"1.0\"?>\n<RSLogix5000Content/>\n")

	if err := c.PutFile(cx, rel, body); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.DeleteFile(context.Background(), rel) })

	got, err := c.GetFile(cx, rel)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) {
		t.Errorf("round trip changed the bytes:\n got %q\nwant %q", got, body)
	}

	files, err := c.ListFiles(cx)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, f := range files {
		if f.Path == rel {
			found = true
			if f.Bytes != int64(len(body)) {
				t.Errorf("listed size = %d, want %d", f.Bytes, len(body))
			}
		}
	}
	if !found {
		t.Errorf("%s missing from the listing", rel)
	}

	if err := c.DeleteFile(cx, rel); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetFile(cx, rel); logixd.Kind(err) != "not_found" {
		t.Errorf("after delete, want not_found, got %v", err)
	}
}

// The file sandbox is the agent's security boundary: it will fetch and
// write files on a caller's behalf, so "any path you name" would make it a
// file server with a controller attached.
func TestAgentRefusesToEscapeItsWorkDirectory(t *testing.T) {
	c := agent(t)
	cx := ctx(t, 60*time.Second)
	for _, bad := range []string{
		"../outside.txt",
		"a/../../outside.txt",
		`..\..\Windows\System32\drivers\etc\hosts`,
		"C:/Windows/System32/drivers/etc/hosts",
	} {
		if err := c.PutFile(cx, bad, []byte("nope")); err == nil {
			t.Errorf("PutFile(%q) was allowed", bad)
		}
		if _, err := c.GetFile(cx, bad); err == nil {
			t.Errorf("GetFile(%q) was allowed", bad)
		}
	}
}

// The probe must always produce a usable report — that is its whole job.
// Even (especially) when the answer is "not usable", the caller needs the
// individual gates, because whichever fails first masks the others.
func TestAgentProbeReportsGates(t *testing.T) {
	c := agent(t)
	// Reporting "unusable" is a successful probe: the call must not fail.
	p, err := c.Probe(ctx(t, 10*time.Minute))
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if len(p.Gates) == 0 {
		t.Fatal("a probe with no gates tells an operator nothing")
	}
	for _, g := range p.Gates {
		status := "ok"
		if !g.OK {
			status = "FAIL"
		}
		t.Logf("  %-4s %-22s %s", status, g.Name, g.Detail)
	}
	if !p.Usable && p.Hint == "" {
		t.Error("an unusable SDK must come with the hint that says how to diagnose it")
	}
}

// A request the agent can refuse should come back classified, not as a
// generic 500 — the caller acts on the kind.
func TestAgentClassifiesABadRequest(t *testing.T) {
	c := agent(t)
	_, err := c.Open(ctx(t, 60*time.Second), "no/such/project.ACD")
	if err == nil {
		t.Fatal("opening a nonexistent project should fail")
	}
	if k := logixd.Kind(err); k == "" || k == "bad_reply" {
		t.Errorf("unclassified error: %v", err)
	}
	t.Logf("kind=%s fatal=%v: %v", logixd.Kind(err), logixd.IsFatal(err), err)
}

// Comm-path discovery needs no licence — it reads what FactoryTalk Linx has
// already browsed. An empty result is legitimate (nobody has browsed to a
// controller on this machine); a parse failure is not.
func TestAgentCommPaths(t *testing.T) {
	c := agent(t)
	paths, err := c.CommPaths(ctx(t, 2*time.Minute))
	if err != nil {
		t.Fatalf("comm paths: %v", err)
	}
	if len(paths) == 0 {
		t.Skip("FactoryTalk Linx has browsed no controllers on this agent")
	}
	for _, p := range paths {
		t.Logf("  %-40s %s %s", p.Path, p.Controller, p.Catalog)
		if p.Controller == "" || p.Driver == "" {
			t.Errorf("incomplete comm path: %+v", p)
		}
		// The path must have the shape SetCommunicationsPath accepts, or
		// it is worse than useless — it looks right and fails as "cannot
		// go online".
		if n := len(strings.Split(p.Path, `\`)); n < 4 {
			t.Errorf("comm path %q has %d segments, want at least 4", p.Path, n)
		}
	}
}

// --- tier 2: the SDK ------------------------------------------------------

func TestSDKConvertAndInspect(t *testing.T) {
	c := agent(t)
	requireSDK(t, c)
	cx := ctx(t, 30*time.Minute)

	acd := seedProject(t, c)
	l5x := acd + ".L5X"
	back := acd + ".roundtrip.ACD"

	// ACD -> L5X, the git-native half.
	res, _, err := c.Convert(cx, acd, l5x, false)
	if err != nil {
		t.Fatalf("convert to L5X: %v", err)
	}
	if res.Bytes == 0 {
		t.Fatal("empty L5X")
	}
	raw, err := c.GetFile(cx, l5x)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "<RSLogix5000Content") {
		t.Fatalf("not an L5X: %.200s", raw)
	}
	t.Logf("ACD -> L5X: %d bytes", res.Bytes)

	// L5X -> ACD, the other direction. Open accepts all three formats, so
	// the round trip is two calls and no GUI.
	if _, _, err := c.Convert(cx, l5x, back, false); err != nil {
		t.Fatalf("convert back to ACD: %v", err)
	}

	s, err := c.Open(cx, acd)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	execs, err := s.Executables(cx)
	if err != nil {
		t.Fatalf("executables: %v", err)
	}
	if len(execs) == 0 {
		t.Error("a real project should have at least one routine")
	}
	t.Logf("%d executables, e.g. %v", len(execs), first(execs, 3))
}

// Build is the CI gate: it compiles the control logic, needs no controller,
// and carries no risk. v37+ only.
//
// It builds a project it creates itself rather than the seed. That is not
// convenience — a real project can carry audit settings that make Build
// fail with RxCMP_E_AUDIT_INVALIDOPTYPE (the committed DemoLine fixture
// does), and it is also the shape CI actually uses: start from a known
// project, import generated logic, compile.
func TestSDKBuild(t *testing.T) {
	c := agent(t)
	requireSDK(t, c)
	cx := ctx(t, 45*time.Minute)

	acd := path.Join(runID(t), "build.ACD")
	if _, err := c.CreateProject(cx, acd, 38, "1756-L85E", "BuildSmoke"); err != nil {
		t.Fatalf("create: %v", err)
	}
	s, err := c.Open(cx, acd)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())

	res, evs, err := s.Build(cx, logixd.BuildDefault)
	for _, e := range evs {
		if e.Kind == "error" {
			t.Logf("build event: %s", e)
		}
	}
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	t.Logf("built for %s in %dms", res.Target, res.ElapsedMs)
}

// A partial export is the read half of every structural operation, and the
// only way to learn a project's contents — the SDK has no browse.
func TestSDKPartialExport(t *testing.T) {
	c := agent(t)
	requireSDK(t, c)
	cx := ctx(t, 30*time.Minute)

	acd := seedProject(t, c)
	s, err := c.Open(cx, acd)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())

	// Ask the project what it contains rather than assuming. A seed project
	// may keep its tags in a program rather than on the controller — the
	// committed DemoLine fixture does — so a hard-coded
	// "Controller/Tags/Tag" is an export of something that does not exist.
	execs, err := s.Executables(cx)
	if err != nil {
		t.Fatalf("executables: %v", err)
	}
	if len(execs) == 0 {
		t.Skip("seed project declares no executables to export")
	}
	out := acd + ".routine.L5X"
	if _, err := s.PartialExport(cx, execs[0], out); err != nil {
		t.Fatalf("partial export of %s: %v", execs[0], err)
	}
	raw, err := c.GetFile(cx, out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "<RSLogix5000Content") {
		t.Errorf("export is not an L5X: %.300s", raw)
	}
	t.Logf("exported %s -> %d bytes", execs[0], len(raw))
}

// UploadToNewProject is the read half of drift detection, and its argument
// order is a trap: projectFilePath comes FIRST, commPath second. Reversed,
// the SDK reports "Invalid file extension" naming the comm path — a good
// error that still shipped unnoticed until this ran against real hardware.
func TestSDKUploadFromController(t *testing.T) {
	c := agent(t)
	requireSDK(t, c)
	commPath := os.Getenv("NAUTILUS_LOGIXD_COMM_PATH")
	if commPath == "" {
		t.Skip("set NAUTILUS_LOGIXD_COMM_PATH to upload from a controller")
	}
	cx := ctx(t, 45*time.Minute)

	id := runID(t)
	acd := path.Join(id, "uploaded.ACD")
	l5x := path.Join(id, "uploaded.L5X")

	if _, err := c.UploadToNew(cx, commPath, acd); err != nil {
		t.Fatalf("upload from %s: %v", commPath, err)
	}
	// Rendering it as L5X is what makes it comparable with the repo, and
	// is the rest of what `naut logix drift` does.
	res, _, err := c.Convert(cx, acd, l5x, false)
	if err != nil {
		t.Fatalf("convert the upload: %v", err)
	}
	raw, err := c.GetFile(cx, l5x)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "<RSLogix5000Content") {
		t.Fatalf("upload did not render as an L5X: %.200s", raw)
	}
	t.Logf("uploaded the controller and rendered %d bytes of L5X", res.Bytes)
}

// --- tier 2, online: the correction in §9 of logix-sdk-api.md, tested -----

// TestSDKOnlineRungImport is the one that matters. It exports a routine's
// own rungs and imports them straight back with FinalizeEdits while the
// project is ONLINE — so the change is semantically a no-op, but the path
// exercised is the real online-edit cycle: accept the edits, send them to
// the controller, and assemble them if it is in Run.
//
// The design brief said for weeks that the SDK had no online-edit API. It
// does. This is the test that settles it.
func TestSDKOnlineRungImport(t *testing.T) {
	c := agent(t)
	requireSDK(t, c)
	commPath := os.Getenv("NAUTILUS_LOGIXD_COMM_PATH")
	if commPath == "" {
		t.Skip("set NAUTILUS_LOGIXD_COMM_PATH to the controller to exercise the online path")
	}
	program := envOr("NAUTILUS_LOGIXD_PROGRAM", "MainProgram")
	routine := envOr("NAUTILUS_LOGIXD_ROUTINE", "MainRoutine")
	cx := ctx(t, 30*time.Minute)

	// The project comes from the CONTROLLER, not from disk. A project file
	// cannot go online even when its logic matches byte for byte: the
	// download stamps match information into the project, and that copy
	// lives wherever the download ran. Opening one and calling GoOnline
	// fails with RxCL_E_CANNOT_UPLOAD_PHYS_ADDR.
	//
	// This test used to require NAUTILUS_LOGIXD_PROJECT to be "an .ACD
	// already downloaded to that controller" -- a thing that cannot exist,
	// so the test skipped every time and the online path shipped uncovered.
	project := path.Join(runID(t), "controller.ACD")
	if _, err := c.UploadToNew(cx, commPath, project); err != nil {
		t.Fatalf("uploading the running project: %v", err)
	}

	s, err := c.Open(cx, project)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())

	if _, err := s.SetCommPath(cx, commPath); err != nil {
		t.Fatalf("comm path: %v", err)
	}
	st, err := s.GoOnline(cx)
	if err != nil {
		t.Fatalf("go online: %v", err)
	}
	t.Cleanup(func() { _, _ = s.GoOffline(context.Background()) })
	mode, err := s.Mode(cx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("online: connection=%s controller=%s", st.Connected, mode)

	// Export rung 0 of the routine, then put it back exactly where it was.
	rungFile := path.Join(runID(t), "rung0.L5X")
	xpath := logixd.RoutinePath(program, routine) +
		"/RLLContent/Rung[@Number='0']"
	if _, err := s.PartialExport(cx, xpath, rungFile); err != nil {
		t.Fatalf("exporting rung 0 (does %s/%s exist and have a rung 0?): %v", program, routine, err)
	}
	exported, err := c.GetFile(cx, rungFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(exported), "<Rung") {
		t.Fatalf("export is not a rung: %.300s", exported)
	}

	res, evs, err := s.ImportRungs(cx, logixd.RoutinePath(program, routine),
		0, 1, rungFile, logixd.FinalizeEdits)
	for _, e := range evs {
		t.Logf("  %s", e)
	}
	if err != nil {
		t.Fatalf("ONLINE rung import with FinalizeEdits: %v", err)
	}
	t.Logf("online rung import ok — %s in %dms", res.OnlineOption, res.ElapsedMs)

	after, err := s.Mode(cx)
	if err != nil {
		t.Fatal(err)
	}
	if after != mode {
		t.Errorf("an online edit changed the controller mode: %s -> %s", mode, after)
	}
}

// --- helpers --------------------------------------------------------------

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func first(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// controllerTagL5X builds the smallest importable L5X that declares one
// controller-scoped tag. The SDK has no "create a tag" call — every
// structural change is an L5X you construct and import — so this is the
// normal way to get anything into a project.
func controllerTagL5X(name, dataType, value string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<RSLogix5000Content SchemaRevision="1.0" TargetName="%[1]s" TargetType="Tag" ContainsContext="true" ExportOptions="References NoRawData L5KData DecoratedData Context">
<Controller Use="Context" Name="Smoke">
<Tags Use="Context">
<Tag Use="Target" Name="%[1]s" TagType="Base" DataType="%[2]s" Radix="Float" Constant="false" ExternalAccess="Read/Write">
<Data Format="Decorated">
<DataValue DataType="%[2]s" Radix="Float" Value="%[3]s"/>
</Data>
</Tag>
</Tags>
</Controller>
</RSLogix5000Content>
`, name, dataType, value)
}
