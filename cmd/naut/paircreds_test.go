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
