package tiadtest

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/siemens/station"
	"github.com/joyautomation/nautilus/siemens/tiad"
)

// The deploy record, as the agent keeps it (tools/tiad/core/Deploys.cs),
// in memory: the archives by sha256, the records oldest first.

// SetArchiveFails makes every archive the agent makes for a deploy record
// fail inside TIA's side with msg ("" archives again).
func (f *Fake) SetArchiveFails(msg string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.archiveFails = msg
}

// Archive is the archived project at file (DeployArchive.File), and
// whether the fake holds it.
func (f *Fake) Archive(file string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sha, ok := strings.CutPrefix(file, "archives/")
	if !ok {
		return nil, false
	}
	b, ok := f.archives[strings.TrimSuffix(sha, ".zap21")]
	return b, ok
}

func fakeNow() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

func newDeployID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	t := time.Now().UTC()
	return "d-" + t.Format("20060102T150405") + fmt.Sprintf("%03dZ", t.Nanosecond()/1e6) + "-" + hex.EncodeToString(b[:])
}

// deployTrustFix is the agent's DeployTrustFix.
const deployTrustFix = "to read it with the download, trust the CPU's certificate blindly (fingerprint.trustUnverifiedCertificate: true); the agent's TIA instance then keeps that trust, and a later download in it needs decisions.acceptBlindTrust, until every session is closed (the agent then retires that instance) or the agent restarts"

// recordIntent is the agent's Record: the project archived, then the
// intent line, just before TIA's download.
func (f *Fake) recordIntent(s *session, req tiad.DownloadRequest, attested *tiad.AttestationRecord) (*tiad.Deploy, *failure) {
	if s.unclean {
		return nil, uncleanFailure("archiving it for the deploy record")
	}
	if s.foreignChanged {
		return nil, foreignUnusable("archiving it for the deploy record")
	}
	if f.archiveFails != "" {
		return nil, fail(http.StatusInternalServerError, tiad.KindOperationFailed,
			"archiving the project for the deploy record failed (TIA: %s); nothing was sent to the CPU", f.archiveFails)
	}
	// Each archive's bytes differ, as a real one's do (time stamps).
	f.archiveN++
	b := []byte(fmt.Sprintf("fake TIA project archive %d of %s", f.archiveN, s.project))
	sum := sha256.Sum256(b)
	sha := hex.EncodeToString(sum[:])
	f.archives[sha] = b
	f.archived[sha] = archivedProject{address: s.station.CPU.Address, cpu: s.station.CPU.Name, project: s.snapshot()}
	s.archive = sha
	d := &tiad.Deploy{
		ID: newDeployID(), At: fakeNow(), Session: s.id, Project: s.project, Address: req.Address,
		Hardware: req.Hardware, Software: req.Software,
		TIA:         tiad.DeployTIA{Version: "V21", Openness: "V21", Agent: "fake"},
		Archive:     tiad.DeployArchive{File: "archives/" + sha + ".zap21", SHA256: sha, Bytes: int64(len(b))},
		Attestation: attested, DeviceAttestation: s.deviceAttested, KeptDevices: keptList(s.kept), BlindTrust: f.blindTrust,
		Status: tiad.DeployInDoubt,
	}
	if req.Source != nil {
		d.Source = &tiad.DeploySourceRecord{Commit: req.Source.Commit, Hash: req.Source.Hash, ClaimedBy: "caller"}
	}
	if o := req.OutsideChanges; o != nil {
		d.OutsideChanges = &tiad.OutsideChangesRecord{By: o.By, Verdict: o.Verdict, Deploy: o.Deploy, ClaimedBy: "caller"}
	}
	f.deployRecs = append(f.deployRecs, d)
	return d, nil
}

