package tiadtest

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/siemens/tiad"
)

// The adopt half of the contract (#1272): an archive becomes a deploy's
// saved project at projects/<name>/<name>.ap21, from a deploy record's
// archive (hash-checked) or from a project archive in the work dir. It
// leaves no session open, never replaces a project, and the first apply
// of the station it holds changes nothing.
func contractAdopt(t *testing.T, start Starter) {
	t.Run("FromDeploy", func(t *testing.T) { contractAdoptFromDeploy(t, start) })
	t.Run("FromArchive", func(t *testing.T) { contractAdoptFromArchive(t, start) })
	t.Run("Refusals", func(t *testing.T) { contractAdoptRefusals(t, start) })
	t.Run("BrokenArchive", func(t *testing.T) { contractAdoptBroken(t, start) })
	t.Run("NoCPU", func(t *testing.T) { contractAdoptNoCPU(t, start) })
}

// contractAdoptNoCPU: an archive whose project holds no CPU tiad can read
// adopts nothing; an archive inside the directory a replace would remove
// is refused before anything is removed.
func contractAdoptNoCPU(t *testing.T, start Starter) {
	a := start(t, Config{Targets: []string{benchCPU}})
	c := a.Client()
	if _, err := c.PutFile(ctx, "blank/blank.ap21", []byte("fake TIA project")); err != nil {
		t.Fatal(err)
	}
	s, err := c.Open(ctx, "blank/blank.ap21")
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Save(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	closeFirst(t, s)
	if _, err := c.AdoptProject(ctx, tiad.AdoptRequest{Name: "nocpu", Archive: p}); tiad.Kind(err) != tiad.KindBadRequest ||
		!strings.Contains(err.Error(), "holds no CPU tiad can read") {
		t.Fatalf("an archive without a CPU: %v", err)
	}
	noSession(t, c)
	if _, err := c.Open(ctx, "projects/nocpu/nocpu.ap21"); tiad.Kind(err) != tiad.KindNotFound {
		t.Fatalf("a refused adopt left a project: %v", err)
	}
	b, err := c.GetFile(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.PutFile(ctx, "projects/inside/in.zap21", b); err != nil {
		t.Fatal(err)
	}
	if _, err := c.AdoptProject(ctx, tiad.AdoptRequest{Name: "inside", Archive: "projects/inside/in.zap21", Replace: true}); tiad.Kind(err) != tiad.KindBadRequest ||
		!strings.Contains(err.Error(), "inside projects/inside/") {
		t.Fatalf("an archive inside the replaced directory: %v", err)
	}
	if _, err := c.GetFile(ctx, "projects/inside/in.zap21"); err != nil {
		t.Fatalf("the refused replace removed the archive: %v", err)
	}
}

// contractAdoptBroken: a deploy's archive that does not hash to its
// record's sha256 adopts nothing, and leaves no project behind.
func contractAdoptBroken(t *testing.T, start Starter) {
	a, s := compiledBench(t, start)
	c := a.Client()
	d1, err := s.Download(ctx, benchSoftwareLoad)
	if err != nil {
		t.Fatal(err)
	}
	d2, err := s.Download(ctx, benchSoftwareLoad)
	if err != nil {
		t.Fatal(err)
	}
	closeFirst(t, s)
	// A project adopted first, which a replace from a broken source must
	// leave as it is.
	if _, err := c.AdoptProject(ctx, tiad.AdoptRequest{Name: "kept", Deploy: d2.Deploy.ID}); err != nil {
		t.Fatal(err)
	}
	a.SwapArchive(t, d1.Deploy.Archive.File, d2.Deploy.Archive.File)
	if _, err := c.AdoptProject(ctx, tiad.AdoptRequest{Name: "kept", Deploy: d1.Deploy.ID, Replace: true}); tiad.Kind(err) != tiad.KindBadRequest {
		t.Fatalf("a replace from a swapped archive: %v", err)
	}
	if k, err := c.Open(ctx, "projects/kept/kept.ap21"); err != nil {
		t.Fatalf("a refused replace removed the project: %v", err)
	} else {
		closeFirst(t, k)
	}
	if _, err := c.AdoptProject(ctx, tiad.AdoptRequest{Name: "broken", Deploy: d1.Deploy.ID}); tiad.Kind(err) != tiad.KindBadRequest ||
		!strings.Contains(err.Error(), "not the record's") {
		t.Fatalf("a swapped archive: %v", err)
	}
	noSession(t, c)
	if _, err := c.Open(ctx, "projects/broken/broken.ap21"); tiad.Kind(err) != tiad.KindNotFound {
		t.Fatalf("a refused adopt left a project: %v", err)
	}
}

// contractAdoptFromDeploy: a loaded deploy's archive, adopted under a new
// name, opens as saved, takes the station it was loaded with without a
// change, and compares equal to the CPU still running that load; another
// address in the spec is a change, named.
func contractAdoptFromDeploy(t *testing.T, start Starter) {
	a, d := deployed(t, start)
	c := a.Client()
	r, err := c.AdoptProject(ctx, tiad.AdoptRequest{Name: "adopted", Deploy: d.ID})
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if r.Project != "projects/adopted/adopted.ap21" || r.CPUAddress != benchCPU || r.Source.Deploy != d.ID || r.Source.SHA256 != d.Archive.SHA256 || r.Source.Archive != "" ||
		r.Address != benchCPU || r.CPU == nil || r.CPU.Name != "PLC_1" || r.CPU.Order != BenchStation().CPU.Order {
		t.Fatalf("adopt: %+v", r)
	}
	noSession(t, c)
	s, err := c.Open(ctx, r.Project)
	if err != nil {
		t.Fatalf("open the adopted project: %v", err)
	}
	// As found, before any spec: it compares with its own CPU.
	if cmp, err := s.CompareWith(ctx, tiad.CompareRequest{Secrets: BenchLogin}); err != nil || !cmp.Equal {
		t.Fatalf("the adopted project as found against its CPU: %+v %v", cmp, err)
	}
	st, err := s.ApplyStation(ctx, BenchStation(), BenchSecrets)
	if err != nil || len(st.Changes) != 0 {
		t.Fatalf("the station it was loaded with: %+v %v", st, err)
	}
	cmp, err := s.CompareWith(ctx, tiad.CompareRequest{Secrets: BenchLogin})
	if err != nil || !cmp.Equal {
		t.Fatalf("the adopted project against the CPU running its load: %+v %v", cmp, err)
	}
	closeFirst(t, s)
	// Another address is a change, with what it was and is.
	s, err = c.Open(ctx, r.Project)
	if err != nil {
		t.Fatal(err)
	}
	moved := BenchStation()
	moved.CPU.Address = "192.0.2.99"
	st, err = s.ApplyStation(ctx, moved, BenchSecrets)
	want := `cpu PLC_1 address "` + benchCPU + `" -> "192.0.2.99"`
	if err != nil || !reflect.DeepEqual(st.Changes, []string{want}) {
		t.Fatalf("another address: %+v %v", st, err)
	}
	closeFirst(t, s)
	// Turning the web server off writes: a change, never "no change".
	s, err = c.Open(ctx, r.Project)
	if err != nil {
		t.Fatal(err)
	}
	off := BenchStation()
	off.WebServer = &tiad.WebServer{Enabled: false}
	st, err = s.ApplyStation(ctx, off, BenchSecrets)
	if err != nil || !reflect.DeepEqual(st.Changes, []string{"cpu PLC_1 users, roles or web server changed as the spec says"}) {
		t.Fatalf("the web server off: %+v %v", st, err)
	}
	closeFirst(t, s)
	// Never over a project unasked: the name is taken now. Asked, it is
	// replaced, as a create replaces one.
	if _, err := c.AdoptProject(ctx, tiad.AdoptRequest{Name: "adopted", Deploy: d.ID}); tiad.Kind(err) != tiad.KindBadRequest ||
		!strings.Contains(err.Error(), "holds a project already") {
		t.Fatalf("adopt over the adopted project: %v", err)
	}
	if r2, err := c.AdoptProject(ctx, tiad.AdoptRequest{Name: "adopted", Deploy: d.ID, Replace: true}); err != nil || r2.Project != r.Project {
		t.Fatalf("adopt again, replacing: %+v %v", r2, err)
	}
	noSession(t, c)
}

// contractAdoptFromArchive: a project archive a save wrote, copied
// anywhere in the work dir, adopts with its CPU and devices; a file that is
// no archive adopts nothing and leaves no project behind.
func contractAdoptFromArchive(t *testing.T, start Starter) {
	a, s := compiledBench(t, start)
	c := a.Client()
	p, err := s.Save(ctx, true)
	if err != nil {
		t.Fatalf("save an archive: %v", err)
	}
	closeFirst(t, s)
	b, err := c.GetFile(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.PutFile(ctx, "naut-src/x/customer.zap21", b); err != nil {
		t.Fatal(err)
	}
	// Expected elsewhere, it is another CPU's project: refused, nothing left.
	if _, err := c.AdoptProject(ctx, tiad.AdoptRequest{Name: "customer", Archive: "naut-src/x/customer.zap21", Address: "192.0.2.77"}); tiad.Kind(err) != tiad.KindCPUMismatch ||
		!strings.Contains(err.Error(), "not at 192.0.2.77") {
		t.Fatalf("an archive of another CPU: %v", err)
	}
	if _, err := c.Open(ctx, "projects/customer/customer.ap21"); tiad.Kind(err) != tiad.KindNotFound {
		t.Fatalf("a refused adopt left a project: %v", err)
	}
	r, err := c.AdoptProject(ctx, tiad.AdoptRequest{Name: "customer", Archive: "naut-src/x/customer.zap21", Address: benchCPU})
	if err != nil {
		t.Fatalf("adopt an archive: %v", err)
	}
	if r.Project != "projects/customer/customer.ap21" || r.Source.Archive != "naut-src/x/customer.zap21" || r.Source.Deploy != "" ||
		len(r.Source.SHA256) != 64 || r.Source.Bytes != int64(len(b)) || r.CPU == nil || r.CPU.Name != "PLC_1" || r.Address != "" {
		t.Fatalf("adopt an archive: %+v", r)
	}
	noSession(t, c)
	o, err := c.Open(ctx, r.Project)
	if err != nil {
		t.Fatalf("open the adopted project: %v", err)
	}
	if st, err := o.ApplyStation(ctx, BenchStation(), BenchSecrets); err != nil || len(st.Changes) != 0 {
		t.Fatalf("the archived station: %+v %v", st, err)
	}
	closeFirst(t, o)

	if _, err := c.PutFile(ctx, "naut-src/x/junk.zap21", []byte("not a project archive")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.AdoptProject(ctx, tiad.AdoptRequest{Name: "junk", Archive: "naut-src/x/junk.zap21"}); err == nil {
		t.Fatal("a file that is no archive adopted")
	}
	noSession(t, c)
	if _, err := c.Open(ctx, "projects/junk/junk.ap21"); tiad.Kind(err) != tiad.KindNotFound {
		t.Fatalf("a failed adopt left a project: %v", err)
	}
}

// contractAdoptRefusals: one source, named; an archive is a .zap21; a
// deploy that loaded nothing, or none, adopts nothing; an existing project
// is never replaced.
func contractAdoptRefusals(t *testing.T, start Starter) {
	a, s := compiledBench(t, start)
	c := a.Client()
	a.SetQuestions(t, []tiad.Question{{ID: tiad.QuestionStopCPU, Message: "Stop the CPU"}})
	dl := benchSoftwareLoad
	dl.Decisions.StopCPU = tiad.No()
	_, err := s.Download(ctx, dl)
	if tiad.Kind(err) != tiad.KindDeclined {
		t.Fatalf("a declined download: %v", err)
	}
	closeFirst(t, s)
	list, err := c.Deploys(ctx, benchCPU)
	if err != nil || len(list.Deploys) != 1 || list.Deploys[0].Status != tiad.DeployNotLoaded {
		t.Fatalf("deploys: %+v %v", list, err)
	}
	if _, err := c.AdoptProject(ctx, tiad.AdoptRequest{Name: "nl", Deploy: list.Deploys[0].ID}); tiad.Kind(err) != tiad.KindBadRequest ||
		!strings.Contains(err.Error(), "loaded nothing") {
		t.Fatalf("a deploy that loaded nothing: %v", err)
	}
	if _, err := c.AdoptProject(ctx, tiad.AdoptRequest{Name: "none", Deploy: "d-20260101T000000000Z-00000000"}); tiad.Kind(err) != tiad.KindNotFound {
		t.Fatalf("no such deploy: %v", err)
	}
	if _, err := c.PutFile(ctx, "naut-src/x/a.ap21", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.AdoptProject(ctx, tiad.AdoptRequest{Name: "notzap", Archive: "naut-src/x/a.ap21"}); tiad.Kind(err) != tiad.KindBadRequest ||
		!strings.Contains(err.Error(), ".zap21") {
		t.Fatalf("not an archive's name: %v", err)
	}
	// Both sources, or none: the client refuses, and so does the agent.
	for _, req := range []map[string]string{{"name": "x"}, {"name": "x", "archive": "a.zap21", "deploy": "d-1"}} {
		if err := rawPost(t, c, "/v1/projects/adopt", req); tiad.Kind(err) != tiad.KindBadRequest || !strings.Contains(err.Error(), "exactly one source") {
			t.Fatalf("%v: %v", req, err)
		}
	}
	p, err := c.Open(ctx, "projects/bench/bench.ap21")
	if err == nil {
		closeFirst(t, p)
	}
	if _, err := c.PutFile(ctx, "naut-src/x/b.zap21", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.AdoptProject(ctx, tiad.AdoptRequest{Name: "bench", Archive: "naut-src/x/b.zap21"}); tiad.Kind(err) != tiad.KindBadRequest ||
		!strings.Contains(err.Error(), "holds a project already") {
		t.Fatalf("adopt over a created project: %v", err)
	}
	noSession(t, c)
}

// rawPost posts body as JSON without the client's own checks and returns
// the agent's error, nil on success.
func rawPost(t *testing.T, c *tiad.Client, rawPath string, body any) error {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	h := http.Header{"Authorization": {"Bearer " + c.Token}, "Content-Type": {"application/json"}}
	_, out := rawWith(t, c, http.MethodPost, rawPath, h, b)
	var env struct {
		OK    bool        `json:"ok"`
		Error *tiad.Error `json:"error"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("%s: %v (%s)", rawPath, err, out)
	}
	if env.OK {
		return nil
	}
	return env.Error
}
