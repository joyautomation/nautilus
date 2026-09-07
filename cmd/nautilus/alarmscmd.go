// `nautilus alarms list`: dump the definitions a manifest's rules and
// alarm files actually expand to.
//
// This is the auditable half of the rule model. Fourteen rules covering
// two thousand alarms is a good trade only if you can see the two
// thousand, and diff them against whatever generated the tag set — so the
// output is YAML a manifest could take back verbatim, and it is sorted by
// id, so two runs of the same project produce byte-identical text.

package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/runtime"
)

const alarmsUsage = `nautilus alarms — inspect a project's alarm definitions

Usage:
  nautilus alarms list [dir]   Expand the manifest's rules and alarm files
                               and print every definition, sorted by id.
                               -count prints only the total; -site and
                               -priority narrow the list; -o yaml|table
                               picks the format (default table).
`

func runAlarms(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, alarmsUsage)
		return 2
	}
	switch args[0] {
	case "list":
		return runAlarmsList(args[1:])
	case "help", "--help", "-h":
		fmt.Print(alarmsUsage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "nautilus alarms: unknown command %q\n\n%s", args[0], alarmsUsage)
		return 2
	}
}

func runAlarmsList(args []string) int {
	fs := flag.NewFlagSet("alarms list", flag.ContinueOnError)
	manifest := fs.String("m", "", manifestFlagUsage)
	format := fs.String("o", "table", "output format: table or yaml")
	site := fs.String("site", "", "only definitions at this site")
	priority := fs.String("priority", "", "only definitions at this priority")
	count := fs.Bool("count", false, "print only the number of definitions")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir := "."
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
	}

	proj, err := project.Load(os.DirFS(dir), *manifest)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nautilus alarms:", err)
		return 1
	}
	if proj.Alarms == nil {
		fmt.Fprintf(os.Stderr, "nautilus alarms: %s declares no `alarms:` section\n", manifestLabel(*manifest))
		return 1
	}
	// A rule matches a struct TYPE and a member, so the definitions cannot
	// be known until the project's TYPE table has compiled. Nothing is
	// opened and no engine is built — this is the same offline pass
	// `nautilus check` runs.
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nautilus alarms: compile:", err)
		return 1
	}
	defs, err := proj.AlarmDefs(rt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nautilus alarms:", err)
		return 1
	}
	defs = filterDefs(defs, *site, *priority)

	if *count {
		fmt.Println(len(defs))
		return 0
	}
	switch *format {
	case "yaml":
		enc := yaml.NewEncoder(os.Stdout)
		enc.SetIndent(2)
		if err := enc.Encode(defs); err != nil {
			fmt.Fprintln(os.Stderr, "nautilus alarms:", err)
			return 1
		}
		if err := enc.Close(); err != nil {
			fmt.Fprintln(os.Stderr, "nautilus alarms:", err)
			return 1
		}
		return 0
	default:
		printAlarmTable(defs)
		return 0
	}
}

func filterDefs(defs []alarm.Def, site, priority string) []alarm.Def {
	if site == "" && priority == "" {
		return defs
	}
	out := defs[:0]
	for _, d := range defs {
		if site != "" && !strings.EqualFold(d.Site, site) {
			continue
		}
		if priority != "" && !strings.EqualFold(d.Priority.String(), priority) {
			continue
		}
		out = append(out, d)
	}
	return out
}

// printAlarmTable is the human format: id, priority, site, the condition
// path, and the name an operator would read. Column widths come from the
// data, so a fleet's ids line up and a one-alarm project does not print
// forty spaces.
func printAlarmTable(defs []alarm.Def) {
	if len(defs) == 0 {
		fmt.Println("no alarm definitions")
		return
	}
	w := func(get func(alarm.Def) string, head string) int {
		n := len(head)
		for _, d := range defs {
			if l := len(get(d)); l > n {
				n = l
			}
		}
		return n
	}
	id := func(d alarm.Def) string { return d.ID }
	pri := func(d alarm.Def) string { return d.Priority.String() }
	site := func(d alarm.Def) string { return d.Site }
	tag := func(d alarm.Def) string { return d.Tag }
	wID, wPri, wSite, wTag := w(id, "ID"), w(pri, "PRIORITY"), w(site, "SITE"), w(tag, "CONDITION")
	fmt.Printf("%-*s  %-*s  %-*s  %-*s  %s\n", wID, "ID", wPri, "PRIORITY", wSite, "SITE", wTag, "CONDITION", "NAME")
	for _, d := range defs {
		fmt.Printf("%-*s  %-*s  %-*s  %-*s  %s\n", wID, d.ID, wPri, d.Priority, wSite, d.Site, wTag, d.Tag, d.Name)
	}
	fmt.Printf("\n%d definitions\n", len(defs))
	byPriority := map[string]int{}
	for _, d := range defs {
		byPriority[d.Priority.String()]++
	}
	keys := make([]string, 0, len(byPriority))
	for k := range byPriority {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", k, byPriority[k]))
	}
	fmt.Println(strings.Join(parts, ", "))
}
