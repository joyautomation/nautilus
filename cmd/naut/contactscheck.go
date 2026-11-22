package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/alarm/channel"
	"github.com/joyautomation/nautilus/alarm/escalate"
	"github.com/joyautomation/nautilus/alarm/oncall"
	"github.com/joyautomation/nautilus/internal/project"
)

// contactNeed is one person a level pages by a kind that needs their own
// address (email, sms, voice), with where the manifest says so.
type contactNeed struct {
	who, kind, where string
}

// contactNeeds lists every person each level could page by a kind that
// needs an address: named principals, and every member of a roster it
// names (anyone on the roster may be on call when it is sent).
func contactNeeds(c *escalate.Config) []contactNeed {
	var out []contactNeed
	seen := map[string]bool{}
	for i, p := range c.Policies {
		for j, l := range p.Levels {
			where := fmt.Sprintf("alarms.escalation[%d] (%s) level %d", i, p.ID, j+1)
			var people []string
			for _, n := range l.Notify {
				if id, ok := strings.CutPrefix(n, escalate.RosterPrefix); ok {
					if r := c.Oncall.Roster(id); r != nil {
						people = append(people, r.Members...)
						for _, s := range r.Shifts {
							people = append(people, s.Members...)
						}
					}
					continue
				}
				people = append(people, n)
			}
			for _, v := range l.Via {
				if !channel.NeedsContact(v) {
					continue
				}
				for _, who := range people {
					if key := who + "\x00" + v; !seen[key] {
						seen[key] = true
						out = append(out, contactNeed{who: who, kind: v, where: where})
					}
				}
			}
		}
	}
	return out
}

// checkContacts is naut check's look at the contacts and secrets files on
// this machine (#715): with a contacts file here (on the box), a person a
// level pages by email, sms or voice with no address for it is an error,
// and so is a channel whose credential the secrets file lacks; with none
// (a checkout, CI), one warning says where they are checked. stateDir is
// "" when this machine has no state directory.
func checkContacts(alarms *project.AlarmsConfig, stateDir string) (errs, warns []string) {
	if alarms == nil {
		return nil, nil
	}
	for _, k := range alarms.Channels.Insecure() {
		warns = append(warns, fmt.Sprintf("alarms.channels.%s: insecure: true: its server's certificate is not verified "+
			"(and, with no credential to send, it may run in plaintext); set ca-file: to your CA's bundle instead", k))
	}
	c, err := alarms.EscalationConfig()
	if err != nil || c == nil {
		return errs, warns
	}
	needs := contactNeeds(c)
	secrets := alarms.Channels.Secrets()
	if len(needs) == 0 && len(secrets) == 0 {
		return errs, warns
	}
	if stateDir == "" {
		return errs, append(warns, "alarms.escalation: no state directory here, so the contacts and secrets files were not checked; "+
			"on the controller: naut alarms contacts check (the built controller answers it: controller alarms contacts --state-dir /var/lib/nautilus check)")
	}
	cpath, spath := oncall.ContactsPath(stateDir), oncall.SecretsPath(stateDir)
	if _, err := os.Stat(cpath); errors.Is(err, fs.ErrNotExist) {
		return errs, append(warns, fmt.Sprintf("alarms.escalation pages people by %s, and this machine has no contacts file (%s): "+
			"they are checked where it exists; on the controller: naut alarms contacts check, and naut alarms contacts set <name> --email … --sms …",
			strings.Join(kindsOf(needs), ", "), cpath))
	}
	contacts, err := oncall.ReadContacts(cpath)
	if err != nil {
		return append(errs, fmt.Sprintf("alarms contacts: %v", err)), warns
	}
	for _, n := range needs {
		if contacts.Contacts[n.who].Address(n.kind) == "" {
			flag := "--" + n.kind
			errs = append(errs, fmt.Sprintf("%s pages %s by %s, but %s has no %s address in %s: "+
				"naut alarms contacts set %s %s <address>", n.where, n.who, n.kind, n.who, n.kind, cpath, n.who, flag))
		}
	}
	s, err := oncall.ReadSecrets(spath)
	if err != nil {
		return append(errs, fmt.Sprintf("alarms secrets: %v", err)), warns
	}
	for _, k := range secrets {
		if s.Secrets[k] == "" {
			errs = append(errs, fmt.Sprintf("alarms.channels.%s needs the secret %s, which %s does not hold: "+
				"naut alarms secrets set %s (reads it from stdin)", strings.Split(k, ".")[0], k, spath, k))
		}
	}
	return errs, warns
}

func kindsOf(needs []contactNeed) []string {
	set := map[string]bool{}
	for _, n := range needs {
		set[n.kind] = true
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
