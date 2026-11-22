package escalate

import (
	"context"
	"errors"
	"fmt"
	"net"
)

// Failure classes: what a notify row's reason says about a failed send.
// A channel returns a *SendError carrying one; the provider's or the mail
// server's own text never reaches a journal row or the log, because it
// may echo an address (an SMTP 550 names the mailbox) or an account (a
// URL error carries the request URL).
const (
	FailNoContact   = "no-contact"     // the person has no address for this channel
	FailConfig      = "config"         // the channel's secret or the contacts file is missing or unreadable
	FailTimeout     = "timeout"        // no answer in time
	FailUnreachable = "unreachable"    // could not connect
	FailTLS         = "tls"            // no TLS where it is required, or a certificate that does not verify
	FailAuth        = "auth"           // the server or provider refused the credentials
	FailRejected    = "rejected"       // the server or provider refused the message (4xx, SMTP 5xx)
	FailProvider    = "provider-error" // the provider failed (5xx, SMTP 4xx)
	FailUnknown     = "error"          // anything else
)

// SendError is a failed send as a Channel reports it: a class and, where
// there is one, the server's numeric code (an HTTP status, an SMTP reply
// code). Nothing else: no address, no provider text.
type SendError struct {
	Class string
	Code  int    // 0 when there is none
	Proto string // "http" or "smtp", naming Code; "" with no code
}

func (e *SendError) Error() string {
	if e.Code != 0 {
		return fmt.Sprintf("%s (%s %d)", e.Class, e.Proto, e.Code)
	}
	return e.Class
}

// Failed is a *SendError of class with no code.
func Failed(class string) *SendError { return &SendError{Class: class} }

// Failure is what a notify row and the log say about err: its SendError's
// text, else a class read from the error's type (a timeout, a refused
// connection), else "error". It never returns err's own text.
func Failure(err error) string {
	if err == nil {
		return ""
	}
	var se *SendError
	if errors.As(err, &se) {
		return se.Error()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return FailTimeout
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return FailTimeout
	}
	var oe *net.OpError
	if errors.As(err, &oe) && oe.Op == "dial" {
		return FailUnreachable
	}
	return FailUnknown
}
