package tiadtest

import (
	"crypto/sha256"
	"encoding/hex"
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

// UnchangedUserNote and SetUserNote are what an apply says about a
// "nautilus" user the project held (AccessSteps.UnchangedNote, SetNote);
// ReusedPassword is the refusal of a password the user has had
// (AccessSteps.Reused).
const (
	UnchangedUserNote = `the project's user "nautilus" has the runtime right Full access (role "nautilus full access"); its password is unchanged: secrets.fullAccessPassword is the one tiad last set`
	SetUserNote       = `the project's user "nautilus" has the runtime right Full access (role "nautilus full access"); its password is now secrets.fullAccessPassword`
	ReusedPassword    = `TIA refuses secrets.fullAccessPassword for the user "nautilus": TIA blocks a user's recent passwords for reuse (the current one included, when tiad has no record of having set it in this project): choose another password, or make the project again (POST /v1/projects)`
)

// fingerprint stands in for the agent's keyed fingerprint (AccessKey):
// the fake never keeps a password where tiad would not.
func fingerprint(password string) string {
	h := sha256.Sum256([]byte("fake\n" + password))
	return hex.EncodeToString(h[:])
}

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
		default:
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
// drops its rights.
type access struct {
	users map[string]*fakeUser
	roles map[string]bool
}

type fakeUser struct {
	password string
	// history is the user's last passwords, the current one last: TIA
	// refuses any of them as a new one (5, its default policy).
	history []string
	// fingerprint is what tiad recorded of the password it set (the
	// user's comment in TIA).
	fingerprint string
	roles       map[string]bool
}

func (a access) clone() access {
	c := access{users: map[string]*fakeUser{}, roles: map[string]bool{}}
	for n, u := range a.users {
		rs := map[string]bool{}
		for r := range u.roles {
			rs[r] = true
		}
		c.users[n] = &fakeUser{password: u.password, history: append([]string(nil), u.history...), fingerprint: u.fingerprint, roles: rs}
	}
	for n, g := range a.roles {
		c.roles[n] = g
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

// applyAccess is AccessSteps.Run on s's project. madeCPU is the CPU the
// apply made; what it makes is recorded on s at once.
func (s *session) applyAccess(want, password string, madeCPU bool, notes *[]string) *failure {
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
			return fail(http.StatusInternalServerError, tiad.KindOperationFailed, "TIA: the new CPU has no access control setting, so it cannot have a Full access user: set cpu.accessControl to keep")
		}
		s.accessControl = AccessEnabled
	}
	if s.access.users == nil {
		s.access = access{users: map[string]*fakeUser{}, roles: map[string]bool{}}
	}
	if _, ok := s.access.roles[FullAccessRole]; !ok {
		s.made.role = true
	}
	s.access.roles[FullAccessRole] = true
	// A found user's password is set only when the secret is not the one
	// tiad last set (its fingerprint), and TIA refuses one the user has
	// had.
	fp := fingerprint(password)
	u, ok := s.access.users[FullAccessUser]
	note := ""
	switch {
	case !ok:
		u = &fakeUser{password: password, history: []string{password}, fingerprint: fp, roles: map[string]bool{}}
		s.access.users[FullAccessUser] = u
		s.made.user = true
	case u.fingerprint == fp:
		note = UnchangedUserNote
	default:
		for _, h := range u.history {
			if h == password {
				return fail(http.StatusInternalServerError, tiad.KindOperationFailed, "TIA: %s", ReusedPassword)
			}
		}
		u.password, u.fingerprint = password, fp
		u.history = append(u.history, password)
		if len(u.history) > 5 {
			u.history = u.history[1:]
		}
		note = SetUserNote
	}
	u.roles[FullAccessRole] = true
	if ok {
		*notes = append(*notes, note)
	} else {
		*notes = append(*notes, fmt.Sprintf("made the user %q with the runtime right Full access (role %q), its password from secrets.fullAccessPassword", FullAccessUser, FullAccessRole))
	}
	return nil
}

// removeMade is ITia.RemoveAccess for what this session made; it
// returns what the upload reports as removedAccess.
func (s *session) removeMade() []string {
	var removed []string
	if s.made.user {
		delete(s.access.users, FullAccessUser)
		removed = append(removed, fmt.Sprintf("user %q", FullAccessUser))
	}
	if s.made.role {
		delete(s.access.roles, FullAccessRole)
		for _, u := range s.access.users {
			delete(u.roles, FullAccessRole)
		}
		removed = append(removed, fmt.Sprintf("role %q", FullAccessRole))
	}
	s.made = accessMade{}
	return removed
}

// SetAccess scripts the projects' password policy for local users and the
// access control an upload brings ("" means Disabled, as the S2a upload
// found the bench CPU).
func (f *Fake) SetAccess(policy PasswordPolicy, uploadAccessControl string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.policy = &policy
	f.uploadAccess = uploadAccessControl
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
