package tiad

import (
	"slices"
	"strings"
	"testing"
)

func lo(name, typ, hash string, refs ...string) LayoutObject {
	kind := "block"
	if typ == "PlcStruct" {
		kind = "type"
	}
	return LayoutObject{Name: name, Kind: kind, Type: typ, Hash: hash, Refs: refs}
}

// layoutProgram is the agent's LayoutTest Program1: a UDT held by a
// global DB, an FB and its instance DB, and a customer's DB.
func layoutProgram(rec, tags, main, mainDB string) []LayoutObject {
	return []LayoutObject{
		lo("Main", "OB", "m"),
		lo("naut_Rec", "PlcStruct", rec),
		lo("naut_Tags", "GlobalDB", tags, "naut_Rec"),
		lo("naut_main", "FB", main, "naut_Rec"),
		lo("naut_main_DB", "InstanceDB", mainDB, "naut_main", "naut_Rec"),
		lo("Cust_DB", "GlobalDB", "c"),
	}
}

func layoutBase(objs []LayoutObject) *LayoutBaseline {
	return &LayoutBaseline{Deploy: "d-1", Scheme: LayoutScheme, TIA: "V21", Objects: objs}
}

func changesOf(l Layout) string {
	var out []string
	for _, c := range l.Changes {
		out = append(out, c.Name+":"+c.Change)
	}
	return strings.Join(out, ",")
}

// TestDiffLayout is the agent's LayoutDiffNamesTheDataBlocks, on the Go
// copy the fake runs (#1341).
func TestDiffLayout(t *testing.T) {
	was := layoutProgram("r1", "t1", "f1", "i1")
	none := DiffLayout(layoutProgram("r1", "t1", "f1", "i1"), nil, "no deploy record for this CPU", "V21", false)
	if none.Baseline != "" || !none.ResetDataNeeded || !none.AllDataBlocks || strings.Join(none.DataBlocks, ",") != "Cust_DB,naut_main_DB,naut_Tags" ||
		!strings.Contains(none.Why, "no layout baseline (no deploy record for this CPU)") {
		t.Fatalf("no baseline: %+v", none)
	}
	same := DiffLayout(layoutProgram("r1", "t1", "f1", "i1"), layoutBase(was), "", "V21", false)
	if same.ResetDataNeeded || len(same.Changes) != 0 || len(same.DataBlocks) != 0 || same.AllDataBlocks {
		t.Fatalf("unchanged: %+v", same)
	}
	fb := DiffLayout(layoutProgram("r1", "t1", "f2", "i2"), layoutBase(was), "", "V21", false)
	if !fb.ResetDataNeeded || changesOf(fb) != "naut_main:changed,naut_main_DB:changed" || strings.Join(fb.DataBlocks, ",") != "naut_main_DB" {
		t.Fatalf("FB static: %+v", fb)
	}
	// A UDT default-only change moves only the UDT: its holders are
	// named by their references.
	udt := DiffLayout(layoutProgram("r2", "t1", "f1", "i1"), layoutBase(was), "", "V21", false)
	if changesOf(udt) != "naut_Rec:changed" || strings.Join(udt.DataBlocks, ",") != "naut_main_DB,naut_Tags" {
		t.Fatalf("UDT default: %+v", udt)
	}
	// Named through a chain: a DB that names only an FB, whose static's
	// type changed.
	chain := []LayoutObject{lo("Inner", "PlcStruct", "a"), lo("Outer", "FB", "b", "Inner"), lo("Outer_DB", "InstanceDB", "c", "Outer")}
	chainNow := []LayoutObject{lo("Inner", "PlcStruct", "a2"), lo("Outer", "FB", "b", "Inner"), lo("Outer_DB", "InstanceDB", "c", "Outer")}
	if ch := DiffLayout(chainNow, layoutBase(chain), "", "V21", false); strings.Join(ch.DataBlocks, ",") != "Outer_DB" {
		t.Fatalf("a chain: %+v", ch)
	}
	more := layoutProgram("r1", "t1", "f1", "i1")
	more = append(more[:5], lo("naut_New_DB", "GlobalDB", "n"))
	more[1].Hash = ""
	m := DiffLayout(more, layoutBase(was), "", "V21", false)
	if changesOf(m) != "naut_New_DB:added,naut_Rec:unreadable,Cust_DB:removed" || !m.AllDataBlocks ||
		strings.Join(m.DataBlocks, ",") != "naut_main_DB,naut_New_DB,naut_Tags,Cust_DB" || !m.ResetDataNeeded {
		t.Fatalf("added, unreadable, removed: %+v", m)
	}
	// An unreadable software unit no export names: every data block.
	withUnit := append(layoutProgram("r1", "t1", "f1", "i1"), LayoutObject{Name: "unit:Line", Kind: "unit", Type: "PlcUnit"})
	if u := DiffLayout(withUnit, layoutBase(was), "", "V21", false); !u.AllDataBlocks || strings.Join(u.DataBlocks, ",") != "Cust_DB,naut_main_DB,naut_Tags" {
		t.Fatalf("an unreadable unit: %+v", u)
	}
	// A change no data block uses names none.
	fcWas := append(layoutProgram("r1", "t1", "f1", "i1"), lo("naut_Calc", "FC", "x1"))
	fcNow := append(layoutProgram("r1", "t1", "f1", "i1"), lo("naut_Calc", "FC", "x2"))
	if fc := DiffLayout(fcNow, layoutBase(fcWas), "", "V21", false); !fc.ResetDataNeeded || fc.AllDataBlocks || len(fc.DataBlocks) != 0 {
		t.Fatalf("an FC's interface: %+v", fc)
	}
	wasBad := layoutProgram("r1", "t1", "f1", "i1")
	wasBad[5].Hash = ""
	if wb := DiffLayout(layoutProgram("r1", "t1", "f1", "i1"), layoutBase(wasBad), "", "V21", false); changesOf(wb) != "Cust_DB:changed" {
		t.Fatalf("unreadable then: %+v", wb)
	}
	if hw := DiffLayout(layoutProgram("r1", "t1", "f1", "i1"), layoutBase(was), "", "V21", true); !hw.ResetDataNeeded || !hw.AllDataBlocks {
		t.Fatalf("hardware: %+v", hw)
	}
	// The same digest as the agent's for the same layout: the two copies
	// hash alike (LayoutTest.LayoutDigestMatchesTheGoCopy).
	if same.Digest != layoutGoldenDigest {
		t.Fatalf("digest %s, want the agent's %s", same.Digest, layoutGoldenDigest)
	}
}

