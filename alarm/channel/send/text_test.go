package send

import (
	"fmt"
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

// An SMS with a reply code (#715 inbound) says how to reply, and keeps the
// code and the link whole under the cut; the code alone replaces "in the
// HMI".
func TestSMSCarriesTheReplyCodeWhole(t *testing.T) {
	link := "https://hmi.example:8443/alarm-ack/" + strings.Repeat("B", 150)
	m := linkMessage(strings.Repeat("very long alarm name ", 30), link)
	m.AckCode = "12345678"
	s := smsText(m)
	if !strings.HasSuffix(s, " Reply ACK 12345678 to acknowledge. "+link) {
		t.Fatalf("sms: %s", s)
	}
	if n := len([]rune(s)); n > smsMax {
		t.Fatalf("sms is %d runes, more than %d", n, smsMax)
	}
	m.AckURL = ""
	s = smsText(m)
	if !strings.HasSuffix(s, " Reply ACK 12345678 to acknowledge.") || strings.Contains(s, "in the HMI") {
		t.Fatalf("code only: %s", s)
	}
}

// A call with a keypress URL wraps the message in a one-digit Gather
// posting to it; the URL is escaped (it lands in an attribute).
func TestVoiceGathersAKeypress(t *testing.T) {
	m := linkMessage("Tank <high>", "")
	if got := voiceTwiml(m); strings.Contains(got, "Gather") || !strings.Contains(got, "Acknowledge it in the HMI.") {
		t.Fatalf("no URL: %s", got)
	}
	m.AckURL = `https://hmi.example/alarm-inbound/voice/tok?a=1&b="2"`
	got := voiceTwiml(m)
	want := `<Response><Gather numDigits="1" timeout="10" method="POST" action="https://hmi.example/alarm-inbound/voice/tok?a=1&amp;b=&#34;2&#34;">` +
		`<Say loop="2">Alarm. Priority high. Tank &lt;high&gt;. Escalation level 2. Press 1 to acknowledge.</Say></Gather>` +
		`<Say>Not acknowledged. Acknowledge it in the HMI.</Say></Response>`
	if got != want {
		t.Fatalf("twiml\n got %s\nwant %s", got, want)
	}
}

// floodMessage is a grouped page listing n alarms.
func floodMessage(n int, link string) escalate.Message {
	g := &escalate.Group{
		Flood:    alarm.FloodBrief{ID: "f1", SinceMs: 1700000000000, Count: 23, WindowMs: 600000, Threshold: 10},
		FirstOut: []alarm.Brief{{ID: "a0", Name: "Seal leak", Priority: alarm.High}},
		Groups:   []alarm.FloodGroup{{Equipment: "P101", Count: 8}},
	}
	for i := range n {
		g.Alarms = append(g.Alarms, escalate.GroupAlarm{ID: fmt.Sprintf("a%d", i), Name: fmt.Sprintf("Pump %d discharge pressure low", i),
			Priority: alarm.Medium, Area: "north", State: alarm.UnackActive, ActiveMs: 1700000000000 + int64(i)})
	}
	return escalate.Message{Policy: "primary", Level: 1, To: "alice", Group: g, Link: link}
}

// A grouped page stays within each channel's limits at 200 alarms: the
// first ones by name, "+N more", and the link to the flood view whole.
// It offers no acknowledgement: no link to ack, no reply code, no
// keypress.
func TestGroupedPageFitsAt200(t *testing.T) {
	link := "https://hmi.example:8443/alarms?focus=1"
	m := floodMessage(200, link)
	s := smsText(m)
	if n := len([]rune(s)); n > smsMax {
		t.Fatalf("sms is %d runes, over %d: %s", n, smsMax, s)
	}
	if !strings.HasSuffix(s, " Open: "+link) || !strings.Contains(s, "200 alarms:") || !strings.Contains(s, "more.") ||
		!strings.Contains(s, "Pump 0 discharge") || strings.Contains(s, "ACK") {
		t.Fatalf("sms: %s", s)
	}
	v := voiceText(m)
	if n := len([]rune(v)); n > 300 {
		t.Fatalf("voice is %d runes: %s", n, v)
	}
	if !strings.Contains(v, "Open the alarm page in the HMI") || strings.Contains(v, "Press") {
		t.Fatalf("voice: %s", v)
	}
	if tw := voiceTwiml(m); strings.Contains(tw, "Gather") {
		t.Fatalf("a grouped call asks for a keypress: %s", tw)
	}
	b := body(m)
	if strings.Count(b, "discharge pressure low") != groupEmailMax || !strings.Contains(b, "+150 more, see "+link) ||
		!strings.Contains(b, "Open the flood view: "+link) || strings.Contains(b, "Acknowledge it:") {
		t.Fatalf("email body:\n%s", b)
	}
	if sub := subject(m); sub != "[FLOOD] Alarm flood: 23 in 10 min (limit 10)" {
		t.Fatalf("subject %q", sub)
	}
	// Few alarms: all named, no "more".
	if s := smsText(floodMessage(2, link)); strings.Contains(s, "more") || !strings.Contains(s, "Pump 1 discharge") {
		t.Fatalf("sms of two: %s", s)
	}
	// No link: the words say where to look, still within the limit.
	if s := smsText(floodMessage(200, "")); len([]rune(s)) > smsMax || !strings.HasSuffix(s, "Open the alarm page in the HMI.") {
		t.Fatalf("sms without a link: %s", s)
	}
}

// A chatter notice says it chatters and links to the alarm, never an
// ack link or code.
func TestChatterNoticeText(t *testing.T) {
	m := linkMessage("Seal leak", "")
	m.Chatter = &alarm.Chatter{SinceMs: 1700000000000, Activations: 7, WindowMs: 60000, Count: 3}
	m.Link = "https://hmi.example:8443/alarms?alarm=a1"
	if s := smsText(m); !strings.Contains(s, "is chattering: 7 times in 60 s") || !strings.HasSuffix(s, " "+m.Link) || strings.Contains(s, "ACK") {
		t.Fatalf("sms: %s", s)
	}
	if b := body(m); !strings.Contains(b, "Chattering: 7 times in 60 s") || !strings.Contains(b, m.Link) || strings.Contains(b, "Acknowledge it") {
		t.Fatalf("email:\n%s", b)
	}
	if v := voiceText(m); !strings.Contains(v, "chattering, 7 times in 60 seconds") || strings.Contains(v, "Press") {
		t.Fatalf("voice: %s", v)
	}
	if sub := subject(m); sub != "[HIGH] Seal leak is chattering" {
		t.Fatalf("subject %q", sub)
	}
}

// A webhook tells the kinds apart: a flood has no alarm, a chatter notice
// carries its chatter.
func TestWebhookKinds(t *testing.T) {
	f := webhookPayload(floodMessage(3, "https://h/alarms?focus=1"))
	if f.Kind != "flood" || f.Alarm != nil || f.Flood == nil || len(f.Flood.Alarms) != 3 || f.Link == "" {
		t.Fatalf("flood payload %+v", f)
	}
	if big := webhookPayload(floodMessage(800, "")); len(big.Flood.Alarms) != webhookFloodMax {
		t.Fatalf("flood payload lists %d, want the cap", len(big.Flood.Alarms))
	}
	m := linkMessage("Seal leak", "")
	m.Chatter = &alarm.Chatter{Activations: 4, WindowMs: 60000}
	if c := webhookPayload(m); c.Kind != "chatter" || c.Alarm == nil || c.Chatter == nil {
		t.Fatalf("chatter payload %+v", c)
	}
	if a := webhookPayload(linkMessage("x", "")); a.Kind != "alarm" || a.Alarm == nil || a.Chatter != nil || a.Flood != nil {
		t.Fatalf("alarm payload %+v", a)
	}
}

// fitList never passes its limit beside a tail that leaves room for
// prose, whatever the head and the names (the Fable review found the
// count-only case 10 runes over).
func TestFitListNeverOverflows(t *testing.T) {
	for n := 1; n <= 120; n += 7 {
		items := make([]escalate.GroupAlarm, n)
		for i := range items {
			items[i] = escalate.GroupAlarm{ID: strings.Repeat("x", 5+i%70)}
		}
		for hl := 0; hl < 330; hl += 3 {
			for _, tl := range []int{0, 30, 80, 200} {
				s := fitList(strings.Repeat("h", hl), items, func(a escalate.GroupAlarm) string { return a.ID }, strings.Repeat("t", tl), 300)
				if l := len([]rune(s)); l > 300 {
					t.Fatalf("n=%d head=%d tail=%d: %d runes: %q", n, hl, tl, l, s)
				}
				if strings.Contains(s, ": +") {
					t.Fatalf("a list of none: %q", s)
				}
			}
		}
	}
}
