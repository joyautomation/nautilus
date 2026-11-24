package tiadtest

import (
	"slices"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/siemens/tiad"
)

// The data layout gate (#1341, siemens-ide.md §3.5 "data layout"): a
// download without decisions.resetData needs the layout check's digest,
// and passes only when no object's layout changed since the deploy a
// verify by this agent found the CPU equal to. TIA asking a stop, a
// reinitialize or a load of every block without the consent is a gate
// miss: cancelled, nothing loaded.

// layoutSources are a program whose data blocks hold a UDT: a global DB,
// an FB with a static of it, and that FB's instance DB.
func layoutSources(rec, mainDecl, mainBody string) map[string]string {
	return map[string]string{
		"lsrc/naut_Rec.udt":     "TYPE \"naut_Rec\"\nVERSION : 0.1\n   STRUCT\n" + rec + "   END_STRUCT;\nEND_TYPE\n",
		"lsrc/naut_Tags.db":     "DATA_BLOCK \"naut_Tags\"\nVERSION : 0.1\n   VAR\n      R1 : \"naut_Rec\";\n   END_VAR\nBEGIN\nEND_DATA_BLOCK\n",
		"lsrc/naut_main.scl":    "FUNCTION_BLOCK \"naut_main\"\nVERSION : 0.1\n   VAR\n      s : \"naut_Rec\";\n" + mainDecl + "   END_VAR\nBEGIN\n" + mainBody + "END_FUNCTION_BLOCK\n",
		"lsrc/naut_main_DB.db":  "DATA_BLOCK \"naut_main_DB\" \"naut_main\"\nBEGIN\nEND_DATA_BLOCK\n",
		"lsrc/naut_OB_main.scl": "ORGANIZATION_BLOCK \"naut_OB_main\"\nBEGIN\n   \"naut_main_DB\"();\nEND_ORGANIZATION_BLOCK\n",
	}
}

var layoutV0 = layoutSources("      sp : Real := 1.0;\n      cnt : DInt;\n", "", "   #s.cnt := #s.cnt + 1;\n")

// importLayout puts srcs and imports them into group nautilus (the
// types' source first, as the writer orders them), then compiles fully.
func importLayout(t *testing.T, c *tiad.Client, s *tiad.Session, srcs map[string]string) {
	t.Helper()
	order := []string{"lsrc/naut_Rec.udt", "lsrc/naut_Tags.db", "lsrc/naut_main.scl", "lsrc/naut_main_DB.db", "lsrc/naut_OB_main.scl"}
	for _, p := range order {
		if _, err := c.PutFile(ctx, p, []byte(srcs[p])); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: order, Prune: true, PrunePrefix: "naut_", OBs: []tiad.CyclicOB{{Name: "naut_OB_main", Number: 30, CycleMS: 100}}}); err != nil {
		t.Fatalf("import: %v", err)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatalf("compile: %v", err)
	}
}

func layoutOf(t *testing.T, s *tiad.Session, hardware bool) tiad.Layout {
	t.Helper()
	l, err := s.Layout(ctx, benchCPU, hardware)
	if err != nil {
		t.Fatalf("layout: %v", err)
	}
	return l
}

func changeList(l tiad.Layout) string {
	var out []string
	for _, c := range l.Changes {
		out = append(out, c.Name+":"+c.Change)
	}
	return strings.Join(out, ",")
}

// keepLoad is a software download without the consent, sending the
// digest the caller was shown.
func keepLoad(digest string) tiad.DownloadRequest {
	return tiad.DownloadRequest{Secrets: BenchDownloadSecrets(), Address: benchCPU, Software: true, Announced: true,
		Decisions: tiad.Decisions{StartAfter: tiad.No()}, LayoutDigest: digest}
}

