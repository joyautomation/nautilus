package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/quorum/node"
)

const redundancyUsage = `naut redundancy — the operator commands of a redundant controller pair
(redundancy: peers:, docs/design/redundancy.md)

Usage:
  naut redundancy [flags] status
      this controller's view of the pair: the leader, the names its voter
      marks as run by two processes at once, whether it is held by a mark,
      whether it is a spare
  naut redundancy [flags] clear-duplicate <name> --copy-stopped
      clear the duplicate mark on <name> (the member the alarm "two
      processes claim to be <name>" names) at all three members: this
      controller, its peer and the witness. First stop the copy and confirm
      it is down: powered off or its process ended, not just unplugged or
      paused. --copy-stopped says you have, and is journalled under your
      name. Every member must be up; one that started less than a lease
      and a quarter ago can't answer yet. The command then checks every member: all three must
      answer, and together they must hear one process named <name>, else
      nothing is cleared. It clears each member, and checks again a lease
      later: a mark that comes back means a copy still runs.

Flags:
  --url <url>         the controller's tag API (default $NAUTILUS_URL, else
                      http://localhost:8080: run it on the box)
  --token-file <f>    a file whose first line is the credential, instead of
                      $NAUTILUS_TOKEN: a personal access token (naut users
                      token) under an auth: block, else the controller's
                      NAUTILUS_TOKEN. Never pass it on the command line.
  --ca-file <f>       the certificate to trust for an https --url (the
                      controller's own, for a self-signed one)
  --json              print the server's answer as JSON
  --timeout <d>       how long to wait for the command (default 2m; the
                      clear takes a few leases)

Under an auth: block the commands need the redundancy permission
(administrator by default). Without one they take only NAUTILUS_TOKEN.
Every command is written to the controller's audit journal
(redundancy.clear-duplicate), accepted or refused.

Exit status: 0 done; 1 refused, incomplete or failed; 2 usage.
`

func runRedundancy(args []string) int {
	return redundancyMain(args, os.Getenv, os.Stdout, os.Stderr)
}

