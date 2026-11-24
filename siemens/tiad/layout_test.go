package tiad

import (
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
