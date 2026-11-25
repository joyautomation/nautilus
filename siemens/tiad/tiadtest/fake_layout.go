package tiadtest

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/siemens/station"
	"github.com/joyautomation/nautilus/siemens/tiad"
)

// The data layout gate (#1341): the agent's LayoutCalls.cs and the
// fake TIA's layout, as tiad-fake's FakeTia.Layout makes it.

// LayoutScript scripts the fake TIA's layout export (Target.SetLayout).
type LayoutScript struct {
	// Unreadable are the objects whose export fails (know-how protected).
	Unreadable []string `json:"layoutUnreadable,omitempty"`
	// Edits are the objects whose layout changed outside any source (a
	// customer's edit in TIA): each listed object's hash moves while it
	// is listed.
	Edits []string `json:"layoutEdits,omitempty"`
}

// SetLayout scripts the fake TIA's layout export.
func (f *Fake) SetLayout(ls LayoutScript) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.layoutScript = ls
}

type verifiedLoad struct {
	deploy string
	at     time.Time
}

var quotedName = regexp.MustCompile(`"([^"]+)"`)

// layoutDecl is a source's declaration part: up to the first line that
// is BEGIN, the whole source without one, line ends as LF.
func layoutDecl(src string) string {
	var sb strings.Builder
	for _, line := range strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) == "BEGIN" {
			break
		}
		sb.WriteString(line + "\n")
	}
	return sb.String()
}

func layoutType(p projectBlock) string {
	switch {
	case p.dataType:
		return "PlcStruct"
	case p.kind == "DB":
		return "GlobalDB"
	}
	return p.kind
}

// layoutObjects is the fake TIA's layout of the session's project, as
// tiad-fake's: each object's hash from its kind, name, type, declaration
// part and a scripted edit, and the hashes of every project object its
// declaration names in quotes, however deep.
func (f *Fake) layoutObjects(s *session) []tiad.LayoutObject {
	var all []tiad.LayoutObject
	for _, p := range f.objects(s) {
		kind := "block"
		if p.dataType {
			kind = "type"
		}
		o := tiad.LayoutObject{Name: p.name, Kind: kind, Type: layoutType(p)}
		for _, m := range quotedName.FindAllStringSubmatch(s.decls[strings.ToLower(p.name)], -1) {
			r := m[1]
			if !strings.EqualFold(r, p.name) && !slices.ContainsFunc(o.Refs, func(x string) bool { return strings.EqualFold(x, r) }) {
				o.Refs = append(o.Refs, r)
			}
		}
		sort.Slice(o.Refs, func(i, j int) bool { return strings.ToLower(o.Refs[i]) < strings.ToLower(o.Refs[j]) })
		all = append(all, o)
	}
	byName := map[string]*tiad.LayoutObject{}
	for i := range all {
		byName[strings.ToLower(all[i].Name)] = &all[i]
	}
	edited := func(name string) string {
		if slices.ContainsFunc(f.layoutScript.Edits, func(x string) bool { return strings.EqualFold(x, name) }) {
			return "edited"
		}
		return ""
	}
	done := map[string]string{}
	var hash func(o *tiad.LayoutObject, visiting map[string]bool) string
	hash = func(o *tiad.LayoutObject, visiting map[string]bool) string {
		k := strings.ToLower(o.Name)
		if h, ok := done[k]; ok {
			return h
		}
		if visiting[k] {
			return "cycle"
		}
		visiting[k] = true
		var sb strings.Builder
		sb.WriteString(o.Kind + "|" + o.Name + "|" + o.Type + "|" + s.decls[k] + "|" + edited(o.Name))
		for _, r := range o.Refs {
			if x, ok := byName[strings.ToLower(r)]; ok {
				sb.WriteString("\n" + hash(x, visiting))
			}
		}
		sum := sha256.Sum256([]byte(sb.String()))
		h := hex.EncodeToString(sum[:])
		done[k] = h
		return h
	}
	for i := range all {
		all[i].Hash = hash(&all[i], map[string]bool{})
		if slices.ContainsFunc(f.layoutScript.Unreadable, func(x string) bool { return strings.EqualFold(x, all[i].Name) }) {
			all[i].Hash = ""
			all[i].Unreadable = "TIA did not export it: know-how protected (fake)"
		}
	}
	return all
}

// fakeAdopt is the agent's AdoptBaseline: a session's layout as opened,
// which an equal compare proved is what the CPU at address runs (#1347).
type fakeAdopt struct {
	address, id, journalHead string
	objects                  []tiad.LayoutObject
	gen                      int
	at                       time.Time
}

