package tiadtest

import (
	"fmt"
	"net/http"
	"strconv"
	"unicode/utf16"

	"github.com/joyautomation/nautilus/siemens/tiad"
)

// The CPU's PLC access control as a project holds it: TIA's
// PlcAccessControlConfiguration names. AccessAbsent is a CPU without it.
const (
	AccessEnabled  = "EnabledWithAccessControl"
	AccessDisabled = "Disabled"
	AccessAbsent   = ""
)

// NoFullAccess is how TIA fails a hardware compile of a CPU with access
// control on and no user holding the runtime right Full access on it.
const NoFullAccess = "Users and roles: At least one user must have the following runtime right: Full access"

// The notes and refusals of AccessSteps, word for word.
const (
	MadeUserNote      = `made the user "nautilus" with the runtime right Full access (role "nautilus full access"), its password from secrets.fullAccessPassword`
	UnchangedUserNote = `the project's user "nautilus" has the runtime right Full access (role "nautilus full access"); its password is unchanged: secrets.fullAccessPassword is the one tiad last set or recorded in this project (a change made in TIA since would not show)`
	SetUserNote       = `the project's user "nautilus" has the runtime right Full access (role "nautilus full access"); its password is now secrets.fullAccessPassword`
	RecordedUserNote  = `the project's user "nautilus" has the runtime right Full access (role "nautilus full access"); its password was not set: on fullAccessRecordOnly, tiad now records secrets.fullAccessPassword as the user's password, which it cannot check offline`
	TakenOverNote     = `the project's user "nautilus", which carried no tiad record, is taken over on fullAccessRecordOnly: its comment is now tiad's record of secrets.fullAccessPassword as its password, which tiad did not set and cannot check offline, and it gets the runtime right Full access (role "nautilus full access")`
	ReusedPassword    = `TIA refuses secrets.fullAccessPassword for the user "nautilus": the user had that password recently, and TIA blocks a user's recent passwords for reuse: choose another password, or make the project again (POST /v1/projects)`
	ReusedOtherKey    = `TIA refuses secrets.fullAccessPassword for the user "nautilus" as a password the user has now or had recently, and the project's record of it was made under another agent key, so tiad cannot tell which: if the password did not change, apply again with fullAccessRecordOnly to record it under this agent's key without setting it (journalled); otherwise choose another password, or make the project again (POST /v1/projects)`
	ForeignUser       = `the project holds a user "nautilus" that carries no tiad record, so tiad did not make it and does not take it over unasked: set cpu.accessControl to keep, or apply with fullAccessRecordOnly to take it over with secrets.fullAccessPassword recorded as its password (journalled; tiad does not set it and cannot check it)`
	ForeignRole       = `the project holds a role "nautilus full access" that tiad did not make, and tiad does not grant Full access to a role it did not make: set cpu.accessControl to keep`
)


// The user and role tiad makes for cpu.accessControl fullAccessUser
// (AccessSteps.UserName and RoleName).
const (
	FullAccessUser = "nautilus"
	FullAccessRole = "nautilus full access"
)

// PasswordPolicy is a project's rules for local users' passwords (TIA's
// PasswordPolicyConfigurator; tiad-fake's PasswordPolicy). The zero
// value is not TIA's default: DefaultPasswordPolicy is.
type PasswordPolicy struct {
	MinLength     int  `json:"minLength"`
	MinDigits     int  `json:"minDigits"`
	MinSpecial    int  `json:"minSpecial"`
	UpperAndLower bool `json:"upperAndLower"`
}

// DefaultPasswordPolicy is TIA's default, as the TIA machine shows it.
func DefaultPasswordPolicy() PasswordPolicy {
	return PasswordPolicy{MinLength: 10, MinDigits: 1, UpperAndLower: true}
}

func (p PasswordPolicy) describe() string {
	s := fmt.Sprintf("at least %d characters, %d digit(s), %d special character(s)", p.MinLength, p.MinDigits, p.MinSpecial)
	if p.UpperAndLower {
		s += ", upper and lower case"
	}
	return s
}

