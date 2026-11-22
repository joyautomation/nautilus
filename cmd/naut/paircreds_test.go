package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/joyautomation/nautilus/alarm/oncall"
	"github.com/joyautomation/nautilus/auth/users"
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/quorum/node"
)

// #708: what a pair member compares at every hello is read anew each time:
// the users file's digest (so a user added on one box shows within one
// hello), its read error when the file is malformed, nothing without
// auth.users, and no TLS part without server.tls.
func TestPairCredentialsAreReadAnew(t *testing.T) {
	cheapHashes(t)
	dir, state := usersProject(t)
	path := filepath.Join(state, users.FileName)
	if code, _, errs := nautUsers(t, dir, "hunter22\n", "add", "akhan"); code != 0 {
		t.Fatalf("add: %d %s", code, errs)
	}
	proj, err := project.Load(os.DirFS(dir), "")
	if err != nil {
		t.Fatal(err)
	}
	creds := pairCredentials(proj, dir, nil, func(string) string { return "" })
	want, err := users.Digest(path)
	if err != nil {
		t.Fatal(err)
	}
	if c := creds(); c.Users != want || c.UsersErr != "" || c.TLS != ([32]byte{}) {
		t.Fatalf("credentials %+v; want the users file's digest and no TLS part", c)
	}
	if code, _, errs := nautUsers(t, dir, "hunter33\n", "add", "jsmith"); code != 0 {
		t.Fatalf("add: %d %s", code, errs)
	}
	if c := creds(); c.Users == want {
		t.Fatal("a user added since was not seen: the digest was not read anew")
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c := creds(); c.UsersErr == "" {
		t.Fatal("a malformed users file read as fine")
	}
	plain, err := project.Load(fstest.MapFS{
		"nautilus.yaml": &fstest.MapFile{Data: []byte("name: plain\ntasks:\n  - program: main.st\n    scan: 100ms\n")},
		"main.st":       &fstest.MapFile{Data: []byte("PROGRAM Main\nEND_PROGRAM\n")},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if c := pairCredentials(plain, "", nil, func(string) string { return "" })(); c != (node.Credentials{}) {
		t.Fatalf("no auth.users: %+v; want nothing", c)
	}
}

// #708 end to end: a pair whose members hold different users files shows
// it on /api/cluster (the dashboard's banner) with how to fix it, keeps
// electing (report only), and clears it once the files match.
func TestPairReportsDifferentUsersFiles(t *testing.T) {
	var mu sync.Mutex
	dig := map[string][32]byte{"a": {1}, "b": {2}}
	r := newPairRigFull(t, nil, func(n string, d *pairDeps) {
		d.creds = func() node.Credentials { mu.Lock(); defer mu.Unlock(); return node.Credentials{Users: dig[n]} }
	})
	lead := r.leader()
	cluster := func(n string) string { _, body, _ := r.call(n, http.MethodGet, "/api/cluster", ""); return body }
	pairWait(t, "the users condition on /api/cluster", 15*time.Second, func() bool {
		return strings.Contains(cluster(lead), "users files differ") && strings.Contains(cluster(lead), "copy the same users.json")
	})
	if r.leader() != lead {
		t.Fatal("the leader changed over a users mismatch; the check is report only")
	}
	mu.Lock()
	dig["b"] = dig["a"]
	mu.Unlock()
	pairWait(t, "the condition to clear", 30*time.Second, func() bool { return !strings.Contains(cluster(lead), "users files differ") })
}

// #715 end to end: a pair whose standby has no alarm contacts file (its
// digest is zero, the leader's is not) shows "alarm contacts differ from
// the peer" on /api/cluster, naming the copy; and it is report only: the
// pair keeps its leader and takes writes meanwhile. Copied, it clears.
func TestPairReportsDifferentAlarmContacts(t *testing.T) {
	var mu sync.Mutex
	dig := map[string][32]byte{"a": {7}, "b": {7}}
	r := newPairRigFull(t, nil, func(n string, d *pairDeps) {
		d.creds = func() node.Credentials { mu.Lock(); defer mu.Unlock(); return node.Credentials{Contacts: dig[n]} }
	})
	lead := r.leader()
	stand := "a"
	if lead == "a" {
		stand = "b"
	}
	mu.Lock()
	dig[stand] = [32]byte{} // the standby's file is missing
	mu.Unlock()
	cluster := func(n string) string { _, body, _ := r.call(n, http.MethodGet, "/api/cluster", ""); return body }
	pairWait(t, "the contacts condition on /api/cluster", 15*time.Second, func() bool {
		b := cluster(lead)
		return strings.Contains(b, "alarm contacts differ from the peer") && strings.Contains(b, "alarm-contacts.json")
	})
	if r.leader() != lead {
		t.Fatal("the leader changed over a contacts mismatch; the check is report only")
	}
	r.setTrip(t, lead, true) // a write still goes through
	mu.Lock()
	dig[stand] = dig[lead]
	mu.Unlock()
	pairWait(t, "the condition to clear", 30*time.Second, func() bool { return !strings.Contains(cluster(lead), "alarm contacts differ") })
}

// The digest pairCredentials reads for the alarm contacts: none without a
// paging channel; the files' content under the peer token once there is
// one, read anew at each hello.
func TestPairCredentialsDigestAlarmContacts(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := "name: contacts\nauth:\n  state-dir: " + state + "\nalarms:\n  defs:\n    - { id: Trip, tag: Trip }\n  channels:\n    email: { smtp: \"mail.example.test:465\", from: n@example.test }\ntags:\n  - { name: Trip, role: state, type: BOOL }\ntasks:\n  - program: main.st\n    scan: 100ms\n"
	if err := os.WriteFile(filepath.Join(dir, "nautilus.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.st"), []byte("PROGRAM Main\nEND_PROGRAM\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	proj, err := project.Load(os.DirFS(dir), "")
	if err != nil {
		t.Fatal(err)
	}
	env := func(k string) string {
		if k == "NAUTILUS_PEER_TOKEN" {
			return "peer-secret"
		}
		return ""
	}
	creds := pairCredentials(proj, dir, nil, env)
	if c := creds(); c.Contacts != ([32]byte{}) || c.ContactsErr != "" {
		t.Fatalf("no contacts yet: %+v", c)
	}
	if err := oncall.UpdateContacts(oncall.ContactsPath(state), "test", func(c *oncall.Contacts) error {
		c.Contacts["alice"] = &oncall.Contact{Email: "alice@example.test"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	c := creds()
	want, _ := oncall.Digest(state, []byte("peer-secret"))
	if c.ContactsErr != "" || c.Contacts == ([32]byte{}) || c.Contacts != want {
		t.Fatalf("with a contact: %+v; want the keyed digest", c)
	}
	if other, _ := oncall.Digest(state, []byte("another token")); other == want {
		t.Fatal("the digest does not depend on the peer token")
	}
}
