package main

// `naut alarms oncall` and `naut alarms escalation` (#715): read a
// manifest's on-call rosters and escalation policies offline, the way
// `naut alarms list` reads its definitions. Nothing is sent.

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/alarm/escalate"
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/runtime"
)

// parseAt reads -at: "" is now, else RFC3339, or a local "YYYY-MM-DD HH:MM"
// in the rosters' zone.
func parseAt(s string, loc *time.Location) (time.Time, error) {
	if s == "" {
		return time.Now(), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04", s, loc); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("-at %q: want RFC3339 or \"YYYY-MM-DD HH:MM\" (in alarms.oncall.timezone)", s)
}

// loadEscalation loads dir's manifest and its prepared escalation config.
func loadEscalation(dir, manifest string) (*project.Project, *escalate.Config, error) {
	proj, err := project.Load(os.DirFS(dir), manifest)
	if err != nil {
		return nil, nil, err
	}
	if proj.Alarms == nil {
		return nil, nil, fmt.Errorf("%s declares no `alarms:` section", manifestLabel(manifest))
	}
	c, err := proj.Alarms.EscalationConfig()
	if err != nil {
		return nil, nil, err
	}
	if c == nil {
		return nil, nil, fmt.Errorf("%s declares no alarms.escalation policies (see the alarm escalation guide)", manifestLabel(manifest))
	}
	return proj, c, nil
}

func runAlarmsOncall(args []string) int {
	fs := flag.NewFlagSet("alarms oncall", flag.ContinueOnError)
	manifest := fs.String("m", "", manifestFlagUsage)
	at := fs.String("at", "", `the moment to ask about: RFC3339, or "YYYY-MM-DD HH:MM" in the rosters' zone (default now)`)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir := "."
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
	}
	_, c, err := loadEscalation(dir, *manifest)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut alarms oncall:", err)
		return 1
	}
	t, err := parseAt(*at, c.Oncall.Location())
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut alarms oncall:", err)
		return 2
	}
	printOncall(os.Stdout, c, t)
	return 0
}

func printOncall(w io.Writer, c *escalate.Config, t time.Time) {
	loc := c.Oncall.Location()
	fmt.Fprintf(w, "on call at %s (%s)\n\n", t.In(loc).Format("Mon 2006-01-02 15:04"), loc)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ROSTER\tKIND\tON CALL")
	for i := range c.Oncall.Rosters {
		r := &c.Oncall.Rosters[i]
		kind := "everyone"
		switch {
		case len(r.Shifts) > 0:
			kind = fmt.Sprintf("%d shifts", len(r.Shifts))
		case r.Rotate != "":
			kind = r.Rotate
		}
		who := strings.Join(r.OnCall(t, loc), ", ")
		if who == "" {
			who = "(nobody)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", r.ID, kind, who)
	}
	tw.Flush()
}

func runAlarmsEscalation(args []string) int {
	fs := flag.NewFlagSet("alarms escalation", flag.ContinueOnError)
	manifest := fs.String("m", "", manifestFlagUsage)
	id := fs.String("id", "", "one alarm: print the steps its policy would send if it began waiting at -at")
	at := fs.String("at", "", `when the alarm begins waiting: RFC3339, or "YYYY-MM-DD HH:MM" in the rosters' zone (default now)`)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir := "."
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
	}
	proj, c, err := loadEscalation(dir, *manifest)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut alarms escalation:", err)
		return 1
	}
	t, err := parseAt(*at, c.Oncall.Location())
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut alarms escalation:", err)
		return 2
	}
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut alarms escalation: compile:", err)
		return 1
	}
	defs, err := proj.AlarmDefs(rt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut alarms escalation:", err)
		return 1
	}
	if *id == "" {
		printCoverage(os.Stdout, c, defs, t)
		return 0
	}
	for _, d := range defs {
		if d.ID == *id {
			printSteps(os.Stdout, c, d, t)
			return 0
		}
	}
	fmt.Fprintf(os.Stderr, "naut alarms escalation: no alarm %q (naut alarms list shows them)\n", *id)
	return 1
}

