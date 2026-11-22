package project

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/alarm/acklink"
	"github.com/joyautomation/nautilus/alarm/channel/channeltest"
	"github.com/joyautomation/nautilus/alarm/escalate"
	"github.com/joyautomation/nautilus/alarm/oncall"
	"github.com/joyautomation/nautilus/internal/pairkey"
	"github.com/joyautomation/nautilus/server"
)

// channelsRig is a project whose escalation pages through the real
// channels, against a fake SMTP server (STARTTLS, AUTH, its certificate
// verified through ca-file) and a fake SMS provider (https, verified),
// with the contacts and secrets in a state directory.
type channelsRig struct {
	smtp *channeltest.SMTP
	sms  *channeltest.Provider
	a    *Alarms
	srv  *server.Server
	clk  *stepClock
	scan func()
}

func newChannelsRig(t *testing.T, smtpOpts channeltest.SMTPOptions, contacts map[string]oncall.Contact) *channelsRig {
	t.Helper()
	return newChannelsRigLinks(t, smtpOpts, contacts, false)
}

// newChannelsRigLinks is newChannelsRig whose pages carry ack links
// (#715) when links is set, wired as naut run wires them: the server
// mints, under a key from the pair token, for email and SMS.
func newChannelsRigLinks(t *testing.T, smtpOpts channeltest.SMTPOptions, contacts map[string]oncall.Contact, links bool) *channelsRig {
	t.Helper()
	smtpOpts.User, smtpOpts.Pass = "alarms", "mail-pass-1"
	r := &channelsRig{smtp: channeltest.NewSMTP(t, smtpOpts), sms: channeltest.NewProvider(t)}
	manifest := alarmTags + fmt.Sprintf(`
alarms:
  defs:
    - { id: intrusion, tag: RTU9_ZONE15_INT_001_YA, area: zone15, priority: high, name: "Zone 15 intrusion" }
  channels:
    email: { smtp: "%s", from: alarms@example.com, username: alarms, ca-file: "%s" }
    sms: { provider: "%s", account: AC0001, from: "+15550100", ca-file: "%s" }
  oncall:
    ack-url: https://hmi.example
    rosters:
      - { id: ops, members: [alice] }
      - { id: maint, members: [bob] }
  escalation:
    - id: zone15
      match: { area: zone15 }
      levels:
        - { after: 0s,  notify: [roster:ops], via: [email] }
        - { after: 10m, notify: [roster:maint], via: [sms] }
        - { after: 30m, notify: [carol, dave], via: [email] }
`, r.smtp.Addr, r.smtp.CAFile, r.sms.URL, r.sms.CAFile)
	proj, err := Load(alarmFS(manifest, nil), "")
	if err != nil {
		t.Fatal(err)
	}
	sd := t.TempDir()
	if err := os.Chmod(sd, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := oncall.UpdateContacts(oncall.ContactsPath(sd), "test", func(c *oncall.Contacts) error {
		for p, ct := range contacts {
			c.Contacts[p] = &ct
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := oncall.UpdateSecrets(oncall.SecretsPath(sd), "test", func(s *oncall.Secrets) error {
		s.Secrets["email.password"], s.Secrets["sms.token"] = "mail-pass-1", "sms-token-1"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st, err := oncall.Open(sd)
	if err != nil {
		t.Fatal(err)
	}
	proj.AlarmDirectory = st
	r.clk = &stepClock{t: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	proj.Runtime.Clock = r.clk
	var srv atomic.Pointer[server.Server]
	var ring *acklink.Keyring
	if links {
		k, _ := pairkey.Derive([]byte("peer-token"), pairkey.AckLink)
		ring, _ = acklink.New(k)
		proj.AckLink = func(m escalate.Message, via string) string {
			if via != "email" && via != "sms" {
				return ""
			}
			return srv.Load().AckLinkURL(m.Alarm.ID, m.Alarm.Occurrence, m.To, via, m.Level)
		}
	}
	_, rt := loadAlarmsWith(t, proj)
	if r.a, err = proj.NewAlarms(rt); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.a.Close() })
	o := server.Options{Alarms: r.a.Engine}
	if links {
		o.AckLinks, o.AckURL = ring, proj.Alarms.Oncall.AckURL
	}
	r.srv = server.New(rt, o)
	srv.Store(r.srv)
	r.scan = func() {
		rt.Scan()
		r.a.Escalation.Drain()
	}
	rt.Tags().SetPath("RTU9_ZONE15_INT_001_YA", true)
	return r
}

func (r *channelsRig) run(d time.Duration) {
	for end := r.clk.Now().Add(d); r.clk.Now().Before(end); {
		r.clk.add(5 * time.Second)
		r.scan()
	}
}

func (r *channelsRig) api(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	r.srv.Handler().ServeHTTP(rec, req)
	return rec
}

func notifySummary(rows []alarm.Event) []string {
	var out []string
	for i := len(rows) - 1; i >= 0; i-- {
		e := rows[i]
		s := fmt.Sprintf("%d:%s:%s:%s", e.Level, e.To, e.Via, e.Result)
		if e.Reason != "" {
			s += ":" + e.Reason
		}
		out = append(out, s)
	}
	return out
}

// The issue's done-when (#715), end to end: an unacknowledged alarm
// escalates through two levels on two channels (email over STARTTLS with
// AUTH, then SMS through the provider), and an acknowledgement through
// the HMI's route stops the third. Every send is a notify row naming the
// person and the channel, never an address.
func TestEscalationTwoChannelsAckStopsLevelThree(t *testing.T) {
	r := newChannelsRig(t, channeltest.SMTPOptions{}, map[string]oncall.Contact{
		"alice": {Email: "alice@example.com"},
		"bob":   {SMS: "+15550101"},
		"carol": {Email: "carol@example.com"},
	})
	r.run(10 * time.Second)
	mails := r.smtp.Mails()
	if len(mails) != 1 || mails[0].To[0] != "alice@example.com" || !mails[0].TLS || !mails[0].Authed ||
		!strings.Contains(mails[0].Data, "Zone 15 intrusion") {
		t.Fatalf("level 1: mails %+v, want one to alice over TLS after AUTH", mails)
	}
	if n := len(r.sms.Requests()); n != 0 {
		t.Fatalf("level 2 sent early: %d SMS", n)
	}

	r.run(10 * time.Minute)
	reqs := r.sms.Requests()
	if len(reqs) != 1 || reqs[0].To != "+15550101" || reqs[0].From != "+15550100" ||
		reqs[0].Account != "AC0001" || reqs[0].Token != "sms-token-1" || reqs[0].Kind != "sms" {
		t.Fatalf("level 2: SMS %+v, want one to bob's number with the token", reqs)
	}

	if rec := r.api(t, "POST", "/api/alarms/ack", `{"ids":["intrusion"],"by":"alice"}`); rec.Code != http.StatusOK {
		t.Fatalf("ack = %d %s", rec.Code, rec.Body)
	}
	r.run(30 * time.Minute)
	if mails := r.smtp.Mails(); len(mails) != 1 {
		t.Fatalf("the ack did not stop level 3: %d mails", len(mails))
	}
	got := notifySummary(notifyRows(t, r.a.Engine))
	want := []string{"1:alice:email:sent", "2:bob:sms:sent"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("notify rows %v, want %v", got, want)
	}
}

// The done-when with ack links (#715 PR 3b-1): each page carries a link;
// bob opens the one in his SMS (the page changes nothing), presses
// Acknowledge, and level 3 never goes. The alarm journal names bob, not
// a typed name. Without links the pages say to use the HMI.
func TestEscalationAckByLinkStopsLevelThree(t *testing.T) {
	r := newChannelsRigLinks(t, channeltest.SMTPOptions{}, map[string]oncall.Contact{
		"alice": {Email: "alice@example.com"},
		"bob":   {SMS: "+15550101"},
		"carol": {Email: "carol@example.com"},
	}, true)
	r.run(10*time.Minute + 10*time.Second)
	mails, reqs := r.smtp.Mails(), r.sms.Requests()
	if len(mails) != 1 || len(reqs) != 1 {
		t.Fatalf("%d mails, %d SMS", len(mails), len(reqs))
	}
	if !strings.Contains(strings.ReplaceAll(mails[0].Data, "=\r\n", ""), "https://hmi.example"+acklink.Path) {
		t.Fatalf("alice's mail carries no link:\n%s", mails[0].Data)
	}
	body := reqs[0].Body
	i := strings.Index(body, "https://hmi.example"+acklink.Path)
	if i < 0 {
		t.Fatalf("bob's SMS carries no link: %s", body)
	}
	path := strings.TrimPrefix(strings.Fields(body[i:])[0], "https://hmi.example")
	if rec := r.api(t, "GET", path, ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<form") {
		t.Fatalf("confirm page: %d %s", rec.Code, rec.Body)
	}
	if rec := r.api(t, "POST", path, ""); rec.Code != http.StatusOK {
		t.Fatalf("ack by link = %d %s", rec.Code, rec.Body)
	}
	if rec, _ := r.a.Engine.Lookup("intrusion"); rec.AckBy != "bob" {
		t.Fatalf("acknowledged by %q, want bob", rec.AckBy)
	}
	r.run(30 * time.Minute)
	if mails := r.smtp.Mails(); len(mails) != 1 {
		t.Fatalf("the link's ack did not stop level 3: %d mails", len(mails))
	}
	got := notifySummary(notifyRows(t, r.a.Engine))
	if want := []string{"1:alice:email:sent", "2:bob:sms:sent"}; strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("notify rows %v, want %v", got, want)
	}
	// alice's link, for the same occurrence, is still unspent but the
	// alarm waits on nobody now: it acknowledges nothing.
	am := strings.ReplaceAll(mails[0].Data, "=\r\n", "")
	j := strings.Index(am, acklink.Path)
	apath := strings.Fields(am[j:])[0]
	if rec := r.api(t, "POST", apath, ""); rec.Code != http.StatusConflict {
		t.Fatalf("alice's link after bob's ack: %d %s", rec.Code, rec.Body)
	}
}

// A person a level pages with no address for the channel is a failed
// send, journalled no-contact; a mail server's refusal is journalled by
// class and code. Neither row, nor the journal API, carries an address or
// the server's text.
func TestEscalationFailuresNeverCarryAnAddress(t *testing.T) {
	r := newChannelsRig(t, channeltest.SMTPOptions{RcptCode: 550}, map[string]oncall.Contact{
		"alice": {Email: "alice@example.com"},
		"bob":   {SMS: "+15550101"},
		"carol": {Email: "carol@example.com"},
	})
	r.run(31 * time.Minute)
	got := strings.Join(notifySummary(notifyRows(t, r.a.Engine)), " ")
	for _, want := range []string{"1:alice:email:failed:rejected (smtp 550)", "2:bob:sms:sent",
		"3:carol:email:failed:rejected (smtp 550)", "3:dave:email:failed:no-contact"} {
		if !strings.Contains(got, want) {
			t.Errorf("notify rows %s\nwant %s", got, want)
		}
	}
	rec := r.api(t, "GET", "/api/alarms/journal?kind=notify&from=0&to=4102444800000", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "no-contact") {
		t.Fatalf("journal API = %d %s", rec.Code, rec.Body)
	}
	all := got + rec.Body.String()
	for _, leak := range []string{"@example.com", "+1555", "1555", "mailbox"} {
		if strings.Contains(all, leak) {
			t.Errorf("%q reached the alarm journal:\n%s", leak, all)
		}
	}
}

// A manifest whose channels page people needs the contacts and secrets
// files: built without them (no state directory), NewAlarms refuses and
// names the fix.
func TestChannelsNeedADirectory(t *testing.T) {
	proj, err := Load(alarmFS(alarmTags+`
alarms:
  defs:
    - { id: intrusion, tag: RTU9_ZONE15_INT_001_YA, area: zone15, priority: high }
  channels:
    sms: { provider: "https://sms.example.com", account: AC0001, from: "+15550100" }
  escalation:
    - id: z
      match: { area: zone15 }
      levels:
        - { after: 0s, notify: [alice], via: [sms] }
`, nil), "")
	if err != nil {
		t.Fatal(err)
	}
	_, rt := loadAlarmsWith(t, proj)
	if _, err := proj.NewAlarms(rt); err == nil || !strings.Contains(err.Error(), "auth.state-dir") {
		t.Fatalf("err = %v", err)
	}
}