// redundancyMain is naut redundancy.
func redundancyMain(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("naut redundancy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, redundancyUsage) }
	defURL := getenv("NAUTILUS_URL")
	if defURL == "" {
		defURL = "http://localhost:8080"
	}
	base := fs.String("url", defURL, "")
	tokenFile := fs.String("token-file", "", "")
	caFile := fs.String("ca-file", "", "")
	asJSON := fs.Bool("json", false, "")
	timeout := fs.Duration("timeout", 2*time.Minute, "")
	copyStopped := fs.Bool("copy-stopped", false, "")
	// Flags may follow the subcommand and its name too: parse what is left
	// after each positional argument.
	var pos []string
	for rest := args; ; {
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	fail := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "naut redundancy: "+format+"\n", a...)
		return 1
	}
	if len(pos) == 0 {
		fmt.Fprint(stderr, redundancyUsage)
		return 2
	}
	tok := getenv("NAUTILUS_TOKEN")
	if *tokenFile != "" {
		b, err := os.ReadFile(*tokenFile)
		if err != nil {
			return fail("%v", err)
		}
		tok, _, _ = strings.Cut(string(b), "\n")
		tok = strings.TrimSpace(tok)
	}
	client := &http.Client{}
	if *caFile != "" {
		pem, err := os.ReadFile(*caFile)
		if err != nil {
			return fail("%v", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return fail("%s holds no PEM certificate", *caFile)
		}
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	do := func(method, path string, body any) (int, []byte, error) {
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(*base, "/")+path, rd)
		if err != nil {
			return 0, nil, err
		}
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		req.Header.Set("X-Nautilus-Client", "cli")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0, nil, err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return resp.StatusCode, b, err
	}
	switch pos[0] {
	case "status":
		if len(pos) != 1 {
			fmt.Fprint(stderr, redundancyUsage)
			return 2
		}
		code, b, err := do(http.MethodGet, "/api/cluster", nil)
		if err != nil {
			return fail("%v", err)
		}
		if code != http.StatusOK {
			return fail("GET /api/cluster: %d %s", code, bytes.TrimSpace(b))
		}
		if *asJSON {
			stdout.Write(b)
			return 0
		}
		printClusterStatus(stdout, b)
		return 0
	case "clear-duplicate":
		if len(pos) != 2 {
			fmt.Fprint(stderr, redundancyUsage)
			return 2
		}
		if !*copyStopped {
			return fail("stop the copy of %s and confirm it is down first, then run this again with --copy-stopped", pos[1])
		}
		fmt.Fprintf(stdout, "clearing the duplicate mark on %s at every member (this takes about a lease)...\n", pos[1])
		code, b, err := do(http.MethodPost, "/api/cluster/clear-duplicate", map[string]any{"name": pos[1], "copyStopped": true})
		if err != nil {
			return fail("%v", err)
		}
		if *asJSON {
			stdout.Write(b)
		} else {
			printClearAnswer(stdout, code, b)
		}
		if code != http.StatusOK {
			return 1
		}
		return 0
	}
	fmt.Fprint(stderr, redundancyUsage)
	return 2
}

// printClusterStatus prints GET /api/cluster for a person.
func printClusterStatus(w io.Writer, b []byte) {
	var st struct {
		Mode     string   `json:"mode"`
		Pod      string   `json:"pod"`
		Leader   string   `json:"leader"`
		IsLeader bool     `json:"isLeader"`
		Marked   []string `json:"marked"`
		Held     bool     `json:"held"`
		Spare    bool     `json:"spare"`
		Warnings []string `json:"warnings"`
	}
	if json.Unmarshal(b, &st) != nil {
		w.Write(b)
		return
	}
	bw := bufio.NewWriter(w)
	defer bw.Flush()
	if st.Mode != "quorum" {
		fmt.Fprintf(bw, "not a redundant pair member (mode %s)\n", st.Mode)
		return
	}
	leader := st.Leader
	if leader == "" {
		leader = "none known"
	}
	fmt.Fprintf(bw, "controller %s; leader: %s", st.Pod, leader)
	if st.IsLeader {
		fmt.Fprint(bw, " (this one)")
	}
	fmt.Fprintln(bw)
	if st.Spare {
		fmt.Fprintln(bw, "SPARE: this controller is a copy of a member and takes no part in the pair")
	}
	if len(st.Marked) > 0 {
		fmt.Fprintf(bw, "this controller's voter marks as run by two processes: %s\n", strings.Join(st.Marked, ", "))
	}
	if st.Held {
		fmt.Fprintf(bw, "HELD: a member marks %s as run by two processes, so it does not campaign until the mark is cleared\n", st.Pod)
	}
	for _, x := range st.Warnings {
		fmt.Fprintf(bw, "warning: %s\n", x)
	}
}

// printClearAnswer prints POST /api/cluster/clear-duplicate's answer for a
// person: the outcome, then each phase member by member.
func printClearAnswer(w io.Writer, code int, b []byte) {
	var ans struct {
		Outcome string           `json:"outcome"`
		Error   string           `json:"error"`
		Report  node.ClearReport `json:"report"`
	}
	if json.Unmarshal(b, &ans) != nil || ans.Outcome == "" {
		fmt.Fprintf(w, "refused (%d): %s\n", code, bytes.TrimSpace(b))
		return
	}
	bw := bufio.NewWriter(w)
	defer bw.Flush()
	phase := func(title string, as []node.DupAnswer) {
		if len(as) == 0 {
			return
		}
		fmt.Fprintf(bw, "%s:\n", title)
		for _, a := range as {
			var s []string
			switch {
			case a.Err != "":
				s = append(s, "NOT REACHED: "+a.Err)
			default:
				s = append(s, fmt.Sprintf("hears %d process(es) named %s", len(a.Boots), ans.Report.Name))
				if a.Cleared {
					s = append(s, "cleared")
				}
				if a.Refused != "" {
					s = append(s, "REFUSED: "+a.Refused)
				}
				if a.Marked {
					s = append(s, "MARKS IT")
				} else {
					s = append(s, "no mark")
				}
			}
			fmt.Fprintf(bw, "  %-8s %s\n", a.Member, strings.Join(s, "; "))
		}
	}
	phase("check", ans.Report.Check)
	phase("clear", ans.Report.Clear)
	phase("re-check a lease later", ans.Report.Recheck)
	switch ans.Outcome {
	case "done":
		fmt.Fprintf(bw, "done: no member marks %s. The controller that was held campaigns again on its own; no restart is needed.\n", ans.Report.Name)
	case "refused":
		fmt.Fprintf(bw, "REFUSED, nothing was cleared: %s\n", ans.Error)
	default:
		fmt.Fprintf(bw, "INCOMPLETE: %s\n", ans.Error)
	}
}
