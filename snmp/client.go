// client.go is the only file that knows gosnmp: it opens one session per
// source (one UDP socket, requests strictly sequential — gosnmp's GoSNMP is
// not safe for concurrent use, and the driver never asks it to be), and
// converts between gosnmp's PDUs and the walk package's neutral Varbind.
// Everything above it — the poll planner, the decoder, the tests — speaks
// Varbind through the Getter seam, so unit tests need no socket and gosnmp
// stays a private detail of this package (brief §1: the dependency lives
// inside snmp/, the way paho lives inside sparkplug/).
package snmp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/gosnmp/gosnmp"
	"github.com/joyautomation/nautilus/snmp/walk"
)

// Getter is one session to one agent — the transport seam. Get fetches
// exact OIDs (the answer for an OID the agent does not hold is a varbind of
// type NoSuchObject/NoSuchInstance, not an error); GetBulk returns up to
// maxRep varbinds lexicographically after oid; Set writes one varbind. An
// error is a TRANSPORT failure (timeout, refused, authentication) or a
// *StatusError when the agent answered with a non-zero error-status.
type Getter interface {
	Get(ctx context.Context, oids []string) ([]walk.Varbind, error)
	GetBulk(ctx context.Context, oid string, maxRep int) ([]walk.Varbind, error)
	Set(ctx context.Context, vb walk.Varbind) error
	Close() error
}

// DialFunc opens a session to a source. The default is Dial; tests hand the
// driver an in-memory walk or an agent on 127.0.0.1:0 through WithDialer.
type DialFunc func(ctx context.Context, s Source) (Getter, error)

// StatusError is an answered request the agent refused: error-status and
// the 1-based index of the varbind it blames (RFC 3416 §4.2).
type StatusError struct {
	Status int
	Index  int
	OID    string
}

// SNMP error-status names (RFC 3416), for legible errors.
var statusNames = []string{"noError", "tooBig", "noSuchName", "badValue", "readOnly", "genErr",
	"noAccess", "wrongType", "wrongLength", "wrongEncoding", "wrongValue", "noCreation",
	"inconsistentValue", "resourceUnavailable", "commitFailed", "undoFailed",
	"authorizationError", "notWritable", "inconsistentName"}

// StatusName names an error-status code.
func StatusName(code int) string {
	if code >= 0 && code < len(statusNames) {
		return statusNames[code]
	}
	return "status" + strconv.Itoa(code)
}

func (e *StatusError) Error() string {
	if e.OID != "" {
		return fmt.Sprintf("agent answered %s(%d) for %s", StatusName(e.Status), e.Status, e.OID)
	}
	return fmt.Sprintf("agent answered %s(%d)", StatusName(e.Status), e.Status)
}

// Dial opens a gosnmp session to s with its credentials read from the
// environment or files named in the manifest. UDP has no handshake, so a
// successful Dial proves only that the address resolved; the first request
// is the real test (and, for v3, performs engine discovery).
func Dial(ctx context.Context, s Source) (Getter, error) {
	host, portStr := s.Host, strconv.Itoa(DefaultPort)
	if h, p, err := net.SplitHostPort(s.Addr()); err == nil {
		host, portStr = h, p
	}
	port, _ := strconv.Atoi(portStr)
	g := &gosnmp.GoSNMP{
		Target:         host,
		Port:           uint16(port),
		Transport:      "udp",
		Timeout:        s.timeout(),
		Retries:        s.retries(),
		MaxOids:        gosnmp.MaxOids,
		MaxRepetitions: uint32(s.maxRepetitions()),
		Context:        ctx,
	}
	switch s.version() {
	case V2c:
		c := s.credentials()
		if len(c) == 0 {
			return nil, fmt.Errorf("source %s: no community configured", s.ID)
		}
		community, err := c[0].resolve(s.ID)
		if err != nil {
			return nil, err
		}
		g.Version = gosnmp.Version2c
		g.Community = community
	case V3:
		usm := &gosnmp.UsmSecurityParameters{UserName: s.User}
		flags := gosnmp.NoAuthNoPriv
		if s.Auth != "" {
			key, err := credential{"auth", s.AuthEnv, s.AuthFile}.resolve(s.ID)
			if err != nil {
				return nil, err
			}
			usm.AuthenticationPassphrase = key
			usm.AuthenticationProtocol = authProtocol(s.Auth)
			flags = gosnmp.AuthNoPriv
		}
		if s.Priv != "" {
			key, err := credential{"priv", s.PrivEnv, s.PrivFile}.resolve(s.ID)
			if err != nil {
				return nil, err
			}
			usm.PrivacyPassphrase = key
			usm.PrivacyProtocol = privProtocol(s.Priv)
			flags = gosnmp.AuthPriv
		}
		g.Version = gosnmp.Version3
		g.SecurityModel = gosnmp.UserSecurityModel
		g.MsgFlags = flags
		g.SecurityParameters = usm
		g.ContextName = s.Context
	default:
		return nil, fmt.Errorf("source %s: version %q", s.ID, s.Version)
	}
	if err := g.Connect(); err != nil {
		return nil, fmt.Errorf("source %s: %s: %w", s.ID, s.Addr(), err)
	}
	return &session{g: g, src: s}, nil
}

