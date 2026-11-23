package send

import (
	"fmt"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/alarm"
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
	switch {
	case m.Group != nil:
		return truncate("[FLOOD] "+floodHead(m.Group), 200)
	case m.Chatter != nil:
		return truncate("["+priorityLabel(m)+"] "+alarmName(m)+" is chattering", 200)
	}
	return truncate("["+priorityLabel(m)+"] "+alarmName(m), 200)
}

// body is the email body, lines joined by \n.
func body(m escalate.Message) string {
	if m.Group != nil {
		return groupBody(m)
	}
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
	if c := m.Chatter; c != nil {
		line("Chattering: %s, since %s", chatterRate(c), time.UnixMilli(c.SinceMs).UTC().Format(time.RFC3339))
		line("Each level of this escalation goes once while it chatters, not once per bounce.")
	}
	line("%s", escalation(m))
	if r := clean(m.Reason); r != "" {
		line("Note: %s", r)
	}
	line("")
	if m.Chatter != nil {
		if m.Link != "" {
			line("Open it (shelve it there with a reason): %s", m.Link)
		} else {
			line("Open the alarm page in the HMI to shelve it with a reason.")
		}
		return b.String()
	}
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
	switch {
	case m.Group != nil:
		return groupSMS(m)
	case m.Chatter != nil:
		s := "[" + priorityLabel(m) + "] " + alarmName(m)
		if p := place(m); p != "" {
			s += " at " + p
		}
		s += " is chattering: " + chatterRate(m.Chatter) + ". " + escalation(m) + "."
		if r := clean(m.Reason); r != "" {
			s += " " + r + "."
		}
		if m.Link == "" {
			return truncate(s+" Open the alarm page in the HMI.", smsMax)
		}
		tail := " " + m.Link
		return truncate(s, max(smsMax-len([]rune(tail)), smsProseMin)) + tail
	}
	s := subject(m)
	if p := place(m); p != "" {
		s += " at " + p
	}
	s += ", " + m.Alarm.State.String() + ". " + escalation(m) + "."
	if r := clean(m.Reason); r != "" {
		s += " " + r + "."
	}
	// What acknowledges is never cut (a cut link or code acknowledges
	// nothing): the prose gives way to it.
	var tail string
	if m.AckCode != "" {
		tail = " Reply ACK " + m.AckCode + " to acknowledge."
	}
	if m.AckURL != "" {
		tail += " " + m.AckURL
	}
	if tail == "" {
		return truncate(s+" Acknowledge it in the HMI.", smsMax)
	}
	return truncate(s, max(smsMax-len([]rune(tail)), smsProseMin)) + tail
}

// voiceText is what the call says: with a keypress URL (inbound, #715),
// how to acknowledge by pressing 1.
func voiceText(m escalate.Message) string {
	switch {
	case m.Group != nil:
		return groupVoice(m)
	case m.Chatter != nil:
		s := fmt.Sprintf("Alarm. Priority %s. %s is chattering, %d times in %s. Escalation level %d.",
			strings.ToLower(priorityLabel(m)), alarmName(m), m.Chatter.Activations, spoken(m.Chatter.WindowMs), m.Level)
		return truncate(s, 260) + " Open the alarm page in the HMI."
	}
	s := fmt.Sprintf("Alarm. Priority %s. %s. Escalation level %d.", strings.ToLower(priorityLabel(m)), alarmName(m), m.Level)
	if m.AckURL != "" {
		return truncate(s, 270) + " Press 1 to acknowledge."
	}
	s += " Acknowledge it in the HMI."
	return truncate(s, 300)
}

// span is a window as a page says it: "60 s", "90 s", "10 min", "1 h 30 min".
func span(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	switch {
	case d%time.Hour == 0 && d >= time.Hour:
		return fmt.Sprintf("%d h", d/time.Hour)
	case d%time.Minute == 0 && d >= 2*time.Minute:
		if d >= time.Hour {
			return fmt.Sprintf("%d h %d min", d/time.Hour, (d%time.Hour)/time.Minute)
		}
		return fmt.Sprintf("%d min", d/time.Minute)
	case d%time.Second == 0:
		return fmt.Sprintf("%d s", d/time.Second)
	}
	return d.String()
}

// spoken is span for a voice: words, not abbreviations.
func spoken(ms int64) string {
	s := span(ms)
	s = strings.ReplaceAll(s, " min", " minutes")
	s = strings.ReplaceAll(s, " h", " hours")
	return strings.ReplaceAll(s, " s", " seconds")
}

func chatterRate(c *alarm.Chatter) string {
	return fmt.Sprintf("%d times in %s", c.Activations, span(c.WindowMs))
}

// floodHead is a flood in a few words.
func floodHead(g *escalate.Group) string {
	return fmt.Sprintf("Alarm flood: %d in %s (limit %d)", g.Flood.Count, span(g.Flood.WindowMs), g.Flood.Threshold)
}

func groupAlarmName(a escalate.GroupAlarm) string {
	n := clean(a.Name)
	if n == "" {
		n = clean(a.ID)
	}
	return n
}

