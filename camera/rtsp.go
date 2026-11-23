//go:build !js

package camera

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// An RTSP (RFC 2326) client for one H.264 video track over TCP: the camera's RTP
// comes interleaved on the control connection (so a firewall needs one port and
// the controller opens no UDP). Everything a camera sends is untrusted: every
// line, header count, body and frame length is bounded, every read has a
// deadline, and no error leaves this file carrying the camera's address, its URL
// or a credential.

const (
	rtspMaxLine     = 8 << 10
	rtspMaxHeaders  = 64
	rtspMaxBody     = 64 << 10
	rtspDialTimeout = dialTimeout
	rtspReqTimeout  = 10 * time.Second
	rtspUserAgent   = "nautilus"
)

type rtspResp struct {
	Code   int
	Status string
	Header map[string]string // keys lower-cased; a repeated header keeps the first, except www-authenticate (joined)
	Auth   []string          // every WWW-Authenticate header
	Body   []byte
}

// rtspFrame is an interleaved binary frame: '$', channel, 16-bit length, payload.
type rtspFrame struct {
	Channel byte
	Payload []byte
}

// readRTSP reads the next item from a control connection: an interleaved frame, or
// a response. It is what a fuzzer is pointed at.
func readRTSP(r *bufio.Reader) (*rtspFrame, *rtspResp, error) {
	b, err := r.Peek(1)
	if err != nil {
		return nil, nil, err
	}
	if b[0] == '$' {
		var h [4]byte
		if _, err := io.ReadFull(r, h[:]); err != nil {
			return nil, nil, err
		}
		n := int(h[2])<<8 | int(h[3])
		p := make([]byte, n)
		if _, err := io.ReadFull(r, p); err != nil {
			return nil, nil, err
		}
		return &rtspFrame{Channel: h[1], Payload: p}, nil, nil
	}
	line, err := readLine(r)
	if err != nil {
		return nil, nil, err
	}
	f := strings.Fields(line)
	if len(f) < 2 || !strings.HasPrefix(f[0], "RTSP/") {
		return nil, nil, errors.New("rtsp: the camera did not answer in RTSP")
	}
	code, err := strconv.Atoi(f[1])
	if err != nil || code < 100 || code > 599 {
		return nil, nil, errors.New("rtsp: the camera's status line is not one")
	}
	resp := &rtspResp{Code: code, Status: strings.Join(f[2:], " "), Header: map[string]string{}}
	for i := 0; ; i++ {
		if i > rtspMaxHeaders {
			return nil, nil, errors.New("rtsp: the camera sent far too many headers")
		}
		line, err := readLine(r)
		if err != nil {
			return nil, nil, err
		}
		if line == "" {
			break
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			return nil, nil, errors.New("rtsp: a header has no colon")
		}
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		if k == "www-authenticate" {
			resp.Auth = append(resp.Auth, v)
		}
		if _, dup := resp.Header[k]; !dup {
			resp.Header[k] = v
		}
	}
	if cl := resp.Header["content-length"]; cl != "" {
		n, err := strconv.Atoi(cl)
		if err != nil || n < 0 || n > rtspMaxBody {
			return nil, nil, errors.New("rtsp: the camera's body is larger than the limit")
		}
		resp.Body = make([]byte, n)
		if _, err := io.ReadFull(r, resp.Body); err != nil {
			return nil, nil, err
		}
	}
	return nil, resp, nil
}

func readLine(r *bufio.Reader) (string, error) {
	var b []byte
	for {
		c, err := r.ReadByte()
		if err != nil {
			return "", err
		}
		if c == '\n' {
			break
		}
		if len(b) >= rtspMaxLine {
			return "", errors.New("rtsp: a line is longer than the limit")
		}
		b = append(b, c)
	}
	return strings.TrimRight(string(b), "\r"), nil
}

// rtspClient is one session.
type rtspClient struct {
	conn      net.Conn
	r         *bufio.Reader
	base      string // the request URL, without credentials
	host      string // for the Host-less error scrub only
	user      string
	pass      string
	cseq      int
	session   string
	timeout   time.Duration
	authz     func(method, uri string) string
	sawDigest bool
	rtpCh     byte
	// frames that arrive while a response is awaited (after PLAY) are kept for run
	pending []*rtspFrame
}

// scrubRTSP turns a transport or protocol error into words that name neither the
// camera's address nor its URL.
func scrubRTSP(err error) error {
	if err == nil {
		return nil
	}
	if strings.HasPrefix(err.Error(), "rtsp:") || strings.HasPrefix(err.Error(), "sdp:") {
		return err
	}
	return scrub(err, "")
}

func dialRTSP(ctx context.Context, raw, user, pass string) (*rtspClient, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "rtsp" && u.Scheme != "rtsps") {
		return nil, errors.New("rtsp: the camera's URL is not an rtsp:// or rtsps:// URL")
	}
	port := u.Port()
	if port == "" {
		port = "554"
		if u.Scheme == "rtsps" {
			port = "322"
		}
	}
	addr := net.JoinHostPort(u.Hostname(), port)
	d := &net.Dialer{Timeout: rtspDialTimeout}
	var conn net.Conn
	if u.Scheme == "rtsps" {
		conn, err = (&tls.Dialer{NetDialer: d, Config: &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12}}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, scrubRTSP(err)
	}
	u.User = nil
	c := &rtspClient{conn: conn, r: bufio.NewReaderSize(conn, 64<<10), base: u.String(), host: addr, user: user, pass: pass, timeout: 60 * time.Second}
	return c, nil
}