// check is PasswordPolicy.Check: lengths in UTF-16 code units, classes
// ASCII, anything else special.
func (p PasswordPolicy) check(pw string) string {
	var broken []string
	n := len(utf16Units(pw))
	if n < p.MinLength {
		broken = append(broken, fmt.Sprintf("shorter than %d characters", p.MinLength))
	}
	if n > 120 {
		broken = append(broken, "longer than 120 characters")
	}
	digits, special := 0, 0
	upper, lower := false, false
	for _, r := range utf16Units(pw) {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= 'a' && r <= 'z':
			lower = true
		case r > ' ' && r < 127:
			special++
		}
	}
	if digits < p.MinDigits {
		broken = append(broken, fmt.Sprintf("fewer than %d digit(s)", p.MinDigits))
	}
	if special < p.MinSpecial {
		broken = append(broken, fmt.Sprintf("fewer than %d special character(s)", p.MinSpecial))
	}
	if p.UpperAndLower && !(upper && lower) {
		broken = append(broken, "not both an upper-case and a lower-case letter")
	}
	if len(broken) == 0 {
		return ""
	}
	out := "breaks the project's password policy for local users (" + p.describe() + "): "
	for i, b := range broken {
		if i > 0 {
			out += ", "
		}
		out += b
	}
	return out
}

// access is a project's users and roles, as TIA keeps them: the
// project's, not the CPU's, so they outlive any device delete. A role
// maps to whether it holds Full access on the project's CPU: replacing the
// CPU (a create, an upload) clears every grant, as deleting the device
// drops its rights. ours holds the roles that carry tiad's comment.
type access struct {
	users map[string]*fakeUser
	roles map[string]bool
	ours  map[string]bool
}

type fakeUser struct {
	password string
	// history is the user's last passwords, the current one last: TIA
	// refuses any of them as a new one (5, its default policy).
	history []string
	// recordKey is whose record the user's comment holds: "this" agent's,
	// an "other" agent key's, or "" for no tiad record; recordPassword is
	// the password it records. The fake never keeps it anywhere tiad would
	// not: tiad keeps only a salted HMAC.
	recordKey, recordPassword string
	roles                     map[string]bool
}

func (a access) clone() access {
	c := access{users: map[string]*fakeUser{}, roles: map[string]bool{}, ours: map[string]bool{}}
	for n, u := range a.users {
		rs := map[string]bool{}
		for r := range u.roles {
			rs[r] = true
		}
		c.users[n] = &fakeUser{password: u.password, history: append([]string(nil), u.history...),
			recordKey: u.recordKey, recordPassword: u.recordPassword, roles: rs}
	}
	for n, g := range a.roles {
		c.roles[n] = g
	}
	for n, o := range a.ours {
		c.ours[n] = o
	}
	return c
}

// dropGrants clears every role's Full access: the CPU it was on is gone.
func (a access) dropGrants() {
	for r := range a.roles {
		a.roles[r] = false
	}
}

// accessMade is AccessMade: what applies on a session made.
type accessMade struct{ user, role bool }

// fullAccess reports whether a user holds Full access on the CPU.
func (a access) fullAccess() bool {
	for _, u := range a.users {
		for r := range u.roles {
			if a.roles[r] {
				return true
			}
		}
	}
	return false
}

// planAccess is AccessRule.Plan: current is the kept CPU's state, or nil
// for a CPU the apply makes.
func planAccess(current *string, want string, havePassword bool) *failure {
	bad := func(f string, a ...any) *failure { return fail(http.StatusBadRequest, tiad.KindBadRequest, f, a...) }
	if want == tiad.AccessKeep {
		return nil
	}
	if want != tiad.AccessFullAccessUser {
		return bad("cpu.accessControl: %q is not one of fullAccessUser, keep", want)
	}
	if !havePassword {
		return bad("cpu.accessControl is fullAccessUser, and secrets.fullAccessPassword is empty (it comes from the secret store, never the spec)")
	}
	if current == nil || *current == AccessEnabled {
		return nil
	}
	what := "set to " + strconv.Quote(*current)
	if *current == AccessAbsent {
		what = "not available (no such setting)"
	}
	return bad("the project's CPU has its access control %s, and tiad turns access control on only for a CPU it makes: to keep the CPU as found, set cpu.accessControl to keep", what)
}

func (s *session) ensureAccess() {
	if s.access.users == nil {
		s.access = access{users: map[string]*fakeUser{}, roles: map[string]bool{}, ours: map[string]bool{}}
	}
}