// routeList registers routes on a mux and keeps their list.
type routeList struct {
	mux *http.ServeMux
	f   *Fake
}

func (m routeList) HandleFunc(pattern string, h http.HandlerFunc) {
	m.mux.HandleFunc(pattern, h)
	m.f.routes = append(m.f.routes, pattern)
}

// testRoutes is GET /v1/test/routes: every route, as tiad-fake's seam.
func (f *Fake) testRoutes(*http.Request) (any, *failure) {
	return map[string]any{"routes": slices.Clone(f.routes)}, nil
}

// journalHead is the agent's JournalHead: the id of the CPU's newest
// journal line, any status, "" for none; ok false when the journal
// cannot be read whole (why).
func (f *Fake) journalHead(address string) (string, string) {
	if f.unreadableLines > 0 {
		return "", "the deploy journal holds " + strconv.Itoa(f.unreadableLines) + " line(s) the agent cannot read, which may be a newer load"
	}
	for i := len(f.deployRecs) - 1; i >= 0; i-- {
		if f.deployRecs[i].Address == address {
			return f.deployRecs[i].ID, ""
		}
	}
	return "", ""
}

// adoptBaselineRefusal is the agent's AdoptBaselineRefusal, "" when the
// agent and the session allow an adopt baseline.
func (f *Fake) adoptBaselineRefusal(s *session) string {
	if f.UI {
		return "the agent runs with TIA's window (TIAD_UI), where a person can change the project by hand: an adopt baseline needs a project only tiad changes"
	}
	if !s.untouched {
		return "the session is not untouched: a call that may change the project ran on it since it was opened (only compare and export keep it); open the project again and compare first"
	}
	return ""
}

// takeAdoptBaseline is the agent's TakeAdoptBaseline, after an equal
// compare with the CPU at address.
func (f *Fake) takeAdoptBaseline(s *session, address string) *tiad.AdoptBaselineResult {
	head, why := f.journalHead(address)
	if why == "" {
		why = f.adoptBaselineRefusal(s)
	}
	if why != "" {
		return &tiad.AdoptBaselineResult{Why: why}
	}
	now := f.layoutObjects(s)
	a := &fakeAdopt{address: address, id: tiad.AdoptBaselineID(now), journalHead: head, objects: now, gen: f.loadGen[address], at: time.Now()}
	s.adopted = a
	n := 0
	for _, o := range now {
		if o.Hash == "" {
			n++
		}
	}
	return &tiad.AdoptBaselineResult{Taken: true, ID: a.id, Objects: len(now), Unreadable: n}
}

// adoptedValid is the agent's AdoptedValid: the session's adopt baseline
// for address while it stands, or why not ("" and nil: none).
func (f *Fake) adoptedValid(s *session, address string) (*fakeAdopt, string) {
	a := s.adopted
	switch {
	case a == nil && s.adoptRefused != "":
		// A compare asked for one and took none: its reason.
		return nil, "the adopt compare took no baseline: " + s.adoptRefused
	case a == nil:
		return nil, ""
	case a.address != address:
		return nil, "the session's adopt baseline was taken for the CPU at " + a.address + ", not " + address
	case time.Since(a.at) > tiad.LayoutVerifyMaxAge:
		return nil, "the compare that took the session's adopt baseline is more than an hour old: open the project again and compare"
	case f.loadGen[address] != a.gen:
		return nil, "a download reached the CPU since the compare that took the session's adopt baseline"
	}
	head, why := f.journalHead(address)
	if why != "" {
		return nil, why
	}
	if head != a.journalHead {
		return nil, "the CPU's deploy journal has a newer line (" + head + ") than when the session's adopt baseline was taken"
	}
	return a, ""
}

// layoutBaseline is the agent's Baseline: the session's adopt baseline
// while it stands, else the layout of the deploy the CPU at address
// verifiably holds, or why there is none.
func (f *Fake) layoutBaseline(s *session, address string) (*tiad.LayoutBaseline, string) {
	a, adoptWhy := f.adoptedValid(s, address)
	if a != nil {
		return &tiad.LayoutBaseline{Deploy: a.id, Scheme: tiad.LayoutScheme, TIA: fakeTIAVersion, Objects: slices.Clone(a.objects)}, ""
	}
	b, why := f.journalBaseline(address, s.project)
	if b == nil && adoptWhy != "" {
		why = adoptWhy
	}
	return b, why
}