// layoutGoldenDigest is the digest of layoutProgram("r1", "t1", "f1",
// "i1") against its own baseline d-1, read by TIA V21, software only.
const layoutGoldenDigest = "5cfb85892e89e93872eed5b06e73dffad713b6cbdeb573cdc350b527cd1c7633"

func TestLayoutBaselineOf(t *testing.T) {
	now := layoutProgram("r1", "t1", "f1", "i1")
	b, why := LayoutBaselineOf("d-7", LayoutRecordOf(now, "V21"), "V21")
	if b == nil || why != "" || len(b.Objects) != len(now) || b.Deploy != "d-7" {
		t.Fatalf("read back: %+v %q", b, why)
	}
	for _, c := range []struct {
		l    *DeployLayout
		tia  string
		want string
	}{
		{nil, "V21", "recorded no data layout"},
		{LayoutRecordOf(now, "V21"), "V22", "a TIA upgrade invalidates every recorded layout"},
		{&DeployLayout{Scheme: "attributes-v0", TIA: "V21"}, "V21", "a change of the scheme invalidates every recorded layout"},
	} {
		if b, why := LayoutBaselineOf("d-7", c.l, c.tia); b != nil || !strings.Contains(why, c.want) {
			t.Errorf("%+v: %+v %q, want %q", c.l, b, why, c.want)
		}
	}
	if !LayoutGateMissQuestion(QuestionStopCPU) || !LayoutGateMissQuestion(QuestionReinitialize) || !LayoutGateMissQuestion(QuestionAllBlocks) || LayoutGateMissQuestion(QuestionConsistentDownload) {
		t.Error("gate miss questions")
	}
}

