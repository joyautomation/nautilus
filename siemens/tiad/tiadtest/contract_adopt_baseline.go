package tiadtest

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/siemens/tiad"
)

// The adopt baseline (#1347): an equal compare of a project nothing changed
// since it was opened takes the session's data layout baseline, so the
// first download onto a CPU that runs a program nautilus never loaded (an
// adopted one) loads only what changed and keeps the CPU's data. An added
// block or type of a proven kind needs no consent; any other change does.
func contractAdoptBaseline(t *testing.T, start Starter) {
	t.Run("Brownfield", func(t *testing.T) { contractAdoptBaselineBrownfield(t, start) })
	t.Run("AdoptedNautilusProject", func(t *testing.T) { contractAdoptBaselineNautilus(t, start) })
	t.Run("Refusals", func(t *testing.T) { contractAdoptBaselineRefusals(t, start) })
	t.Run("EveryRouteTouches", func(t *testing.T) { contractAdoptBaselineRoutes(t, start) })
}

// custSources are a customer's program, not nautilus's: a UDT, a global DB
// holding it, an FB and its instance DB, and the cyclic OB that runs them.
var custSources = map[string]string{
	"csrc/cust_Rec.udt":       "TYPE \"cust_Rec\"\nVERSION : 0.1\n   STRUCT\n      sp : Real := 5.0;\n      count : DInt;\n   END_STRUCT;\nEND_TYPE\n",
	"csrc/cust_Ret.db":        "DATA_BLOCK \"cust_Ret\"\nVERSION : 0.1\n   VAR RETAIN\n      r : \"cust_Rec\";\n   END_VAR\nBEGIN\nEND_DATA_BLOCK\n",
	"csrc/cust_Counter.scl":   "FUNCTION_BLOCK \"cust_Counter\"\nVERSION : 0.1\n   VAR\n      s : DInt;\n   END_VAR\nBEGIN\n   #s := #s + 1;\nEND_FUNCTION_BLOCK\n",
	"csrc/cust_Counter_DB.db": "DATA_BLOCK \"cust_Counter_DB\" \"cust_Counter\"\nBEGIN\nEND_DATA_BLOCK\n",
	"csrc/cust_Cycle.scl":     "ORGANIZATION_BLOCK \"cust_Cycle\"\nBEGIN\n   \"cust_Counter_DB\"();\nEND_ORGANIZATION_BLOCK\n",
}