// journalBaseline is the agent's JournalBaseline: the layout of the
// deploy the CPU at address verifiably holds, or why there is none.
func (f *Fake) journalBaseline(address, project string) (*tiad.LayoutBaseline, string) {
	v, ok := f.verifiedLoads[address]
	if !ok {
		return nil, "no verify by this agent, since it started, found the CPU equal to its last deploy (a deploy runs one before every download)"
	}
	if time.Since(v.at) > tiad.LayoutVerifyMaxAge {
		return nil, "the verify that found the CPU equal to deploy " + v.deploy + " is more than an hour old: verify again"
	}
	if f.unreadableLines > 0 {
		return nil, "the deploy journal holds " + strconv.Itoa(f.unreadableLines) + " line(s) the agent cannot read, which may be a newer load"
	}
	var latest *tiad.Deploy
	for i := len(f.deployRecs) - 1; i >= 0; i-- {
		d := f.deployRecs[i]
		if d.Address == address && d.Status != tiad.DeployNotLoaded {
			latest = d
			break
		}
	}
	if latest == nil {
		return nil, "no deploy record for this CPU"
	}
	if latest.ID != v.deploy {
		return nil, "the CPU's newest load is deploy " + latest.ID + ", not deploy " + v.deploy + ", which the verify found equal"
	}
	if !strings.EqualFold(latest.Project, project) {
		return nil, "the CPU's newest load, deploy " + latest.ID + ", loaded another project (" + latest.Project + ")"
	}
	return tiad.LayoutBaselineOf(latest.ID, latest.Layout, fakeTIAVersion)
}

// fakeTIAVersion is the TIA version the fake reports (health, records).
const fakeTIAVersion = "V21"

// readLayout is the agent's ReadLayout.
func (f *Fake) readLayout(s *session, address string, hardware bool) (tiad.Layout, []tiad.LayoutObject) {
	now := f.layoutObjects(s)
	b, why := f.layoutBaseline(s, address)
	return tiad.DiffLayout(now, b, why, fakeTIAVersion, hardware), now
}

// layout is POST /v1/sessions/{id}/layout.
func (f *Fake) layout(r *http.Request) (any, *failure) {
	s, fl := f.session(r)
	if fl != nil {
		return nil, fl
	}
	var req struct {
		Address  string `json:"address"`
		Hardware bool   `json:"hardware"`
	}
	if fl := decode(r, &req); fl != nil {
		return nil, fl
	}
	if !station.IsIPv4(req.Address) {
		return nil, fail(http.StatusBadRequest, tiad.KindBadRequest, "layout: address %q is not an IPv4 address", req.Address)
	}
	if s.station == nil || s.uploaded {
		return nil, fail(http.StatusBadRequest, tiad.KindBadRequest, "the project has no station spec")
	}
	if s.station.CPU.Address != req.Address {
		return nil, fail(http.StatusBadRequest, tiad.KindBadRequest,
			"the project's CPU is at %s, not %s: one station spec per CPU", s.station.CPU.Address, req.Address)
	}
	if !s.compiled {
		return nil, fail(http.StatusConflict, tiad.KindNotCompiled, "the project changed since the last full compile; compile hardware and software first")
	}
	l, _ := f.readLayout(s, req.Address, req.Hardware)
	return l, nil
}

// layoutRefusal is the agent's LayoutRefusal.
func layoutRefusal(l tiad.Layout, req tiad.DownloadRequest) *failure {
	data := map[string]any{"layout": l}
	if req.LayoutDigest != "" && req.LayoutDigest != l.Digest {
		return &failure{status: http.StatusConflict, kind: tiad.KindDecisionRequired,
			msg: "the data layout is no longer the one the caller showed (layoutDigest \"" + req.LayoutDigest + "\", now " + l.Digest +
				"): the project, the CPU's last deploy or its verify changed since; read POST /v1/sessions/{id}/layout again; nothing was sent to the CPU",
			data: data}
	}
	if l.ResetDataNeeded && (req.Decisions.ResetData == nil || !*req.Decisions.ResetData) {
		data["questions"] = []tiad.Question{{ID: tiad.QuestionResetData, Message: l.Why, Objects: l.DataBlocks}}
		return &failure{status: http.StatusConflict, kind: tiad.KindDecisionRequired,
			msg: "the download needs decisions.resetData: " + l.Why + "; nothing was sent to the CPU", data: data}
	}
	return nil
}