// verifyLatest closes s, verifies the CPU's newest load (loaded or in
// doubt; it must be
// equal), and opens the project again.
func verifyLatest(t *testing.T, c *tiad.Client, s *tiad.Session) *tiad.Session {
	t.Helper()
	project := s.Project
	closeFirst(t, s)
	list, err := c.Deploys(ctx, benchCPU)
	if err != nil || len(list.Deploys) == 0 {
		t.Fatalf("deploys: %+v %v", list, err)
	}
	i := slices.IndexFunc(list.Deploys, func(d tiad.Deploy) bool { return d.Status != tiad.DeployNotLoaded })
	if i < 0 {
		t.Fatalf("no load in %+v", list.Deploys)
	}
	if r := verify(t, c, tiad.VerifyRequest{Secrets: BenchLogin, Deploy: list.Deploys[i].ID}); r.Verdict != tiad.VerifyEqual {
		t.Fatalf("verify of the newest load: %+v", r)
	}
	s, err = c.Open(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	// As a deploy does: the station spec applied again.
	if _, err := s.ApplyStation(ctx, BenchStation(), BenchSecrets); err != nil {
		t.Fatalf("apply: %v", err)
	}
	return s
}

func contractLayoutGate(t *testing.T, start Starter) {
	a := start(t, Config{Targets: []string{benchCPU}})
	c := a.Client()
	installGSD(t, c)
	s, err := c.CreateProject(ctx, "bench", BenchStation(), BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	importLayout(t, c, s, layoutV0)

	// No deploy yet: no baseline, so the load replaces the whole program
	// and needs the consent; every data block is named.
	l := layoutOf(t, s, false)
	if l.Baseline != "" || !l.ResetDataNeeded || !l.AllDataBlocks || strings.Join(l.DataBlocks, ",") != "naut_main_DB,naut_Tags" ||
		!strings.Contains(l.NoBaseline, "no verify by this agent") || l.Scheme != tiad.LayoutScheme || len(l.Digest) != 64 {
		t.Fatalf("no baseline: %+v", l)
	}
	if _, err := s.Download(ctx, keepLoad("")); tiad.Kind(err) != tiad.KindBadRequest || !strings.Contains(err.Error(), "needs layoutDigest") {
		t.Fatalf("no consent, no digest: %v", err)
	}
	_, err = s.Download(ctx, keepLoad(l.Digest))
	if tiad.Kind(err) != tiad.KindDecisionRequired || questionIDs(tiad.Questions(err)) != tiad.QuestionResetData ||
		!strings.Contains(err.Error(), "the download needs decisions.resetData: no layout baseline") || !strings.Contains(err.Error(), "nothing was sent to the CPU") {
		t.Fatalf("no baseline, no consent: %v %+v", err, tiad.Questions(err))
	}
	if q := tiad.Questions(err); len(q) != 1 || strings.Join(q[0].Objects, ",") != "naut_main_DB,naut_Tags" {
		t.Fatalf("the reset-data question names the data blocks: %+v", q)
	}
	if n := len(a.Loaded(t)); n != 0 {
		t.Fatalf("%d load(s) without the consent", n)
	}
	if list, _ := c.Deploys(ctx, benchCPU); len(list.Deploys) != 0 {
		t.Fatalf("a refusal of the gate recorded %+v", list.Deploys)
	}
	// With the consent and no baseline: the whole program.
	if _, err := s.Download(ctx, benchSoftwareLoad); err != nil {
		t.Fatal(err)
	}
	if l := a.Loaded(t); len(l) != 1 || !l[0].EveryBlock {
		t.Fatalf("a load with the consent and no baseline takes every block: %+v", l)
	}
	list, err := c.Deploys(ctx, benchCPU)
	if err != nil || len(list.Deploys) != 1 || list.Deploys[0].Layout == nil || list.Deploys[0].Layout.Scheme != tiad.LayoutScheme ||
		list.Deploys[0].Layout.TIA == "" || len(list.Deploys[0].Layout.Objects) < 6 {
		t.Fatalf("the deploy record keeps the layout: %+v %v", list.Deploys, err)
	}
	first := list.Deploys[0].ID

	// Loaded but not verified: still no baseline.
	if l := layoutOf(t, s, false); l.Baseline != "" || !l.ResetDataNeeded {
		t.Fatalf("no verify yet: %+v", l)
	}

	// Verified equal, nothing changed: the load keeps the CPU's data and
	// needs no consent; only what changed loads.
	s = verifyLatest(t, c, s)
	importLayout(t, c, s, layoutV0)
	l = layoutOf(t, s, false)
	if l.Baseline != first || l.ResetDataNeeded || len(l.Changes) != 0 || len(l.DataBlocks) != 0 || l.AllDataBlocks || !strings.Contains(l.Why, "unchanged since deploy "+first) {
		t.Fatalf("unchanged: %+v", l)
	}
	// The hardware configuration: the consent, whatever the layout.
	if hw := layoutOf(t, s, true); !hw.ResetDataNeeded || !hw.AllDataBlocks || hw.Digest == l.Digest {
		t.Fatalf("a hardware load: %+v", hw)
	}
	if r, err := s.Download(ctx, keepLoad(l.Digest)); err != nil || !r.Downloaded {
		t.Fatalf("an unchanged layout loads without the consent: %+v %v", r, err)
	}
	if ld := a.Loaded(t); len(ld) != 2 || ld[1].EveryBlock {
		t.Fatalf("only what changed loads: %+v", ld)
	}
	// The download forgot the verify: the CPU's newest load is a new one.
	if l := layoutOf(t, s, false); l.Baseline != "" || !l.ResetDataNeeded {
		t.Fatalf("after a download, before its verify: %+v", l)
	}

	// A body change keeps the layout.
	s = verifyLatest(t, c, s)
	importLayout(t, c, s, layoutSources("      sp : Real := 1.0;\n      cnt : DInt;\n", "", "   #s.cnt := #s.cnt + 2;\n"))
	l = layoutOf(t, s, false)
	if l.ResetDataNeeded || len(l.Changes) != 0 {
		t.Fatalf("a body change: %+v", l)
	}
	if _, err := s.Download(ctx, keepLoad(l.Digest)); err != nil {
		t.Fatalf("a body change loads without the consent: %v", err)
	}

	// An FB static: the FB and its instance DB; the DB is named.
	s = verifyLatest(t, c, s)
	importLayout(t, c, s, layoutSources("      sp : Real := 1.0;\n      cnt : DInt;\n", "      x : Int;\n", "   #s.cnt := #s.cnt + 2;\n"))
	l = layoutOf(t, s, false)
	if !l.ResetDataNeeded || changeList(l) != "naut_main:changed,naut_main_DB:changed" || strings.Join(l.DataBlocks, ",") != "naut_main_DB" {
		t.Fatalf("an FB static: %+v", l)
	}
	n := len(a.Loaded(t))
	_, err = s.Download(ctx, keepLoad(l.Digest))
	if tiad.Kind(err) != tiad.KindDecisionRequired || questionIDs(tiad.Questions(err)) != tiad.QuestionResetData || !strings.Contains(err.Error(), "the data layout changed since deploy") {
		t.Fatalf("an FB static without the consent: %v", err)
	}
	if len(a.Loaded(t)) != n {
		t.Fatal("a refusal loaded")
	}
	// With the consent: TIA's reinitialize is answered, and only what
	// changed loads.
	a.SetQuestions(t, []tiad.Question{{ID: tiad.QuestionReinitialize, Message: "reinit"}})
	withConsent := keepLoad(l.Digest)
	withConsent.Decisions.ResetData = tiad.Yes()
	// The consent alone never answers it: its yes stops the CPU.
	if _, err := s.Download(ctx, withConsent); tiad.Kind(err) != tiad.KindDecisionRequired || questionIDs(tiad.Questions(err)) != tiad.QuestionReinitialize {
		t.Fatalf("the reset without the stop's consent: %v", err)
	}
	s = verifyLatest(t, c, s)
	importLayout(t, c, s, layoutSources("      sp : Real := 1.0;\n      cnt : DInt;\n", "      x : Int;\n", "   #s.cnt := #s.cnt + 2;\n"))
	withConsent = keepLoad(layoutOf(t, s, false).Digest)
	withConsent.Decisions.ResetData, withConsent.Decisions.StopCPU = tiad.Yes(), tiad.Yes()
	if r, err := s.Download(ctx, withConsent); err != nil || !r.Downloaded {
		t.Fatalf("an FB static with the consent: %+v %v", r, err)
	}
	if ld := a.Loaded(t); ld[len(ld)-1].EveryBlock {
		t.Fatalf("a load with a baseline takes only what changed, the consent too: %+v", ld)
	}
	a.SetQuestions(t, nil)

	// A UDT member: the UDT and every holder; both DBs named.
	s = verifyLatest(t, c, s)
	importLayout(t, c, s, layoutSources("      sp : Real := 1.0;\n      cnt : DInt;\n      extra : Int;\n", "      x : Int;\n", "   #s.cnt := #s.cnt + 2;\n"))
	l = layoutOf(t, s, false)
	if changeList(l) != "naut_main:changed,naut_main_DB:changed,naut_Rec:changed,naut_Tags:changed" || strings.Join(l.DataBlocks, ",") != "naut_main_DB,naut_Tags" {
		t.Fatalf("a UDT member: %+v", l)
	}
	// The digest names the layout read: another import since refuses.
	old := l.Digest
	importLayout(t, c, s, layoutV0)
	_, err = s.Download(ctx, keepLoad(old))
	if tiad.Kind(err) != tiad.KindDecisionRequired || !strings.Contains(err.Error(), "the data layout is no longer the one the caller showed") {
		t.Fatalf("a stale digest: %v", err)
	}
	withConsent = keepLoad(old)
	withConsent.Decisions.ResetData = tiad.Yes()
	if _, err := s.Download(ctx, withConsent); tiad.Kind(err) != tiad.KindDecisionRequired || !strings.Contains(err.Error(), "no longer the one the caller showed") {
		t.Fatalf("a stale digest refuses the consent too: %v", err)
	}

	// Back to what the CPU holds (the FB static's load).
	importLayout(t, c, s, layoutSources("      sp : Real := 1.0;\n      cnt : DInt;\n", "      x : Int;\n", "   #s.cnt := #s.cnt + 2;\n"))

	// Another project, the same program: the CPU's newest load is not its
	// own, so no baseline, even with the verify standing.
	s = verifyLatest(t, c, s)
	project := s.Project
	closeFirst(t, s)
	other, err := c.CreateProject(ctx, "bench2", BenchStation(), BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	importLayout(t, c, other, layoutSources("      sp : Real := 1.0;\n      cnt : DInt;\n", "      x : Int;\n", "   #s.cnt := #s.cnt + 2;\n"))
	if l := layoutOf(t, other, false); l.Baseline != "" || !strings.Contains(l.NoBaseline, "loaded another project") {
		t.Fatalf("another project: %+v", l)
	}
	closeFirst(t, other)
	if s, err = c.Open(ctx, project); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyStation(ctx, BenchStation(), BenchSecrets); err != nil {
		t.Fatal(err)
	}
	importLayout(t, c, s, layoutSources("      sp : Real := 1.0;\n      cnt : DInt;\n", "      x : Int;\n", "   #s.cnt := #s.cnt + 2;\n"))
	if l := layoutOf(t, s, false); l.Baseline == "" {
		t.Fatalf("its own project again: %+v", l)
	}

	// A customer's object edited in TIA, and one that does not export.
	a.SetLayout(t, LayoutScript{Edits: []string{"Main"}})
	if l := layoutOf(t, s, false); !l.ResetDataNeeded || !slices.Contains(strings.Split(changeList(l), ","), "Main:changed") {
		t.Fatalf("a customer's edit: %+v", l)
	}
	a.SetLayout(t, LayoutScript{Unreadable: []string{"naut_Rec"}})
	if l := layoutOf(t, s, false); !l.ResetDataNeeded || !slices.Contains(strings.Split(changeList(l), ","), "naut_Rec:unreadable") ||
		!slices.Contains(l.DataBlocks, "naut_Tags") {
		t.Fatalf("an object that does not export: %+v", l)
	}
	a.SetLayout(t, LayoutScript{})

	// The RUN backstop: the gate found nothing changed, TIA asks to
	// reinitialize anyway: a gate miss, cancelled, nothing loaded.
	l = layoutOf(t, s, false)
	if l.ResetDataNeeded {
		t.Fatalf("back to the loaded layout: %+v", l)
	}
	// Every download attempt forgets the verify, a cancelled one too:
	// the next needs a verify again.
	for _, q := range []string{tiad.QuestionReinitialize, tiad.QuestionStopCPU, tiad.QuestionAllBlocks} {
		a.SetQuestions(t, []tiad.Question{{ID: q, Message: "TIA asks"}})
		if q != tiad.QuestionReinitialize {
			if l := layoutOf(t, s, false); l.Baseline != "" {
				t.Fatalf("a cancelled download forgets the verify: %+v", l)
			}
			s = verifyLatest(t, c, s)
			importLayout(t, c, s, layoutSources("      sp : Real := 1.0;\n      cnt : DInt;\n", "      x : Int;\n", "   #s.cnt := #s.cnt + 2;\n"))
			l = layoutOf(t, s, false)
		}
		n := len(a.Loaded(t))
		_, err = s.Download(ctx, keepLoad(l.Digest))
		if tiad.Kind(err) != tiad.KindDecisionRequired || !strings.Contains(err.Error(), "GATE MISS: TIA asked \""+q+"\"") || !strings.Contains(err.Error(), "nothing was loaded") {
			t.Fatalf("a gate miss at %s: %v", q, err)
		}
		if len(a.Loaded(t)) != n {
			t.Fatalf("a gate miss at %s loaded", q)
		}
	}
	// After an acting answer it is in doubt, never clean.
	s = verifyLatest(t, c, s)
	importLayout(t, c, s, layoutSources("      sp : Real := 1.0;\n      cnt : DInt;\n", "      x : Int;\n", "   #s.cnt := #s.cnt + 2;\n"))
	l = layoutOf(t, s, false)
	a.SetQuestions(t, []tiad.Question{{ID: tiad.QuestionConsistentDownload}, {ID: tiad.QuestionReinitialize}})
	acting := keepLoad(l.Digest)
	acting.Decisions.ConsistentDownload = tiad.Yes()
	_, err = s.Download(ctx, acting)
	if tiad.Kind(err) != tiad.KindOutcomeUnknown || !strings.Contains(err.Error(), "GATE MISS") || strings.Join(tiad.ActingAnswers(err), ",") != "consistent-download" {
		t.Fatalf("a gate miss after an acting answer: %v (acting %v)", err, tiad.ActingAnswers(err))
	}
	a.SetQuestions(t, nil)

	// A CPU changed outside nautilus: the verify finds it different, and
	// a different verdict is never a baseline.
	project = s.Project
	closeFirst(t, s)
	a.LoadOutside(t, benchCPU, "")
	list, err = c.Deploys(ctx, benchCPU)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(list.Deploys, func(d tiad.Deploy) bool { return d.Status != tiad.DeployNotLoaded })
	if r := verify(t, c, tiad.VerifyRequest{Secrets: BenchLogin, Deploy: list.Deploys[i].ID}); r.Verdict != tiad.VerifyDifferent {
		t.Fatalf("verify after an outside load: %+v", r)
	}
	if s, err = c.Open(ctx, project); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyStation(ctx, BenchStation(), BenchSecrets); err != nil {
		t.Fatal(err)
	}
	importLayout(t, c, s, layoutSources("      sp : Real := 1.0;\n      cnt : DInt;\n", "      x : Int;\n", "   #s.cnt := #s.cnt + 2;\n"))
	if l := layoutOf(t, s, false); l.Baseline != "" || !l.ResetDataNeeded {
		t.Fatalf("a verify that found the CPU different is no baseline: %+v", l)
	}
	// The agent's log names every gate verdict and every miss (the guide
	// points the operator at them). Only an agent process has a log.
	if lg, ok := a.(interface{ Log() string }); ok {
		log := lg.Log()
		for _, want := range []string{"audit layout-gate session=", "resetDataNeeded=no resetData=unanswered digest=match",
			`audit layout-gate-miss session=`, `question="reinitialize" baseline=d-`, `question="stop-cpu"`, `acting=["consistent-download"]`} {
			if !strings.Contains(log, want) {
				t.Errorf("the agent's log lacks %q", want)
			}
		}
	}
}
