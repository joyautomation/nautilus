package tiadtest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/joyautomation/nautilus/siemens/tiad"
)

// projectNode is the CPU's interface node as a project holds it: the
// agent's ProjectCpu node fields. read is false for a node the fake does
// not know (the agent's NodeRead).
type projectNode struct {
	address, subnetMask, router, profinetName string
	auto, read                                bool
}

// nodeOf is the node an apply of st leaves.
func nodeOf(st tiad.Station) projectNode {
	return projectNode{address: st.CPU.Address, subnetMask: st.CPU.SubnetMask, router: st.CPU.Router, profinetName: st.CPU.ProfinetName, read: true}
}

// UploadedSubnetMask and UploadedProfinetName are the interface an upload
// brings with the CPU's station, besides its address (tiad-fake's
// UploadedSubnetMask and UploadedProfinetName): a spec that names them
// changes nothing on the first apply after the upload.
const (
	UploadedSubnetMask   = "255.255.255.0"
	UploadedProfinetName = "plc-1"
)

func uploadedNode(address string) projectNode {
	return projectNode{address: address, subnetMask: UploadedSubnetMask, profinetName: UploadedProfinetName, read: true}
}

// FoundChanges is the agent's CpuReuse.Found: what the first apply on an
// opened or uploaded project changes in the CPU it reuses, in its words:
// the node, the protection step, and whether the access and web steps
// wrote anything. Empty is "no change".
func FoundChanges(found projectNode, next tiad.Station, protection string, usersWrote bool) []string {
	out := []string{}
	cpu := "cpu " + next.CPU.Name
	node := func(what, was, now string) {
		if was == now {
			return
		}
		q := func(v string) string {
			if v == "" {
				return "none"
			}
			return jsonQuote(v)
		}
		out = append(out, what+" "+q(was)+" -> "+q(now))
	}
	if !found.read {
		out = append(out, cpu+" network settings applied: the project's could not be read to compare")
	} else {
		node(cpu+" address", found.address, next.CPU.Address)
		node(cpu+" subnet mask", found.subnetMask, next.CPU.SubnetMask)
		node(cpu+" router", found.router, next.CPU.Router)
		if found.auto {
			out = append(out, cpu+" PROFINET name "+jsonQuote(next.CPU.ProfinetName)+" set (TIA generated it before)")
		} else {
			node(cpu+" PROFINET name", found.profinetName, next.CPU.ProfinetName)
		}
	}
	if protection != actNothing {
		out = append(out, cpu+" configuration protection: "+protection)
	}
	if usersWrote {
		out = append(out, cpu+" users, roles or web server changed as the spec says")
	}
	return out
}

// snapshot is the project as a save or an archive keeps it. restored is
// kept only while the project is unchanged since that load.
func (s *session) snapshot() savedProject {
	var cpu *tiad.ProjectCPU
	if s.cpu != nil {
		c := *s.cpu
		cpu = &c
	}
	sp := savedProject{cpu: cpu, others: append([]tiad.KeptDevice(nil), s.others...), protection: s.protection, protectedWith: s.protectedWith,
		accessControl: s.accessControl, access: s.access.clone(), webServer: s.webServer, webHTTPS: s.webHTTPS,
		blocks: slices.Clone(s.blocks), groups: maps.Clone(s.groups), obInfo: maps.Clone(s.obInfo), node: s.node}
	if !s.changed {
		sp.restored = s.restored
	}
	return sp
}

