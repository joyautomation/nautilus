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
	// Fingerprint is the read after the load, or nil with
	// FingerprintError saying why: a failed read leaves the load as it
	// is. DownloadID is its DownloadId item.
	Fingerprint      *DeployFingerprint `json:"fingerprint"`
	FingerprintError string             `json:"fingerprintError,omitempty"`
	DownloadID       string             `json:"downloadId,omitempty"`
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
// that ended with kind (DeployLoaded on success). Only the kinds that say
// nothing was loaded are DeployNotLoaded; anything else is in doubt.
func DeployStatusOf(kind string) string {
	switch kind {
	case DeployLoaded:
		return DeployLoaded
	case KindDecisionRequired, KindDeclined, KindBadRequest, KindTargetRefused, KindBusy, KindNotCompiled, KindOperationFailed:
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
	DownloadID          string             `json:"downloadId,omitempty"`
	ActingAnswers       []string           `json:"actingAnswers,omitempty"`
	BlindTrust          *BlindTrust        `json:"blindTrust"`
}

// Deploy is one deploy record: its intent line, with its outcome (nil
// when the agent wrote none). Status is the outcome's, or DeployInDoubt
// without one.
type Deploy struct {
	ID          string              `json:"id"`
	At          string              `json:"at"`
	Session     string              `json:"session"`
	Project     string              `json:"project"`
	Address     string              `json:"address"`
	Hardware    bool                `json:"hardware"`
	Software    bool                `json:"software"`
	Source      *DeploySourceRecord `json:"source"`
	TIA         DeployTIA           `json:"tia"`
	Archive     DeployArchive       `json:"archive"`
	Attestation *AttestationRecord  `json:"attestation"`
	KeptDevices []KeptDevice        `json:"keptDevices"`
	BlindTrust  *BlindTrust         `json:"blindTrust"`
	Outcome     *DeployOutcome      `json:"outcome"`
	Status      string              `json:"status"`
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
