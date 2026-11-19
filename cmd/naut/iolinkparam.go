package main

// naut iolink param list | get | set: the parameters of the device on an
// IO-Link port, through the controller's API (docs/design/iolink.md §7). It
// is a client of /api/iolink, never of the master: a write goes through the
// controller's authorization and its audit journal, and is refused while
// the device on the port is not the one the project expects.

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/joyautomation/nautilus/internal/apiclient"
)

const iolinkParamUsage = `usage: naut iolink param <cmd> --host <controller> [--port 8080] <port device> ...

  list <port device>                    the device's parameters, from its IODD
  get  <port device> <parameter>        read one (works whichever device is on
                                        the port, and says which)
  set  <port device> <parameter> <value> write one: needs the config
                                        permission (or, with no auth: block,
                                        the write credential: NAUTILUS_TOKEN),
                                        goes in the audit journal, and is
                                        refused unless the device on the port
                                        is the one expected

A parameter is a variable id (V_Setpoint), its name, or its index (64); for a
record, the item after a dot (Window.2). A value is a number, true/false or
text; --string forces text. --host is a hostname or IP, or the controller's
URL (https://plc.example:8443 for one with server.tls).
`

// The client's time limits. A read is one ISDU call, which the controller
// bounds at 10 s. A write is the controller's read of the value before it
// (10 s), then the write (10 s): the client must outwait both, or it would
// call a write that is still under way a failure to reach the controller.
var (
	paramGetTimeout = 20 * time.Second
	paramPutTimeout = 45 * time.Second
)

type paramListResp struct {
	Port   string `json:"port"`
	Master string `json:"master"`
	Params []struct {
		ID      string   `json:"id"`
		Name    string   `json:"name"`
		Index   int      `json:"index"`
		Ref     string   `json:"ref"`
		Type    string   `json:"type"`
		Access  string   `json:"access"`
		Octets  int      `json:"octets"`
		Range   []string `json:"range"`
		Default string   `json:"default"`
		Carried bool     `json:"carried"`
	} `json:"params"`
}

type paramAnswerResp struct {
	Port            string `json:"port"`
	Variable        string `json:"variable"`
	Index           int    `json:"index"`
	Subindex        int    `json:"subindex"`
	Value           any    `json:"value"`
	Raw             string `json:"raw"`
	Identity        string `json:"identity"`
	IdentityChecked bool   `json:"identityChecked"`
}

func runIOLinkParam(args []string, out, errw io.Writer) int {
	fl := flag.NewFlagSet("iolink param", flag.ContinueOnError)
	fl.SetOutput(errw)
	host := fl.String("host", "", "controller hostname or IP")
	port := fl.Int("port", 8080, "tag API port")
	asText := fl.Bool("string", false, "set: the value is text, whatever it looks like")
	fl.Usage = func() { fmt.Fprint(errw, iolinkParamUsage) }
	if len(args) < 1 {
		fmt.Fprint(errw, iolinkParamUsage)
		return 2
	}
	cmd := args[0]
	if err := fl.Parse(args[1:]); err != nil {
		return 2
	}
	if *host == "" {
		fmt.Fprintln(errw, "naut iolink param: --host is required")
		return 2
	}
	base, err := apiclient.BaseURL(*host, *port)
	if err != nil {
		fmt.Fprintln(errw, "naut iolink param:", err)
		return 2
	}
	return iolinkParamAt(base, cmd, fl.Args(), *asText, out, errw)
}