// prepareAccess is AccessSteps.Prepare: before any write of the apply, a
// found user's password, and a refusal of a user or role tiad did not
// make. A refusal leaves the project as it was.
func (s *session) prepareAccess(want, password string, recordOnly bool, notes *[]string) (wrote bool, fl *failure) {
	bad := func(m string) (bool, *failure) { return false, fail(http.StatusBadRequest, tiad.KindBadRequest, "%s", m) }
	if want != tiad.AccessFullAccessUser {
		return false, nil
	}
	s.ensureAccess()
	if _, ok := s.access.roles[FullAccessRole]; ok && !s.access.ours[FullAccessRole] {
		return bad(ForeignRole)
	}
	u, ok := s.access.users[FullAccessUser]
	if !ok {
		return false, nil
	}
	switch {
	case u.recordKey == "":
		if !recordOnly {
			return bad(ForeignUser)
		}
		u.recordKey, u.recordPassword = "this", password
		*notes = append(*notes, TakenOverNote)
	case u.recordKey == "this" && u.recordPassword == password:
		*notes = append(*notes, UnchangedUserNote)
		return false, nil
	case recordOnly:
		u.recordKey, u.recordPassword = "this", password
		*notes = append(*notes, RecordedUserNote)
	default:
		for _, h := range u.history {
			if h == password {
				if u.recordKey == "this" {
					return bad(ReusedPassword)
				}
				return bad(ReusedOtherKey)
			}
		}
		u.password, u.recordKey, u.recordPassword = password, "this", password
		u.history = append(u.history, password)
		if len(u.history) > 5 {
			u.history = u.history[1:]
		}
		*notes = append(*notes, SetUserNote)
	}
	return true, nil
}

// applyAccess is AccessSteps.Run on s's project. madeCPU is the CPU the
// apply made; what it makes is recorded on s at once. A found user's
// password is prepareAccess's.
func (s *session) applyAccess(want, password string, madeCPU bool, notes *[]string) *failure {
	tiaFail := func(m string) *failure { return fail(http.StatusInternalServerError, tiad.KindOperationFailed, "TIA: %s", m) }
	if want == tiad.AccessFullAccessUser {
		// As the agent: once set on this session, a later keep keeps
		// what tiad set (#1159 review).
		s.accessSet = true
	}
	if want == tiad.AccessKeep {
		return nil
	}
	var current *string
	if !madeCPU {
		current = &s.accessControl
	}
	if fl := planAccess(current, want, password != ""); fl != nil {
		return fl
	}
	if madeCPU {
		if s.accessControl == AccessAbsent {
			return tiaFail("the new CPU has no access control setting, so it cannot have a Full access user: set cpu.accessControl to keep")
		}
		s.accessControl = AccessEnabled
	}
	s.ensureAccess()
	if _, ok := s.access.roles[FullAccessRole]; !ok {
		s.access.ours[FullAccessRole] = true
		s.made.role = true
	} else if !s.access.ours[FullAccessRole] {
		return tiaFail(ForeignRole)
	}
	s.access.roles[FullAccessRole] = true
	u, ok := s.access.users[FullAccessUser]
	if !ok {
		u = &fakeUser{password: password, history: []string{password}, recordKey: "this", recordPassword: password, roles: map[string]bool{}}
		s.access.users[FullAccessUser] = u
		s.made.user = true
		*notes = append(*notes, MadeUserNote)
	} else if u.recordKey == "" {
		return tiaFail(ForeignUser)
	}
	u.roles[FullAccessRole] = true
	return nil
}

// removeMade is ITia.RemoveAccess for what this session made; it
// returns what the upload reports as removedAccess.
func (s *session) removeMade() []string {
	var removed []string
	if _, ok := s.access.users[FullAccessUser]; ok && s.made.user {
		delete(s.access.users, FullAccessUser)
		removed = append(removed, fmt.Sprintf("user %q", FullAccessUser))
	}
	if _, ok := s.access.roles[FullAccessRole]; ok && s.made.role {
		delete(s.access.roles, FullAccessRole)
		delete(s.access.ours, FullAccessRole)
		for _, u := range s.access.users {
			delete(u.roles, FullAccessRole)
		}
		removed = append(removed, fmt.Sprintf("role %q", FullAccessRole))
	}
	s.made = accessMade{}
	return removed
}

