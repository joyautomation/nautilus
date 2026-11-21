package main

import (
	"bufio"
	"bytes"
	"context"
	crand "crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/quorum"
	"github.com/joyautomation/nautilus/quorum/node"
)

const redundancyUsage = `naut redundancy — the operator commands of a redundant controller pair
(redundancy: peers:, docs/design/redundancy.md)

Usage:
  naut redundancy new-pair [--dir <d>] [--print-secrets]
      draw a new pair's cluster ID and peer token, offline, before any
      controller runs. It writes a.env, b.env and witness.env (mode 0600)
      into --dir (default: the current directory), refusing to overwrite
      one, and prints their paths and the lines that are not secret. Copy
      each to its box's /etc/nautilus/env (naut deploy). --print-secrets
      prints all three to stdout instead and writes nothing.
  naut redundancy [flags] commission
      commission a new pair, once both controllers and the witness run
      with the env lines new-pair wrote: every member must answer, and
      none may have been commissioned before. Run it on either controller.
      It prints the pair's leader once one is elected.
  naut redundancy [flags] recommission [--floor <n>] [--incarnation <w>] [--box-replaced]
      re-commission this controller after it lost its state and could not
      re-adopt from its peer (the alarm says "re-commission by hand").
      Without --floor it prints the evidence: the highest term each member
      knows, and a floor above them. --floor must be above every term a
      member that answered knows. --box-replaced: this controller's state
      was written on another box, and you replaced the box (never for a
      copy of a running controller).
  naut redundancy [flags] recommission --from-spare --copy-stopped
      on a spare, make it the member: first stop the controller the voters
      hold under its name, confirm it is down (powered off, or its process
      ended), and keep it from starting again (leave it off, or wipe its
      disk): one switched on later inside a network split can lead beside
      the member. Both other voters must hear no process of the name.
      It clears the name's marks, keeping none, moves this box's state
      file aside, and asks you to restart naut here: it then re-adopts
      from its peer.
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
Every command but new-pair is written to the controller's audit journal
(redundancy.commission, redundancy.recommission,
redundancy.clear-duplicate), accepted or refused.

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
	dir := fs.String("dir", ".", "")
	printSecrets := fs.Bool("print-secrets", false, "")
	floor := fs.Uint64("floor", 0, "")
	incarnation := fs.String("incarnation", "", "")
	boxReplaced := fs.Bool("box-replaced", false, "")
	fromSpare := fs.Bool("from-spare", false, "")
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
	if pos[0] == "new-pair" {
		if len(pos) != 1 {
			fmt.Fprint(stderr, redundancyUsage)
			return 2
		}
		if err := newPair(*dir, *printSecrets, stdout); err != nil {
			return fail("%v", err)
		}
		return 0
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
	case "commission":
		if len(pos) != 1 {
			fmt.Fprint(stderr, redundancyUsage)
			return 2
		}
		fmt.Fprintln(stdout, "commissioning the pair (this takes a few leases)...")
		code, b, err := do(http.MethodPost, "/api/cluster/commission", map[string]any{})
		if err != nil {
			return fail("%v", err)
		}
		if *asJSON {
			stdout.Write(b)
		} else {
			printCommissionAnswer(stdout, code, b)
		}
		if code != http.StatusOK {
			return 1
		}
		return 0
	case "recommission":
		if len(pos) != 1 {
			fmt.Fprint(stderr, redundancyUsage)
			return 2
		}
		body := map[string]any{}
		if *fromSpare {
			if *floor != 0 || *incarnation != "" || *boxReplaced {
				return fail("--from-spare takes no --floor, --incarnation or --box-replaced: the spare re-adopts from its peer")
			}
			if !*copyStopped {
				return fail("stop the controller the voters hold under this name, confirm it is down (powered off, or its process ended) and keep it from starting again, then run this again with --copy-stopped")
			}
			body["fromSpare"], body["copyStopped"] = true, true
		} else {
			if *floor != 0 {
				body["floor"] = *floor
			}
			if *incarnation != "" {
				inc, err := strconv.ParseUint(strings.TrimPrefix(*incarnation, "0x"), 16, 64)
				if err != nil || inc == 0 {
					return fail("--incarnation %q: want the witness's incarnation in hex, as status and the evidence print it", *incarnation)
				}
				body["incarnation"] = strconv.FormatUint(inc, 10)
			}
			if *boxReplaced {
				body["boxReplaced"] = true
			}
		}
		code, b, err := do(http.MethodPost, "/api/cluster/recommission", body)
		if err != nil {
			return fail("%v", err)
		}
		if *asJSON {
			stdout.Write(b)
		} else if *fromSpare {
			printFromSpareAnswer(stdout, code, b)
		} else {
			printRecommissionAnswer(stdout, code, b)
		}
		if code != http.StatusOK {
			return 1
		}
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
	if ans.Outcome != "refused" && len(ans.Report.Retired) == 0 {
		fmt.Fprintf(bw, "note: no member knew another process named %s, so none was set aside: a copy still alive is marked again only once heard beside the original. Make sure it is stopped.\n", ans.Report.Name)
	}
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

// newPair draws a new pair's cluster ID and peer token (#1029 3c-3c) and
// writes each box's env lines into dir, mode 0600, never over a file; or,
// with printSecrets, prints them all and writes nothing (naut-lead: the
// token is a secret, so stdout only on request).
func newPair(dir string, printSecrets bool, stdout io.Writer) error {
	cluster := node.NewClusterID()
	var t [32]byte
	if _, err := crand.Read(t[:]); err != nil {
		return err
	}
	token := hex.EncodeToString(t[:])
	envs := []struct{ file, box, lines string }{
		{"a.env", "controller a", "NAUTILUS_NODE=a\nNAUTILUS_CLUSTER=" + cluster + "\nNAUTILUS_PEER_TOKEN=" + token + "\n"},
		{"b.env", "controller b", "NAUTILUS_NODE=b\nNAUTILUS_CLUSTER=" + cluster + "\nNAUTILUS_PEER_TOKEN=" + token + "\n"},
		{"witness.env", "the witness", "NAUTILUS_PEER_TOKEN=" + token + "\n"},
	}
	if printSecrets {
		for _, e := range envs {
			fmt.Fprintf(stdout, "# %s: /etc/nautilus/env\n%s\n", e.box, e.lines)
		}
		fmt.Fprintln(stdout, "NAUTILUS_PEER_TOKEN is a secret: keep it out of logs and tickets.")
		return nil
	}
	for _, e := range envs {
		if _, err := os.Lstat(filepath.Join(dir, e.file)); err == nil {
			return fmt.Errorf("%s exists: new-pair never overwrites an env file (it may hold a running pair's token); move it away or give another --dir", filepath.Join(dir, e.file))
		}
	}
	var written []string
	for _, e := range envs {
		p := filepath.Join(dir, e.file)
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			_, err = f.WriteString(e.lines)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
		}
		if err != nil {
			for _, w := range written {
				_ = os.Remove(w)
			}
			_ = os.Remove(p)
			return err
		}
		written = append(written, p)
	}
	fmt.Fprintf(stdout, "a new pair, cluster ID %s\n", cluster)
	for _, e := range envs {
		fmt.Fprintf(stdout, "  %-12s for %s (mode 0600)\n", filepath.Join(dir, e.file), e.box)
	}
	fmt.Fprintf(stdout, "Each holds NAUTILUS_PEER_TOKEN, the pair's shared secret: copy each to its box's /etc/nautilus/env (naut deploy), then delete it here.\n")
	fmt.Fprintf(stdout, "Not secret: NAUTILUS_NODE=a or b (it differs per box) and NAUTILUS_CLUSTER=%s on both controllers.\n", cluster)
	fmt.Fprintln(stdout, "Then start both controllers and the witness, and run `naut redundancy commission` on either controller.")
	return nil
}

// memberLine is one member's state, for a person.
func memberLine(m node.MemberState) string {
	if m.Err != "" {
		return "NOT REACHED: " + m.Err
	}
	var s []string
	switch {
	case m.Commissioned:
		s = append(s, fmt.Sprintf("commissioned (incarnation %x)", m.Incarnation))
	case m.Member == "witness":
		s = append(s, fmt.Sprintf("incarnation %x", m.Incarnation))
	default:
		s = append(s, "uncommissioned")
	}
	s = append(s, fmt.Sprintf("knows terms up to %d", m.Highest()))
	if m.Spare {
		s = append(s, "SPARE")
	}
	if m.BoxChanged != "" {
		s = append(s, "BOX CHANGED: "+m.BoxChanged)
	}
	if !m.Settled {
		s = append(s, "still starting")
	}
	return strings.Join(s, "; ")
}

// printCommissionAnswer prints POST /api/cluster/commission's answer.
func printCommissionAnswer(w io.Writer, code int, b []byte) {
	var ans struct {
		Outcome string                `json:"outcome"`
		Error   string                `json:"error"`
		Report  node.CommissionReport `json:"report"`
	}
	if json.Unmarshal(b, &ans) != nil || ans.Outcome == "" {
		fmt.Fprintf(w, "refused (%d): %s\n", code, bytes.TrimSpace(b))
		return
	}
	bw := bufio.NewWriter(w)
	defer bw.Flush()
	for _, m := range ans.Report.Members {
		fmt.Fprintf(bw, "  %-8s %s\n", m.Member, memberLine(m))
	}
	if ans.Outcome != "done" {
		fmt.Fprintf(bw, "REFUSED, nothing changed: %s\n", ans.Error)
		return
	}
	if ans.Report.Leader == "" {
		fmt.Fprintf(bw, "commission issued for cluster %s: incarnation %x, floor %d set on this controller, but the pair elected no leader yet.\n"+
			"Check `naut redundancy status` in a minute. If this controller restarts before the pair adopts, the commission is cut short: re-commission it with `naut redundancy recommission --floor` above %d.\n",
			ans.Report.Cluster, ans.Report.Incarnation, ans.Report.Floor, ans.Report.Floor)
		if ans.Report.VouchRefused != "" {
			fmt.Fprintf(bw, "vouch refused (the operator-intent log stays read-only until synced): %s\n", ans.Report.VouchRefused)
		}
		return
	}
	fmt.Fprintf(bw, "commissioned cluster %s: incarnation %x, floor %d\n", ans.Report.Cluster, ans.Report.Incarnation, ans.Report.Floor)
	if ans.Report.VouchRefused != "" {
		fmt.Fprintf(bw, "vouch refused (the pair is commissioned all the same; its operator-intent log stays read-only until synced): %s\n", ans.Report.VouchRefused)
	}
	fmt.Fprintf(bw, "leader: %s\n", ans.Report.Leader)
}

// printRecommissionAnswer prints POST /api/cluster/recommission's answer.
func printRecommissionAnswer(w io.Writer, code int, b []byte) {
	var ans struct {
		Outcome string                  `json:"outcome"`
		Error   string                  `json:"error"`
		Report  node.RecommissionReport `json:"report"`
	}
	if json.Unmarshal(b, &ans) != nil || ans.Outcome == "" {
		fmt.Fprintf(w, "refused (%d): %s\n", code, bytes.TrimSpace(b))
		return
	}
	bw := bufio.NewWriter(w)
	defer bw.Flush()
	if len(ans.Report.Members) > 0 {
		fmt.Fprintln(bw, "the floor's evidence:")
		for _, m := range ans.Report.Members {
			fmt.Fprintf(bw, "  %-8s %s\n", m.Member, memberLine(m))
		}
		fmt.Fprintf(bw, "highest term known: %d; a floor above it: %d\n", ans.Report.Evidence, ans.Report.Suggested)
	}
	if ans.Outcome != "done" {
		fmt.Fprintf(bw, "REFUSED, nothing changed: %s\n", ans.Error)
		return
	}
	fmt.Fprintf(bw, "re-commissioned: incarnation %x, floor %d", ans.Report.Incarnation, ans.Report.Floor)
	if ans.Report.BoxReplaced {
		fmt.Fprint(bw, " (box replaced)")
	}
	fmt.Fprintln(bw)
	if ans.Report.VouchRefused != "" {
		fmt.Fprintf(bw, "vouch refused (re-commissioned all the same; the operator-intent log stays read-only until synced): %s\n", ans.Report.VouchRefused)
	}
}

// printFromSpareAnswer prints recommission --from-spare's answer.
func printFromSpareAnswer(w io.Writer, code int, b []byte) {
	var ans struct {
		Outcome string               `json:"outcome"`
		Error   string               `json:"error"`
		Report  node.FromSpareReport `json:"report"`
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
			switch {
			case a.Err != "":
				fmt.Fprintf(bw, "  %-8s NOT REACHED: %s\n", a.Member, a.Err)
			case a.Refused != "":
				fmt.Fprintf(bw, "  %-8s REFUSED: %s\n", a.Member, a.Refused)
			case title == "fence":
				fmt.Fprintf(bw, "  %-8s refuses %s's state from before this command (below adoption generation %d)\n", a.Member, ans.Report.Name, a.Fence)
			default:
				fmt.Fprintf(bw, "  %-8s hears %d process(es) named %s\n", a.Member, len(a.Boots), ans.Report.Name)
			}
		}
	}
	phase("check", ans.Report.Check)
	if len(ans.Report.Retired) > 0 {
		fmt.Fprintf(bw, "set aside (marked if heard again): %s\n", bootList(ans.Report.Retired))
	}
	phase("clear", ans.Report.Clear)
	phase("fence", ans.Report.Fence)
	switch ans.Outcome {
	case "done":
		fmt.Fprintf(bw, "done: this box's state file is moved aside to %s.\nNOW RESTART naut on this box (systemctl restart nautilus under naut deploy): it starts uncommissioned and re-adopts from its peer within seconds, as %s.\n", ans.Report.MovedAside, ans.Report.Name)
	case "refused":
		fmt.Fprintf(bw, "REFUSED, nothing changed: %s\n", ans.Error)
	default:
		fmt.Fprintf(bw, "INCOMPLETE: %s\n", ans.Error)
	}
}

func bootList(bs []quorum.BootSeq) string {
	var s []string
	for _, b := range bs {
		s = append(s, fmt.Sprintf("boot %x", b.Nonce))
	}
	return strings.Join(s, ", ")
}