// escalationNoAckWarnings names, per policy, the alarms its match covers
// that have ack-required: false. Such an alarm never waits on a person, so
// today it never pages, which surprises whoever wrote the policy for it.
func escalationNoAckWarnings(c *escalate.Config, defs []alarm.Def) []string {
	var out []string
	for i := range c.Policies {
		p := &c.Policies[i]
		var ids []string
		for _, d := range defs {
			if !d.AckRequired && p.Match.Covers(d) {
				ids = append(ids, d.ID)
			}
		}
		if len(ids) == 0 {
			continue
		}
		shown := ids
		if len(shown) > 3 {
			shown = append(shown[:3:3], fmt.Sprintf("and %d more", len(ids)-3))
		}
		out = append(out, fmt.Sprintf("alarms.escalation %q matches %d alarm(s) with ack-required: false (%s): "+
			"they never wait on an ack, so this policy never pages for them; set ack-required: true on them, "+
			"or narrow the policy's match", p.ID, len(ids), strings.Join(shown, ", ")))
	}
	return out
}

// printCoverage counts the alarms each policy would run, and the ones no
// policy covers, for an alarm that began waiting at t.
func printCoverage(w io.Writer, c *escalate.Config, defs []alarm.Def, t time.Time) {
	count := map[string]int{}
	none, noAck := 0, 0
	for _, d := range defs {
		if !d.AckRequired {
			noAck++ // never waits on a person, so never pages
			continue
		}
		if p := c.Select(d, t); p != nil {
			count[p.ID]++
		} else {
			none++
		}
	}
	loc := c.Oncall.Location()
	fmt.Fprintf(w, "for an alarm that begins waiting at %s (%s)\n\n", t.In(loc).Format("Mon 2006-01-02 15:04"), loc)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "POLICY\tALARMS\tLEVELS")
	for _, p := range c.Policies {
		fmt.Fprintf(tw, "%s\t%d\t%d\n", p.ID, count[p.ID], len(p.Levels))
	}
	fmt.Fprintf(tw, "(none: pages nobody)\t%d\t\n", none)
	fmt.Fprintf(tw, "(ack not required: never pages)\t%d\t\n", noAck)
	tw.Flush()
}

// printSteps prints what alarm d's policy would send if it began waiting
// at t and nobody acknowledged it: each step's time, level, people (with
// rosters read at that time) and channels.
func printSteps(w io.Writer, c *escalate.Config, d alarm.Def, t time.Time) {
	p := c.Select(d, t)
	loc := c.Oncall.Location()
	if !d.AckRequired {
		fmt.Fprintf(w, "%s: ack-required is false, so it never waits on a person: it pages nobody\n", d.ID)
		return
	}
	if p == nil {
		fmt.Fprintf(w, "%s: no policy covers it at %s: it pages nobody\n", d.ID, t.In(loc).Format("Mon 2006-01-02 15:04"))
		return
	}
	fmt.Fprintf(w, "%s: policy %s (stop: %s), if it begins waiting at %s (%s) and nobody acknowledges it\n\n",
		d.ID, p.ID, p.Stop, t.In(loc).Format("Mon 2006-01-02 15:04"), loc)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "AFTER\tAT\tLEVEL\tWHO\tVIA")
	for _, s := range p.Steps() {
		when := t.Add(s.At)
		l := p.Levels[s.Level]
		var who []string
		for _, n := range l.Notify {
			if rid, ok := strings.CutPrefix(n, escalate.RosterPrefix); ok {
				people := c.Oncall.Roster(rid).OnCall(when, loc)
				if len(people) == 0 {
					who = append(who, n+" (nobody on call)")
				} else {
					who = append(who, n+" ("+strings.Join(people, ", ")+")")
				}
				continue
			}
			who = append(who, n)
		}
		level := fmt.Sprintf("%d", s.Level+1)
		if s.Repeat > 0 {
			level += fmt.Sprintf(" (repeat %d)", s.Repeat)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", s.At, when.In(loc).Format("15:04"), level,
			strings.Join(who, "; "), strings.Join(l.Via, ", "))
	}
	tw.Flush()
}