func authProtocol(a string) gosnmp.SnmpV3AuthProtocol {
	switch a {
	case AuthSHA1:
		return gosnmp.SHA
	case AuthSHA256:
		return gosnmp.SHA256
	case AuthMD5:
		return gosnmp.MD5
	}
	return gosnmp.NoAuth
}

func privProtocol(p string) gosnmp.SnmpV3PrivProtocol {
	switch p {
	case PrivAES128:
		return gosnmp.AES
	case PrivAES256:
		return gosnmp.AES256
	case PrivAES256C:
		return gosnmp.AES256C
	case PrivDES:
		return gosnmp.DES
	}
	return gosnmp.NoPriv
}

// session is the gosnmp-backed Getter.
type session struct {
	g   *gosnmp.GoSNMP
	src Source
}

func (s *session) Close() error {
	if s.g.Conn == nil {
		return nil
	}
	return s.g.Conn.Close()
}

func (s *session) Get(ctx context.Context, oids []string) ([]walk.Varbind, error) {
	var out []walk.Varbind
	for start := 0; start < len(oids); start += s.g.MaxOids {
		end := min(start+s.g.MaxOids, len(oids))
		s.g.Context = ctx
		pkt, err := s.g.Get(dotted(oids[start:end]))
		if err != nil {
			return nil, s.explain(ctx, err)
		}
		if pkt.Error != gosnmp.NoError {
			return nil, statusError(pkt, oids[start:end])
		}
		for _, v := range pkt.Variables {
			vb, err := fromPDU(v)
			if err != nil {
				return nil, err
			}
			out = append(out, vb)
		}
	}
	return out, nil
}

func (s *session) GetBulk(ctx context.Context, oid string, maxRep int) ([]walk.Varbind, error) {
	s.g.Context = ctx
	pkt, err := s.g.GetBulk([]string{"." + oid}, 0, uint32(maxRep))
	if err != nil {
		return nil, s.explain(ctx, err)
	}
	if pkt.Error != gosnmp.NoError {
		return nil, statusError(pkt, []string{oid})
	}
	out := make([]walk.Varbind, 0, len(pkt.Variables))
	for _, v := range pkt.Variables {
		vb, err := fromPDU(v)
		if err != nil {
			return nil, err
		}
		out = append(out, vb)
	}
	return out, nil
}

func (s *session) Set(ctx context.Context, vb walk.Varbind) error {
	pdu, err := toPDU(vb)
	if err != nil {
		return err
	}
	s.g.Context = ctx
	pkt, err := s.g.Set([]gosnmp.SnmpPDU{pdu})
	if err != nil {
		return s.explain(ctx, err)
	}
	if pkt.Error != gosnmp.NoError {
		return statusError(pkt, []string{vb.OID})
	}
	return nil
}

// explain turns gosnmp's transport errors into what a commissioning tech
// needs: a v2c agent silently DROPS a request with the wrong community, so a
// timeout on a source that has never answered is most often exactly that,
// and a v3 agent answers a Report that gosnmp maps to a named error.
func (s *session) explain(ctx context.Context, err error) error {
	if strings.Contains(err.Error(), "not authentic") && s.src.version() == V3 {
		return s.diagnoseV3(ctx, err)
	}
	switch {
	case errors.Is(err, gosnmp.ErrWrongDigest):
		return fmt.Errorf("%s: authentication failed — wrong auth pass phrase or protocol for user %q (usmStatsWrongDigests)", s.src.Addr(), s.src.User)
	case errors.Is(err, gosnmp.ErrUnknownUsername):
		return fmt.Errorf("%s: the agent does not know user %q (usmStatsUnknownUserNames)", s.src.Addr(), s.src.User)
	case errors.Is(err, gosnmp.ErrDecryption):
		return fmt.Errorf("%s: the agent could not decrypt the request — wrong priv pass phrase or protocol for user %q (usmStatsDecryptionErrors)", s.src.Addr(), s.src.User)
	case errors.Is(err, gosnmp.ErrUnknownSecurityLevel):
		return fmt.Errorf("%s: the agent refuses this security level for user %q — check auth/priv against the device (usmStatsUnsupportedSecLevels)", s.src.Addr(), s.src.User)
	case errors.Is(err, gosnmp.ErrNotInTimeWindow):
		return fmt.Errorf("%s: v3 time window rejected (usmStatsNotInTimeWindows)", s.src.Addr())
	case strings.Contains(err.Error(), "timeout"):
		hint := "wrong community, SNMP disabled, or an ACL dropping this host"
		if s.src.version() == V3 {
			hint = "SNMP disabled, an ACL dropping this host, or a v3 user the agent silently ignores"
		}
		return fmt.Errorf("%s: no answer within %s × %d tries — %s? (%w)", s.src.Addr(), s.src.timeout(), s.src.retries()+1, hint, err)
	case strings.Contains(err.Error(), "connection refused"):
		return fmt.Errorf("%s: port unreachable — nothing is listening for SNMP there (%w)", s.src.Addr(), err)
	}
	return fmt.Errorf("%s: %w", s.src.Addr(), err)
}

