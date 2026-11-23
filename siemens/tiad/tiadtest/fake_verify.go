package tiadtest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"

	"github.com/joyautomation/nautilus/siemens/station"
	"github.com/joyautomation/nautilus/siemens/tiad"
)

// A deploy's verify, as the agent does it (tools/tiad/core/Verify.cs): a
// copy of the archive hashed against the record, a session of its own,
// the archived project's CPU address checked against the record's, the
// compare, an optional fingerprint read, the session closed.

// archivedProject is what an archive holds: its CPU's address and name.
type archivedProject struct {
	address, cpu string
	// project is the project as archived: what an adopt retrieves.
	project savedProject
}

// verifyTrustFix is the agent's VerifyTrustFix.
const verifyTrustFix = "to read it with the verify, trust the CPU's certificate blindly (fingerprint.trustUnverifiedCertificate: true); the agent's TIA instance then keeps that trust until the verify's session closes (the agent then retires that instance)"

var deployIDShape = regexp.MustCompile(`^[0-9A-Za-z-]{1,64}$`)

var archiveFile = regexp.MustCompile(`^archives/([0-9a-f]{64})\.zap21$`)

// LoadOutside makes the CPU at address hold a load the agent did not make
// (another engineering station's), as its fingerprint shows too.
func (f *Fake) LoadOutside(address string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	f.loads[address]++
	f.loaded[address] = "outside"
	f.loadedArchive[address] = "outside"
}

// LoadArchive makes the CPU at address hold what the archive file
// (DeployArchive.File) holds, loaded without the agent.
func (f *Fake) LoadArchive(address, file string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	m := archiveFile.FindStringSubmatch(file)
	if m == nil {
		panic("LoadArchive: not an archive file: " + file)
	}
	f.loads[address]++
	f.loaded[address] = "outside"
	f.loadedArchive[address] = m[1]
}

// SwapArchive replaces the bytes of the archive at file with those of the
// archive at with: what a swapped or altered archive looks like.
func (f *Fake) SwapArchive(file, with string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, b := archiveFile.FindStringSubmatch(file), archiveFile.FindStringSubmatch(with)
	if a == nil || b == nil {
		panic("SwapArchive: not archive files: " + file + ", " + with)
	}
	f.archives[a[1]] = append([]byte(nil), f.archives[b[1]]...)
}

// SetDeployAddress rewrites the address of the record id: what a mixed-up
// or tampered journal line looks like.
func (f *Fake) SetDeployAddress(id, address string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.deployRecs {
		if d.ID == id {
			d.Address = address
			return
		}
	}
	panic("SetDeployAddress: no deploy " + id)
}

func boolp(b bool) *bool { return &b }