// iolinkParamAt runs cmd against the controller at base.
func iolinkParamAt(base, cmd string, rest []string, asText bool, out, errw io.Writer) int {
	need := map[string]int{"list": 1, "get": 2, "set": 3}[cmd]
	if need == 0 {
		fmt.Fprintf(errw, "naut iolink param: unknown command %q\n\n%s", cmd, iolinkParamUsage)
		return 2
	}
	if len(rest) != need {
		fmt.Fprintf(errw, "naut iolink param %s: want %d argument(s), got %d\n\n%s", cmd, need, len(rest), iolinkParamUsage)
		return 2
	}
	path := base + "/api/iolink/" + url.PathEscape(rest[0]) + "/params"
	switch cmd {
	case "list":
		var l paramListResp
		if code := paramCall(http.MethodGet, path, nil, &l, errw); code != 0 {
			return code
		}
		tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintf(tw, "port device %s on %s\nPARAMETER\tINDEX\tACCESS\tTYPE\tOCTETS\tRANGE\tDEFAULT\tCARRIED\n", l.Port, l.Master)
		for _, p := range l.Params {
			fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%d\t%s\t%s\t%v\n", p.ID, p.Index, p.Access, p.Type, p.Octets, strings.Join(p.Range, ","), p.Default, p.Carried)
		}
		tw.Flush()
		return 0
	case "get":
		var a paramAnswerResp
		if code := paramCall(http.MethodGet, path+"/"+url.PathEscape(rest[1]), nil, &a, errw); code != 0 {
			return code
		}
		fmt.Fprintf(out, "%s = %v  (index %d, identity %s)\n", a.Variable, a.Value, a.Index, a.Identity)
		paramIdentityNote(a, errw)
		return 0
	}
	body, _ := json.Marshal(map[string]any{"value": paramValueOf(rest[2], asText)})
	var a paramAnswerResp
	if code := paramCall(http.MethodPut, path+"/"+url.PathEscape(rest[1]), body, &a, errw); code != 0 {
		return code
	}
	fmt.Fprintf(out, "%s now holds %v  (index %d, identity %s)\n", a.Variable, a.Value, a.Index, a.Identity)
	return 0
}

// paramIdentityNote says on stderr when a parameter came from a device that
// is not known to be the right one.
func paramIdentityNote(a paramAnswerResp, errw io.Writer) {
	switch {
	case !a.IdentityChecked:
		fmt.Fprintln(errw, "note: the master's ids have not arrived, so it is not known which device answered")
	case a.Identity != "Ok" && a.Identity != "Unchecked":
		fmt.Fprintf(errw, "note: the identity check says %s: this is what the device on the port holds, and it is not the one expected\n", a.Identity)
	}
}

// paramValueOf is the value a command line spells: a number, true or false,
// else text (or text always, with asText).
func paramValueOf(s string, asText bool) any {
	if asText {
		return s
	}
	switch s {
	case "true":
		return true
	case "false":
		return false
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}
	if n, err := strconv.ParseUint(s, 10, 64); err == nil {
		return n
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	return s
}

// paramCall makes one request and decodes a 200 answer into into. Any other
// status is the controller's message on stderr: exit 1 for what it refused
// (a 4xx), 2 for a failure to reach it or its own (5xx).
func paramCall(method, u string, body []byte, into any, errw io.Writer) int {
	req, err := http.NewRequest(method, u, bytes.NewReader(body))
	if err != nil {
		fmt.Fprintln(errw, "naut iolink param:", err)
		return 2
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	apiclient.Authorize(req)
	write := method == http.MethodPut
	client := http.Client{Timeout: paramGetTimeout}
	if write {
		client.Timeout = paramPutTimeout
	}
	resp, err := client.Do(req)
	if err != nil {
		if write {
			// The request was sent. Whether the device took the value is not
			// known, and a retry could write it twice, to a device that has
			// since been swapped: say so, and never retry.
			fmt.Fprintf(errw, "naut iolink param: outcome unknown: the write was sent to %s and no answer came back (%v). The parameter may or may not have been written: check it with `naut iolink param get`; do not repeat the write until you have\n", u, err)
			return 2
		}
		fmt.Fprintf(errw, "naut iolink param: no controller at %s: %v\n", u, err)
		return 2
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		msg := strings.TrimSpace(string(raw))
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			msg = e.Error
		}
		unknown := write && (strings.EqualFold(resp.Header.Get("X-Nautilus-Outcome"), "unknown") || resp.StatusCode >= 500)
		if unknown {
			fmt.Fprintf(errw, "naut iolink param: outcome unknown: %s: %s. The parameter may or may not have been written: check it with `naut iolink param get`\n", resp.Status, msg)
		} else {
			fmt.Fprintf(errw, "naut iolink param: %s: %s\n", resp.Status, msg)
		}
		if resp.StatusCode >= 500 {
			return 2
		}
		return 1
	}
	if err := json.Unmarshal(raw, into); err != nil {
		fmt.Fprintf(errw, "naut iolink param: bad answer from %s: %v\n", u, err)
		return 2
	}
	return 0
}
