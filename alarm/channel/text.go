package channel

import (
	"fmt"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/alarm/escalate"
)

// clean strips control characters (a name from a manifest is treated as
// hostile: no line breaks into a header, no escape codes into a pager).
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == 0x2028 || r == 0x2029 {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func priorityLabel(m escalate.Message) string {
	return strings.ToUpper(m.Alarm.Priority.String())
}

func alarmName(m escalate.Message) string {
	n := clean(m.Alarm.Name)
	if n == "" {
		n = clean(m.Alarm.ID)
	}
	return n
}

func escalation(m escalate.Message) string {
	s := fmt.Sprintf("Escalation %s, level %d", clean(m.Policy), m.Level)
	if m.Repeat > 0 {
		s += fmt.Sprintf(", repeat %d", m.Repeat)
	}
	return s
}

func place(m escalate.Message) string {
	site, area := clean(m.Alarm.Site), clean(m.Alarm.Area)
	switch {
	case site != "" && area != "":
		return site + "/" + area
	case site != "":
		return site
	}
	return area
}

// subject is the email subject and the first words of every page.
func subject(m escalate.Message) string {
	return truncate("["+priorityLabel(m)+"] "+alarmName(m), 200)
}

// body is the email body, lines joined by \n.
func body(m escalate.Message) string {
	var b strings.Builder
	line := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }
	line("%s", subject(m))
	line("Alarm: %s", alarmName(m))
	line("Id: %s", clean(m.Alarm.ID))
	if p := place(m); p != "" {
		line("Where: %s", p)
	}
	line("State: %s", m.Alarm.State.String())
	if m.Alarm.ActiveMs > 0 {
		line("Active since: %s", time.UnixMilli(m.Alarm.ActiveMs).UTC().Format(time.RFC3339))
	}
	line("%s", escalation(m))
	if r := clean(m.Reason); r != "" {
		line("Note: %s", r)
	}
	line("")
	if m.AckURL != "" {
		line("Acknowledge it: %s", m.AckURL)
		line("(The link opens a page that asks you to confirm. It works once, for this occurrence, as you.)")
	} else {
		line("Acknowledge it in the HMI.")
	}
	return b.String()
}

// smsMax is the most an SMS page says; the ack link, when there is one,
// is kept whole and the words before it are cut to fit.
const smsMax = 300

// smsProseMin is the least prose an SMS keeps beside a long link.
const smsProseMin = 60

// smsText is one line, at most 300 characters.
func smsText(m escalate.Message) string {
	s := subject(m)
	if p := place(m); p != "" {
		s += " at " + p
	}
	s += ", " + m.Alarm.State.String() + ". " + escalation(m) + "."
	if r := clean(m.Reason); r != "" {
		s += " " + r + "."
	}
	if m.AckURL == "" {
		return truncate(s+" Acknowledge it in the HMI.", smsMax)
	}
	// The link is never cut (a cut link acknowledges nothing): the prose
	// gives way to it.
	return truncate(s, max(smsMax-1-len([]rune(m.AckURL)), smsProseMin)) + " " + m.AckURL
}

// voiceText is what the call says.
func voiceText(m escalate.Message) string {
	s := fmt.Sprintf("Alarm. Priority %s. %s. Escalation level %d.", strings.ToLower(priorityLabel(m)), alarmName(m), m.Level)
	s += " Acknowledge it in the HMI."
	return truncate(s, 300)
}