// customerOnCPU loads the customer's program onto the bench CPU (with the
// consent: nothing was there) and closes its session; it returns the
// deploy, whose archive an adopt takes as the CPU's project.
func customerOnCPU(t *testing.T, start Starter, cfg Config) (Target, *tiad.DeployResult) {
	t.Helper()
	a := start(t, cfg)
	c := a.Client()
	installGSD(t, c)
	s, err := c.CreateProject(ctx, "customer", BenchStation(), BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	order := []string{"csrc/cust_Rec.udt", "csrc/cust_Ret.db", "csrc/cust_Counter.scl", "csrc/cust_Counter_DB.db", "csrc/cust_Cycle.scl"}
	for _, p := range order {
		if _, err := c.PutFile(ctx, p, []byte(custSources[p])); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Software(ctx, tiad.SoftwareRequest{Group: "cust", Sources: order, Prune: true, PrunePrefix: "cust_", OBs: []tiad.CyclicOB{{Name: "cust_Cycle", Number: 35, CycleMS: 50}}}); err != nil {
		t.Fatalf("customer import: %v", err)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatalf("customer compile: %v", err)
	}
	r, err := s.Download(ctx, benchSoftwareLoad)
	if err != nil || r.Deploy == nil {
		t.Fatalf("customer download: %+v %v", r, err)
	}
	closeFirst(t, s)
	return a, r.Deploy
}

// adoptAndCompare adopts deploy d's archive as projects/<name>/, opens it
// and compares it with the CPU, asking for the adopt baseline.
func adoptAndCompare(t *testing.T, c *tiad.Client, name string, d *tiad.DeployResult) (*tiad.Session, tiad.CompareResult) {
	t.Helper()
	if _, err := c.AdoptProject(ctx, tiad.AdoptRequest{Name: name, Deploy: d.ID, Replace: true, Address: benchCPU}); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	s, err := c.Open(ctx, "projects/"+name+"/"+name+".ap21")
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.CompareWith(ctx, tiad.CompareRequest{Secrets: BenchLogin, LayoutBaseline: true})
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	return s, r
}

// contractAdoptBaselineBrownfield: the deploy's first download onto a CPU
// that runs a customer's program adds nautilus's blocks without the
// consent, loads only what changed, and records the adopt baseline; then
// the baseline is spent.
func contractAdoptBaselineBrownfield(t *testing.T, start Starter) {
	a, d := customerOnCPU(t, start, Config{Targets: []string{benchCPU}})
	c := a.Client()
	s, r := adoptAndCompare(t, c, "plant", d)
	b := r.LayoutBaseline
	if !r.Equal || b == nil || !b.Taken || !strings.HasPrefix(b.ID, tiad.AdoptBaselinePrefix) || len(b.ID) != len(tiad.AdoptBaselinePrefix)+16 || b.Objects == 0 || b.Unreadable != 0 || b.Why != "" {
		t.Fatalf("compare took no adopt baseline: %+v %+v", r, b)
	}
	// As a deploy does: the station, then nautilus's program.
	if _, err := s.ApplyStation(ctx, BenchStation(), BenchSecrets); err != nil {
		t.Fatalf("apply: %v", err)
	}
	importLayout(t, c, s, layoutV0)
	l := layoutOf(t, s, false)
	if l.Baseline != b.ID || l.BaselineFrom != tiad.BaselineFromAdopted || l.ResetDataNeeded || l.AllDataBlocks || len(l.DataBlocks) != 0 ||
		changeList(l) != "naut_main:added,naut_main_DB:added,naut_OB_main:added,naut_Rec:added,naut_Tags:added" ||
		!strings.Contains(l.Why, "unchanged since the adopted project ("+b.ID+")") || !strings.Contains(l.Why, "the load only adds 5 object(s): the load keeps the CPU's data") {
		t.Fatalf("first deploy after adopt: %+v", l)
	}
	// The layout of a hardware load still needs the consent.
	if hw := layoutOf(t, s, true); !hw.ResetDataNeeded || !hw.AllDataBlocks {
		t.Fatalf("hardware: %+v", hw)
	}
	before := len(a.Loaded(t))
	res, err := s.Download(ctx, keepLoad(l.Digest))
	if err != nil || res.Deploy == nil {
		t.Fatalf("the first download after adopt, without the consent: %+v %v", res, err)
	}
	ld := a.Loaded(t)
	if len(ld) != before+1 || ld[len(ld)-1].EveryBlock {
		t.Fatalf("it loads only what changed: %+v", ld)
	}
	list, err := c.Deploys(ctx, benchCPU)
	if err != nil || len(list.Deploys) == 0 || list.Deploys[0].ID != res.Deploy.ID || list.Deploys[0].LayoutBaseline != b.ID || list.Deploys[0].Layout == nil {
		t.Fatalf("the record names the adopt baseline: %+v %v", list.Deploys, err)
	}
	// Spent: the download reached the CPU, and the next one needs a verify.
	l = layoutOf(t, s, false)
	if l.Baseline != "" || !l.ResetDataNeeded || !strings.Contains(l.NoBaseline, "a download reached the CPU since the compare that took the session's adopt baseline") {
		t.Fatalf("after the download: %+v", l)
	}
	// The next deploy builds on its record, as before.
	s = verifyLatest(t, c, s)
	importLayout(t, c, s, layoutV0)
	if l := layoutOf(t, s, false); l.Baseline != res.Deploy.ID || l.BaselineFrom != tiad.BaselineFromDeploy || l.ResetDataNeeded {
		t.Fatalf("the deploy after: %+v", l)
	}
	closeFirst(t, s)
}

// contractAdoptBaselineNautilus: an adopted project that holds nautilus's
// blocks already: a body-only change keeps the data; an FB static needs
// the consent and names its instance DB.
func contractAdoptBaselineNautilus(t *testing.T, start Starter) {
	a, s := compiledLayoutBench(t, start)
	c := a.Client()
	r, err := s.Download(ctx, benchSoftwareLoad)
	if err != nil {
		t.Fatal(err)
	}
	closeFirst(t, s)
	s, cr := adoptAndCompare(t, c, "again", r.Deploy)
	if cr.LayoutBaseline == nil || !cr.LayoutBaseline.Taken {
		t.Fatalf("no adopt baseline: %+v", cr)
	}
	if _, err := s.ApplyStation(ctx, BenchStation(), BenchSecrets); err != nil {
		t.Fatalf("apply: %v", err)
	}
	importLayout(t, c, s, layoutSources("      sp : Real := 1.0;\n      cnt : DInt;\n", "", "   #s.cnt := #s.cnt + 2;\n"))
	if l := layoutOf(t, s, false); l.BaselineFrom != tiad.BaselineFromAdopted || l.ResetDataNeeded || len(l.Changes) != 0 {
		t.Fatalf("a body-only change: %+v", l)
	}
	importLayout(t, c, s, layoutSources("      sp : Real := 1.0;\n      cnt : DInt;\n", "      extra : Int;\n", "   #s.cnt := #s.cnt + 2;\n"))
	l := layoutOf(t, s, false)
	if !l.ResetDataNeeded || strings.Join(l.DataBlocks, ",") != "naut_main_DB" || !strings.Contains(l.Why, "changed since the adopted project") {
		t.Fatalf("an FB static after adopt: %+v", l)
	}
	if _, err := s.Download(ctx, keepLoad(l.Digest)); tiad.Kind(err) != tiad.KindDecisionRequired || questionIDs(tiad.Questions(err)) != tiad.QuestionResetData {
		t.Fatalf("an FB static after adopt, no consent: %v", err)
	}
	closeFirst(t, s)
}

// compiledLayoutBench is a bench project with the layout program,
// compiled, nothing loaded.
func compiledLayoutBench(t *testing.T, start Starter) (Target, *tiad.Session) {
	t.Helper()
	a := start(t, Config{Targets: []string{benchCPU}})
	c := a.Client()
	installGSD(t, c)
	s, err := c.CreateProject(ctx, "bench", BenchStation(), BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	importLayout(t, c, s, layoutV0)
	return a, s
}

// contractAdoptBaselineRefusals: no adopt baseline from a touched session,
// an unequal compare, or an agent with TIA's window; and none unless asked.
func contractAdoptBaselineRefusals(t *testing.T, start Starter) {
	a, d := customerOnCPU(t, start, Config{Targets: []string{benchCPU}})
	c := a.Client()
	if _, err := c.AdoptProject(ctx, tiad.AdoptRequest{Name: "plant", Deploy: d.ID, Address: benchCPU}); err != nil {
		t.Fatal(err)
	}
	const project = "projects/plant/plant.ap21"

	// Not asked: nothing said, nothing taken.
	s, err := c.Open(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	if r, err := s.CompareWith(ctx, tiad.CompareRequest{Secrets: BenchLogin}); err != nil || !r.Equal || r.LayoutBaseline != nil {
		t.Fatalf("a compare not asked: %+v %v", r, err)
	}
	// Touched: an apply ran first.
	if _, err := s.ApplyStation(ctx, BenchStation(), BenchSecrets); err != nil {
		t.Fatal(err)
	}
	r, err := s.CompareWith(ctx, tiad.CompareRequest{Secrets: BenchLogin, LayoutBaseline: true})
	if err != nil || !r.Equal || r.LayoutBaseline == nil || r.LayoutBaseline.Taken || !strings.Contains(r.LayoutBaseline.Why, "the session is not untouched") {
		t.Fatalf("a touched session: %+v %+v %v", r, r.LayoutBaseline, err)
	}
	importLayout(t, c, s, layoutV0)
	if l := layoutOf(t, s, false); l.Baseline != "" || !l.ResetDataNeeded || !strings.Contains(l.NoBaseline, "the adopt compare took no baseline: the session is not untouched") {
		t.Fatalf("no baseline from a touched session: %+v", l)
	}
	// A failed call touches too.
	closeFirst(t, s)
	s, err = c.Open(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: []string{"nowhere/x.scl"}}); err == nil {
		t.Fatal("an import of no file passed")
	}
	if r, err := s.CompareWith(ctx, tiad.CompareRequest{Secrets: BenchLogin, LayoutBaseline: true}); err != nil || r.LayoutBaseline == nil || r.LayoutBaseline.Taken {
		t.Fatalf("after a failed import: %+v %v", r, err)
	}
	closeFirst(t, s)

	// A dry run spends it too: TIA may load one without asking.
	s, r = adoptAndCompare(t, c, "plant", d)
	if r.LayoutBaseline == nil || !r.LayoutBaseline.Taken {
		t.Fatalf("no baseline to spend: %+v", r)
	}
	if _, err := s.ApplyStation(ctx, BenchStation(), BenchSecrets); err != nil {
		t.Fatal(err)
	}
	importLayout(t, c, s, layoutV0)
	// TIA asks a check, at which the dry run is cancelled.
	a.SetQuestions(t, []tiad.Question{{ID: tiad.QuestionConsistentDownload, Message: "consistent download"}})
	dry := keepLoad(layoutOf(t, s, false).Digest)
	dry.DryRun = true
	if res, err := s.Download(ctx, dry); err != nil || !res.DryRun {
		t.Fatalf("dry run: %+v %v", res, err)
	}
	if l := layoutOf(t, s, false); l.Baseline != "" || !strings.Contains(l.NoBaseline, "a download reached the CPU since the compare") {
		t.Fatalf("after a dry run: %+v", l)
	}
	a.SetQuestions(t, nil)
	closeFirst(t, s)

	// Not equal: the project changed on the CPU since (another load).
	s, err = c.Open(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	// An export keeps it untouched; the compare still takes it.
	if _, err := s.Export(ctx, tiad.ExportRequest{Dir: "exp/plant", Blocks: "scl"}); err != nil {
		t.Fatalf("export: %v", err)
	}
	if r, err := s.CompareWith(ctx, tiad.CompareRequest{Secrets: BenchLogin, LayoutBaseline: true}); err != nil || r.LayoutBaseline == nil || !r.LayoutBaseline.Taken {
		t.Fatalf("after an export: %+v %v", r, err)
	}
	// Asked again once touched: none, and the one taken before is gone.
	if _, err := s.ApplyStation(ctx, BenchStation(), BenchSecrets); err != nil {
		t.Fatal(err)
	}
	if r, err := s.CompareWith(ctx, tiad.CompareRequest{Secrets: BenchLogin, LayoutBaseline: true}); err != nil || r.LayoutBaseline == nil || r.LayoutBaseline.Taken {
		t.Fatalf("asked again, touched: %+v %v", r, err)
	}
	importLayout(t, c, s, layoutV0)
	if l := layoutOf(t, s, false); l.Baseline != "" || !strings.Contains(l.NoBaseline, "the session is not untouched") {
		t.Fatalf("the earlier adopt baseline stayed: %+v", l)
	}
	closeFirst(t, s)
	cs, err := c.CreateProject(ctx, "other", BenchStation(), BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	importLayout(t, c, cs, layoutV0)
	if _, err := cs.Download(ctx, benchSoftwareLoad); err != nil {
		t.Fatal(err)
	}
	closeFirst(t, cs)
	s, err = c.Open(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.CompareWith(ctx, tiad.CompareRequest{Secrets: BenchLogin, LayoutBaseline: true})
	if err != nil || r.Equal || r.LayoutBaseline == nil || r.LayoutBaseline.Taken || !strings.Contains(r.LayoutBaseline.Why, "not equal to the CPU") {
		t.Fatalf("an unequal compare: %+v %+v %v", r, r.LayoutBaseline, err)
	}
	closeFirst(t, s)

	// An agent with TIA's window takes none, and says so first (it
	// downloads nothing either).
	ui := start(t, Config{Targets: []string{benchCPU}, UI: true}).Client()
	installGSD(t, ui)
	us, err := ui.CreateProject(ctx, "viewer", BenchStation(), BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	arch, err := us.Save(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	closeFirst(t, us)
	if _, err := ui.AdoptProject(ctx, tiad.AdoptRequest{Name: "plant", Archive: arch, Address: benchCPU}); err != nil {
		t.Fatal(err)
	}
	s, err = ui.Open(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.CompareWith(ctx, tiad.CompareRequest{Secrets: BenchLogin, LayoutBaseline: true})
	if err != nil || r.LayoutBaseline == nil || r.LayoutBaseline.Taken || !strings.Contains(r.LayoutBaseline.Why, "TIAD_UI") {
		t.Fatalf("a UI agent: %+v %+v %v", r, r.LayoutBaseline, err)
	}
	closeFirst(t, s)
}

// contractAdoptBaselineRoutes walks every route the agent has: each call
// under /v1/sessions/{id} touches the session, failed or not, except the
// ones tiad.UntouchedKeeps names. A route added later touches it unless
// someone puts it on that list.
func contractAdoptBaselineRoutes(t *testing.T, start Starter) {
	a, d := customerOnCPU(t, start, Config{Targets: []string{benchCPU}})
	c := a.Client()
	if _, err := c.AdoptProject(ctx, tiad.AdoptRequest{Name: "plant", Deploy: d.ID, Address: benchCPU}); err != nil {
		t.Fatal(err)
	}
	routes := agentRoutes(t, c)
	var session []string
	for _, r := range routes {
		if strings.Contains(r, " /v1/sessions/{id}") {
			session = append(session, r)
			continue
		}
		// Any other route that names a session some other way would be
		// neither touched nor walked: none may exist.
		if strings.Contains(r, "sessions") && r != "GET /v1/sessions" && r != "POST /v1/sessions" {
			t.Fatalf("route %q names a session without {id}: the untouched rule does not see it", r)
		}
	}
	for _, k := range tiad.UntouchedKeeps {
		if !slices.Contains(session, k) {
			t.Fatalf("UntouchedKeeps names %q, which the agent has no route for: %v", k, routes)
		}
	}
	if len(session) < 10 {
		t.Fatalf("too few session routes: %v", session)
	}
	for _, route := range session {
		method, pattern, _ := strings.Cut(route, " ")
		s, err := c.Open(ctx, "projects/plant/plant.ap21")
		if err != nil {
			t.Fatalf("%s: open: %v", route, err)
		}
		// The call itself, with an empty body: most refuse it, which
		// must touch the session all the same.
		req, _ := http.NewRequestWithContext(ctx, method, c.BaseURL+strings.Replace(pattern, "{id}", s.ID, 1), strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer "+c.Token)
		req.Header.Set("Content-Type", "application/json")
		if resp, err := http.DefaultClient.Do(req); err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		if method == http.MethodDelete {
			// Closed: nothing left to compare. The rule covers it.
			if !tiad.UntouchedTouches(route) {
				t.Fatalf("%s keeps a session", route)
			}
			continue
		}
		r, err := s.CompareWith(ctx, tiad.CompareRequest{Secrets: BenchLogin, LayoutBaseline: true})
		if err != nil || r.LayoutBaseline == nil {
			t.Fatalf("%s: compare: %+v %v", route, r, err)
		}
		keeps := slices.Contains(tiad.UntouchedKeeps, route)
		if r.LayoutBaseline.Taken != keeps {
			t.Fatalf("%s: taken %v, want %v: %+v", route, r.LayoutBaseline.Taken, keeps, r.LayoutBaseline)
		}
		closeFirst(t, s)
	}
}

// agentRoutes is every route the agent registers (GET /v1/test/routes, a
// test seam).
func agentRoutes(t *testing.T, c *tiad.Client) []string {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/v1/test/routes", nil)
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var env struct {
		Data struct {
			Routes []string `json:"routes"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil || len(env.Data.Routes) == 0 {
		t.Fatalf("routes: %v %+v", err, env)
	}
	return env.Data.Routes
}
