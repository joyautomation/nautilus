package tiad

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// StateFromTIAMessage is DownloadResult.StateSource for a CPU mode read
// from TIA's download messages.
const StateFromTIAMessage = "tia-message"

// Question IDs: what TIA's pre-download checks can ask (siemens-ide.md
// §3.5). Anything tiad does not recognise is QuestionOther, which no
// decision answers.
const (
	QuestionStopCPU      = "stop-cpu"
	QuestionReinitialize = "reinitialize"
	QuestionOverwrite    = "overwrite"
	// QuestionConsistentDownload is TIA's ConsistentBlocksDownload check
	// ("Download software to device"), asked after stop-cpu: loading the
	// PLC software as one consistent whole. It acts; yes loads.
	QuestionConsistentDownload = "consistent-download"
	// The rest of the software-load family (#789 restart window), each
	// answered only by its own decision: alarm-texts (TIA's
	// AlarmTextLibrariesDownload, acting) and all-blocks
	// (AllBlocksDownload, acting) load; user-management
	// (UserManagementDownload) keeps the CPU's online users (passive), only
	// when the spec's accessControl is keep, or, on a hardware load, resets
	// them to the project's (acting), only under CheckReset;
	// identification-data (LoadIdentificationData) loads nothing (passive).
	QuestionAlarmTexts         = "alarm-texts"
	QuestionAllBlocks          = "all-blocks"
	QuestionUserManagement     = "user-management"
	QuestionIdentificationData = "identification-data"
	QuestionStartAfter         = "start-after"
	QuestionPassword           = "password"
	// QuestionConfigurationPassword asks for the password protecting the
	// CPU's confidential configuration data. It is answered only by
	// Secrets.ConfigurationPassword, never by the protection password.
	QuestionConfigurationPassword = "configuration-password"
	QuestionCertificate           = "certificate"
	// QuestionKeptDevices is the agent's own question, never TIA's: a
	// download also loads the devices an apply kept as found. Its Objects
	// name them; Decisions.KeptDevices must name exactly those.
	QuestionKeptDevices = "kept-devices"
	// QuestionUnverifiedHardware is the agent's own question too: a
	// download that loads the hardware configuration, which tiad cannot
	// verify (Openness V21 has no online hardware compare). Only
	// Decisions.AcceptUnverifiedHardware yes with the request's
	// Attestation answers it (CheckHardware).
	QuestionUnverifiedHardware = "unverified-hardware"
	// QuestionBlindTrust is the agent's own question too: a download in a
	// TIA instance that trusted a CPU's certificate blindly earlier
	// (CheckBlindTrust). Only Decisions.AcceptBlindTrust yes answers it.
	QuestionBlindTrust = "blind-trust"
	QuestionOther      = "other"
)

// Question is one pre-download check that needs an answer.
type Question struct {
	ID      string `json:"id"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"` // certificate: the SHA-256 fingerprint
	// Objects names what the question is about: the DBs a reinitialize
	// would reset.
	Objects []string `json:"objects,omitempty"`
}

// Decisions are a download's answers. A nil pointer is "not answered",
// which is different from "no".
type Decisions struct {
	StopCPU   *bool `json:"stopCpu,omitempty"`
	Overwrite *bool `json:"overwrite,omitempty"`
	// ConsistentDownload answers QuestionConsistentDownload; only yes
	// lets the download go on.
	ConsistentDownload *bool `json:"consistentDownload,omitempty"`
	// AlarmTexts answers QuestionAlarmTexts: load the alarm and text
	// list texts with the software.
	AlarmTexts *bool `json:"alarmTexts,omitempty"`
	// AllBlocks answers QuestionAllBlocks: load every block.
	AllBlocks *bool `json:"allBlocks,omitempty"`
	// UserManagementKeepOnline answers QuestionUserManagement: keep the
	// CPU's online user management as it is. The agent accepts it only
	// when the spec's accessControl is keep.
	UserManagementKeepOnline *bool `json:"userManagementKeepOnline,omitempty"`
	// UserManagementResetToProject answers QuestionUserManagement by
	// resetting the CPU's user management to the project's (TIA's
	// DownloadAllUserManagementDataResetToProject: "Delete user management
	// data and disable access control in runtime" when the project has
	// access control off). It acts, and the agent accepts it only under
	// CheckReset, and by the logon route only once the download logged
	// on (ResetGate.At). It and UserManagementKeepOnline are never both
	// yes.
	UserManagementResetToProject *bool `json:"userManagementResetToProject,omitempty"`
	// IdentificationDataLoadNothing answers QuestionIdentificationData:
	// load no identification data.
	IdentificationDataLoadNothing *bool `json:"identificationDataLoadNothing,omitempty"`
	StartAfter                    *bool `json:"startAfter,omitempty"`
	// Reinitialize names every DB the caller agrees to reset. It must
	// cover every DB the question names.
	Reinitialize []string `json:"reinitialize,omitempty"`
	// TrustCertificate is the fingerprint of the CPU certificate the
	// caller trusts; it must equal the question's.
	TrustCertificate string `json:"trustCertificate,omitempty"`
	// KeptDevices names every device the caller agrees to load as the
	// project keeps it (QuestionKeptDevices). It must be exactly the kept
	// set: a yes for another set is refused, so an old answer does not
	// carry over. Leave it empty when nothing is kept.
	KeptDevices []string `json:"keptDevices,omitempty"`
	// AcceptUnverifiedHardware answers QuestionUnverifiedHardware: load
	// the hardware configuration although tiad cannot verify it. Yes
	// needs DownloadRequest.Attestation too.
	AcceptUnverifiedHardware *bool `json:"acceptUnverifiedHardware,omitempty"`
	// AcceptBlindTrust answers QuestionBlindTrust: download although the
	// agent's TIA instance trusted a CPU's certificate blindly earlier (an
	// upload's or a fingerprint read's trustUnverifiedCertificate). It
	// answers no TLS question of the download's own: that still takes
	// TrustCertificate.
	AcceptBlindTrust *bool `json:"acceptBlindTrust,omitempty"`
}

