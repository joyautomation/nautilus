// Package channeltest holds fakes for alarm/channel: a real TCP SMTP
// server, a Twilio-compatible provider, a webhook receiver and a map
// Directory. Every one is TLS with a self-signed certificate, written as
// a PEM file for a channel's ca-file, so a test keeps verification on.
package channeltest

import "errors"

// Dir is a map-backed channel.Directory.
type Dir struct {
	// Addrs is principal -> kind -> address.
	Addrs map[string]map[string]string
	// Secrets is key -> value.
	Secrets map[string]string
	// Err, when set, makes every lookup fail.
	Err error
}

func (d Dir) Address(principal, kind string) (string, error) {
	if d.Err != nil {
		return "", d.Err
	}
	return d.Addrs[principal][kind], nil
}

func (d Dir) Secret(key string) (string, error) {
	if d.Err != nil {
		return "", d.Err
	}
	v, ok := d.Secrets[key]
	if !ok {
		return "", errors.New("no such secret")
	}
	return v, nil
}
