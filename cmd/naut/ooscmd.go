package main

// naut alarms out-of-service | in-service | oos-list (#715 PR 5): take a
// piece of equipment's alarms out of service on a running controller,
// put it back, and list what is out.

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/internal/apiclient"
)

const alarmsOOSUsage = `naut alarms out-of-service — hide a piece of equipment's alarms while it is worked on

Usage:
  naut alarms out-of-service <equipment> --host <controller> (--for 4h | --until <RFC3339>) --reason "<why>"
  naut alarms in-service <equipment> --host <controller>
  naut alarms oos-list --host <controller>

Every alarm of the equipment (an alarm's equipment: field; by default the
tag its condition is in, so Pump_101.Fault and Pump_101.Overload are
Pump_101's) is suppressed until the window ends, a week at most, with the
reason on each row. There is no permanent out of service: when the window
ends the alarms are evaluated again at once, and a condition still present
annunciates, unacknowledged. Take it out again to renew.

Out of service needs the out-of-service permission over every alarm it
hides, and a fresh password (NAUTILUS_TOKEN or a token is always fresh).
Back in service needs only ack. Both are journalled.

Flags:
  --host HOST    the controller (required)
  --port N       its tag API port (default 8080)
`

// runAlarmsOOS is naut alarms out-of-service|in-service|oos-list.
func runAlarmsOOS(which string, args []string) int {
	if len(args) > 0 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h") {
		fmt.Print(alarmsOOSUsage)
		return 0
	}
	var eq string
	if which != "oos-list" {
		if len(args) == 0 || strings.HasPrefix(args[0], "-") {
			fmt.Fprintf(os.Stderr, "naut alarms %s: name the equipment first\n\n%s", which, alarmsOOSUsage)
			return 2
		}
		eq, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("alarms "+which, flag.ContinueOnError)
	host := fs.String("host", "", "controller hostname or IP")
	port := fs.Int("port", 8080, "tag API port")
	dur := fs.Duration("for", 0, "how long (out-of-service)")
	until := fs.String("until", "", "when it ends, RFC3339 (out-of-service)")
	reason := fs.String("reason", "", "why (out-of-service, required)")
	fs.Usage = func() { fmt.Fprint(os.Stderr, alarmsOOSUsage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "naut alarms %s: unexpected argument %q\n", which, fs.Arg(0))
		return 2
	}
	if *host == "" {
		fmt.Fprintf(os.Stderr, "naut alarms %s: --host is required\n", which)
		return 2
	}
	base, err := apiclient.BaseURL(*host, *port)
	if err != nil {
		fmt.Fprintf(os.Stderr, "naut alarms %s: %v\n", which, err)
		return 2
	}
	switch which {
	case "oos-list":
		return oosList(base)
	case "in-service":
		return oosPost(which, base+"/api/alarms/in-service", map[string]any{"equipment": eq})
	}
	body := map[string]any{"equipment": eq, "reason": *reason}
	switch {
	case *dur > 0 && *until != "":
		fmt.Fprintln(os.Stderr, "naut alarms out-of-service: --for or --until, not both")
		return 2
	case *dur > 0:
		body["seconds"] = dur.Seconds()
	case *until != "":
		body["until"] = *until
	default:
		fmt.Fprintln(os.Stderr, "naut alarms out-of-service: say how long: --for 4h or --until <RFC3339> (there is no permanent out of service)")
		return 2
	}
	if strings.TrimSpace(*reason) == "" {
		fmt.Fprintln(os.Stderr, `naut alarms out-of-service: --reason is required: what is being done to the equipment (shown on each alarm it hides)`)
		return 2
	}
	return oosPost(which, base+"/api/alarms/out-of-service", body)
}

// oosDo sends req and returns the body of a 200, or prints the refusal.
func oosDo(which string, req *http.Request) ([]byte, bool) {
	apiclient.Authorize(req)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "naut alarms %s: no controller at %s: %v\n", which, req.URL, err)
		return nil, false
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "naut alarms %s: %s returned %s: %s\n", which, req.URL, resp.Status, strings.TrimSpace(string(out)))
		return nil, false
	}
	return out, true
}

func oosPost(which, url string, body map[string]any) int {
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		fmt.Fprintf(os.Stderr, "naut alarms %s: %v\n", which, err)
		return 2
	}
	req.Header.Set("Content-Type", "application/json")
	out, ok := oosDo(which, req)
	if !ok {
		return 1
	}
	var a struct {
		Equipment    string   `json:"equipment"`
		OutOfService bool     `json:"outOfService"`
		UntilMs      int64    `json:"untilMs"`
		Alarms       []string `json:"alarms"`
	}
	_ = json.Unmarshal(out, &a)
	if a.OutOfService {
		fmt.Printf("%s out of service until %s: %d alarm(s) hidden\n", a.Equipment,
			time.UnixMilli(a.UntilMs).Format(time.RFC3339), len(a.Alarms))
	} else {
		fmt.Printf("%s back in service: %d alarm(s) evaluated again\n", a.Equipment, len(a.Alarms))
	}
	return 0
}

func oosList(base string) int {
	req, err := http.NewRequest(http.MethodGet, base+"/api/alarms/out-of-service", nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut alarms oos-list:", err)
		return 2
	}
	out, ok := oosDo("oos-list", req)
	if !ok {
		return 1
	}
	var list []alarm.OutOfService
	if err := json.Unmarshal(out, &list); err != nil {
		fmt.Fprintln(os.Stderr, "naut alarms oos-list: unexpected answer:", err)
		return 1
	}
	if len(list) == 0 {
		fmt.Println("no equipment is out of service")
		return 0
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "EQUIPMENT\tUNTIL\tBY\tALARMS\tREASON")
	for _, o := range list {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n", o.Equipment, time.UnixMilli(o.UntilMs).Format(time.RFC3339), o.By, len(o.Alarms), o.Reason)
	}
	tw.Flush()
	return 0
}