func (c *rtspClient) close() { c.conn.Close() }

// do sends a request and reads its response, answering a 401 once in the scheme the
// camera asked for (digest preferred; never basic after digest was asked). Frames
// that arrive meanwhile are kept.
func (c *rtspClient) do(ctx context.Context, method, uri string, hdr map[string]string) (*rtspResp, error) {
	for attempt := 0; attempt < 2; attempt++ {
		c.cseq++
		var b strings.Builder
		fmt.Fprintf(&b, "%s %s RTSP/1.0\r\nCSeq: %d\r\nUser-Agent: %s\r\n", method, uri, c.cseq, rtspUserAgent)
		if c.session != "" {
			fmt.Fprintf(&b, "Session: %s\r\n", c.session)
		}
		if c.authz != nil {
			fmt.Fprintf(&b, "Authorization: %s\r\n", c.authz(method, uri))
		}
		for k, v := range hdr {
			if strings.ContainsAny(k+v, "\r\n") {
				return nil, errors.New("rtsp: a header holds a line break")
			}
			fmt.Fprintf(&b, "%s: %s\r\n", k, v)
		}
		b.WriteString("\r\n")
		dl := time.Now().Add(rtspReqTimeout)
		if d, ok := ctx.Deadline(); ok && d.Before(dl) {
			dl = d
		}
		c.conn.SetDeadline(dl)
		if _, err := io.WriteString(c.conn, b.String()); err != nil {
			return nil, scrubRTSP(err)
		}
		var resp *rtspResp
		for resp == nil {
			fr, rp, err := readRTSP(c.r)
			if err != nil {
				return nil, scrubRTSP(err)
			}
			if fr != nil {
				if len(c.pending) < preplayFrames {
					c.pending = append(c.pending, fr)
				}
				continue
			}
			resp = rp
		}
		if resp.Code != 401 || c.user == "" || attempt == 1 {
			return resp, nil
		}
		// the camera asks: answer in digest if it offers it, else basic; but a
		// camera that asked for digest once is never sent the password in basic
		dig, basicOK := pickChallenge(resp.Auth)
		switch {
		case dig != "":
			if _, err := digest(dig, c.user, c.pass, method, uri); err != nil {
				return nil, err
			}
			ch := dig
			c.sawDigest = true
			c.authz = func(m, u string) string { a, _ := digest(ch, c.user, c.pass, m, u); return a }
		case c.sawDigest:
			return nil, errors.New("rtsp: the camera asked for digest before and now asks for basic: not sending the password")
		case basicOK:
			a := basic(c.user, c.pass)
			c.authz = func(string, string) string { return a }
		default:
			return resp, nil
		}
	}
	return nil, errors.New("rtsp: the camera refused the controller's credentials")
}

func statusErr(what string, resp *rtspResp) error {
	switch resp.Code {
	case 401, 403:
		return errors.New("rtsp: the camera refused the controller's credentials")
	case 404:
		return errors.New("rtsp: the camera has no such stream (check the URL path)")
	}
	return fmt.Errorf("rtsp: the camera answered %s with %d", what, resp.Code)
}

// describe asks for the session description and returns its H.264 track.
func (c *rtspClient) describe(ctx context.Context) (Media, error) {
	resp, err := c.do(ctx, "DESCRIBE", c.base, map[string]string{"Accept": "application/sdp"})
	if err != nil {
		return Media{}, err
	}
	if resp.Code != 200 {
		return Media{}, statusErr("DESCRIBE", resp)
	}
	// the camera's Content-Base is taken only if it is the camera's own address: a
	// base on another host would have every later request, with its credentials,
	// name that host
	if b := resp.Header["content-base"]; b != "" {
		if u, err := url.Parse(b); err == nil && u.Host != "" {
			if o, err := url.Parse(c.base); err == nil {
				// the camera's path on the address the controller dialled: a camera that says
				// its own IP when it was dialled by name (or by host:554) is taken at its
				// path, never at its host
				o.Path, o.RawQuery = u.Path, u.RawQuery
				if o.Path == "" {
					o.Path = "/"
				}
				c.base = o.String()
			}
		}
	}
	return ParseSDP(string(resp.Body))
}

// trackURL is the SETUP URL of a track's control attribute.
func (c *rtspClient) trackURL(control string) string {
	switch {
	case control == "" || control == "*":
		return c.base
	case strings.HasPrefix(strings.ToLower(control), "rtsp://") || strings.HasPrefix(strings.ToLower(control), "rtsps://"):
		// an absolute control URL stays on the camera's own address: one naming
		// another host is not followed
		if u, err := url.Parse(control); err == nil {
			if b, err := url.Parse(c.base); err == nil && u.Host == b.Host {
				u.User = nil
				return u.String()
			}
		}
		return c.base
	}
	// a relative control is a path: anything that could break the request line
	// (whitespace, control characters) means the base is used instead
	for _, r := range control {
		if r <= ' ' || r == 0x7f {
			return c.base
		}
	}
	base := c.base
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	return base + strings.TrimPrefix(control, "/")
}

