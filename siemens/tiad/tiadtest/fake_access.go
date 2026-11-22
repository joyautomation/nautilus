package tiadtest

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
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
// maps to the runtime rights it holds on the project's CPU: replacing the
// CPU (a create, an upload) clears every grant, as deleting the device
// drops its rights. ours holds the roles that carry tiad's comment for
// their name; anonymous is whether the project's anonymous user is active.
type access struct {
	users     map[string]*fakeUser
	roles     map[string]map[string]bool
	ours      map[string]bool
	anonymous bool
}

// fullAccessRight is the runtime right Full access's identifier.
const fullAccessRight = "ProtectionLevelFullAccess"

// offeredRights are the runtime rights the fake's CPU offers (a FW 3.1
// CPU's, as the TIA machine listed them, in part).
var offeredRights = map[string]bool{
	fullAccessRight: true, "ProtectionLevelReadAccess": true, "ProtectionLevelHMIAccess": true,
	"WebReadTags": true, "WebWriteTags": true, "WebChangeOperatingMode": true, "WebAllowDiagnostics": true,
	"WebReadSyslog": true, "WebReadWatchTables": true, "WebWriteWatchTables": true,
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
	inactive                  bool
}

func (a access) clone() access {
	c := access{users: map[string]*fakeUser{}, roles: map[string]map[string]bool{}, ours: map[string]bool{}, anonymous: a.anonymous}
	for n, u := range a.users {
		rs := map[string]bool{}
		for r := range u.roles {
			rs[r] = true
		}
		c.users[n] = &fakeUser{password: u.password, history: append([]string(nil), u.history...),
			recordKey: u.recordKey, recordPassword: u.recordPassword, roles: rs, inactive: u.inactive}
	}
	for n, g := range a.roles {
		rs := map[string]bool{}
		for r := range g {
			rs[r] = true
		}
		c.roles[n] = rs
	}
	for n, o := range a.ours {
		c.ours[n] = o
	}
	return c
}

// dropGrants clears every role's rights: the CPU they were on is gone.
func (a access) dropGrants() {
	for r := range a.roles {
		a.roles[r] = map[string]bool{}
	}
}

// accessMade is AccessMade: what applies on a session made.
type accessMade struct {
	user, role         bool
	webUsers, webRoles []string
}

// fullAccess reports whether a user holds Full access on the CPU.
func (a access) fullAccess() bool {
	for _, u := range a.users {
		for r := range u.roles {
			if a.roles[r][fullAccessRight] {
				return true
			}
		}
	}
	return false
}

// deleteRole takes role off every user, then deletes it.
func (a access) deleteRole(role string) {
	for _, u := range a.users {
		delete(u.roles, role)
	}
	delete(a.roles, role)
	delete(a.ours, role)
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
		s.access = access{users: map[string]*fakeUser{}, roles: map[string]map[string]bool{}, ours: map[string]bool{}}
	}
}

// prepareAccess is AccessSteps.Prepare: before any write of the apply, a
// found user's password, and a refusal of a user or role tiad did not
// make. A refusal leaves the project as it was.
func (s *session) prepareAccess(want, password string, recordOnly bool, notes *[]string) (wrote bool, fl *failure) {
	bad := func(m string) (bool, *failure) {
		return false, fail(http.StatusBadRequest, tiad.KindBadRequest, "%s", m)
	}
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
	tiaFail := func(m string) *failure {
		return fail(http.StatusInternalServerError, tiad.KindOperationFailed, "TIA: %s", m)
	}
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
		s.access.roles[FullAccessRole] = map[string]bool{}
		s.access.ours[FullAccessRole] = true
		s.made.role = true
	} else if !s.access.ours[FullAccessRole] {
		return tiaFail(ForeignRole)
	}
	s.access.roles[FullAccessRole][fullAccessRight] = true
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