// diagnoseV3 names the cause when gosnmp discards the agent's answer as
// "not authentic". That answer is the agent's usmStats Report — wrong
// digest, unknown user, decryption error — which RFC 3414 has the agent
// send at noAuthNoPriv because it could not authenticate US; gosnmp
// v1.45.0 checks it against the REQUEST's security level, fails, and
// discards it before reading which report it was, so its own
// ErrWrongDigest / ErrUnknownUsername never fire against a standards-
// following agent (the snmpsim foreign test caught this). Two cheap probes
// recover the distinction: a noAuthNoPriv request answers "unknown user" by
// name, and an authNoPriv request with the configured auth key tells a
// wrong auth key (its answer fails authentication too) from a wrong priv
// key (it authenticates). Each probe is one round trip on its own socket.
func (s *session) diagnoseV3(ctx context.Context, cause error) error {
	usm, _ := s.g.SecurityParameters.(*gosnmp.UsmSecurityParameters)
	addr, user := s.src.Addr(), s.src.User
	if usm == nil {
		return fmt.Errorf("%s: %w", addr, cause)
	}
	probe := func(flags gosnmp.SnmpV3MsgFlags) error {
		p := &gosnmp.UsmSecurityParameters{UserName: usm.UserName}
		if flags&gosnmp.AuthNoPriv != 0 {
			p.AuthenticationProtocol = usm.AuthenticationProtocol
			p.AuthenticationPassphrase = usm.AuthenticationPassphrase
		}
		g := &gosnmp.GoSNMP{
			Target: s.g.Target, Port: s.g.Port, Transport: "udp", Version: gosnmp.Version3,
			Timeout: s.g.Timeout, Retries: 0, MaxOids: gosnmp.MaxOids, Context: ctx,
			SecurityModel: gosnmp.UserSecurityModel, MsgFlags: flags, SecurityParameters: p,
			ContextName: s.g.ContextName,
		}
		if err := g.Connect(); err != nil {
			return err
		}
		defer g.Conn.Close()
		_, err := g.Get([]string{".1.3.6.1.2.1.1.3.0"})
		return err
	}
	if err := probe(gosnmp.NoAuthNoPriv); errors.Is(err, gosnmp.ErrUnknownUsername) {
		return fmt.Errorf("%s: the agent does not know user %q (usmStatsUnknownUserNames)", addr, user)
	}
	if s.src.Auth == "" {
		return fmt.Errorf("%s: the agent's answer failed authentication for user %q (%w)", addr, user, cause)
	}
	err := probe(gosnmp.AuthNoPriv)
	switch {
	case err != nil && (strings.Contains(err.Error(), "not authentic") || errors.Is(err, gosnmp.ErrWrongDigest)):
		return fmt.Errorf("%s: authentication failed — wrong auth pass phrase or protocol (%s) for user %q", addr, s.src.Auth, user)
	case s.src.Priv != "" && (err == nil || errors.Is(err, gosnmp.ErrUnknownSecurityLevel)):
		return fmt.Errorf("%s: authentication succeeded but privacy failed — wrong priv pass phrase or protocol (%s) for user %q", addr, s.src.Priv, user)
	}
	return fmt.Errorf("%s: the agent's answer failed authentication for user %q — check auth/priv pass phrases and protocols (%w)", addr, user, cause)
}

func statusError(pkt *gosnmp.SnmpPacket, oids []string) error {
	e := &StatusError{Status: int(pkt.Error), Index: int(pkt.ErrorIndex)}
	if e.Index >= 1 && e.Index <= len(oids) {
		e.OID = oids[e.Index-1]
	}
	return e
}

func dotted(oids []string) []string {
	out := make([]string, len(oids))
	for i, o := range oids {
		out[i] = "." + o
	}
	return out
}

