package channel

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/alarm/escalate"
)

func linkMessage(name, link string) escalate.Message {
	return escalate.Message{
		Alarm: alarm.Record{
			Def:   alarm.Def{ID: "a1", Name: name, Priority: alarm.High, Site: "plant", Area: "north"},
			State: alarm.UnackActive,
		},
		Policy: "primary", Level: 2, To: "alice", AckURL: link,
	}
}

// The ack link (#715) replaces "acknowledge it in the HMI" in an email
// and an SMS; without one the page says what it always did.
func TestPagesCarryTheAckLink(t *testing.T) {
	link := "https://hmi.example:8443/alarm-ack/" + strings.Repeat("A", 140)
	m := linkMessage("Tank high", link)
	if b := body(m); !strings.Contains(b, "Acknowledge it: "+link+"\n") || strings.Contains(b, "in the HMI") {
		t.Fatalf("email body:\n%s", b)
	}
	if s := smsText(m); !strings.HasSuffix(s, " "+link) || strings.Contains(s, "in the HMI") {
		t.Fatalf("sms: %s", s)
	}
	m.AckURL = ""
	if !strings.Contains(body(m), "Acknowledge it in the HMI.") || !strings.HasSuffix(smsText(m), "Acknowledge it in the HMI.") {
		t.Fatal("no link: the page must send the person to the HMI")
	}
}

// A long alarm name gives way to the link: an SMS never cuts the link
// (a cut link acknowledges nothing), and stays within smsMax.
func TestSMSKeepsTheLinkWhole(t *testing.T) {
	link := "https://hmi.example:8443/alarm-ack/" + strings.Repeat("B", 150)
	s := smsText(linkMessage(strings.Repeat("very long alarm name ", 30), link))
	if !strings.HasSuffix(s, " "+link) {
		t.Fatalf("link cut: %s", s)
	}
	if n := len([]rune(s)); n > smsMax {
		t.Fatalf("sms is %d runes, more than %d", n, smsMax)
	}
	if !strings.Contains(s, "…") {
		t.Fatalf("prose not cut to fit: %s", s)
	}
	// A link longer than the room leaves smsProseMin of prose, whole link.
	huge := "https://hmi.example/alarm-ack/" + strings.Repeat("C", 400)
	s = smsText(linkMessage("x "+strings.Repeat("y", 200), huge))
	if !strings.HasSuffix(s, " "+huge) {
		t.Fatal("huge link cut")
	}
}