// groupLine is one listed alarm in an email.
func groupLine(a escalate.GroupAlarm) string {
	s := "[" + strings.ToUpper(a.Priority.String()) + "] " + groupAlarmName(a)
	if id := clean(a.ID); id != groupAlarmName(a) {
		s += " (" + id + ")"
	}
	if ar := clean(a.Area); ar != "" {
		s += " in " + ar
	}
	if a.ActiveMs > 0 {
		s += ", active since " + time.UnixMilli(a.ActiveMs).UTC().Format("15:04:05Z")
	}
	if a.Chattering {
		s += ", chattering"
	}
	return truncate(s, 300)
}

// groupEmailMax is how many alarms an email lists by name.
const groupEmailMax = 50

// groupBody is a grouped page's email body: the flood, the alarms it is
// about, the first out and the equipment, and where to look. No ack link:
// a grouped page acknowledges nothing.
func groupBody(m escalate.Message) string {
	g := m.Group
	var b strings.Builder
	line := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }
	line("%s", subject(m))
	line("%s", floodHead(g))
	line("Flood since: %s", time.UnixMilli(g.Flood.SinceMs).UTC().Format(time.RFC3339))
	line("%s", escalation(m))
	if r := clean(m.Reason); r != "" {
		line("Note: %s", r)
	}
	line("")
	line("%d %s waiting on a person at this level:", len(g.Alarms), plural(len(g.Alarms), "alarm", "alarms"))
	for i, a := range g.Alarms {
		if i == groupEmailMax {
			line("  +%d more, see %s", len(g.Alarms)-i, linkOr(m, "the alarm page in the HMI"))
			break
		}
		line("  %s", groupLine(a))
	}
	if len(g.FirstOut) > 0 {
		var fo []string
		for _, a := range g.FirstOut {
			n := clean(a.Name)
			if n == "" {
				n = clean(a.ID)
			}
			fo = append(fo, n)
		}
		line("First out: %s", truncate(strings.Join(fo, ", "), 300))
	}
	if len(g.Groups) > 0 {
		var gs []string
		for _, x := range g.Groups {
			e := clean(x.Equipment)
			if e == "" {
				e = "(no equipment)"
			}
			gs = append(gs, fmt.Sprintf("%s (%d)", e, x.Count))
		}
		line("Equipment: %s", truncate(strings.Join(gs, ", "), 300))
	}
	line("")
	line("Open the flood view: %s", linkOr(m, "the alarm page in the HMI"))
	line("(One page for these alarms. Acknowledge them in the HMI.)")
	return b.String()
}

func linkOr(m escalate.Message, or string) string {
	if m.Link != "" {
		return m.Link
	}
	return or
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// groupSMS is a grouped page in one SMS line of at most smsMax: the
// flood, as many alarms as fit, "+N more", and the link kept whole.
func groupSMS(m escalate.Message) string {
	g := m.Group
	head := "[FLOOD] " + floodHead(g) + ". " + escalation(m) + "."
	if r := clean(m.Reason); r != "" {
		head += " " + r + "."
	}
	tail := " Open the alarm page in the HMI."
	if m.Link != "" {
		tail = " Open: " + m.Link
	}
	return fitList(head, g.Alarms, func(a escalate.GroupAlarm) string {
		return groupAlarmName(a) + " [" + strings.ToUpper(a.Priority.String()) + "]"
	}, tail, smsMax)
}

// fitList joins head, as many items as fit in limit runes beside tail
// (then "+N more" for the rest), and tail. The tail (a link) is never
// cut; the head is cut to what is left beside it.
func fitList(head string, items []escalate.GroupAlarm, name func(escalate.GroupAlarm) string, tail string, limit int) string {
	room := max(limit-len([]rune(tail)), smsProseMin)
	s := truncate(head, room)
	n := len(items)
	if n == 0 {
		return s + tail
	}
	room-- // the list's closing "."
	intro := fmt.Sprintf(" %d %s:", n, plural(n, "alarm", "alarms"))
	if len([]rune(s+intro)) > room {
		return s + tail
	}
	bare := s
	s += intro
	shown := 0
	for i, a := range items {
		piece := " " + truncate(name(a), 60)
		if i > 0 {
			piece = "," + piece
		}
		more := ""
		if rest := n - i - 1; rest > 0 {
			more = fmt.Sprintf(", +%d more", rest)
		}
		if len([]rune(s+piece+more)) > room {
			break
		}
		s += piece
		shown++
	}
	switch {
	case shown == n:
	case shown == 0:
		// Not one name fits: the count alone (shorter than the intro,
		// which fit).
		s = bare + fmt.Sprintf(" %d %s", n, plural(n, "alarm", "alarms"))
	default:
		s += fmt.Sprintf(", +%d more", n-shown)
	}
	return s + "." + tail
}

// groupVoice is what a grouped call says: the flood and the first alarms,
// then to open the alarm page. It asks for no keypress (the page binds no
// occurrence).
func groupVoice(m escalate.Message) string {
	g := m.Group
	head := fmt.Sprintf("Alarm flood. %d alarms in %s. Escalation level %d.", g.Flood.Count, spoken(g.Flood.WindowMs), m.Level)
	return fitList(head, g.Alarms, func(a escalate.GroupAlarm) string {
		return strings.ToLower(a.Priority.String()) + " " + groupAlarmName(a)
	}, " Open the alarm page in the HMI.", 300)
}
