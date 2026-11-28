package tiad

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// The deploy record (siemens-ide.md §3.5 and §7, D7). Every real download
// archives the session's project just before TIA's download, in the same
// TIA work item, under the agent's own TIAD_DEPLOYS_DIR (never the work
// dir), and journals an intent line before TIA's download and an outcome
// line on every end. An archive or an intent line the agent cannot write
// refuses the download, with nothing sent to the CPU. After a load that
// compared equal, the agent reads the CPU's fingerprint in the same
// session (its DownloadId names the load): through the same trust path as
// Session.Fingerprint, with its own opt-in (DownloadRequest.Fingerprint).
// Client.VerifyDeploy opens a copy of a deploy's archive and compares it
// with the CPU: whether the CPU still runs that deploy.

// DeploySource is the caller's claim of what a deploy was built from: the
// git commit (40 or 64 lower-case hex digits) and, optionally, a hash of
// the sources sent ("sha256:" and 64 lower-case hex digits). The agent
// checks only its shape and records it as the caller's claim.
type DeploySource struct {
	Commit string `json:"commit"`
	Hash   string `json:"hash,omitempty"`
}

// DeploySourceHashPrefix starts DeploySource.Hash.
const DeploySourceHashPrefix = "sha256:"

func lowerHex(s string) bool {
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// Validate is the agent's DeploySource.Validate.
func (s DeploySource) Validate() error {
	if (len(s.Commit) != 40 && len(s.Commit) != 64) || !lowerHex(s.Commit) {
		return errors.New("source.commit is not a git commit id (40 or 64 lower-case hex digits)")
	}
	if s.Hash != "" {
		h, ok := strings.CutPrefix(s.Hash, DeploySourceHashPrefix)
		if !ok || len(h) != 64 || !lowerHex(h) {
			return errors.New("source.hash is not sha256:<64 lower-case hex digits>")
		}
	}
	return nil
}

// OutsideChanges is the operator's accept of a download onto a CPU whose
// deploy record says it was changed outside nautilus, or cannot say: By
// is who accepts, Verdict what the caller's check of the last deploy
// found (OutsideDifferent, OutsideBroken, OutsideOtherProject or
// OutsideUnreadable), Deploy
// that record's id ("" only for OutsideUnreadable, which found none to
// verify). The agent checks only its shape and journals it with the
// deploy, as the caller's claim, so an overwrite of someone's change is
// on the record (#789).
type OutsideChanges struct {
	By      string `json:"by"`
	Verdict string `json:"verdict"`
	Deploy  string `json:"deploy,omitempty"`
}

// OutsideChanges.Verdict's words.
const (
	// OutsideDifferent: the CPU compared different from the last deploy
	// that may have loaded it.
	OutsideDifferent = "different"
	// OutsideBroken: that deploy's archive is broken, so nothing says
	// what the CPU runs.
	OutsideBroken = "broken"
	// OutsideUnreadable: journal lines the agent cannot read may hide a
	// newer deploy.
	OutsideUnreadable = "unreadable"
	// OutsideOtherProject: the last deploy that may have loaded the CPU
	// (which may still compare equal) loaded another project than the one
	// this download builds on: a restore, another deploy (#1272).
	OutsideOtherProject = "other-project"
)

// MaxOutsideBy and MaxOutsideDeploy bound OutsideChanges, in bytes.
const (
	MaxOutsideBy     = 200
	MaxOutsideDeploy = 100
)

// Validate is the agent's OutsideChanges.Validate.
func (o OutsideChanges) Validate() error {
	switch {
	case strings.TrimSpace(o.By) == "":
		return errors.New("outsideChanges.by is empty: who accepts replacing the CPU's outside change")
	case len(o.By) > MaxOutsideBy:
		return fmt.Errorf("outsideChanges.by is longer than %d bytes", MaxOutsideBy)
	case o.Verdict != OutsideDifferent && o.Verdict != OutsideBroken && o.Verdict != OutsideOtherProject && o.Verdict != OutsideUnreadable:
		return errors.New("outsideChanges.verdict is not one of different, broken, other-project, unreadable")
	case len(o.Deploy) > MaxOutsideDeploy:
		return fmt.Errorf("outsideChanges.deploy is longer than %d bytes", MaxOutsideDeploy)
	case o.Deploy == "" && o.Verdict != OutsideUnreadable:
		return errors.New("outsideChanges.deploy is empty: name the deploy record whose verify found " + o.Verdict)
	}
	return nil
}

// OutsideChangesRecord is an accept as the agent journalled it: ClaimedBy
// is always "caller".
type OutsideChangesRecord struct {
	By        string `json:"by"`
	Verdict   string `json:"verdict"`
	Deploy    string `json:"deploy,omitempty"`
	ClaimedBy string `json:"claimedBy"`
}

// DeploySourceRecord is a source as the agent recorded it: ClaimedBy is
// always "caller", since the agent checked only its shape.
type DeploySourceRecord struct {
	Commit    string `json:"commit"`
	Hash      string `json:"hash,omitempty"`
	ClaimedBy string `json:"claimedBy"`
}

// DeployArchive is a deploy's archived project: File is relative to the
// agent's TIAD_DEPLOYS_DIR ("archives/<sha256>.zap21").
type DeployArchive struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// DeployFingerprint is the fingerprint the agent read after a load, as a
// fingerprint call returns it, without the address.
type DeployFingerprint struct {
	Items          []FingerprintItem `json:"items"`
	TrustedBlindly []string          `json:"trustedBlindly"`
	BlindTrust     *BlindTrust       `json:"blindTrust"`
}

// Value is the item named id, and whether the read holds it.
func (f DeployFingerprint) Value(id string) (string, bool) {
	return FingerprintResult{Items: f.Items}.Value(id)
}

// DeployResult is a successful download's deploy record (DownloadResult.Deploy).
type DeployResult struct {
	ID      string              `json:"id"`
	Archive DeployArchive       `json:"archive"`
	Source  *DeploySourceRecord `json:"source"`
	// OutsideChanges is the request's accept of an outside change, as
	// journalled; nil without one.
	OutsideChanges *OutsideChangesRecord `json:"outsideChanges,omitempty"`
	// Fingerprint is the read after the load, or nil with
	// FingerprintError saying why: a failed read leaves the load as it
	// is. FingerprintUnavailable, in place of FingerprintError, says the
	// CPU has no fingerprint to read: its access control is on (#1336),
	// so nothing was tried; verify is its reference. DownloadID is its
	// DownloadId item.
	Fingerprint            *DeployFingerprint `json:"fingerprint"`
	FingerprintError       string             `json:"fingerprintError,omitempty"`
	FingerprintUnavailable string             `json:"fingerprintUnavailable,omitempty"`
	DownloadID             string             `json:"downloadId,omitempty"`
	// BlindTrustAfter is the agent's TIA instance's blind trust once the
	// read ended, read or failed (a failed read with the opt-in may have
	// trusted): what a later download in it rides on.
	BlindTrustAfter *BlindTrust `json:"blindTrustAfter"`
	// Recorded is whether the outcome line was written; when not, the
	// record lists as in doubt and RecordError says why.
	Recorded    bool   `json:"recorded"`
	RecordError string `json:"recordError,omitempty"`
}

// Deploy statuses: what a record says of the CPU.
const (
	// DeployLoaded: TIA loaded and the compare right after was equal.
	DeployLoaded = "loaded"
	// DeployNotLoaded: refused or cancelled before anything was loaded.
	DeployNotLoaded = "not-loaded"
	// DeployInDoubt: the CPU may or may not be loaded, including a
	// record with no outcome line (the agent died during the download).
	DeployInDoubt = "in-doubt"
)

// DeployStatusOf is the agent's DeployStatus.Of: the status for a download
// that ended with kind (DeployLoaded on success), once its intent line was
// written. Only the kinds that there say nothing was loaded are
// DeployNotLoaded: a check TIA asked that the request does not answer yes,
// and TIA failing before it asked one. Anything else is in doubt.
func DeployStatusOf(kind string) string {
	switch kind {
	case DeployLoaded:
		return DeployLoaded
	case KindDecisionRequired, KindDeclined, KindOperationFailed:
		return DeployNotLoaded
	}
	return DeployInDoubt
}

// DeployTIA is the TIA the agent deployed with.
type DeployTIA struct {
	Version  string `json:"version"`
	Openness string `json:"openness"`
	Agent    string `json:"agent"`
}

// DeployOutcome is a deploy's outcome line.
type DeployOutcome struct {
	At     string `json:"at"`
	Status string `json:"status"`
	// Outcome is DeployLoaded, or the failure's kind (KindOutcomeUnknown,
	// KindDecisionRequired, ...); Message is the failure's text.
	Outcome             string             `json:"outcome"`
	Message             string             `json:"message,omitempty"`
	State               string             `json:"state,omitempty"`
	StateSource         string             `json:"stateSource,omitempty"`
	CompareEqual        bool               `json:"compareEqual,omitempty"`
	Blocks              []string           `json:"blocks,omitempty"`
	HardwareVerified    *bool              `json:"hardwareVerified,omitempty"`
	UserManagementReset string             `json:"userManagementReset,omitempty"`
	Fingerprint         *DeployFingerprint `json:"fingerprint,omitempty"`
	FingerprintError    string             `json:"fingerprintError,omitempty"`
	// FingerprintUnavailable: as DeployResult's.
	FingerprintUnavailable string      `json:"fingerprintUnavailable,omitempty"`
	DownloadID             string      `json:"downloadId,omitempty"`
	BlindTrustAfter        *BlindTrust `json:"blindTrustAfter,omitempty"`
	ActingAnswers          []string    `json:"actingAnswers,omitempty"`
	BlindTrust             *BlindTrust `json:"blindTrust"`
}

// Deploy is one deploy record: its intent line, with its outcome (nil
// when the agent wrote none). Status is the outcome's, or DeployInDoubt
// without one.
type Deploy struct {
	ID       string              `json:"id"`
	At       string              `json:"at"`
	Session  string              `json:"session"`
	Project  string              `json:"project"`
	Address  string              `json:"address"`
	Hardware bool                `json:"hardware"`
	Software bool                `json:"software"`
	Source   *DeploySourceRecord `json:"source"`
	// OutsideChanges is the download's accept of an outside change; nil
	// without one.
	OutsideChanges *OutsideChangesRecord `json:"outsideChanges,omitempty"`
	TIA            DeployTIA             `json:"tia"`
	Archive        DeployArchive         `json:"archive"`
	Attestation    *AttestationRecord    `json:"attestation"`
	// DeviceAttestation is the operator's reading of the CPU's identity
	// the download rested on (#1325); nil when none was used.
	DeviceAttestation *DeviceAttestationRecord `json:"deviceAttestation"`
	KeptDevices       []KeptDevice             `json:"keptDevices"`
	BlindTrust        *BlindTrust              `json:"blindTrust"`
	// AccessTurnOn: the download loaded a project an apply turned access
	// control on in (accessControlTurnOn); an adopt of its archive is
	// marked turn-on pending unless Status is loaded.
	AccessTurnOn bool `json:"accessTurnOn,omitempty"`
	// Layout is the data layout the download loaded (#1341): the next
	// download's baseline once a verify finds the CPU equal to this
	// deploy. Records made before the layout gate have none.
	Layout *DeployLayout `json:"layout,omitempty"`
	// LayoutBaseline is what the download's layout gate compared with: a
	// deploy id, or the session's adopt baseline ("adopted:...", #1347);
	// "" for none.
	LayoutBaseline string         `json:"layoutBaseline,omitempty"`
	Outcome        *DeployOutcome `json:"outcome"`
	Status         string         `json:"status"`
}

// DeployList is GET /v1/deploys: the records, newest first, and how many
// journal lines the agent could not read (skipped, never guessed at).
type DeployList struct {
	Deploys         []Deploy `json:"deploys"`
	UnreadableLines int      `json:"unreadableLines"`
}

// Deploys lists the agent's deploy records, newest first: every CPU's when
// address is "", else that CPU's. It never starts TIA.
func (c *Client) Deploys(ctx context.Context, address string) (DeployList, error) {
	var r DeployList
	p := "/v1/deploys"
	if address != "" {
		if !isIPv4(address) {
			return r, &Error{Kind: KindBadRequest, Message: fmt.Sprintf("deploys: address %q is not an IPv4 address", address)}
		}
		p += "?address=" + url.QueryEscape(address)
	}
	_, err := c.doJSON(ctx, http.MethodGet, p, nil, &r)
	return r, err
}

// DeployIDOf is the deploy record a failed download left (data.deploy), or
// "" when it left none: a download refused before the agent archived the
// project leaves none.
func DeployIDOf(err error) string {
	var e *Error
	if !errors.As(err, &e) || len(e.Data) == 0 {
		return ""
	}
	var d struct {
		Deploy string `json:"deploy"`
	}
	if json.Unmarshal(e.Data, &d) != nil {
		return ""
	}
	return d.Deploy
}

// Verify verdicts (VerifyResult.Verdict).
const (
	// VerifyEqual: the archived project's PLC software compared equal
	// with the CPU.
	VerifyEqual = "equal"
	// VerifyDifferent: it compared different.
	VerifyDifferent = "different"
	// VerifyBroken: the archive is not the one the record names (its
	// sha256 differs, it is gone, or the record's archive entry is not
	// one the agent writes). It is never opened, never equal.
	VerifyBroken = "broken"
)

// VerifyRequest is POST /v1/deploys/verify: the deploy to verify, and
// optionally a fingerprint read after the compare.
type VerifyRequest struct {
	Deploy string `json:"deploy"`
	// Fingerprint, when set, reads the CPU's fingerprint after the
	// compare, with its own opt-in to trust the CPU's certificate
	// blindly; the agent retires that trust when the verify's session
	// closes.
	Fingerprint *FingerprintRequest `json:"fingerprint,omitempty"`
	// Secrets answer TIA's credentials question for the compare and the
	// fingerprint read (OnlineSecrets).
	Secrets *OnlineSecrets `json:"secrets,omitempty"`
	// DeviceAttestation is the operator's reading of the CPU's order
	// number and firmware off the device, for a pinned CPU the agent
	// cannot identify online (DeviceAttestation, #1325); unused otherwise.
	DeviceAttestation *DeviceAttestation `json:"deviceAttestation,omitempty"`
}

// VerifyFingerprint is a verify's fingerprint read, with whether its
// DownloadId and Hardware items are the deploy record's (nil when the
// record or the read has no such item): a DownloadId that differs means
// the CPU was loaded since, whatever the compare says.
type VerifyFingerprint struct {
	Items          []FingerprintItem `json:"items"`
	TrustedBlindly []string          `json:"trustedBlindly"`
	BlindTrust     *BlindTrust       `json:"blindTrust"`
	DownloadIDSame *bool             `json:"downloadIdSame"`
	HardwareSame   *bool             `json:"hardwareSame"`
}

// VerifyResult is what a verify found. Nothing in the project, on the CPU
// or in the journal changes.
type VerifyResult struct {
	Deploy  string `json:"deploy"`
	Address string `json:"address"`
	// Status is the record's (DeployLoaded, ...); Latest the newest
	// record for the address that may have loaded it (loaded or in
	// doubt), "" for none.
	Status string `json:"status"`
	Latest string `json:"latest"`
	// UnreadableLines counts the journal lines the agent could not read:
	// one may be a newer deploy, so ChangedOutside is nil while any is.
	UnreadableLines int `json:"unreadableLines"`
	// Verdict is VerifyEqual, VerifyDifferent or VerifyBroken; Broken
	// says why an archive is broken.
	Verdict     string       `json:"verdict"`
	Broken      string       `json:"broken,omitempty"`
	Session     string       `json:"session,omitempty"`
	Differences []Difference `json:"differences,omitempty"`
	Blocks      []string     `json:"blocks,omitempty"`
	BlindTrust  *BlindTrust  `json:"blindTrust"`
	// ChangedOutside is set only for a loaded record that compared, with
	// every journal line read: true when it is Latest and compared
	// different (the CPU changed without a deploy of this agent), false
	// otherwise (a superseded record is expected to differ); nil for a
	// broken archive, a record that is not loaded, or unreadable lines.
	ChangedOutside *bool `json:"changedOutside"`
	// Fingerprint is the read when the request asked one (nil with
	// FingerprintError when it failed: the verdict stands; nil with
	// FingerprintUnavailable when the CPU's access control is on, #1336:
	// the verdict, from a logged-on compare, is the reference).
	Fingerprint            *VerifyFingerprint `json:"fingerprint,omitempty"`
	FingerprintError       string             `json:"fingerprintError,omitempty"`
	FingerprintUnavailable string             `json:"fingerprintUnavailable,omitempty"`
	// CloseError says the verify's session could not be closed: it is
	// kept, broken; close it again. The verdict stands.
	CloseError string `json:"closeError,omitempty"`
}

// VerifyMismatchOf returns the two addresses a verify refused with
// (cpu_mismatch): the record's, and the archived project's CPU's.
func VerifyMismatchOf(err error) (record, project string, ok bool) {
	var e *Error
	if !errors.As(err, &e) || e.Kind != KindCPUMismatch || len(e.Data) == 0 {
		return "", "", false
	}
	var d struct {
		Record  *string `json:"recordAddress"`
		Project *string `json:"projectAddress"`
	}
	if json.Unmarshal(e.Data, &d) != nil || d.Record == nil || d.Project == nil {
		return "", "", false
	}
	return *d.Record, *d.Project, true
}

// VerifyDeploy asks whether the CPU still runs a deploy: the agent opens
// a copy of its archive in a session of its own (so no other session may
// be open), refuses (KindCPUMismatch, VerifyMismatchOf) when the archived
// project's CPU is not at the record's address, compares it with the CPU
// online and closes the session. An archive whose sha256 is not the
// record's is VerifyBroken, never compared.
func (c *Client) VerifyDeploy(ctx context.Context, req VerifyRequest) (VerifyResult, error) {
	var r VerifyResult
	if req.Deploy == "" {
		return r, &Error{Kind: KindBadRequest, Message: "verify: deploy is required"}
	}
	if req.Fingerprint != nil && req.Fingerprint.Secrets != nil {
		return r, &Error{Kind: KindBadRequest, Message: "verify: the secrets go in the request's secrets, not its fingerprint's"}
	}
	_, err := c.doJSON(ctx, http.MethodPost, "/v1/deploys/verify", req, &r)
	return r, err
}
