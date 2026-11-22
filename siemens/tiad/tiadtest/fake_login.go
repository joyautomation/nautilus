package tiadtest

import (
	"fmt"

	"github.com/joyautomation/nautilus/siemens/tiad"
)

// The agent's reasons for answering TIA's credentials question no
// (LoginUse in tools/tiad/core/TiaRules.cs, #789 stage 5 (A)).
const (
	LoginNoPassword  = `the request carries no secrets.fullAccessPassword (the password of the CPU's user "nautilus", which cpu.accessControl fullAccessUser makes), so tiad has no credentials to answer`
	LoginTrustedHere = "password not sent: this operation trusted the CPU's unverified certificate, and tiad sends no password to a CPU it cannot verify"
	LoginAskedAgain  = `password not sent again: TIA asked credentials again in this operation after tiad answered them as the user "nautilus" (the CPU may have refused secrets.fullAccessPassword), and tiad never sends a password twice`
	// LoginDownloadNeedsPassword is the agent's refusal of a download of
	// a fullAccessUser station without the password.
	LoginDownloadNeedsPassword = `the session's station has cpu.accessControl fullAccessUser, so its CPU asks credentials going online once a hardware load put them there, and the compare after this download would: secrets.fullAccessPassword (the password of the CPU's user "nautilus") is required; nothing was sent to the CPU`
	// LoginQuestion is how the agent names TIA's credentials question.
	LoginQuestion = "credentials (OnlineAuthenticationConfiguration)"
)

// loginBlocked is the agent's reason while its TIA instance trusts a
// certificate blindly (OnlineLogin.For).
func loginBlocked(bt *tiad.BlindTrust) string {
	return "password not sent: this agent's TIA instance trusted an unverified CPU certificate (" + bt.Describe() +
		"), and tiad sends no password over a connection that may reach a CPU it cannot verify: close every session (the agent then retires the instance) or restart the agent, then call again"
}

// onlineRefused is the agent's TiaNames.OnlineRefused for one question.
func onlineRefused(address, asked string) string {
	return fmt.Sprintf("going online to %s, TIA asked: %s. tiad trusts no CPU certificate unless the request opts in (trustUnverifiedCertificate), and answers credentials only as the CPU's user \"nautilus\" with the request's secrets.fullAccessPassword, so it answered no (TIA: Online connection failed.)",
		address, asked)
}

// onlinePassword is the request's secrets.fullAccessPassword, "" for none.
func onlinePassword(s *tiad.OnlineSecrets) string {
	if s == nil {
		return ""
	}
	return s.FullAccessPassword
}

// askLogin is one connection going online to address: TIA asks credentials
// only when a hardware load turned the CPU's access control on (logins),
// over secure communication, offering project users, as the C# fake's
// CPU does. It answers as the agent's LoginUse does, and returns "" when
// it logged on (or nothing was asked), else the refusal. bt is the
// instance's blind trust as the agent read it before the call (the fake's
// CPU asks no TLS question before credentials, so no call of its trusts
// first; tiad-fake's upload models that). A wrong password is asked again,
// as TIA does when a CPU refuses one, and refused.
func (f *Fake) askLogin(address, password string, bt *tiad.BlindTrust) string {
	want, ok := f.logins[address]
	if !ok {
		return ""
	}
	why := ""
	switch {
	case password == "":
		why = LoginNoPassword
	case bt != nil:
		why = loginBlocked(bt)
	case password != want:
		why = LoginAskedAgain
	default:
		return ""
	}
	return onlineRefused(address, LoginQuestion+": "+why)
}

// fullAccessPassword is the password of the user "nautilus" when the
// project's access control is on and the user holds Full access: what a
// hardware load leaves on the CPU.
func (s *session) fullAccessPassword() (string, bool) {
	if s.accessControl != AccessEnabled {
		return "", false
	}
	u, ok := s.access.users[FullAccessUser]
	if !ok {
		return "", false
	}
	for r := range u.roles {
		if s.access.roles[r][fullAccessRight] {
			return u.password, true
		}
	}
	return "", false
}
