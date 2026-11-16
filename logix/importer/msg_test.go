package importer

import (
	"fmt"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/internal/project"
)

// msgL5X is miniL5X with a flat UDT (the corpus shape: INT and REAL
// members) and MESSAGE tags, each "Name|MessageType|Remote|Local|Path|extra
// attributes".
func msgL5X(tags []string, msgs []string, rungs ...string) []byte {
	doc := string(miniL5X(tags, rungs...))
	doc = strings.Replace(doc, "<DataTypes/>", `<DataTypes>
<DataType Name="LineStatus" Family="NoFamily" Class="User">
<Members>
<Member Name="Flow" DataType="REAL" Dimension="0" Radix="Float" Hidden="false" ExternalAccess="Read/Write"/>
<Member Name="Count" DataType="INT" Dimension="0" Radix="Decimal" Hidden="false" ExternalAccess="Read/Write"/>
</Members>
</DataType>
</DataTypes>`, 1)
	var b strings.Builder
	for _, m := range msgs {
		f := strings.Split(m, "|")
		fmt.Fprintf(&b, `<Tag Name="%s" TagType="Base" DataType="MESSAGE" ExternalAccess="Read/Write">
<Data Format="Message">
<MessageParameters MessageType="%s" RemoteElement="%s" RequestedLength="1" ConnectedFlag="2" ConnectionPath="%s" CommTypeCode="0" LocalIndex="0" LocalElement="%s"%s/>
</Data>
</Tag>
`, f[0], f[1], f[2], f[4], f[3], f[5])
	}
	return []byte(strings.Replace(doc, "<Tags>\n", "<Tags>\n"+b.String(), 1))
}

// MSG imports as READ/WRITE on a channel per peer (James, 2026-11-16): the
// corpus's round-robin poll rung, its copy of the status word, and a write
// whose connection flags differ from the writer's defaults.
func TestMessagesImportAsReadWrite(t *testing.T) {
	tags := []string{"Step:DINT", "PollOn:BOOL", "Send:BOOL", "Ok:BOOL", "PollStat:INT", "Dwell:TIMER=2000",
		"Line2:LineStatus", "Setpoints:LineStatus"}
	msgs := []string{
		"PollLine2|CIP Data Table Read|LineStatusRemote|Line2|2, 192.0.2.12|",
		`PushSp|CIP Data Table Write|Setpoints|Setpoints|2, 192.0.2.13, 1, 2| CacheConnections="TRUE"`,
	}
	doc := msgL5X(tags, msgs,
		"XIC(Step.3)[XIC(PollOn) MSG(PollLine2) ,[XIC(PollLine2.DN) ,XIC(PollLine2.ER) ] TON(Dwell,?,?) ,[XIC(Dwell.DN) ,XIO(PollOn) ] OTL(Step.4) OTU(Step.3) ];",
		"MOVE(PollLine2.Flags,PollStat);",
		"XIC(PollStat.5)OTE(Ok);",
		"XIC(Send)MSG(PushSp);",
	)
	doc = []byte(strings.Replace(string(doc), `ConnectedFlag="2" ConnectionPath="2, 192.0.2.13, 1, 2"`, `ConnectedFlag="1" ConnectionPath="2, 192.0.2.13, 1, 2"`, 1))
	p := mustImport(t, doc)
	for _, n := range p.Notes {
		if n.Rung >= 0 {
			t.Errorf("note: %s", n)
		}
	}
	src := string(p.Files["Main.ld"])
	for _, want := range []string{
		": READ; (* controller-scoped in the export",
		": WRITE; (* controller-scoped in the export",
		"PollLine2:READ(ID := 'cip_192_0_2_12', VAR_1 := 'LineStatusRemote', RD_1 := Line2)",
		"[ PollLine2.NDR | PollLine2.ERROR ]",
		"{ PollStat := SEL(PollLine2.NDR, 0, 32) + SEL(PollLine2.ERROR, 0, 16) (* @logix MSG.Flags *) }",
		"Send PushSp:WRITE(ID := 'cip_192_0_2_13_s2', VAR_1 := 'Setpoints', SD_1 := Setpoints)",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("missing %q in\n%s", want, src)
		}
	}
	man := string(p.Files["nautilus.yaml"])
	for _, want := range []string{
		"channels:\n",
		"  - { name: cip_192_0_2_12, type: cip, host: 192.0.2.12 }\n",
		"  - { name: cip_192_0_2_13_s2, type: cip, host: 192.0.2.13, slot: 2 }\n",
		"    messages:\n      - { name: PushSp, connected-flag: \"1\", cache-connections: \"TRUE\" }\n",
		"    controller-scoped:\n",
		"      - { name: PollLine2 }\n",
	} {
		if !strings.Contains(man, want) {
			t.Errorf("nautilus.yaml lacks %q:\n%s", want, man)
		}
	}
	if strings.Contains(man, "name: PollLine2, connected") {
		t.Errorf("a message with the default flags has no messages: entry:\n%s", man)
	}
	// The imported project loads as `naut run` loads it, channels and all.
	fsys := fstest.MapFS{}
	for name, raw := range p.Files {
		fsys[name] = &fstest.MapFile{Data: raw}
	}
	if _, err := project.Load(fsys, ""); err != nil {
		t.Errorf("the imported project does not load: %v\n%s", err, src)
	}
}