// adoptProject is POST /v1/projects/adopt (the agent's Adopt.cs): an
// archive becomes the deploy's saved project, projects/<name>/<name>.ap21.
func (f *Fake) adoptProject(r *http.Request) (any, *failure) {
	var req struct {
		Name    string `json:"name"`
		Archive string `json:"archive"`
		Deploy  string `json:"deploy"`
		Replace bool   `json:"replace"`
		Address string `json:"address"`
	}
	if fl := decode(r, &req); fl != nil {
		return nil, fl
	}
	if err := tiad.CheckProjectName(req.Name); err != nil {
		return nil, fail(http.StatusBadRequest, tiad.KindBadRequest, "%v", err)
	}
	if (req.Archive == "") == (req.Deploy == "") {
		return nil, fail(http.StatusBadRequest, tiad.KindBadRequest, "adopt: name exactly one source, archive (a project archive in the work dir) or deploy (a deploy record's archive)")
	}
	project := "projects/" + req.Name + "/" + req.Name + ".ap21"
	guard := "projects/" + req.Name + "/"
	out := map[string]any{"project": project}
	source := map[string]any{}
	var sp savedProject
	var b []byte
	pendingTurnOn := false
	if req.Deploy != "" {
		var d *tiad.Deploy
		for _, x := range f.deployRecs {
			if x.ID == req.Deploy {
				d = x
			}
		}
		if d == nil {
			return nil, fail(http.StatusNotFound, tiad.KindNotFound, "no deploy %s", jsonQuote(req.Deploy))
		}
		if d.Status == tiad.DeployNotLoaded {
			return nil, fail(http.StatusBadRequest, tiad.KindBadRequest, "deploy %s loaded nothing (status not-loaded): its archive is not what any CPU runs; nothing was adopted", jsonQuote(req.Deploy))
		}
		sha := d.Archive.SHA256
		var ok bool
		if b, ok = f.archives[sha]; !ok || d.Archive.File != "archives/"+sha+".zap21" {
			return nil, fail(http.StatusConflict, tiad.KindBadRequest, "deploy %s's archive is gone; nothing was adopted", req.Deploy)
		}
		if sum := sha256.Sum256(b); hex.EncodeToString(sum[:]) != sha {
			return nil, fail(http.StatusConflict, tiad.KindBadRequest, "deploy %s's archive has sha256 %s, not the record's: it is broken, and nothing was adopted", req.Deploy, hex.EncodeToString(sum[:]))
		}
		held := f.archived[sha]
		sp = held.project
		sp.restored = sha
		pendingTurnOn = d.AccessTurnOn && d.Status != tiad.DeployLoaded
		source["deploy"] = req.Deploy
		out["address"] = d.Address
		if held.address != d.Address {
			return nil, fail(http.StatusConflict, tiad.KindBadRequest, "deploy %s names CPU %s, but its archived project's CPU is at %s: nothing was adopted", req.Deploy, d.Address, held.address)
		}
	} else {
		p, fl := cleanPath(req.Archive)
		if fl != nil {
			return nil, fl
		}
		if !strings.HasSuffix(strings.ToLower(p), ".zap21") {
			return nil, fail(http.StatusBadRequest, tiad.KindBadRequest, "adopt: archive %s is not a project archive (.zap21)", jsonQuote(p))
		}
		if strings.HasPrefix(strings.ToLower(p), strings.ToLower(guard)) {
			return nil, fail(http.StatusBadRequest, tiad.KindBadRequest, "adopt: archive %s is inside projects/%s/, which the adopt replaces: copy it elsewhere in the work dir first", jsonQuote(p), req.Name)
		}
		var ok bool
		if b, ok = f.files[p]; !ok {
			return nil, fail(http.StatusNotFound, tiad.KindNotFound, "no file %s", p)
		}
		source["archive"] = p
	}
	if fl := f.oneProject(); fl != nil {
		return nil, fl
	}
	for p := range f.files {
		if strings.HasPrefix(strings.ToLower(p), strings.ToLower(guard)) && !req.Replace {
			return nil, fail(http.StatusConflict, tiad.KindBadRequest,
				"projects/%s/ holds a project already: adopt makes a deploy's first project and replaces one only when asked (replace); open it (POST /v1/sessions)", req.Name)
		}
	}
	// As the agent: a replace removes the project there once the source
	// is staged and hashed, before TIA retrieves it; whatever fails after
	// leaves no project.
	if req.Replace {
		for p := range f.files {
			if strings.HasPrefix(strings.ToLower(p), strings.ToLower(guard)) {
				delete(f.files, p)
				delete(f.saved, p)
			}
		}
	}
	if req.Archive != "" {
		// What TIA retrieves from the file: a deploy's archive or a
		// save's, else nothing.
		sum := sha256.Sum256(b)
		sha := hex.EncodeToString(sum[:])
		switch held, deployed := f.archived[sha]; {
		case deployed:
			sp = held.project
			sp.restored = sha
		default:
			saved, ok := f.savedArchives[sha]
			if !ok {
				return nil, fail(http.StatusInternalServerError, tiad.KindOperationFailed, "TIA: %s is not a project archive TIA can retrieve; nothing was adopted", source["archive"])
			}
			sp = saved
		}
	}
	if sp.cpu == nil {
		return nil, fail(http.StatusConflict, tiad.KindBadRequest, "the archive's project holds no CPU tiad can read: nothing was adopted")
	}
	at := ""
	if sp.node.read {
		at = sp.node.address
	}
	if req.Address != "" && at != req.Address {
		return nil, fail(http.StatusConflict, tiad.KindCPUMismatch, "the archive's project has its CPU at %s, not at %s, the deploy's CPU: it is another CPU's project (or one from before the address changed), and nothing was adopted", at, req.Address)
	}
	out["cpuAddress"] = at
	sum := sha256.Sum256(b)
	source["sha256"] = hex.EncodeToString(sum[:])
	source["bytes"] = len(b)
	f.files[project] = []byte("fake TIA project")
	if f.saved == nil {
		f.saved = map[string]savedProject{}
	}
	f.saved[project] = sp
	delete(f.turnOnPending, project)
	if pendingTurnOn {
		if f.turnOnPending == nil {
			f.turnOnPending = map[string]bool{}
		}
		f.turnOnPending[project] = true
		out["notes"] = []string{tiad.TurnOnPendingNote}
	}
	if sp.cpu != nil {
		out["cpu"] = *sp.cpu
	} else {
		out["cpu"] = nil
	}
	out["keptDevices"] = keptList(sp.others)
	out["source"] = source
	return out, nil
}

// jsonQuote is the agent's Json.Quote: a string as a JSON string.
func jsonQuote(v string) string {
	b, _ := json.Marshal(v)
	return string(b)
}