// recordOutcome is the agent's WriteOutcome, with the fingerprint read
// after a load that compared equal (ReadDeployFingerprint): res and fl are
// how TIA's part of the download ended.
func (f *Fake) recordOutcome(d *tiad.Deploy, s *session, req tiad.DownloadRequest, res any, fl *failure, acting []string, given string) (any, *failure) {
	// A reset given carries its route whatever the end, as the agent's.
	o := &tiad.DeployOutcome{At: fakeNow(), BlindTrust: d.BlindTrust, ActingAnswers: slices.Clone(acting), UserManagementReset: given}
	if fl != nil {
		o.Outcome = fl.kind
		o.Status = tiad.DeployStatusOf(fl.kind)
		o.Message = fl.msg
		if len(o.Message) > 4000 {
			o.Message = fmt.Sprintf("%s ... (%d characters in all)", o.Message[:4000], len(o.Message))
		}
		data, _ := fl.data.(map[string]any)
		if data == nil {
			data = map[string]any{}
		}
		if st, ok := data["state"].(string); ok {
			o.State = st
			o.StateSource, _ = data["stateSource"].(string)
		}
		// Whatever the end, the caller can find its record.
		data["deploy"] = d.ID
		fl.data = data
		d.Outcome, d.Status = o, o.Status
		return nil, fl
	}
	r := res.(tiad.DownloadResult)
	o.Outcome, o.Status = tiad.DeployLoaded, tiad.DeployLoaded
	o.State, o.StateSource = r.State, r.StateSource
	o.CompareEqual = true
	o.Blocks = slices.Clone(r.Blocks)
	o.HardwareVerified = r.HardwareVerified
	o.UserManagementReset = r.UserManagementReset
	dr := &tiad.DeployResult{ID: d.ID, Archive: d.Archive, Source: d.Source, OutsideChanges: d.OutsideChanges}
	trust := req.Fingerprint != nil && req.Fingerprint.TrustUnverifiedCertificate
	pw := ""
	if req.Secrets != nil {
		pw = req.Secrets.FullAccessPassword
	}
	fp, ffl := f.fingerprintOf(s, req.Address, trust, pw, deployTrustFix)
	if ffl != nil {
		dr.FingerprintError = ffl.msg
		o.FingerprintError = ffl.msg
	} else {
		df := &tiad.DeployFingerprint{Items: fp.Items, TrustedBlindly: fp.TrustedBlindly, BlindTrust: fp.BlindTrust}
		dr.Fingerprint, o.Fingerprint = df, df
		if v, ok := fp.Value("DownloadId"); ok {
			dr.DownloadID, o.DownloadID = v, v
		}
	}
	// The instance's trust after the read, read or failed.
	dr.BlindTrustAfter, o.BlindTrustAfter = f.blindTrust, f.blindTrust
	d.Outcome, d.Status = o, o.Status
	dr.Recorded = true
	r.Deploy = dr
	return r, nil
}

// listDeploys is GET /v1/deploys: newest first, for one CPU or all.
func (f *Fake) listDeploys(r *http.Request) (any, *failure) {
	q := r.URL.Query()
	address := ""
	for k, vs := range q {
		if k != "address" {
			return nil, fail(http.StatusBadRequest, tiad.KindBadRequest, "deploys: unknown query parameter %q", k)
		}
		if len(vs) != 1 {
			return nil, fail(http.StatusBadRequest, tiad.KindBadRequest, "deploys: address given more than once")
		}
		address = vs[0]
		if !station.IsIPv4(address) {
			return nil, fail(http.StatusBadRequest, tiad.KindBadRequest, "deploys: address %q is not an IPv4 address", address)
		}
	}
	out := tiad.DeployList{Deploys: []tiad.Deploy{}}
	for i := len(f.deployRecs) - 1; i >= 0; i-- {
		d := f.deployRecs[i]
		if address != "" && d.Address != address {
			continue
		}
		out.Deploys = append(out.Deploys, *d)
	}
	out.UnreadableLines = f.unreadableLines
	return out, nil
}

// SetUnreadableLines makes the deploy list report n journal lines the
// agent cannot read, as a damaged journal would.
func (f *Fake) SetUnreadableLines(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unreadableLines = n
}