// What a READ/WRITE cannot say stays refused, each by reason.
func TestMessagesRefusedByReason(t *testing.T) {
	tags := []string{"go:BOOL", "Line2:LineStatus", "n:DINT"}
	msgs := []string{
		`Sock|CIP Generic|||1, 2| ServiceCode="16#004b"`,
		"Hop|CIP Data Table Read|R|Line2|2, 192.0.2.1, 2, 192.0.2.2|",
		"Ok|CIP Data Table Read|OkRemote|Line2|2, 192.0.2.1|",
	}
	cases := map[string]string{
		"XIC(go)MSG(Sock);":  "CIP Generic",
		"XIC(go)MSG(Hop);":   "is not out the Ethernet port to one controller",
		"XIO(Ok.EN)MSG(Ok);": ".EN is TRUE while the rung is true",
		"XIC(Ok.ST)OTE(go);": ".ST has no READ/WRITE pin",
		"MOVE(Ok.EXERR,n);":  ".EXERR has no READ/WRITE pin",
	}
	for rung, want := range cases {
		p := mustImport(t, msgL5X(tags, msgs, rung))
		var got []string
		for _, n := range p.Notes {
			if n.Rung >= 0 {
				got = append(got, n.Why)
			}
		}
		if len(got) != 1 || !strings.Contains(got[0], want) {
			t.Errorf("%s: notes %q, want one with %q", rung, got, want)
		}
	}
	// More than one element is refused too.
	doc := strings.Replace(string(msgL5X(tags, msgs, "XIC(go)MSG(Ok);")), `RemoteElement="OkRemote" RequestedLength="1"`, `RemoteElement="OkRemote" RequestedLength="4"`, 1)
	p := mustImport(t, []byte(doc))
	if !strings.Contains(fmt.Sprint(p.Notes), "moves 4 elements") {
		t.Errorf("RequestedLength 4: notes %v", p.Notes)
	}
}

// An output branch that ends the rung keeps every leg that does something:
// a timer or a data box beside the coil leg is a rung of its own, in order.
// It used to be dropped without a note.
func TestLastBranchKeepsActingLegs(t *testing.T) {
	tags := []string{"a:BOOL", "y:BOOL", "n:DINT", "t:TIMER=1500"}
	p := mustImport(t, miniL5X(tags, "XIC(a)[XIC(t.DN) OTE(y) ,TON(t,?,?) ,MOVE(7,n) ];"))
	for _, n := range p.Notes {
		if n.Rung >= 0 {
			t.Errorf("note: %s", n)
		}
	}
	src := string(p.Files["Main.ld"])
	order := []string{"a t.Q ( y )", "a t:TON(PT := T#1500MS)", "a { n := 7 }"}
	last := -1
	for _, want := range order {
		i := strings.Index(src, want)
		if i < 0 {
			t.Fatalf("missing %q in\n%s", want, src)
		}
		if i < last {
			t.Errorf("%q out of order in\n%s", want, src)
		}
		last = i
	}
}


