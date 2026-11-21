package tiad

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
)

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
	// (UserManagementDownload) keeps the CPU's online users, and only when
	// the spec's accessControl is keep; identification-data
	// (LoadIdentificationData) loads nothing. The last two are passive.
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
	QuestionOther       = "other"
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
	// HardwareVerified is set (false) on a download that loaded hardware:
	// the compare covers the PLC software only, since Openness has no
	// online hardware compare. Show it as a warning, never a silent
	// success. Warning says so in words.
	HardwareVerified *bool  `json:"hardwareVerified,omitempty"`
	Warning          string `json:"warning,omitempty"`
	// Messages is TIA's download result's messages, when it has any.
	Messages string `json:"messages,omitempty"`
	State    string `json:"state,omitempty"` // "RUN" | "STOP" | "" (unknown)
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
	return nil
}

// Resolve applies the download rule: every question is answered only from
// d. It returns the questions with no answer and the ones answered "no".
// A real download goes ahead only when both are empty; tiad never picks a
// default. A password question is answered by the request's protection
// secret and a configuration-password question by its configuration
// password, never by a decision and never by each other's secret.
// accessControlKept is whether the session's spec keeps the CPU's access
// control as found: only then can user-management be answered.
func Resolve(qs []Question, d Decisions, haveProtection, haveConfigurationPassword, accessControlKept bool) (unanswered, declined []Question) {
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
			// Keeping the CPU's users is the only answer, and only for a
			// spec that keeps access control.
			if d.UserManagementKeepOnline != nil && *d.UserManagementKeepOnline && !accessControlKept {
				q.Message += " (tiad keeps the CPU's online user management only for a keep spec on a session that never set access control: an apply of fullAccessUser on this session needs a fresh session that applies keep)"
				unanswered = append(unanswered, q)
			} else {
				sortAnswer(q, d.UserManagementKeepOnline, &unanswered, &declined)
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