// KeptQuestion is the question a download asks when the project keeps
// devices: the agent's KeptRule.Question.
func KeptQuestion(kept []KeptDevice) Question {
	return Question{ID: QuestionKeptDevices,
		Message: "load the devices the project keeps as found besides the CPU (name each in decisions.keptDevices)",
		Objects: keptNames(kept)}
}

// CheckKept is the agent's KeptRule.Check: "" when answer names exactly
// the kept devices, or why not.
func CheckKept(kept []KeptDevice, answer []string) string {
	want := keptNames(kept)
	got := sortedSet(answer)
	if slices.Equal(want, got) {
		return ""
	}
	if len(got) == 0 {
		return "the project keeps device(s) " + quotedList(want) + " that a download loads too: name them in decisions.keptDevices to load them"
	}
	now := "none"
	if len(want) > 0 {
		now = quotedList(want)
	}
	return "decisions.keptDevices names " + quotedList(got) + ", but the project keeps " + now +
		" now: the answer must name exactly the devices kept, so a yes for another set does not carry over"
}

func keptNames(kept []KeptDevice) []string {
	var ns []string
	for _, k := range kept {
		ns = append(ns, k.Name)
	}
	return sortedSet(ns)
}

func sortedSet(ss []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// quotedList is the agent's "[\"a\", \"b\"]" for names.
func quotedList(ns []string) string {
	q := make([]string, len(ns))
	for i, n := range ns {
		q[i] = quote(n)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

// DownloadRequest loads a session's project into one CPU.
type DownloadRequest struct {
	Address   string    `json:"address"`
	Hardware  bool      `json:"hardware"`
	Software  bool      `json:"software"`
	DryRun    bool      `json:"dryRun"`
	Decisions Decisions `json:"decisions"`
	// Announced records that the people sharing the CPU were told first.
	// It must be true for every download, a dry run included: a dry run
	// reaches the CPU, and that its cancel loads nothing is provisional
	// until S2. It is a convention, not a safety.
	Announced bool     `json:"announced"`
	Secrets   *Secrets `json:"secrets,omitempty"`
	// Attestation is the operator's statement that stands in for the
	// hardware check tiad cannot make: needed, with
	// Decisions.AcceptUnverifiedHardware, by every real download that
	// loads hardware, on every CPU (#789 hardware gate).
	Attestation *Attestation `json:"attestation,omitempty"`
	// Source is the caller's claim of what the project was built from,
	// kept in the deploy record as the caller's claim.
	Source *DeploySource `json:"source,omitempty"`
	// Fingerprint is the options of the fingerprint read the agent makes
	// after a load that compared equal, for the deploy record: its own
	// opt-in to trust the CPU's certificate blindly, which the agent's
	// TIA instance then keeps (Decisions.AcceptBlindTrust) until every
	// session is closed. Close the session after a deploy.
	Fingerprint *FingerprintRequest `json:"fingerprint,omitempty"`
}

// Attestation is the operator's statement that the CPU's hardware
// configuration is what they intend to load. By is who says so: the
// caller's claim, since tiad knows only a bearer token, so a naut caller
// fills it from the principal it authenticated. Text is the statement.
// Each is at most MaxAttestationBy / MaxAttestationText bytes.
//
// CPUAccessControl is the operator's statement of the CPU's access control
// as it is now: AttestCPUAccessDisabled, AttestCPUAccessEnabled or ""
// (unset). A user-management reset rests on "disabled" (the attestation
// route) or "enabled" (the logon route, which needs tiad logged on to the
// CPU in the download); CheckReset.
type Attestation struct {
	By               string `json:"by"`
	Text             string `json:"text"`
	CPUAccessControl string `json:"cpuAccessControl,omitempty"`
}

// Attestation.CPUAccessControl's words.
const (
	AttestCPUAccessDisabled = "disabled"
	AttestCPUAccessEnabled  = "enabled"
)

// Attestation limits, in bytes.
const (
	MaxAttestationBy   = 200
	MaxAttestationText = 2000
)

// AttestationRecord is an attestation as tiad journalled it: At is when
// tiad accepted the download, by tiad's own UTC clock
// ("2006-01-02T15:04:05.000Z"), never the caller's.
type AttestationRecord struct {
	By               string `json:"by"`
	At               string `json:"at"`
	Text             string `json:"text"`
	CPUAccessControl string `json:"cpuAccessControl,omitempty"`
}

// given is whether a says both who and what.
func (a *Attestation) given() bool {
	return a != nil && strings.TrimSpace(a.By) != "" && strings.TrimSpace(a.Text) != ""
}

// HardwareQuestion is the question a download asks when it loads the
// hardware configuration: the agent's HardwareRule.Question.
func HardwareQuestion() Question {
	return Question{ID: QuestionUnverifiedHardware,
		Message: "load the hardware configuration unverified: set decisions.acceptUnverifiedHardware and give attestation.by and attestation.text, the operator's statement that the CPU's hardware configuration is what they intend to load"}
}

const hardwareWhy = "the download loads the hardware configuration, which tiad cannot verify (Openness V21 has no online hardware compare; only the PLC software is compared after loading)"

// CheckHardware is the agent's HardwareRule.Check: "" when r may load
// hardware, or why not, naming the fix; declined when r answered no. A
// software-only download needs nothing.
func CheckHardware(r DownloadRequest) (why string, declined bool) {
	if !r.Hardware {
		return "", false
	}
	d := r.Decisions.AcceptUnverifiedHardware
	switch {
	case d == nil:
		return hardwareWhy + ": set decisions.acceptUnverifiedHardware and give attestation.by and attestation.text to load it, or download the software only (hardware: false)", false
	case !*d:
		return hardwareWhy + ", and decisions.acceptUnverifiedHardware says no: download the software only (hardware: false)", true
	case !r.Attestation.given():
		return hardwareWhy + ": decisions.acceptUnverifiedHardware needs the operator's attestation too (attestation.by: who, attestation.text: that the CPU's hardware configuration is what they intend to load)", false
	}
	return "", false
}

// BlindTrustQuestion is the question a download asks in a TIA instance
// with blind trust b: the agent's BlindTrustRule.Question.
func BlindTrustQuestion(b BlindTrust) Question {
	return Question{ID: QuestionBlindTrust,
		Message: "download in a TIA instance that trusted a CPU's certificate blindly (" + b.Describe() + "): set decisions.acceptBlindTrust, or close every session (the agent then retires that instance) or restart the agent"}
}

// CheckBlindTrust is the agent's BlindTrustRule.Check: "" when a download
// may go on in a TIA instance with blind trust b (nil: none), or why not,
// naming the fix; declined when accept answered no. A download, a dry run
// too, in such an instance rides on that trust, since TIA asks no TLS
// question again until the instance ends.
func CheckBlindTrust(b *BlindTrust, accept *bool) (why string, declined bool) {
	if b == nil {
		return "", false
	}
	w := "this agent's TIA instance trusted a CPU's certificate blindly (" + b.Describe() + "), and TIA keeps that trust until that instance ends, so this download would ride on it"
	switch {
	case accept == nil:
		return w + ": close every session (the agent then retires that instance) or restart the agent for one that trusts nothing, or set decisions.acceptBlindTrust", false
	case !*accept:
		return w + ", and decisions.acceptBlindTrust says no: close every session (the agent then retires that instance) or restart the agent for one that trusts nothing", true
	}
	return "", false
}

// DownloadResult is a download's outcome. After a dry run, Questions are
// what a real download must answer.
type DownloadResult struct {
	DryRun     bool `json:"dryRun"`
	Downloaded bool `json:"downloaded"`
	// Provisional marks a dry run: that a cancelled download loads
	// nothing is unproven until S2 (siemens-ide.md §3.5).
	Provisional bool `json:"provisional,omitempty"`
	// QuestionsIncomplete marks a dry run's questions as only the
	// first check TIA asked: TIA asks its checks one at a time and lists
	// them nowhere, so a dry run, cancelled at the first, sees no more
	// (#789 S2b).
	QuestionsIncomplete bool       `json:"questionsIncomplete,omitempty"`
	Questions           []Question `json:"questions"`
	// CompareEqual is true on every successful real download: success
	// needs TIA's result to say loaded AND an online compare right after,
	// in the same call, to be equal (#789); anything less is
	// outcome_unknown.
	CompareEqual bool `json:"compareEqual,omitempty"`
	// Blocks are the labels of every program block the project holds
	// ("Main [OB1]"), each of which that compare saw identical: check
	// the blocks a download sent are among them.
	Blocks []string `json:"blocks,omitempty"`
	// HardwareVerified is set (false) on a download that loaded hardware:
	// the compare covers the PLC software only, since Openness has no
	// online hardware compare. Show it as a warning, never a silent
	// success. Warning says so in words.
	HardwareVerified *bool  `json:"hardwareVerified,omitempty"`
	Warning          string `json:"warning,omitempty"`
	// Attestation is, on a download that loaded hardware, the operator's
	// attestation that stood in for the check, as tiad journalled it:
	// keep it in the deploy record.
	Attestation *AttestationRecord `json:"attestation,omitempty"`
	// UserManagementReset is set when tiad answered TIA's user-management
	// check by resetting it to the project's: the route that let it,
	// ResetRouteUpload, ResetRouteAttestation, ResetRouteLogon or
	// ResetRouteInstanceLogon. Keep it in the deploy record.
	UserManagementReset string `json:"userManagementReset,omitempty"`
	// Messages is TIA's download result's messages, when it has any.
	Messages string `json:"messages,omitempty"`
	// State is the CPU's mode after the download, "RUN" or "STOP", or ""
	// when nothing says. StateSource says where it comes from:
	// StateFromTIAMessage is TIA's own download messages ("PLC_1
	// started."), evidence of what TIA did, not a read of the CPU. A
	// download that never stopped the CPU has no state.
	State       string `json:"state,omitempty"`
	StateSource string `json:"stateSource,omitempty"`
	// KeptDevices are the devices the last apply kept as found: the
	// download loads them too. A decision_required or declined error
	// lists them as well (KeptDevicesOf), before anything is loaded.
	KeptDevices []KeptDevice `json:"keptDevices"`
	// BlindTrust is the agent's TIA instance's blind trust the download
	// rode on (Decisions.AcceptBlindTrust), or nil: keep it in the deploy
	// record.
	BlindTrust *BlindTrust `json:"blindTrust"`
	// Deploy is a successful download's deploy record: the archive of
	// what was loaded, the source the caller claimed, and the CPU's
	// fingerprint after the load. A failure after the agent archived the
	// project names its record too (DeployIDOf).
	Deploy *DeployResult `json:"deploy,omitempty"`
}

// Acting reports whether answering the question id can change the CPU
// itself (stop it, start it, overwrite or reset its data), as opposed to a
// password or a certificate, which only lets the download go on. TIA asks
// its checks one at a time and may act on an answer before it asks the
// next (#789 S2b), so a download cancelled after an acting answer is
// outcome_unknown, naming it (ActingAnswers). An ID tiad does not know is
// acting.
func Acting(id string) bool {
	switch id {
	case QuestionPassword, QuestionConfigurationPassword, QuestionCertificate, QuestionUserManagement, QuestionIdentificationData:
		return false
	}
	return true
}

// ActingAnswer is whether answering id yes (with choice, for
// user-management: AnswerKeep or AnswerReset) acts on the CPU, by what the
// answer writes: start-after no changes nothing, user-management keep
// changes nothing, its reset deletes the CPU's users and sets its access
// control.
func ActingAnswer(id string, yes bool, choice string) bool {
	switch id {
	case QuestionStartAfter:
		return yes
	case QuestionUserManagement:
		return choice == AnswerReset
	}
	return Acting(id)
}

// User-management's two yeses (the agent's Answer.Choice).
const (
	AnswerKeep  = "keep"
	AnswerReset = "reset"
)

// UserManagementChoice is the answer d gives user-management once Resolve
// let it through: AnswerReset, AnswerKeep, or "" for no yes.
func UserManagementChoice(d Decisions) string {
	switch {
	case d.UserManagementResetToProject != nil && *d.UserManagementResetToProject:
		return AnswerReset
	case d.UserManagementKeepOnline != nil && *d.UserManagementKeepOnline:
		return AnswerKeep
	}
	return ""
}

// ActingAnswers returns the acting answers (question IDs) the agent gave
// TIA before a download ended outcome_unknown: what TIA may already have
// done to the CPU (a stop, a start) even though nothing may be loaded.
func ActingAnswers(err error) []string {
	var e *Error
	if !errors.As(err, &e) || len(e.Data) == 0 {
		return nil
	}
	var d struct {
		ActingAnswers []string `json:"actingAnswers"`
	}
	if json.Unmarshal(e.Data, &d) != nil {
		return nil
	}
	return d.ActingAnswers
}

// StateOf returns the CPU's mode an outcome_unknown download error
// carries, as DownloadResult.State and StateSource: what TIA's messages
// said before the download ended in doubt (a compare after the load that
// failed or differs), or "" when they said nothing.
func StateOf(err error) (state, source string) {
	var e *Error
	if !errors.As(err, &e) || len(e.Data) == 0 {
		return "", ""
	}
	var d struct {
		State       string `json:"state"`
		StateSource string `json:"stateSource"`
	}
	if json.Unmarshal(e.Data, &d) != nil {
		return "", ""
	}
	return d.State, d.StateSource
}

// KeptDevicesOf returns the kept devices a decision_required or declined
// download error lists.
// BlindTrustOf is the TIA instance's blind trust a decision_required or
// declined download error names (CheckBlindTrust), or nil.
func BlindTrustOf(err error) *BlindTrust {
	var e *Error
	if !errors.As(err, &e) || len(e.Data) == 0 {
		return nil
	}
	var d struct {
		BlindTrust *BlindTrust `json:"blindTrust"`
	}
	if json.Unmarshal(e.Data, &d) != nil {
		return nil
	}
	return d.BlindTrust
}

func KeptDevicesOf(err error) []KeptDevice {
	var e *Error
	if !errors.As(err, &e) || len(e.Data) == 0 {
		return nil
	}
	var d struct {
		KeptDevices []KeptDevice `json:"keptDevices"`
	}
	if json.Unmarshal(e.Data, &d) != nil {
		return nil
	}
	return d.KeptDevices
}

// Validate checks the request's own shape.
func (r DownloadRequest) Validate() error {
	if !isIPv4(r.Address) {
		return fmt.Errorf("download: address %q is not an IPv4 address", r.Address)
	}
	if !r.Hardware && !r.Software {
		return fmt.Errorf("download: neither hardware nor software selected")
	}
	if r.Fingerprint != nil && r.Fingerprint.Secrets != nil {
		return fmt.Errorf("download: the Full access password goes in secrets.fullAccessPassword, not in the fingerprint's secrets")
	}
	if !r.Announced {
		return fmt.Errorf("download: announce it first (announced: true); a dry run reaches the CPU too until S2 proves a cancel loads nothing")
	}
	if a := r.Attestation; a != nil {
		if len(a.By) > MaxAttestationBy {
			return fmt.Errorf("download: attestation.by is longer than %d bytes", MaxAttestationBy)
		}
		if len(a.Text) > MaxAttestationText {
			return fmt.Errorf("download: attestation.text is longer than %d bytes", MaxAttestationText)
		}
		if !validCPUAccess(a.CPUAccessControl) {
			return fmt.Errorf("download: attestation.cpuAccessControl is not one of disabled, enabled")
		}
	}
	if isYes(r.Decisions.UserManagementKeepOnline) && isYes(r.Decisions.UserManagementResetToProject) {
		return fmt.Errorf("download: decisions.userManagementKeepOnline and decisions.userManagementResetToProject are both yes; user-management takes one answer")
	}
	if r.Source != nil {
		if err := r.Source.Validate(); err != nil {
			return fmt.Errorf("download: %w", err)
		}
	}
	return nil
}

func validCPUAccess(s string) bool {
	return s == "" || s == AttestCPUAccessDisabled || s == AttestCPUAccessEnabled
}

func isYes(b *bool) bool { return b != nil && *b }

// ProjectAccessDisabled and ProjectAccessEnabled are TIA's
// PlcAccessControlConfiguration names for access control off, and on with
// users (what cpu.accessControl fullAccessUser sets), exactly.
const (
	ProjectAccessDisabled = "Disabled"
	ProjectAccessEnabled  = "EnabledWithAccessControl"
)

// The routes that let a user-management reset
// (DownloadResult.UserManagementReset): the CPU's access control shown off
// (upload, attestation), or tiad logged on to it (logon), in this
// download or, when the download asked no credentials before TIA's check,
// by an equal compare earlier in the same TIA instance (instance-logon,
// given by ResetGate.At only).
const (
	ResetRouteUpload        = "upload"
	ResetRouteAttestation   = "attestation"
	ResetRouteLogon         = "logon"
	ResetRouteInstanceLogon = "instance-logon"
)

// ResetGate is CheckReset's verdict. Its zero value refuses: a download
// that never checked cannot reset. A gate allowed by the logon route still
// needs At, when TIA asks.
type ResetGate struct {
	Allowed bool
	Why     string
	Route   string
}

// At is the verdict when TIA asks its user-management check: loggedOn is
// whether this download logged on to the CPU as "nautilus" and nothing was
// refused since (the agent's LoginUse.LoggedOn); credentialsAsked whether
// TIA asked credentials in it at all, answered or not
// (LoginUse.CredentialsAsked). The logon route needs the logon; the others
// rest on the CPU's access control being off, which a CPU that asked
// credentials contradicts, whatever tiad answered.
//
// instanceLogon is whether the agent's TIA instance holds the evidence of
// an equal compare to the download's address that logged on with the
// request's password (the agent's InstanceLogons), as read just before
// TIA's download. It stands in for the download's own logon only when the
// download asked no credentials and refused nothing before the check; the
// gate it lets says route instance-logon.
//
// answeredBefore is whether tiad answered credentials for the download's
// address earlier in the same TIA instance (the agent's
// InstanceLogons.AnsweredBefore): TIA may keep that logon and ask nothing
// in this download, so the off routes are refused then too.
func (g ResetGate) At(loggedOn, credentialsAsked, instanceLogon, answeredBefore bool) ResetGate {
	switch {
	case !g.Allowed:
		return g
	case g.Route == ResetRouteLogon && !loggedOn && instanceLogon && !credentialsAsked:
		return ResetGate{Allowed: true, Route: ResetRouteInstanceLogon}
	case g.Route == ResetRouteLogon && !loggedOn:
		return ResetGate{Why: resetRefused + ResetNoLogon}
	case g.Route != ResetRouteLogon && (loggedOn || credentialsAsked):
		return ResetGate{Why: resetRefused + "the CPU asked credentials in this download, so its access control is on, but the " + g.Route + " route rests on it being off: if it is on, attest \"enabled\" (the logon route)"}
	case g.Route != ResetRouteLogon && answeredBefore:
		return ResetGate{Why: resetRefused + ResetLoggedOnBefore(g.Route)}
	}
	return g
}

// ResetLoggedOnBefore is the refusal of an off route over a CPU tiad
// logged on to earlier in the same TIA instance.
func ResetLoggedOnBefore(route string) string {
	return "tiad logged on to this CPU earlier in this agent's TIA instance, so its access control was on then, and TIA may have kept that logon and asked this download nothing, but the " + route + " route rests on it being off: if it is on, attest \"enabled\" (the logon route); if it is off, restart the agent for a TIA instance that holds no logon"
}

// UploadRouteEnabled turns CheckReset's upload route on. It is off
// (naut-lead, 2026-11-21): that an upload brings a CPU's access control
// into the project is proven only for the bench's Disabled CPU. Turn it
// on, here and in the agent's UserManagementRule.UploadRouteEnabled, only
// with a bench upload of a CPU with access control on that reads back as
// not Disabled.
const UploadRouteEnabled = false

const (
	resetRefused   = "decisions.userManagementResetToProject (delete the CPU's user management data and set its access control as the project has it) is refused: "
	uploadRouteWhy = "the upload route is not yet available: that an upload brings a CPU's access control into the project is unproven until a bench upload of a CPU with access control on shows it"
	// ResetNoLogon, ResetNoPassword and ResetPasswordNotRecorded are the
	// agent's refusals of the logon route and of tiad's user.
	ResetNoLogon             = `the logon route (attestation.cpuAccessControl "enabled") needs tiad logged on to the CPU as "nautilus" before TIA asked: in this download, or (the instance-logon route, when the download asked no credentials) by a compare to this CPU in this agent's TIA instance with the same secrets.fullAccessPassword that came back equal, with no download to the CPU, blind trust or refused credentials since; neither holds (the CPU asked no credentials and the instance holds no such compare, or tiad's answer was refused), so nothing shows tiad holds the CPU's Full access: compare (or verify the last deploy, whose archived project compares equal) first, then download again; if the compare asks no credentials either (TIA kept an earlier logon), restart the agent for a TIA instance that holds none; if the CPU's access control is off, attest "disabled"`
	ResetNoPassword          = `the logon route (attestation.cpuAccessControl "enabled") logs on to the CPU as "nautilus", and the request carries no secrets.fullAccessPassword`
	ResetRecordOnly          = `an apply on this session recorded secrets.fullAccessPassword as the user "nautilus"'s password without setting it (fullAccessRecordOnly), so tiad cannot show the project's user holds it: a reset to it could leave the CPU with a password this download cannot log on with, locking tiad out; apply on a session that sets the password`
	ResetPasswordNotRecorded = `the request's secrets.fullAccessPassword is not the password tiad set or recorded for the project's user "nautilus": after the reset the CPU's Full access user would have a password this download cannot log on with`
)

func resetRoutes(uploadRoute bool) string {
	s := `tiad resets it only on a hardware download, to a project whose access control is Disabled under cpu.accessControl keep, or one whose user "nautilus" this session made (cpu.accessControl fullAccessUser, secrets.fullAccessPassword its recorded password), with attestation.cpuAccessControl "disabled"`
	if uploadRoute {
		s += " or an upload from this CPU in this session (to a Disabled project)"
	}
	s += `, or "enabled" with tiad logged on as "nautilus" in the download (or by an equal compare in this TIA instance)`
	if !uploadRoute {
		s += " (" + uploadRouteWhy + ")"
	}
	return s
}

// UserManagementResetQuestion is the question a refused reset lists.
func UserManagementResetQuestion(why string) Question {
	return Question{ID: QuestionUserManagement, Message: why, Detail: "UserManagementDownload"}
}

// ResetFacts is what a session shows CheckReset (the agent's
// UserManagementRule.Facts). AccessControlKept is whether the session's
// spec keeps access control and no apply on it ever set it;
// FullAccessUserSet whether its spec is fullAccessUser and an apply on
// this session set it; UploadedFrom the address this session last
// uploaded from ("" for none); UploadRoute whether the upload route counts
// (UploadRouteEnabled). ProjectAccess reads the project's access control
// (ok false when it could not be read) and PasswordRecorded whether the
// project's record of the user "nautilus" is of the request's password (ok
// false: unreadable); each is asked only once everything cheaper holds,
// PasswordRecorded only for tiad's user.
type ResetFacts struct {
	AccessControlKept bool
	FullAccessUserSet bool
	UploadedFrom      string
	UploadRoute       bool
	// RecordOnly is whether an apply on the session asked for
	// fullAccessRecordOnly: a reset to tiad's user is then refused.
	RecordOnly       bool
	ProjectAccess    func() (string, bool)
	PasswordRecorded func() (recorded, ok bool)
}

// CheckUserManagementReset is CheckReset for a session that keeps access
// control (or not), with no user of tiad's to reset to.
func CheckUserManagementReset(r DownloadRequest, accessControlKept bool, uploadedFrom string, uploadRoute bool, projectAccess func() (string, bool)) ResetGate {
	return CheckReset(r, ResetFacts{AccessControlKept: accessControlKept, UploadedFrom: uploadedFrom, UploadRoute: uploadRoute, ProjectAccess: projectAccess})
}

// CheckReset is the agent's UserManagementRule.Check: when tiad may answer
// TIA's user-management check by resetting the CPU's user management to
// the project's, decided before anything reaches the CPU. A hardware load
// asks it, TIA refuses keep there, and its reset deletes the CPU's users
// and sets its access control and users as the project has them (#789
// stage 4). Only on a hardware download, and only for a closed set:
//
//	project after the reset        CPU now (evidence)            route
//	Disabled, spec keep            off: attested "disabled"      attestation
//	Disabled, spec keep            off: uploaded here (OFF)      upload
//	Disabled, spec keep            on: tiad logged on            logon
//	tiad's user, fullAccessUser    off: attested "disabled"      attestation
//	tiad's user, fullAccessUser    on: tiad logged on            logon
//
// "tiad's user" is the session's spec fullAccessUser applied on this
// session, the project's access control exactly EnabledWithAccessControl,
// and its record of "nautilus" of the request's password. The logon route
// ("enabled" attested) needs the password, and ResetGate.At when TIA
// asks. Every other case refuses.
func CheckReset(r DownloadRequest, f ResetFacts) ResetGate {
	keep := f.AccessControlKept
	switch {
	case !isYes(r.Decisions.UserManagementResetToProject):
		return ResetGate{Why: resetRefused + "it is not asked"}
	case !r.Hardware:
		return ResetGate{Why: resetRefused + "this download loads no hardware; " + resetRoutes(f.UploadRoute)}
	case !keep && !f.FullAccessUserSet:
		return ResetGate{Why: resetRefused + "the session's spec does not keep access control as found (cpu.accessControl keep on a session that never applied fullAccessUser), nor is it fullAccessUser applied on this session; " + resetRoutes(f.UploadRoute)}
	}
	if !keep && f.RecordOnly {
		return ResetGate{Why: resetRefused + ResetRecordOnly}
	}
	enabled := r.Attestation != nil && r.Attestation.CPUAccessControl == AttestCPUAccessEnabled
	if enabled && (r.Secrets == nil || r.Secrets.FullAccessPassword == "") {
		return ResetGate{Why: resetRefused + ResetNoPassword}
	}
	want := ProjectAccessDisabled
	if !keep {
		want = ProjectAccessEnabled
	}
	project, ok := "", false
	if f.ProjectAccess != nil {
		project, ok = f.ProjectAccess()
	}
	if !ok || project != want {
		what := "unreadable"
		switch {
		case ok && project == "":
			what = "absent (no such setting)"
		case ok:
			what = strconv.Quote(project)
		}
		tail := ""
		if !keep {
			tail = " (cpu.accessControl fullAccessUser)"
		}
		return ResetGate{Why: resetRefused + "the project's access control is " + what + ", not " + want + tail + "; " + resetRoutes(f.UploadRoute)}
	}
	if !keep {
		recorded, ok := false, false
		if f.PasswordRecorded != nil {
			recorded, ok = f.PasswordRecorded()
		}
		switch {
		case !ok:
			return ResetGate{Why: resetRefused + `tiad could not read the project's record of the user "nautilus", so nothing shows the CPU's Full access user would have the password this download logs on with`}
		case !recorded:
			return ResetGate{Why: resetRefused + ResetPasswordNotRecorded}
		}
	}
	uploaded := keep && f.UploadRoute && f.UploadedFrom != "" && f.UploadedFrom == r.Address
	switch {
	case enabled && r.Attestation.given():
		return ResetGate{Allowed: true, Route: ResetRouteLogon}
	case !enabled && uploaded:
		return ResetGate{Allowed: true, Route: ResetRouteUpload}
	case r.Attestation.given() && r.Attestation.CPUAccessControl == AttestCPUAccessDisabled:
		return ResetGate{Allowed: true, Route: ResetRouteAttestation}
	}
	if keep && f.UploadRoute {
		return ResetGate{Why: resetRefused + "nothing shows the CPU's access control as it is now: upload from " + r.Address + ` in this session first, or give attestation.cpuAccessControl "disabled" or "enabled" (with attestation.by and attestation.text), the operator's own observation of the CPU, never a value a caller fills in by default`}
	}
	why := resetRefused + `nothing shows the CPU's access control as it is now: give attestation.cpuAccessControl "disabled" or "enabled" (with attestation.by and attestation.text), the operator's own observation of the CPU, never a value a caller fills in by default`
	if keep {
		why += "; " + uploadRouteWhy
	}
	return ResetGate{Why: why}
}

// Resolve applies the download rule: every question is answered only from
// d. It returns the questions with no answer and the ones answered "no".
// A real download goes ahead only when both are empty; tiad never picks a
// default. A password question is answered by the request's protection
// secret and a configuration-password question by its configuration
// password, never by a decision and never by each other's secret.
// accessControlKept is whether the session's spec keeps the CPU's access
// control as found: only then can user-management be answered keep.
// reset is CheckUserManagementReset's verdict (its zero value refuses):
// only an allowed one answers it reset.
//
// user-management, keep x reset: a yes answers it (both yes is a bad
// request, refused before this); with no yes, a no declines; with
// neither, it is unanswered.
func Resolve(qs []Question, d Decisions, haveProtection, haveConfigurationPassword, accessControlKept bool, reset ResetGate) (unanswered, declined []Question) {
	for _, q := range qs {
		switch q.ID {
		case QuestionStopCPU:
			sortAnswer(q, d.StopCPU, &unanswered, &declined)
		case QuestionOverwrite:
			sortAnswer(q, d.Overwrite, &unanswered, &declined)
		case QuestionConsistentDownload:
			sortAnswer(q, d.ConsistentDownload, &unanswered, &declined)
		case QuestionAlarmTexts:
			sortAnswer(q, d.AlarmTexts, &unanswered, &declined)
		case QuestionAllBlocks:
			sortAnswer(q, d.AllBlocks, &unanswered, &declined)
		case QuestionUserManagement:
			keep, resetYes := isYes(d.UserManagementKeepOnline), isYes(d.UserManagementResetToProject)
			switch {
			case keep && resetYes:
				q.Message += " (decisions.userManagementKeepOnline and decisions.userManagementResetToProject are both yes; user-management takes one answer)"
				unanswered = append(unanswered, q)
			case resetYes:
				// Resetting acts: only the verdict tiad checked before
				// calling TIA lets it.
				if !reset.Allowed {
					why := reset.Why
					if why == "" {
						why = "decisions.userManagementResetToProject is refused"
					}
					q.Message += " (" + why + ")"
					unanswered = append(unanswered, q)
				}
			case keep:
				// Keeping the CPU's users only for a spec that keeps
				// access control.
				if !accessControlKept {
					q.Message += " (tiad keeps the CPU's online user management only for a keep spec on a session that never set access control: an apply of fullAccessUser on this session needs a fresh session that applies keep)"
					unanswered = append(unanswered, q)
				}
			case d.UserManagementKeepOnline != nil || d.UserManagementResetToProject != nil:
				declined = append(declined, q)
			default:
				unanswered = append(unanswered, q)
			}
		case QuestionIdentificationData:
			sortAnswer(q, d.IdentificationDataLoadNothing, &unanswered, &declined)
		case QuestionStartAfter:
			// Either answer is a decision: no means load and stay in
			// STOP, which is a legitimate choice, not a refusal.
			if d.StartAfter == nil {
				unanswered = append(unanswered, q)
			}
		case QuestionReinitialize:
			// A reset nobody named cannot be agreed to: without objects
			// the question is never answered.
			if missing := notCovered(q.Objects, d.Reinitialize); len(d.Reinitialize) == 0 || len(q.Objects) == 0 {
				unanswered = append(unanswered, q)
			} else if len(missing) > 0 {
				q.Message = fmt.Sprintf("%s (not agreed: %s)", q.Message, strings.Join(missing, ", "))
				unanswered = append(unanswered, q)
			}
		case QuestionCertificate:
			if d.TrustCertificate == "" {
				unanswered = append(unanswered, q)
			} else if !strings.EqualFold(d.TrustCertificate, q.Detail) {
				declined = append(declined, q)
			}
		case QuestionPassword:
			if !haveProtection {
				unanswered = append(unanswered, q)
			}
		case QuestionConfigurationPassword:
			if !haveConfigurationPassword {
				unanswered = append(unanswered, q)
			}
		default:
			// A check tiad has never seen cannot be answered by accident.
			unanswered = append(unanswered, q)
		}
	}
	return unanswered, declined
}

func sortAnswer(q Question, a *bool, unanswered, declined *[]Question) {
	switch {
	case a == nil:
		*unanswered = append(*unanswered, q)
	case !*a:
		*declined = append(*declined, q)
	}
}

// notCovered returns the names in want that agreed does not contain,
// case-insensitively (TIA block names are case-insensitive).
func notCovered(want, agreed []string) []string {
	ok := map[string]bool{}
	for _, a := range agreed {
		ok[strings.ToLower(a)] = true
	}
	var out []string
	for _, w := range want {
		if !ok[strings.ToLower(w)] {
			out = append(out, w)
		}
	}
	sort.Strings(out)
	return out
}

// Yes is a convenience for a Decisions answer.
func Yes() *bool { t := true; return &t }

// No is a convenience for a Decisions answer.
func No() *bool { f := false; return &f }
