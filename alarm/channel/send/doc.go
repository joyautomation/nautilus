// Package send sends escalation pages outside the box (#715 PR 3):
// email over SMTP, SMS and voice through a Twilio-compatible REST
// provider, and a webhook. Each is an escalate.Channel: it turns the
// message's principal into an address through a Directory (the contacts
// file on the box, package alarm/oncall), reads its credential from the
// same Directory (the secrets file beside it, never the manifest, never
// the environment), and sends.
//
// Transport security is on by default: SMTP runs over TLS (STARTTLS or
// implicit) with the certificate verified, and the provider and the
// webhook are https. `insecure: true` on a channel relaxes only two
// things: the certificate is not verified, and a channel that sends no
// credential (email with no username, a webhook with no header) may run
// in plaintext. A password or a token never crosses a connection that is
// not TLS, insecure or not. `ca-file:` names a PEM bundle on the box to
// verify against instead of the system roots: how a site with its own CA
// keeps verification on.
//
// A failed send is an *escalate.SendError: a class and a numeric code,
// never the server's or the provider's text, which may echo an address.
//
// Build turns the manifest's channel.Config into one escalate.Channel per
// kind. cmd/naut registers it with the manifest
// (project.RegisterAlarmSenders); nothing else imports this package, so the
// playground's wasm never links it. It imports only alarm/channel and
// alarm/escalate (docs/design/context-map.md §2).
package send