// The review's cases: two MSGs on one tag, a program tag shadowing a
// controller MESSAGE's local element, and a controller MESSAGE read from a
// program that does not send it.
func TestMessagesSharedAndShadowed(t *testing.T) {
	tags := []string{"go:BOOL", "b:BOOL", "Line2:LineStatus"}
	msgs := []string{"M|CIP Data Table Read|R|Line2|2, 192.0.2.1|"}

	p := mustImport(t, msgL5X(tags, msgs, "XIC(go)MSG(M);", "XIC(b)MSG(M);"))
	if s := fmt.Sprint(p.Notes); !strings.Contains(s, "is sent by 2 MSG instructions") {
		t.Errorf("two MSGs on M: notes %s", s)
	}

	doc := strings.Replace(string(msgL5X(tags, msgs, "XIC(go)MSG(M);")), "<Program Name=\"Main\" TestEdits=\"false\" MainRoutineName=\"MainRoutine\" Disabled=\"false\" UseAsFolder=\"false\">\n<Tags/>",
		"<Program Name=\"Main\" TestEdits=\"false\" MainRoutineName=\"MainRoutine\" Disabled=\"false\" UseAsFolder=\"false\">\n<Tags>\n<Tag Name=\"Line2\" TagType=\"Base\" DataType=\"LineStatus\" ExternalAccess=\"Read/Write\"/>\n</Tags>", 1)
	p = mustImport(t, []byte(doc))
	if s := fmt.Sprint(p.Notes); !strings.Contains(s, "this program has a tag of that name") {
		t.Errorf("shadowed local element: notes %s", s)
	}

	// A second program reads M.DN; only Main sends M.
	two := string(msgL5X(tags, msgs, "XIC(go)MSG(M);"))
	other := "<Program Name=\"Other\" TestEdits=\"false\" MainRoutineName=\"MainRoutine\" Disabled=\"false\" UseAsFolder=\"false\">\n<Tags/>\n<Routines>\n<Routine Name=\"MainRoutine\" Type=\"RLL\">\n<RLLContent>\n<Rung Number=\"0\" Type=\"N\">\n<Text>\n<![CDATA[XIC(M.DN)OTE(b);]]>\n</Text>\n</Rung>\n</RLLContent>\n</Routine>\n</Routines>\n</Program>\n</Programs>"
	two = strings.Replace(two, "</Programs>", other, 1)
	p = mustImport(t, []byte(two))
	if s := fmt.Sprint(p.Notes); !strings.Contains(s, "is sent by program Main") {
		t.Errorf("M.DN read from Other: notes %s", s)
	}
}

// A last output branch's one-shot or AFI leg stays a condition, as before
// the dropped-leg fix: the rung still imports.
func TestLastBranchConditionLegsStay(t *testing.T) {
	for _, rung := range []string{"XIC(a)[XIC(b) OTE(y) ,XIC(b) ONS(s) ];", "XIC(a)[XIC(b) OTE(y) ,AFI() ];"} {
		p := mustImport(t, miniL5X([]string{"a:BOOL", "b:BOOL", "y:BOOL", "s:BOOL"}, rung))
		for _, n := range p.Notes {
			if n.Rung >= 0 {
				t.Errorf("%s: note %s", rung, n)
			}
		}
		if !strings.Contains(string(p.Files["Main.ld"]), "a b ( y )") {
			t.Errorf("%s: imported as\n%s", rung, p.Files["Main.ld"])
		}
	}
}
