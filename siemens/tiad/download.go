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
	// them to the project's (acting), only under CheckUserManagementReset;
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
	QuestionOther              = "other"
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
	// CheckUserManagementReset. It and UserManagementKeepOnline are never
	// both yes.
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
}

// Attestation is the operator's statement that the CPU's hardware
// configuration is what they intend to load. By is who says so: the
// caller's claim, since tiad knows only a bearer token, so a naut caller
// fills it from the principal it authenticated. Text is the statement.
// Each is at most MaxAttestationBy / MaxAttestationText bytes.
//
// CPUAccessControl is the operator's statement of the CPU's access control
// as it is now: AttestCPUAccessDisabled, AttestCPUAccessEnabled or ""
// (unset). A user-management reset can rest on "disabled"
// (CheckUserManagementReset).
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
	// ResetRouteUpload or ResetRouteAttestation. Keep it in the deploy
	// record.
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
	return nil
}

func validCPUAccess(s string) bool {
	return s == "" || s == AttestCPUAccessDisabled || s == AttestCPUAccessEnabled
}

func isYes(b *bool) bool { return b != nil && *b }

// ProjectAccessDisabled is TIA's PlcAccessControlConfiguration name for
// access control off, exactly.
const ProjectAccessDisabled = "Disabled"

// The routes that show a CPU's access control is off
// (DownloadResult.UserManagementReset).
const (
	ResetRouteUpload      = "upload"
	ResetRouteAttestation = "attestation"
)

// ResetGate is CheckUserManagementReset's verdict. Its zero value refuses:
// a download that never checked cannot reset.
type ResetGate struct {
	Allowed bool
	Why     string
	Route   string
}

// UploadRouteEnabled turns CheckUserManagementReset's upload route on. It
// is off (naut-lead, 2026-11-21): that an upload brings a CPU's access
// control into the project is proven only for the bench's Disabled CPU.
// Turn it on, here and in the agent's UserManagementRule.UploadRouteEnabled,
// only with a bench upload of a CPU with access control on that reads back
// as not Disabled.
const UploadRouteEnabled = false

const (
	resetRefused   = "decisions.userManagementResetToProject (delete the CPU's user management data and set its access control as the project has it) is refused: "
	uploadRouteWhy = "the upload route is not yet available: that an upload brings a CPU's access control into the project is unproven until a bench upload of a CPU with access control on shows it"
)

func resetRoutes(uploadRoute bool) string {
	if uploadRoute {
		return `tiad resets it only on a hardware download, with cpu.accessControl keep, a project whose access control is Disabled, and either an upload from this CPU in this session or attestation.cpuAccessControl "disabled"`
	}
	return `tiad resets it only on a hardware download, with cpu.accessControl keep, a project whose access control is Disabled, and attestation.cpuAccessControl "disabled" (` + uploadRouteWhy + ")"
}

// UserManagementResetQuestion is the question a refused reset lists.
func UserManagementResetQuestion(why string) Question {
	return Question{ID: QuestionUserManagement, Message: why, Detail: "UserManagementDownload"}
}

// CheckUserManagementReset is the agent's UserManagementRule.Check: when
// tiad may answer TIA's user-management check by resetting the CPU's user
// management to the project's. A hardware load asks it, TIA refuses keep
// there, and its reset deletes the CPU's users and sets its access
// control as the project has it (#789 stage 4). It is let only when that
// changes nothing that protects the CPU: r loads hardware,
// accessControlKept (the spec keeps access control and no apply on the
// session set it), the project's access control (projectAccess, read
// only once the rest holds; ok false when it could not be read) is
// exactly Disabled, and the CPU's is shown off: this session uploaded
// from r.Address (uploadedFrom; only when uploadRoute, UploadRouteEnabled
// for the agent), or the attestation says "disabled". An attestation that
// says "enabled" refuses whatever else holds.
func CheckUserManagementReset(r DownloadRequest, accessControlKept bool, uploadedFrom string, uploadRoute bool, projectAccess func() (string, bool)) ResetGate {
	switch {
	case !isYes(r.Decisions.UserManagementResetToProject):
		return ResetGate{Why: resetRefused + "it is not asked"}
	case !r.Hardware:
		return ResetGate{Why: resetRefused + "this download loads no hardware; " + resetRoutes(uploadRoute)}
	case !accessControlKept:
		return ResetGate{Why: resetRefused + "the session's spec does not keep access control as found (cpu.accessControl keep on a session that never applied fullAccessUser); " + resetRoutes(uploadRoute)}
	case r.Attestation != nil && r.Attestation.CPUAccessControl == AttestCPUAccessEnabled:
		return ResetGate{Why: resetRefused + "the attestation says the CPU's access control is enabled, and a reset would turn it off and delete its users; tiad never resets a CPU whose access control is on"}
	}
	project, ok := "", false
	if projectAccess != nil {
		project, ok = projectAccess()
	}
	if !ok || project != ProjectAccessDisabled {
		what := "unreadable"
		switch {
		case ok && project == "":
			what = "absent (no such setting)"
		case ok:
			what = strconv.Quote(project)
		}
		return ResetGate{Why: resetRefused + "the project's access control is " + what + ", not Disabled; " + resetRoutes(uploadRoute)}
	}
	if uploadRoute && uploadedFrom != "" && uploadedFrom == r.Address {
		return ResetGate{Allowed: true, Route: ResetRouteUpload}
	}
	if r.Attestation.given() && r.Attestation.CPUAccessControl == AttestCPUAccessDisabled {
		return ResetGate{Allowed: true, Route: ResetRouteAttestation}
	}
	if !uploadRoute {
		return ResetGate{Why: resetRefused + `nothing shows the CPU's access control is off: give attestation.cpuAccessControl "disabled" (with attestation.by and attestation.text), the operator's statement that it is; ` + uploadRouteWhy}
	}
	return ResetGate{Why: resetRefused + "nothing shows the CPU's access control is off: upload from " + r.Address + ` in this session first, or give attestation.cpuAccessControl "disabled" (with attestation.by and attestation.text), the operator's statement that it is`}
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