// TestLayoutAddedKindsNeedNoConsent (#1347): an added OB, FB, FC, data
// block or PLC data type needs no consent; any other kind added, and any
// changed, removed or unreadable object, does; the why counts only what
// needs it; an adopt baseline names itself.
func TestLayoutAddedKindsNeedNoConsent(t *testing.T) {
	was := []LayoutObject{{Name: "Main", Kind: "block", Type: "OB", Hash: "m"}, {Name: "Cust_DB", Kind: "block", Type: "GlobalDB", Hash: "c"}}
	base := &LayoutBaseline{Deploy: AdoptBaselineID(was), Objects: was}
	now := append(slices.Clone(was),
		LayoutObject{Name: "naut_OB", Kind: "block", Type: "OB", Hash: "o"},
		LayoutObject{Name: "naut_FB", Kind: "block", Type: "FB", Hash: "f"},
		LayoutObject{Name: "naut_FB_DB", Kind: "block", Type: "InstanceDB", Hash: "i", Refs: []string{"naut_FB"}},
		LayoutObject{Name: "naut_FC", Kind: "block", Type: "FC", Hash: "x"},
		LayoutObject{Name: "naut_T", Kind: "block", Type: "GlobalDB", Hash: "t", Refs: []string{"naut_U"}},
		LayoutObject{Name: "naut_U", Kind: "type", Type: "PlcStruct", Hash: "u"})
	l := DiffLayout(now, base, "", "V21", false)
	if l.ResetDataNeeded || len(l.Changes) != 6 || len(l.DataBlocks) != 0 || l.BaselineFrom != BaselineFromAdopted ||
		!strings.Contains(l.Why, "unchanged since the adopted project ("+base.Deploy+")") || !strings.Contains(l.Why, "only adds 6 object(s)") {
		t.Fatalf("added only: %+v", l)
	}
	arr := append(slices.Clone(was), LayoutObject{Name: "naut_A", Kind: "block", Type: "ArrayDB", Hash: "a"})
	if l := DiffLayout(arr, base, "", "V21", false); !l.ResetDataNeeded {
		t.Fatalf("an added ArrayDB needs the consent: %+v", l)
	}
	for _, k := range LayoutAddedKeepsData {
		for _, ch := range []string{LayoutChanged, LayoutRemoved, LayoutUnreadable} {
			if !LayoutNeedsConsent(LayoutChange{Type: k, Change: ch}) {
				t.Fatalf("%s %s needs the consent", k, ch)
			}
		}
		if LayoutNeedsConsent(LayoutChange{Type: k, Change: LayoutAdded}) {
			t.Fatalf("%s added needs none", k)
		}
	}
	if got := strings.Join(LayoutAddedKeepsData, ","); got != "OB,FB,FC,GlobalDB,InstanceDB,PlcStruct" {
		t.Fatalf("the kinds the bench proved: %s", got)
	}
	changed := slices.Clone(now)
	changed[1].Hash = "c2"
	if l := DiffLayout(changed, base, "", "V21", false); !l.ResetDataNeeded || !strings.Contains(l.Why, "(1 object(s))") || strings.Join(l.DataBlocks, ",") != "Cust_DB" {
		t.Fatalf("added beside a change: %+v", l)
	}
	if l := DiffLayout(was, &LayoutBaseline{Deploy: "d-1", Objects: was}, "", "V21", false); l.BaselineFrom != BaselineFromDeploy || !strings.Contains(l.Why, "since deploy d-1") {
		t.Fatalf("a deploy's baseline: %+v", l)
	}
	if !strings.Contains(LayoutGateMiss("stop-cpu", base.Deploy), "since the adopted project ("+base.Deploy+")") {
		t.Fatal("the gate miss names the adopt baseline")
	}
	if UntouchedTouches("POST /v1/sessions/{id}/compare") || !UntouchedTouches("POST /v1/sessions/{id}/software") || UntouchedTouches("GET /v1/sessions") {
		t.Fatal("UntouchedTouches")
	}
}
