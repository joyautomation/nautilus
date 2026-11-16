package writer

import (
	"strings"
	"testing"
)

func msgProg(decls, rung string) string {
	return "PROGRAM Main\nVAR_EXTERNAL Go : BOOL; X : DINT; Y : BOOL; END_VAR\nVAR rd : READ; " + decls + " END_VAR\nLD\n  RUNG r0\n    " + rung + "\nEND_LD\nEND_PROGRAM\n"
}

var line2 = []Channel{{Name: "line2", Type: "cip", Host: "192.0.2.12"}}

// A READ on a rung is an MSG with its tag's parameters from the call and
// the channel.
func TestMessageWritten(t *testing.T) {
	src := msgProg("", "Go rd:READ(ID := 'LINE2', VAR_1 := 'Remote$'s', RD_1 := X) ( Y )")
	out, diags, err := Write(src, Options{Channels: line2})
	if err != nil || len(diags) > 0 {
		t.Fatalf("%v %v", err, diags)
	}
	doc := string(out)
	for _, want := range []string{
		"XIC(Go)MSG(rd)OTE(Y);",
		`<Tag Name="rd" TagType="Base" DataType="MESSAGE" ExternalAccess="Read/Write">`,
		`<MessageParameters MessageType="CIP Data Table Read" RemoteElement="Remote's" RequestedLength="1" ConnectedFlag="2" ConnectionPath="2, 192.0.2.12" CommTypeCode="0" LocalIndex="0" LocalElement="X"/>`,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("missing %q in\n%s", want, doc)
		}
	}
}

// What an MSG cannot say is a diagnostic, never a guess.
func TestMessageRefused(t *testing.T) {
	cases := []struct {
		decls, rung string
		chans       []Channel
		want        string
	}{
		{"", "Go rd:READ(ID := 'nope', VAR_1 := 'R', RD_1 := X)", line2, `names no channel`},
		{"", "Go rd:READ(ID := 'ua', VAR_1 := 'R', RD_1 := X)", []Channel{{Name: "ua", Type: "opcua"}}, "over a cip channel"},
		{"", "Go rd:READ(ID := 'line2', VAR_1 := 'R', RD_1 := X + 1)", line2, "the local element is a tag"},
		{"", "Go rd:READ(ID := 'line2', VAR_1 := 'R', RD_1 := X, NDR => Y)", line2, "read the block's outputs"},
		{"", "rd.BUSY ( Y )", line2, "BUSY has no MESSAGE bit"},
		{"", "Go ( Y )", line2, "no rung calls this READ or WRITE"},
		{"", "{ X := SEL(rd.NDR, 0, 32) (* @logix MSG.Flags *) }", line2, "@logix MSG.Flags pragma"},
	}
	for _, c := range cases {
		_, diags, err := Write(msgProg(c.decls, c.rung), Options{Channels: c.chans})
		if err != nil {
			t.Errorf("%s: %v", c.rung, err)
			continue
		}
		var got []string
		for _, d := range diags {
			got = append(got, d.Message)
		}
		if !strings.Contains(strings.Join(got, "\n"), c.want) {
			t.Errorf("%s: diags %q, want one with %q", c.rung, got, c.want)
		}
	}
}

// The rules check (naut check --target logix, the editor) has no manifest:
// a channel it cannot see is the manifest check's to report, not its own.
func TestMessageCheckWithoutManifest(t *testing.T) {
	diags, err := Check(msgProg("", "Go rd:READ(ID := 'line2', VAR_1 := 'R', RD_1 := X)"))
	if err != nil || len(diags) > 0 {
		t.Fatalf("%v %v", err, diags)
	}
}

// The #702 review's cases.
func TestMessageReviewCases(t *testing.T) {
	// A whole array moves whole: RequestedLength is its length, from [0].
	src := "PROGRAM Main\nVAR_EXTERNAL Go : BOOL; END_VAR\nVAR\n  rd : READ;\n  Buf : ARRAY [0..9] OF DINT;\nEND_VAR\nLD\n  RUNG r0\n    Go rd:READ(ID := 'line2', VAR_1 := 'Remote', RD_1 := Buf)\nEND_LD\nEND_PROGRAM\n"
	out, diags, err := Write(src, Options{Channels: line2})
	if err != nil || len(diags) > 0 {
		t.Fatalf("%v %v", err, diags)
	}
	if !strings.Contains(string(out), `RemoteElement="Remote" RequestedLength="10"`) || !strings.Contains(string(out), `LocalElement="Buf[0]"`) {
		t.Errorf("array message:\n%s", out)
	}

	cases := []struct {
		src   string
		chans []Channel
		want  string
	}{
		// A second call, even an identical one.
		{msgProg("", "Go rd:READ(ID := 'line2', VAR_1 := 'R', RD_1 := X)\n\n  RUNG r1\n    Y rd:READ(ID := 'line2', VAR_1 := 'R', RD_1 := X)"), line2, "called on another rung too"},
		// A channel on another port.
		{msgProg("", "Go rd:READ(ID := 'p', VAR_1 := 'R', RD_1 := X)"), []Channel{{Name: "p", Type: "cip", Host: "h", Port: 2222}}, "uses port 2222"},
		// Inside a FUNCTION_BLOCK.
		{"FUNCTION_BLOCK Poller\nVAR_INPUT Go : BOOL; END_VAR\nVAR\n  rd : READ;\n  X : DINT;\nEND_VAR\nLD\n  RUNG r0\n    Go rd:READ(ID := 'line2', VAR_1 := 'R', RD_1 := X)\nEND_LD\nEND_FUNCTION_BLOCK\n" +
			"PROGRAM Main\nVAR_EXTERNAL Go : BOOL; END_VAR\nVAR\n  p : Poller;\nEND_VAR\nLD\n  RUNG r0\n    p:Poller(Go := Go)\nEND_LD\nEND_PROGRAM\n", line2, "an Add-On Instruction cannot hold a MESSAGE tag"},
	}
	for _, c := range cases {
		_, diags, err := Write(c.src, Options{Channels: c.chans})
		if err != nil {
			t.Errorf("%v", err)
			continue
		}
		var got []string
		for _, d := range diags {
			got = append(got, d.Message)
		}
		if !strings.Contains(strings.Join(got, "\n"), c.want) {
			t.Errorf("diags %q, want one with %q", got, c.want)
		}
	}

	// In ST: the message refusal, not the generic subset list.
	diags, err = CheckST("PROGRAM Main\nVAR rd : READ; X : DINT; END_VAR\nrd(REQ := TRUE, ID := 'line2', VAR_1 := 'R', RD_1 := X);\nEND_PROGRAM\n")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range diags {
		found = found || (d.Rule == ruleMessage && strings.Contains(d.Message, "call it from a .ld program"))
	}
	if !found {
		t.Errorf("ST READ: diags %v", diags)
	}
}
