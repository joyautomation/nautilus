package tiad

import (
	"fmt"
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
	QuestionStartAfter   = "start-after"
	QuestionPassword     = "password"
	// QuestionConfigurationPassword asks for the password protecting the
	// CPU's confidential configuration data. It is answered only by
	// Secrets.ConfigurationPassword, never by the protection password.
	QuestionConfigurationPassword = "configuration-password"
	QuestionCertificate           = "certificate"
	QuestionOther                 = "other"
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
	StopCPU    *bool `json:"stopCpu,omitempty"`
	Overwrite  *bool `json:"overwrite,omitempty"`
	StartAfter *bool `json:"startAfter,omitempty"`
	// Reinitialize names every DB the caller agrees to reset. It must
	// cover every DB the question names.
	Reinitialize []string `json:"reinitialize,omitempty"`
	// TrustCertificate is the fingerprint of the CPU certificate the
	// caller trusts; it must equal the question's.
	TrustCertificate string `json:"trustCertificate,omitempty"`
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
	Provisional bool       `json:"provisional,omitempty"`
	Questions   []Question `json:"questions"`
	State       string     `json:"state,omitempty"` // "RUN" | "STOP" | "" (unknown)
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
func Resolve(qs []Question, d Decisions, haveProtection, haveConfigurationPassword bool) (unanswered, declined []Question) {
	for _, q := range qs {
		switch q.ID {
		case QuestionStopCPU:
			sortAnswer(q, d.StopCPU, &unanswered, &declined)
		case QuestionOverwrite:
			sortAnswer(q, d.Overwrite, &unanswered, &declined)
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