// openRecords is what an open does to tiad's records (AccessScript.Records).
func (f *Fake) openRecords(a access) {
	switch f.accessRecords {
	case "strip-users":
		for _, u := range a.users {
			u.recordKey, u.recordPassword = "", ""
		}
	case "strip-roles":
		for r := range a.ours {
			delete(a.ours, r)
		}
	case "rekey":
		for _, u := range a.users {
			if u.recordKey == "this" {
				u.recordKey = "other"
			}
		}
	}
}

// AccessScript scripts what TIA holds about access control: the
// projects' password policy for local users (nil: TIA's default), the
// access control an upload brings ("": Disabled, as the S2a upload found
// the bench CPU), and Records, what an open does to tiad's records in a
// saved project: "" keeps them; "strip-users" opens the users without
// them and "strip-roles" the roles without tiad's comment (a project tiad
// did not make them in); "rekey" opens them as another agent key's.
type AccessScript struct {
	Policy              *PasswordPolicy `json:"passwordPolicy,omitempty"`
	UploadAccessControl string          `json:"uploadAccessControl,omitempty"`
	Records             string          `json:"accessRecords,omitempty"`
}

// SetAccess scripts access control (AccessScript).
func (f *Fake) SetAccess(a AccessScript) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.policy = a.Policy
	f.uploadAccess = a.UploadAccessControl
	f.accessRecords = a.Records
}

func (f *Fake) passwordPolicy() PasswordPolicy {
	if f.policy == nil {
		return DefaultPasswordPolicy()
	}
	return *f.policy
}

func (f *Fake) uploadAccessControl() string {
	if f.uploadAccess == "" {
		return AccessDisabled
	}
	return f.uploadAccess
}

// checkPolicy is the agent's CheckPolicy: before any write.
func (f *Fake) checkPolicy(st tiad.Station, sec tiad.Secrets) *failure {
	if st.CPU.AccessControl != tiad.AccessFullAccessUser {
		return nil
	}
	if e := f.passwordPolicy().check(sec.FullAccessPassword); e != "" {
		return fail(http.StatusBadRequest, tiad.KindBadRequest, "secrets.fullAccessPassword %s", e)
	}
	return nil
}

func utf16Units(s string) []uint16 { return utf16.Encode([]rune(s)) }

// WebOnNote is WebSteps.OnNote: an apply that turns the web server on
// says so.
const WebOnNote = `the web server is on, over access control EnabledWithAccessControl: a web client may do what the runtime rights of its user's roles on this CPU allow`

// webRefusal is WebRule.Refusal: only EnabledWithAccessControl lets the
// web server be on (with access control off, a FW 3.1 CPU's web server
// lets anyone in as "Anonymous" with full rights).
func webRefusal(state string) string {
	if state == AccessEnabled {
		return ""
	}
	what := "set to " + strconv.Quote(state)
	if state == AccessAbsent {
		what = "not available (no such setting)"
	}
	return "webServer.enabled is true, and the CPU's access control is " + what +
		`: with access control off, a FW 3.1 CPU's web server lets anyone in as "Anonymous" with full rights;` +
		" turn access control on (a CPU tiad makes has it on), or set webServer.enabled to false"
}

// planWeb is WebRule.Plan: before any write, a web server turned on over
// a kept CPU's access control (current nil: a CPU the apply makes).
func planWeb(current *string, want *tiad.WebServer) *failure {
	if want == nil || !want.Enabled || current == nil {
		return nil
	}
	if why := webRefusal(*current); why != "" {
		return fail(http.StatusBadRequest, tiad.KindBadRequest, "%s", why)
	}
	return nil
}

// applyWeb is WebSteps.Run, after applyAccess: the web server on reads
// the access control first; want nil leaves a kept CPU's as found and
// turns a new CPU's off.
func (s *session) applyWeb(want *tiad.WebServer, madeCPU bool, notes *[]string) *failure {
	if want == nil && !madeCPU {
		return nil
	}
	on := want != nil && want.Enabled
	if on {
		if why := webRefusal(s.accessControl); why != "" {
			return fail(http.StatusInternalServerError, tiad.KindOperationFailed, "TIA: %s", why)
		}
		*notes = append(*notes, WebOnNote)
	}
	s.webServer = on
	return nil
}
