package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/internal/project"
	_ "github.com/lib/pq"
)

func envOf(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

// The copy's rows carry the project's name, and on a pair the member's, or
// in a cluster the pod's: each keeps its own journal and chain.
func TestAuditControllerName(t *testing.T) {
	plain := &project.Project{Name: "plant"}
	pair := &project.Project{Name: "plant", Redundancy: &project.RedundancyConfig{
		Peers: map[string]string{"a": "10.0.0.1:7000", "b": "10.0.0.2:7000"}, Witness: "10.0.0.3:7001"}}
	for _, c := range []struct {
		p    *project.Project
		env  map[string]string
		want string
	}{
		{plain, nil, "plant"},
		{plain, map[string]string{"POD_NAME": "plant-0"}, "plant/plant-0"},
		{pair, map[string]string{"NAUTILUS_NODE": " B "}, "plant/b"},
		// A box outside a pair ignores NAUTILUS_NODE (naut run refuses it
		// anyway).
		{plain, map[string]string{"NAUTILUS_NODE": "a"}, "plant"},
	} {
		if got := auditController(c.p, envOf(c.env)); got != c.want {
			t.Errorf("%v %v: %q, want %q", c.p.Redundancy != nil, c.env, got, c.want)
		}
	}
}

// audit.sink: postgres without a connection string is refused at start,
// naming the variable; without a sink nothing starts.
func TestStartAuditCopyNeedsItsVariable(t *testing.T) {
	j, err := audit.OpenFile(t.TempDir(), audit.FileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	ctx := t.Context()
	none := &project.Project{Name: "plant"}
	if tl, stop, err := startAuditCopy(ctx, none, j, envOf(nil), io.Discard); tl != nil || err != nil {
		t.Errorf("no sink: %v %v", tl, err)
	} else {
		stop()
	}
	pg := &project.Project{Name: "plant", Audit: &project.AuditConfig{Sink: "postgres"}}
	_, _, err = startAuditCopy(ctx, pg, j, envOf(nil), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "NAUTILUS_AUDIT_DB_URL") || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Errorf("sink without a DSN: %v, want the variables named", err)
	}
	named := &project.Project{Name: "plant", Audit: &project.AuditConfig{Sink: "postgres", DSNEnv: "SITE_AUDIT_DB"}}
	_, _, err = startAuditCopy(ctx, named, j, envOf(map[string]string{"DATABASE_URL": "postgres://x"}), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "SITE_AUDIT_DB") {
		t.Errorf("a named variable that is empty: %v, want it named (and no fallback to DATABASE_URL)", err)
	}
	// A database that is down is not an error: the copy waits for it.
	tl, stop, err := startAuditCopy(ctx, pg, j, envOf(map[string]string{"DATABASE_URL": "postgres://nobody@127.0.0.1:1/x?sslmode=disable&connect_timeout=1"}), io.Discard)
	if err != nil || tl == nil {
		t.Fatalf("a database that is down: %v %v, want the copy started", tl, err)
	}
	stop()
	if st := tl.Status(); st.State != audit.CopyBehind || st.LastError == "" {
		t.Errorf("status %+v, want behind with the error", st)
	}
}

// `naut run` with audit.sink: postgres copies the journal into Postgres,
// says so on the drivers block, and on stop copies what the shutdown wrote
// (the open refusal window's summary) before it exits: the copy then holds
// every line of the journal, byte for byte.
func TestPostgresNautRunCopiesTheJournal(t *testing.T) {
	dsn := os.Getenv("NAUTILUS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set NAUTILUS_TEST_DATABASE_URL to run this test against a real Postgres")
	}
	if testing.Short() {
		t.Skip("builds and runs the CLI")
	}
	if goruntime.GOOS == "windows" {
		t.Skip("stops naut run with SIGTERM")
	}
	// A database of its own: internal/auditpg's tests drop and recreate
	// the copy's tables in the shared one, and CI runs the packages at
	// once.
	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme != "postgres" && u.Scheme != "postgresql" {
		t.Skip("NAUTILUS_TEST_DATABASE_URL is not a postgres:// URL; this test needs a database of its own")
	}
	for _, q := range []string{`DROP DATABASE IF EXISTS naut_run_copy_test`, `CREATE DATABASE naut_run_copy_test`} {
		if _, err := admin.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	u.Path = "/naut_run_copy_test"
	dsn = u.String()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	bin := buildNautCLI(t, t.TempDir(), nil)
	dir := t.TempDir()
	files := map[string]string{
		"nautilus.yaml": "name: copied\ntasks:\n  - program: main.st\n    scan: 20ms\n" +
			"tags:\n  - { name: SP, role: setpoint, type: REAL, init: 1 }\n" +
			"server:\n  addr: \"127.0.0.1:0\"\naudit:\n  sink: postgres\n  dsn-env: COPY_TEST_DB\n",
		"main.st": "PROGRAM Main\nVAR_EXTERNAL\n SP : REAL;\nEND_VAR\nEND_PROGRAM\n",
	}
	for n, body := range files {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(bin, "run")
	cmd.Dir = dir
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "NAUTILUS_ADDR=") && !strings.HasPrefix(kv, "NAUTILUS_TOKEN=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "COPY_TEST_DB="+dsn)
	var stdout, stderr lockedBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	t.Cleanup(func() {
		cmd.Process.Kill()
		<-exited
	})
	api := apiAddrFromBanner(t, stdout.String, exited, stderr.String)
	post := func(body string) int {
		deadline := time.Now().Add(30 * time.Second)
		for {
			resp, err := http.Post("http://"+api+"/api/tags", "application/json", strings.NewReader(body))
			if err == nil {
				resp.Body.Close()
				return resp.StatusCode
			}
			if time.Now().After(deadline) {
				t.Fatalf("no tag API on %s: %v; stderr:\n%s", api, err, stderr.String())
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	if code := post(`{"name":"SP","value":7}`); code != http.StatusNoContent {
		t.Fatalf("write = %d; stderr:\n%s", code, stderr.String())
	}
	// The card says the copy is current once it has the write.
	deadline := time.Now().Add(15 * time.Second)
	for {
		resp, err := http.Get("http://" + api + "/api/drivers")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if strings.Contains(string(b), `"name":"audit copy"`) && strings.Contains(string(b), `"state":"connected"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("/api/drivers = %s, want the audit copy's card, connected; stderr:\n%s", b, stderr.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Refusals open a coalescing window; its summary is written at stop.
	for i := 0; i < 8; i++ {
		post(`{"name":"Nope","value":1}`)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	case <-time.After(20 * time.Second):
		t.Fatalf("naut run did not stop; stderr:\n%s", stderr.String())
	}
	var journal []string
	jfiles, _ := filepath.Glob(filepath.Join(dir, "audit", "audit-*.jsonl"))
	for _, f := range jfiles {
		b, _ := os.ReadFile(f)
		journal = append(journal, strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")...)
	}
	rows, err := db.Query(`SELECT line FROM audit_entries WHERE controller = 'copied' ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	var copied []string
	for rows.Next() {
		var l string
		rows.Scan(&l)
		copied = append(copied, l)
	}
	rows.Close()
	if strings.Join(copied, "\n") != strings.Join(journal, "\n") {
		t.Fatalf("the copy (%d lines) is not the journal (%d lines):\ncopy:\n%s\njournal:\n%s\nstderr:\n%s",
			len(copied), len(journal), strings.Join(copied, "\n"), strings.Join(journal, "\n"), stderr.String())
	}
	if !strings.Contains(journal[len(journal)-1], `"coalesced":3`) {
		t.Errorf("the journal's last line is not the stop's summary: %s", journal[len(journal)-1])
	}
}
