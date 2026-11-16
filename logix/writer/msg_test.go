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