// removeMade is ITia.RemoveAccess (AccessSteps.Remove) for what this
// session made; it returns what the upload reports as removedAccess.
func (s *session) removeMade() []string {
	var removed []string
	if _, ok := s.access.users[FullAccessUser]; ok && s.made.user {
		delete(s.access.users, FullAccessUser)
		removed = append(removed, fmt.Sprintf("user %q", FullAccessUser))
	}
	if _, ok := s.access.roles[FullAccessRole]; ok && s.made.role {
		s.access.deleteRole(FullAccessRole)
		removed = append(removed, fmt.Sprintf("role %q", FullAccessRole))
	}
	for _, n := range s.made.webUsers {
		if _, ok := s.access.users[n]; ok {
			delete(s.access.users, n)
			removed = append(removed, fmt.Sprintf("user %q", n))
		}
	}
	for _, r := range s.made.webRoles {
		if _, ok := s.access.roles[r]; ok {
			s.access.deleteRole(r)
			removed = append(removed, fmt.Sprintf("role %q", r))
		}
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
	// UploadWebServer makes an upload bring a CPU whose web server is on
	// (the bench CPU's is off).
	UploadWebServer bool `json:"uploadWebServer,omitempty"`
	// UploadAnonymous makes an upload bring a project whose anonymous
	// user is active.
	UploadAnonymous bool `json:"uploadAnonymous,omitempty"`
}

// SetAccess scripts access control (AccessScript).
func (f *Fake) SetAccess(a AccessScript) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.policy = a.Policy
	f.uploadAccess = a.UploadAccessControl
	f.uploadWeb = a.UploadWebServer
	f.uploadAnonymous = a.UploadAnonymous
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

// checkPolicy is the agent's CheckPolicy: before any write, the Full
// access user's password and every web user's.
func (f *Fake) checkPolicy(st tiad.Station, sec tiad.Secrets) *failure {
	pol := f.passwordPolicy()
	if st.CPU.AccessControl == tiad.AccessFullAccessUser {
		if e := pol.check(sec.FullAccessPassword); e != "" {
			return fail(http.StatusBadRequest, tiad.KindBadRequest, "secrets.fullAccessPassword %s", e)
		}
	}
	if w := st.WebServer; w != nil && w.Enabled {
		for _, u := range w.Users {
			if e := pol.check(sec.WebUsers[u.Name]); e != "" {
				return fail(http.StatusBadRequest, tiad.KindBadRequest, "secrets.webUsers[%q] %s", u.Name, e)
			}
		}
	}
	return nil
}

func utf16Units(s string) []uint16 { return utf16.Encode([]rune(s)) }

// WebOnNote is WebSteps.OnNote: an apply that turns the web server on
// says so.
const WebOnNote = `the web server is on, HTTPS only, over access control EnabledWithAccessControl: a web client may do what the runtime rights of its user's roles on this CPU allow`

// WebAnonymousNote is WebSteps.AnonymousNote: a kept CPU's web server on
// over access control off, left as found.
const WebAnonymousNote = `WARNING: the kept CPU's web server is on and its access control is not on, so it lets anyone in as "Anonymous" with full rights; webServer is left out, so tiad leaves it as found, and a download loads it so: set webServer.enabled to false to turn it off`

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

// Notes and refusals of WebSteps and WebUsers, word for word.
const (
	WebUnreadNote        = `WARNING: the kept CPU's web server could not be read in full (see the agent's log); webServer is left out, so tiad leaves it as found`
	WebPlainHTTPNote     = `WARNING: the kept CPU's web server is on and allows plain HTTP, so web passwords may cross the network in clear; webServer is left out, so tiad leaves it as found: set webServer.enabled to true (HTTPS only) or false`
	WebAnonymousUserNote = `WARNING: the kept CPU's web server is on and the project's anonymous user is active, so anyone who reaches it gets the rights of the anonymous user's roles; webServer is left out, so tiad leaves it as found: deactivate the anonymous user in TIA, or set webServer.enabled to false`
	WebAnonymousActive   = `webServer.enabled is true, and the project's anonymous user is active: anyone who reaches the web server would get the rights of its roles; deactivate the anonymous user in TIA, or set webServer.enabled to false`
)

// WebRoleName is the role tiad makes for a web user (WebUsers.RoleName).
func WebRoleName(user string) string { return "nautilus web " + user }

// webRights maps a spec's web rights to the CPU's runtime rights
// (WebUsers.Rights).
var webRights = map[string]string{"read": "WebReadTags", "write": "WebWriteTags", "mode": "WebChangeOperatingMode", "diagnostics": "WebAllowDiagnostics"}

// webIDs is the runtime rights u's role holds, sorted (WebUsers.Ids).
func webIDs(u tiad.WebUser) []string {
	set := map[string]bool{}
	for _, r := range u.Rights {
		set[webRights[r]] = true
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func webDescribe(u tiad.WebUser) string {
	ids := webIDs(u)
	if len(ids) == 0 {
		return "none"
	}
	return strings.Join(ids, ", ")
}

func webSecret(user string) string { return fmt.Sprintf("secrets.webUsers[%q]", user) }

// The notes and refusals of WebUsers naming a user, word for word.
func WebForeignRole(user string) string {
	return fmt.Sprintf("the project holds a role %q that tiad did not make, and tiad does not give web rights through a role it did not make: rename or delete it in TIA, or leave the web user %q out", WebRoleName(user), user)
}

func WebForeignUser(user string) string {
	return fmt.Sprintf("the project holds a user %q that carries no tiad record, so tiad did not make it and does not take it over: rename or delete it in TIA, or name the web user otherwise", user)
}

func WebReused(user string) string {
	return fmt.Sprintf("TIA refuses %s for the web user %q: the user had that password recently, and TIA blocks a user's recent passwords for reuse: choose another password, or make the project again (POST /v1/projects)", webSecret(user), user)
}

func WebReusedOtherKey(user string) string {
	return fmt.Sprintf("TIA refuses %s for the web user %q as a password the user has now or had recently, and the project's record of it was made under another agent key, so tiad cannot tell which; web users have no record-only route, so the password must change: set a new one in %s and apply again (or make the project again, POST /v1/projects)", webSecret(user), user, webSecret(user))
}

func WebMadeNote(u tiad.WebUser) string {
	return fmt.Sprintf("made the web user %q (role %q, runtime rights %s), its password from %s", u.Name, WebRoleName(u.Name), webDescribe(u), webSecret(u.Name))
}

func WebFoundNote(u tiad.WebUser) string {
	return fmt.Sprintf("the web user %q holds exactly the role %q, runtime rights %s", u.Name, WebRoleName(u.Name), webDescribe(u))
}

func WebUnchangedNote(user string) string {
	return fmt.Sprintf("the web user %q's password is unchanged: %s is the one tiad last set in this project (a change made in TIA since would not show)", user, webSecret(user))
}

func WebSetNote(user string) string {
	return fmt.Sprintf("the web user %q's password is now %s", user, webSecret(user))
}

func WebRemovedNote(user string, userToo bool) string {
	who := ""
	if userToo {
		who = fmt.Sprintf("the web user %q and ", user)
	}
	return fmt.Sprintf("removed %sits role %q: no longer in webServer.users", who, WebRoleName(user))
}

func WebNoReadNote(user string) string {
	return fmt.Sprintf(`the web user %q may write tags or change the operating mode but not read tags (no "read" right): add "read" to its rights unless that is meant`, user)
}

// webCheck is WebUsers.Check: before any write, an active anonymous user
// while the server is to be on, and a web user or role tiad did not make.
func (s *session) webCheck(want *tiad.WebServer) *failure {
	bad := func(m string) *failure { return fail(http.StatusBadRequest, tiad.KindBadRequest, "%s", m) }
	if want == nil || !want.Enabled {
		return nil
	}
	s.ensureAccess()
	if s.access.anonymous {
		return bad(WebAnonymousActive)
	}
	for _, u := range want.Users {
		if _, ok := s.access.roles[WebRoleName(u.Name)]; ok && !s.access.ours[WebRoleName(u.Name)] {
			return bad(WebForeignRole(u.Name))
		}
		if fu, ok := s.access.users[u.Name]; ok && fu.recordKey == "" {
			return bad(WebForeignUser(u.Name))
		}
	}
	return nil
}

// webPasswords is WebUsers.Passwords: a found web user's password,
// before the station step. A refusal after an earlier user's was set
// leaves that one set and says it wrote.
func (s *session) webPasswords(want *tiad.WebServer, sec tiad.Secrets, notes *[]string) (wrote bool, fl *failure) {
	if want == nil || !want.Enabled {
		return false, nil
	}
	for _, wu := range want.Users {
		u, ok := s.access.users[wu.Name]
		if !ok {
			continue
		}
		pw := sec.WebUsers[wu.Name]
		if u.recordKey == "this" && u.recordPassword == pw {
			*notes = append(*notes, WebUnchangedNote(wu.Name))
			continue
		}
		for _, h := range u.history {
			if h == pw {
				msg := WebReusedOtherKey(wu.Name)
				if u.recordKey == "this" {
					msg = WebReused(wu.Name)
				}
				return wrote, fail(http.StatusBadRequest, tiad.KindBadRequest, "%s", msg)
			}
		}
		u.password, u.recordKey, u.recordPassword = pw, "this", pw
		u.history = append(u.history, pw)
		if len(u.history) > 5 {
			u.history = u.history[1:]
		}
		wrote = true
		*notes = append(*notes, WebSetNote(wu.Name))
	}
	return wrote, nil
}

// prepareAll is AccessSteps.PrepareAll: every refusal it can make
// without writing, then the Full access user's password, then the web
// users'.
func (s *session) prepareAll(st tiad.Station, sec tiad.Secrets, recordOnly bool, notes *[]string) (bool, *failure) {
	if fl := s.webCheck(st.WebServer); fl != nil {
		return false, fl
	}
	wrote, fl := s.prepareAccess(st.CPU.AccessControl, sec.FullAccessPassword, recordOnly, notes)
	if fl != nil {
		return wrote, fl
	}
	w2, fl := s.webPasswords(st.WebServer, sec, notes)
	return wrote || w2, fl
}

// applyWebUsers is WebUsers.Run: stale tiad web roles (by tiad's
// comment) removed with their users when those carry a tiad record, then
// each wanted user exact: its role marked, exactly its rights, the user
// with its record, active, holding its role alone.
func (s *session) applyWebUsers(want *tiad.WebServer, sec tiad.Secrets, notes *[]string) *failure {
	tiaFail := func(m string) *failure {
		return fail(http.StatusInternalServerError, tiad.KindOperationFailed, "TIA: %s", m)
	}
	s.ensureAccess()
	var wanted []tiad.WebUser
	if want != nil && want.Enabled {
		wanted = want.Users
	}
	keep := map[string]bool{}
	for _, u := range wanted {
		keep[strings.ToLower(WebRoleName(u.Name))] = true
	}
	var stale []string
	for r := range s.access.roles {
		if strings.HasPrefix(r, "nautilus web ") && s.access.ours[r] && !keep[strings.ToLower(r)] {
			stale = append(stale, r)
		}
	}
	sort.Strings(stale)
	for _, r := range stale {
		user := strings.TrimPrefix(r, "nautilus web ")
		s.access.deleteRole(r)
		u, ok := s.access.users[user]
		userToo := !strings.EqualFold(user, FullAccessUser) && ok && u.recordKey != ""
		if userToo {
			delete(s.access.users, user)
		}
		*notes = append(*notes, WebRemovedNote(user, userToo))
	}
	for _, wu := range wanted {
		for _, id := range webIDs(wu) {
			if !offeredRights[id] {
				var all []string
				for r := range offeredRights {
					all = append(all, r)
				}
				sort.Strings(all)
				return tiaFail(fmt.Sprintf("the CPU offers no runtime right %q for the web user %q; it offers %s", id, wu.Name, strings.Join(all, ", ")))
			}
		}
	}
	for _, wu := range wanted {
		role := WebRoleName(wu.Name)
		if _, ok := s.access.roles[role]; !ok {
			s.access.ours[role] = true
			s.made.webRoles = append(s.made.webRoles, role)
		} else if !s.access.ours[role] {
			return tiaFail(WebForeignRole(wu.Name))
		}
		rights := map[string]bool{}
		for _, id := range webIDs(wu) {
			rights[id] = true
		}
		s.access.roles[role] = rights
		u, ok := s.access.users[wu.Name]
		switch {
		case !ok:
			pw := sec.WebUsers[wu.Name]
			u = &fakeUser{password: pw, history: []string{pw}, recordKey: "this", recordPassword: pw, roles: map[string]bool{}}
			s.access.users[wu.Name] = u
			s.made.webUsers = append(s.made.webUsers, wu.Name)
			*notes = append(*notes, WebMadeNote(wu))
		case u.recordKey == "":
			return tiaFail(WebForeignUser(wu.Name))
		default:
			*notes = append(*notes, WebFoundNote(wu))
		}
		u.inactive = false
		u.roles = map[string]bool{role: true}
		for n, other := range s.access.users {
			if n != wu.Name {
				delete(other.roles, role)
			}
		}
		has := func(r string) bool {
			for _, x := range wu.Rights {
				if x == r {
					return true
				}
			}
			return false
		}
		if !has("read") && (has("write") || has("mode")) {
			*notes = append(*notes, WebNoReadNote(wu.Name))
		}
	}
	return nil
}

// applyWeb is WebSteps.Run, after applyAccess: the web server on reads
// the access control and the anonymous user first; the users come before
// the switch; want nil leaves a kept CPU's as found (never silently) and
// turns a new CPU's off.
func (s *session) applyWeb(want *tiad.WebServer, sec tiad.Secrets, madeCPU bool, notes *[]string) *failure {
	if want == nil && !madeCPU {
		switch {
		case !s.webServer:
		case webRefusal(s.accessControl) != "":
			*notes = append(*notes, WebAnonymousNote)
		default:
			if !s.webHTTPS {
				*notes = append(*notes, WebPlainHTTPNote)
			}
			if s.access.anonymous {
				*notes = append(*notes, WebAnonymousUserNote)
			}
		}
		return nil
	}
	on := want != nil && want.Enabled
	if on {
		if why := webRefusal(s.accessControl); why != "" {
			return fail(http.StatusInternalServerError, tiad.KindOperationFailed, "TIA: %s", why)
		}
		if s.access.anonymous {
			return fail(http.StatusInternalServerError, tiad.KindOperationFailed, "TIA: %s", WebAnonymousActive)
		}
	}
	if fl := s.applyWebUsers(want, sec, notes); fl != nil {
		return fl
	}
	if on {
		*notes = append(*notes, WebOnNote)
	}
	s.webServer, s.webHTTPS = on, on
	return nil
}