// setup asks for the track interleaved on the control connection.
func (c *rtspClient) setup(ctx context.Context, m Media) error {
	resp, err := c.do(ctx, "SETUP", c.trackURL(m.Control), map[string]string{"Transport": "RTP/AVP/TCP;unicast;interleaved=0-1"})
	if err != nil {
		return err
	}
	if resp.Code != 200 {
		return statusErr("SETUP", resp)
	}
	if s := resp.Header["session"]; s != "" {
		sess, params, _ := strings.Cut(s, ";")
		if len(sess) > 128 || strings.ContainsAny(sess, " \r\n") {
			return errors.New("rtsp: the session id is not one")
		}
		c.session = sess
		for _, p := range strings.Split(params, ";") {
			if k, v, ok := strings.Cut(strings.TrimSpace(p), "="); ok && strings.EqualFold(k, "timeout") {
				if n, err := strconv.Atoi(v); err == nil && n >= 5 && n <= 3600 {
					c.timeout = time.Duration(n) * time.Second
				}
			}
		}
	} else {
		return errors.New("rtsp: the camera gave no session")
	}
	c.rtpCh = 0
	if t := resp.Header["transport"]; t != "" {
		if !strings.Contains(strings.ToUpper(t), "TCP") {
			return errors.New("rtsp: the camera will not send over the control connection (TCP)")
		}
		for _, p := range strings.Split(t, ";") {
			if k, v, ok := strings.Cut(strings.TrimSpace(p), "="); ok && strings.EqualFold(k, "interleaved") {
				lo, _, _ := strings.Cut(v, "-")
				if n, err := strconv.Atoi(lo); err == nil && n >= 0 && n < 256 {
					c.rtpCh = byte(n)
				}
			}
		}
	}
	return nil
}

func (c *rtspClient) play(ctx context.Context) error {
	resp, err := c.do(ctx, "PLAY", c.base, map[string]string{"Range": "npt=0.000-"})
	if err != nil {
		return err
	}
	if resp.Code != 200 {
		return statusErr("PLAY", resp)
	}
	return nil
}

// run reads the interleaved RTP until ctx ends or the camera stops, calling onRTP
// with each packet of the video channel. A camera that sends nothing for idle ends
// it; the session is kept alive with OPTIONS every half timeout.
func (c *rtspClient) run(ctx context.Context, idle time.Duration, onRTP func(RTPPacket) error) error {
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			c.conn.SetReadDeadline(time.Now()) // unblock the read
		case <-stop:
		}
	}()
	keep := time.Now().Add(c.timeout / 2)
	handle := func(fr *rtspFrame) error {
		if fr.Channel != c.rtpCh {
			return nil // RTCP, or a channel not asked for
		}
		p, err := ParseRTP(fr.Payload)
		if err != nil {
			return nil // one bad packet is a loss, not the end
		}
		return onRTP(p)
	}
	for _, fr := range c.pending {
		if err := handle(fr); err != nil {
			return err
		}
	}
	c.pending = nil
	for {
		if ctx.Err() != nil {
			return nil
		}
		c.conn.SetReadDeadline(time.Now().Add(idle))
		fr, resp, err := readRTSP(c.r)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				return errors.New("the camera stopped sending")
			}
			if errors.Is(err, io.EOF) {
				return errors.New("the camera closed the connection")
			}
			return scrubRTSP(err)
		}
		if resp != nil {
			continue // an answer to a keepalive
		}
		if err := handle(fr); err != nil {
			return err
		}
		if time.Now().After(keep) {
			keep = time.Now().Add(c.timeout / 2)
			c.cseq++
			c.conn.SetWriteDeadline(time.Now().Add(rtspReqTimeout))
			req := fmt.Sprintf("OPTIONS %s RTSP/1.0\r\nCSeq: %d\r\nSession: %s\r\nUser-Agent: %s\r\n", c.base, c.cseq, c.session, rtspUserAgent)
			if c.authz != nil {
				req += "Authorization: " + c.authz("OPTIONS", c.base) + "\r\n"
			}
			if _, err := io.WriteString(c.conn, req+"\r\n"); err != nil {
				return scrubRTSP(err)
			}
		}
	}
}

// teardown is best effort: the camera frees the session sooner.
func (c *rtspClient) teardown() {
	c.cseq++
	c.conn.SetWriteDeadline(time.Now().Add(time.Second))
	req := fmt.Sprintf("TEARDOWN %s RTSP/1.0\r\nCSeq: %d\r\nSession: %s\r\nUser-Agent: %s\r\n", c.base, c.cseq, c.session, rtspUserAgent)
	if c.authz != nil {
		req += "Authorization: " + c.authz("TEARDOWN", c.base) + "\r\n"
	}
	io.WriteString(c.conn, req+"\r\n")
}