// fromPDU converts gosnmp's decoded varbind to a walk.Varbind. gosnmp's Go
// types per ASN.1 type are: Integer int, Counter32/Gauge32 uint,
// TimeTicks uint32, Counter64 uint64, OctetString []byte, ObjectIdentifier
// and IPAddress string.
func fromPDU(p gosnmp.SnmpPDU) (walk.Varbind, error) {
	vb := walk.Varbind{OID: strings.TrimPrefix(p.Name, ".")}
	switch p.Type {
	case gosnmp.Integer:
		vb.Type = walk.Integer
		vb.Int = gosnmp.ToBigInt(p.Value).Int64()
	case gosnmp.Counter32, gosnmp.Gauge32, gosnmp.TimeTicks, gosnmp.Counter64, gosnmp.Uinteger32:
		vb.Type = map[gosnmp.Asn1BER]walk.Type{
			gosnmp.Counter32: walk.Counter32, gosnmp.Gauge32: walk.Gauge32, gosnmp.Uinteger32: walk.Gauge32,
			gosnmp.TimeTicks: walk.TimeTicks, gosnmp.Counter64: walk.Counter64,
		}[p.Type]
		vb.Uint = gosnmp.ToBigInt(p.Value).Uint64()
	case gosnmp.OctetString, gosnmp.BitString, gosnmp.Opaque:
		vb.Type = walk.OctetString
		if p.Type == gosnmp.Opaque {
			vb.Type = walk.Opaque
		}
		b, _ := p.Value.([]byte)
		vb.Bytes = append([]byte{}, b...)
	case gosnmp.ObjectIdentifier:
		vb.Type = walk.ObjectID
		s, _ := p.Value.(string)
		vb.Str = strings.TrimPrefix(s, ".")
	case gosnmp.IPAddress:
		vb.Type = walk.IPAddress
		vb.Str, _ = p.Value.(string)
	case gosnmp.Null:
		vb.Type = walk.Null
	case gosnmp.NoSuchObject:
		vb.Type = walk.NoSuchObject
	case gosnmp.NoSuchInstance:
		vb.Type = walk.NoSuchInstance
	case gosnmp.EndOfMibView:
		vb.Type = walk.EndOfMibView
	default:
		return vb, fmt.Errorf("%s: unsupported ASN.1 type %s", vb.OID, p.Type)
	}
	return vb, nil
}

// toPDU is fromPDU's inverse, for Set and for the in-repo agent's
// responses (which marshal through gosnmp too, so the agent and the client
// are two ends of one well-tested encoder).
func toPDU(v walk.Varbind) (gosnmp.SnmpPDU, error) {
	p := gosnmp.SnmpPDU{Name: "." + v.OID}
	switch v.Type {
	case walk.Integer:
		p.Type, p.Value = gosnmp.Integer, int(v.Int)
	case walk.Counter32:
		p.Type, p.Value = gosnmp.Counter32, uint32(v.Uint)
	case walk.Gauge32:
		p.Type, p.Value = gosnmp.Gauge32, uint32(v.Uint)
	case walk.TimeTicks:
		p.Type, p.Value = gosnmp.TimeTicks, uint32(v.Uint)
	case walk.Counter64:
		p.Type, p.Value = gosnmp.Counter64, v.Uint
	case walk.OctetString:
		p.Type, p.Value = gosnmp.OctetString, v.Bytes
	case walk.Opaque:
		p.Type, p.Value = gosnmp.Opaque, v.Bytes
	case walk.ObjectID:
		p.Type, p.Value = gosnmp.ObjectIdentifier, "."+v.Str
	case walk.IPAddress:
		p.Type, p.Value = gosnmp.IPAddress, v.Str
	case walk.Null:
		p.Type = gosnmp.Null
	case walk.NoSuchObject:
		p.Type = gosnmp.NoSuchObject
	case walk.NoSuchInstance:
		p.Type = gosnmp.NoSuchInstance
	case walk.EndOfMibView:
		p.Type = gosnmp.EndOfMibView
	default:
		return p, fmt.Errorf("%s: cannot encode %s", v.OID, v.Type)
	}
	return p, nil
}

// ToPDU and FromPDU are exported for snmp/agent, the in-repo stand-in,
// which encodes its answers with the same gosnmp marshaller the client
// decodes with. They are the ONLY gosnmp types in the package's API, and
// only snmp/... may import gosnmp.
func ToPDU(v walk.Varbind) (gosnmp.SnmpPDU, error) { return toPDU(v) }

// FromPDU converts a decoded gosnmp varbind.
func FromPDU(p gosnmp.SnmpPDU) (walk.Varbind, error) { return fromPDU(p) }