func (f *Fake) verifyDeploy(r *http.Request) (any, *failure) {
	var req tiad.VerifyRequest
	if fl := decode(r, &req); fl != nil {
		return nil, fl
	}
	if req.Deploy == "" {
		return nil, fail(http.StatusBadRequest, tiad.KindBadRequest, "verify: deploy is required")
	}
	if !deployIDShape.MatchString(req.Deploy) {
		return nil, fail(http.StatusNotFound, tiad.KindNotFound, "no deploy %q", req.Deploy)
	}
	// The verify's session is reserved first: a busy refusal touches
	// nothing.
	if fl := f.oneProject(); fl != nil {
		return nil, fl
	}
	var d *tiad.Deploy
	for _, x := range f.deployRecs {
		if x.ID == req.Deploy {
			d = x
		}
	}
	if d == nil {
		return nil, fail(http.StatusNotFound, tiad.KindNotFound, "no deploy %q", req.Deploy)
	}
	address := d.Address
	res := tiad.VerifyResult{Deploy: d.ID, Address: address, Status: d.Status}
	for i := len(f.deployRecs) - 1; i >= 0; i-- {
		x := f.deployRecs[i]
		if x.Address == address && x.Status != tiad.DeployNotLoaded {
			res.Latest = x.ID
			break
		}
	}
	broken := ""
	var sha string
	if !station.IsIPv4(address) {
		broken = fmt.Sprintf("the record's address %q is not an IPv4 address", address)
	} else {
		if !contains(f.Targets, address) {
			return nil, fail(http.StatusForbidden, tiad.KindTargetRefused, "%s is not in TIAD_TARGETS", address)
		}
		m := archiveFile.FindStringSubmatch(d.Archive.File)
		b, ok := f.archives[d.Archive.SHA256]
		switch {
		case m == nil || m[1] != d.Archive.SHA256:
			broken = "the record's archive entry is not one tiad writes (archives/<sha256>.zap21 with its sha256)"
		case !ok:
			broken = "the archive " + d.Archive.File + " is gone"
		default:
			sum := sha256.Sum256(b)
			if got := hex.EncodeToString(sum[:]); got != d.Archive.SHA256 {
				broken = "the archive " + d.Archive.File + " has sha256 " + got + ", not the record's"
			}
			sha = hex.EncodeToString(sum[:])
		}
	}
	if broken != "" {
		res.Verdict, res.Broken = tiad.VerifyBroken, broken
		return res, nil
	}
	s := f.newSession("deploy:" + d.ID)
	res.Session = s.id
	defer func() {
		if f.VerifyCloseError != "" {
			return
		}
		delete(f.sessions, s.id)
		f.retireIfTrusted()
	}()
	held := f.archived[sha]
	if held.address != address {
		at := "at " + held.address
		if held.address == "" {
			at = "at no address"
		}
		return nil, &failure{status: http.StatusConflict, kind: tiad.KindCPUMismatch,
			msg: fmt.Sprintf("deploy %s names CPU %s, but its archived project's CPU is %s: the verify was refused before anything went online (a record and an archive that disagree name no CPU to compare)",
				d.ID, address, at),
			data: map[string]any{"recordAddress": address, "projectAddress": held.address}}
	}
	if fl := f.checkDevicePin(address, "verify"); fl != nil {
		return nil, fl
	}
	password := onlinePassword(req.Secrets)
	why, loggedOn := f.login(address, password, f.blindTrust)
	if why != "" {
		f.forgetFailedCompare(address)
		return nil, fail(http.StatusInternalServerError, tiad.KindOperationFailed, "TIA: %s", why)
	}
	equal := f.loadedArchive[address] == sha
	f.noteCompare(address, password, loggedOn, equal)
	res.BlindTrust = f.blindTrust
	res.Differences = []tiad.Difference{}
	// As the agent's compare names them: every program block the
	// archived project holds ("Main [OB1]").
	held.project.sessionBlocks(func(b projectBlock) {
		res.Blocks = append(res.Blocks, fmt.Sprintf("%s [%s%d]", b.name, b.kind, b.number))
	})
	res.Verdict = tiad.VerifyEqual
	if !equal {
		res.Verdict = tiad.VerifyDifferent
		res.Differences = []tiad.Difference{{Object: held.cpu, State: "different"}}
	}
	if d.Status == tiad.DeployLoaded {
		res.ChangedOutside = boolp(!equal && res.Latest == d.ID)
	}
	if req.Fingerprint != nil {
		fp, fl := f.fingerprintOf(s, address, req.Fingerprint.TrustUnverifiedCertificate, password, verifyTrustFix)
		if fl != nil {
			res.FingerprintError = fl.msg
		} else {
			vf := &tiad.VerifyFingerprint{Items: fp.Items, TrustedBlindly: fp.TrustedBlindly, BlindTrust: fp.BlindTrust}
			same := func(id string) *bool {
				if d.Outcome == nil || d.Outcome.Fingerprint == nil {
					return nil
				}
				was, ok := d.Outcome.Fingerprint.Value(id)
				now, ok2 := fp.Value(id)
				if !ok || !ok2 {
					return nil
				}
				return boolp(was == now)
			}
			vf.DownloadIDSame, vf.HardwareSame = same("DownloadId"), same("Hardware")
			res.Fingerprint = vf
		}
	}
	if f.VerifyCloseError != "" {
		res.CloseError = f.VerifyCloseError + ": close it again (DELETE /v1/sessions/" + s.id + ")"
	}
	return res, nil
}

// sessionBlocks calls each with the project's program blocks as a session
// on it reads them (session.projectBlocks).
func (p savedProject) sessionBlocks(each func(projectBlock)) {
	s := &session{blocks: p.blocks, groups: p.groups, obInfo: p.obInfo}
	for _, b := range s.projectBlocks() {
		each(b)
	}
}
