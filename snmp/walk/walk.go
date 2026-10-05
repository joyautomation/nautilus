// Package walk is the recorded-device format of the snmp driver: the
// numeric text `snmpwalk -One` prints (".1.3.6.1.2.1.2.2.1.8.1 = INTEGER: 1"),
// parsed into typed varbinds and written back byte for byte.
//
// One format serves four consumers, which is the point of it: `naut snmp
// browse --record` writes it, `naut snmp import --walk` generates a manifest
// from it, the in-repo agent (snmp/agent) and `naut snmp serve` answer from
// it, and the foreign test converts it to snmpsim's .snmprec. A walk taken
// off a real switch with net-snmp's own snmpwalk drops into any of them, so
// "the same bytes from a live device and from its recording" is a property
// of the format, not a promise of the importer.
//
// It is a leaf package — stdlib only — so the agent, the codegen and the
// driver can all share it without an import cycle through snmp itself.
package walk

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Type is an SMIv2 value type as it appears on the wire (and in the walk's
// "TYPE:" column).
type Type uint8

// The value types. The three exception types never appear in a recording;
// they are what an agent answers for an OID it does not hold, and the driver
// maps them to a Bad tag.
const (
	Integer Type = iota + 1
	OctetString
	Null
	ObjectID
	IPAddress
	Counter32
	Gauge32
	TimeTicks
	Counter64
	Opaque
	NoSuchObject
	NoSuchInstance
	EndOfMibView
)

var typeNames = map[Type]string{
	Integer: "INTEGER", OctetString: "STRING", Null: "NULL", ObjectID: "OID",
	IPAddress: "IpAddress", Counter32: "Counter32", Gauge32: "Gauge32",
	TimeTicks: "Timeticks", Counter64: "Counter64", Opaque: "Opaque",
	NoSuchObject: "noSuchObject", NoSuchInstance: "noSuchInstance",
	EndOfMibView: "endOfMibView",
}

func (t Type) String() string {
	if s, ok := typeNames[t]; ok {
		return s
	}
	return fmt.Sprintf("Type(%d)", uint8(t))
}

// IsException reports the three "there is no value here" answers.
func (t Type) IsException() bool {
	return t == NoSuchObject || t == NoSuchInstance || t == EndOfMibView
}

// Varbind is one OID and its value. Exactly one value field is meaningful,
// chosen by Type: Int for Integer; Uint for Counter32, Gauge32, TimeTicks and
// Counter64; Bytes for OctetString and Opaque; Str for ObjectID (dotted, no
// leading dot) and IPAddress.
type Varbind struct {
	OID   string // numeric, dotted, no leading dot: "1.3.6.1.2.1.1.5.0"
	Type  Type
	Int   int64
	Uint  uint64
	Bytes []byte
	Str   string
}

// Walk is a recording: varbinds in OID order, no duplicates.
type Walk []Varbind

// ── OIDs ───────────────────────────────────────────────────────────────

// ParseOID validates a numeric OID and returns it normalised: no leading
// dot, no empty arcs, every arc a decimal uint32. At least two arcs, the
// first 0..2 (X.690 §8.19) — a bound OID that fails here could never be
// encoded, so the manifest refuses it offline.
func ParseOID(s string) (string, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, ".")
	if s == "" {
		return "", fmt.Errorf("empty OID")
	}
	arcs := strings.Split(s, ".")
	if len(arcs) < 2 {
		return "", fmt.Errorf("OID %q: needs at least two arcs", s)
	}
	for i, a := range arcs {
		if a == "" {
			return "", fmt.Errorf("OID %q: empty arc", s)
		}
		if len(a) > 1 && a[0] == '0' {
			return "", fmt.Errorf("OID %q: arc %q has a leading zero", s, a)
		}
		n, err := strconv.ParseUint(a, 10, 32)
		if err != nil {
			return "", fmt.Errorf("OID %q: arc %q is not a number (OIDs are numeric — no MIB names)", s, a)
		}
		if i == 0 && n > 2 {
			return "", fmt.Errorf("OID %q: first arc must be 0, 1 or 2", s)
		}
	}
	return s, nil
}

// Compare orders two normalised OIDs arc by arc, numerically — the order an
// agent walks in ("1.3.6.1.2.1.2.2.1.10" sorts after "…1.2.2.1.9", which a
// string compare gets wrong). -1, 0 or +1.
func Compare(a, b string) int {
	for {
		if a == "" || b == "" {
			switch {
			case a == "" && b == "":
				return 0
			case a == "":
				return -1
			default:
				return 1
			}
		}
		ai, arest := cutArc(a)
		bi, brest := cutArc(b)
		if len(ai) != len(bi) {
			if len(ai) < len(bi) {
				return -1
			}
			return 1
		}
		if ai != bi {
			if ai < bi {
				return -1
			}
			return 1
		}
		a, b = arest, brest
	}
}

func cutArc(s string) (string, string) {
	if i := strings.IndexByte(s, '.'); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

// HasPrefix reports whether oid is prefix itself or lies under it
// ("1.3.6.1.2.1.2.2.1.8.3" is under "1.3.6.1.2.1.2.2.1.8"; "…1.80" is not).
func HasPrefix(oid, prefix string) bool {
	return oid == prefix || strings.HasPrefix(oid, prefix+".")
}

// Parent drops the last arc: the table column of an instanced OID.
func Parent(oid string) string {
	if i := strings.LastIndexByte(oid, '.'); i >= 0 {
		return oid[:i]
	}
	return ""
}

// ── lookup ─────────────────────────────────────────────────────────────

// Sort orders the walk by OID.
func (w Walk) Sort() {
	sort.SliceStable(w, func(i, j int) bool { return Compare(w[i].OID, w[j].OID) < 0 })
}

// Get finds an exact OID (the walk must be sorted).
func (w Walk) Get(oid string) (Varbind, bool) {
	i := sort.Search(len(w), func(i int) bool { return Compare(w[i].OID, oid) >= 0 })
	if i < len(w) && w[i].OID == oid {
		return w[i], true
	}
	return Varbind{}, false
}

// Next finds the first varbind strictly after oid — GetNext.
func (w Walk) Next(oid string) (Varbind, bool) {
	i := sort.Search(len(w), func(i int) bool { return Compare(w[i].OID, oid) > 0 })
	if i < len(w) {
		return w[i], true
	}
	return Varbind{}, false
}

// Subtree returns the varbinds under prefix, in order.
func (w Walk) Subtree(prefix string) Walk {
	i := sort.Search(len(w), func(i int) bool { return Compare(w[i].OID, prefix) >= 0 })
	j := i
	for j < len(w) && HasPrefix(w[j].OID, prefix) {
		j++
	}
	return w[i:j]
}

// ── values ─────────────────────────────────────────────────────────────

// Printable reports whether an OCTET STRING reads as text — the rule the
// walk format and the driver's decoder share: valid UTF-8, no control
// characters but tab/CR/LF, after dropping trailing NULs (C-string agents
// pad with them).
func Printable(b []byte) bool {
	b = bytes.TrimRight(b, "\x00")
	if !utf8.Valid(b) {
		return false
	}
	for _, r := range string(b) {
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' || r == 0x7f {
			return false
		}
	}
	return true
}

// HexString renders bytes the way the driver delivers a non-text OCTET
// STRING: uppercase pairs joined by colons ("00:1A:2B:3C:4D:5E"), which reads
// as the MAC address it usually is.
func HexString(b []byte) string {
	parts := make([]string, len(b))
	for i, c := range b {
		parts[i] = fmt.Sprintf("%02X", c)
	}
	return strings.Join(parts, ":")
}

// ValueString is the varbind's value as `naut snmp browse` prints it.
func (v Varbind) ValueString() string {
	switch v.Type {
	case Integer:
		return strconv.FormatInt(v.Int, 10)
	case Counter32, Gauge32, TimeTicks, Counter64:
		return strconv.FormatUint(v.Uint, 10)
	case OctetString, Opaque:
		if Printable(v.Bytes) {
			return strconv.Quote(string(bytes.TrimRight(v.Bytes, "\x00")))
		}
		return HexString(v.Bytes)
	case ObjectID, IPAddress:
		return v.Str
	}
	return v.Type.String()
}

// ── the text format ────────────────────────────────────────────────────

// Line renders one varbind as `snmpwalk -One` does. Strings are quoted with
// backslash escapes for `"` and `\`; a non-printable OCTET STRING is a
// Hex-STRING; an empty one is `""` with no type, as net-snmp prints it.
func (v Varbind) Line() string {
	var val string
	switch v.Type {
	case Integer:
		val = "INTEGER: " + strconv.FormatInt(v.Int, 10)
	case OctetString:
		switch {
		case len(v.Bytes) == 0:
			val = `""`
		case Printable(v.Bytes) && !bytes.ContainsAny(v.Bytes, "\x00\r"):
			r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
			val = `STRING: "` + r.Replace(string(v.Bytes)) + `"`
		default:
			val = "Hex-STRING: " + hexSpaced(v.Bytes)
		}
	case Null:
		val = "NULL"
	case ObjectID:
		val = "OID: ." + v.Str
	case IPAddress:
		val = "IpAddress: " + v.Str
	case Counter32:
		val = "Counter32: " + strconv.FormatUint(v.Uint, 10)
	case Gauge32:
		val = "Gauge32: " + strconv.FormatUint(v.Uint, 10)
	case TimeTicks:
		val = fmt.Sprintf("Timeticks: (%d) %s", v.Uint, ticksText(v.Uint))
	case Counter64:
		val = "Counter64: " + strconv.FormatUint(v.Uint, 10)
	case Opaque:
		val = "Opaque: Hex-STRING: " + hexSpaced(v.Bytes)
	default:
		val = v.Type.String()
	}
	return "." + v.OID + " = " + val
}

func hexSpaced(b []byte) string {
	parts := make([]string, len(b))
	for i, c := range b {
		parts[i] = fmt.Sprintf("%02X", c)
	}
	return strings.Join(parts, " ")
}

// ticksText is net-snmp's human column after the raw ticks: "d:hh:mm:ss.cc"
// with the day count as net-snmp writes it ("2 days, 3:04:05.06").
func ticksText(t uint64) string {
	cs := t % 100
	s := t / 100
	days := s / 86400
	s %= 86400
	hms := fmt.Sprintf("%d:%02d:%02d.%02d", s/3600, s/60%60, s%60, cs)
	switch days {
	case 0:
		return hms
	case 1:
		return "1 day, " + hms
	}
	return fmt.Sprintf("%d days, %s", days, hms)
}

// Bytes renders the whole walk, one varbind per line (multi-line strings as
// they are), in OID order.
func (w Walk) Bytes() []byte {
	var b strings.Builder
	for _, v := range w {
		b.WriteString(v.Line())
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

var (
	lineRE  = regexp.MustCompile(`^\s*(\.?[0-9]+(?:\.[0-9]+)*)\s+=\s?(.*)$`)
	intRE   = regexp.MustCompile(`^-?[0-9]+`)
	parenRE = regexp.MustCompile(`\((-?[0-9]+)\)`)
	wrongRE = regexp.MustCompile(`^Wrong Type \(should be [^)]*\):\s*`)
)

// Parse reads a recording. It accepts what net-snmp's snmpwalk prints with
// -On (numeric OIDs) with or without -e (numeric enums: "INTEGER: up(1)"
// reads as 1), multi-line strings and wrapped Hex-STRINGs, and the
// "No Such Object…" / "No more variables…" end-of-walk lines, which it skips.
// Errors carry the line number. The result is sorted; a duplicate OID is an
// error, because it means two recordings were concatenated.
func Parse(r io.Reader) (Walk, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	var out Walk
	var (
		pend     *pending
		lineNo   int
		startNo  int
		finished = func() error {
			if pend == nil {
				return nil
			}
			vb, skip, err := pend.decode()
			if err != nil {
				return fmt.Errorf("line %d: %s: %w", startNo, pend.oid, err)
			}
			if !skip {
				out = append(out, vb)
			}
			pend = nil
			return nil
		}
	)
	for sc.Scan() {
		lineNo++
		line := strings.TrimRight(sc.Text(), "\r")
		if pend != nil && pend.openQuote {
			pend.text += "\n" + line
			pend.openQuote = !closesQuote(line)
			continue
		}
		m := lineRE.FindStringSubmatch(line)
		if m == nil {
			if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			if pend != nil && pend.hex {
				pend.text += " " + strings.TrimSpace(line)
				continue
			}
			return nil, fmt.Errorf("line %d: not a walk line (want `.OID = TYPE: value`): %q", lineNo, truncate(line))
		}
		if err := finished(); err != nil {
			return nil, err
		}
		oid, err := ParseOID(m[1])
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
		startNo = lineNo
		pend = &pending{oid: oid, text: m[2]}
		body := wrongRE.ReplaceAllString(m[2], "")
		pend.text = body
		switch {
		case strings.HasPrefix(body, `STRING: "`):
			pend.openQuote = !closesQuote(strings.TrimPrefix(body, `STRING: "`))
		case body == `"` || (strings.HasPrefix(body, `"`) && !closesQuote(body[1:])):
			pend.openQuote = true
		case strings.HasPrefix(body, "Hex-STRING:"), strings.HasPrefix(body, "BITS:"):
			pend.hex = true
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if pend != nil && pend.openQuote {
		return nil, fmt.Errorf("line %d: %s: unterminated string", startNo, pend.oid)
	}
	if err := finished(); err != nil {
		return nil, err
	}
	out.Sort()
	for i := 1; i < len(out); i++ {
		if out[i].OID == out[i-1].OID {
			return nil, fmt.Errorf("OID %s appears twice — two walks concatenated?", out[i].OID)
		}
	}
	return out, nil
}

// ParseBytes is Parse over a byte slice.
func ParseBytes(b []byte) (Walk, error) { return Parse(bytes.NewReader(b)) }

type pending struct {
	oid       string
	text      string
	openQuote bool
	hex       bool
}

// closesQuote reports whether s (the text after an opening quote) contains
// the unescaped closing quote.
func closesQuote(s string) bool {
	esc := false
	for _, r := range s {
		switch {
		case esc:
			esc = false
		case r == '\\':
			esc = true
		case r == '"':
			return true
		}
	}
	return false
}

func truncate(s string) string {
	if len(s) > 80 {
		return s[:80] + "…"
	}
	return s
}

func (p *pending) decode() (vb Varbind, skip bool, err error) {
	vb.OID = p.oid
	body := strings.TrimSpace(p.text)
	if strings.HasPrefix(body, "No Such") || strings.HasPrefix(body, "No more variables") {
		return vb, true, nil
	}
	if body == `""` {
		vb.Type = OctetString
		vb.Bytes = []byte{}
		return vb, false, nil
	}
	if strings.HasPrefix(body, `"`) { // a typeless quoted string
		body = "STRING: " + body
	}
	typ, val, ok := strings.Cut(body, ":")
	if !ok {
		if body == "NULL" {
			vb.Type = Null
			return vb, false, nil
		}
		return vb, false, fmt.Errorf("no TYPE: in %q", truncate(body))
	}
	val = strings.TrimSpace(val)
	switch strings.TrimSpace(typ) {
	case "INTEGER":
		vb.Type = Integer
		vb.Int, err = parseInt(val)
	case "STRING":
		vb.Type = OctetString
		vb.Bytes, err = unquote(val)
	case "Hex-STRING", "BITS":
		vb.Type = OctetString
		vb.Bytes, err = parseHex(val)
	case "OID":
		vb.Type = ObjectID
		vb.Str, err = ParseOID(val)
	case "IpAddress":
		vb.Type = IPAddress
		if net.ParseIP(val) == nil {
			err = fmt.Errorf("IpAddress %q", val)
		}
		vb.Str = val
	case "Network Address":
		vb.Type = IPAddress
		var b []byte
		b, err = parseHex(strings.ReplaceAll(val, ":", " "))
		if err == nil && len(b) == 4 {
			vb.Str = net.IP(b).String()
		} else if err == nil {
			err = fmt.Errorf("Network Address %q", val)
		}
	case "Counter32":
		vb.Type = Counter32
		vb.Uint, err = parseUint(val, 32)
	case "Gauge32", "Unsigned32", "UNSIGNED":
		vb.Type = Gauge32
		vb.Uint, err = parseUint(val, 32)
	case "Timeticks":
		vb.Type = TimeTicks
		if m := parenRE.FindStringSubmatch(val); m != nil {
			val = m[1]
		}
		vb.Uint, err = parseUint(val, 32)
	case "Counter64":
		vb.Type = Counter64
		vb.Uint, err = parseUint(val, 64)
	case "Opaque":
		vb.Type = Opaque
		vb.Bytes, err = parseHex(strings.TrimPrefix(val, "Hex-STRING:"))
	default:
		return vb, false, fmt.Errorf("unsupported type %q", typ)
	}
	return vb, false, err
}

// parseInt reads "5", "-5", "up(1)" or "5 percent".
func parseInt(s string) (int64, error) {
	if m := parenRE.FindStringSubmatch(s); m != nil {
		return strconv.ParseInt(m[1], 10, 64)
	}
	if m := intRE.FindString(s); m != "" {
		return strconv.ParseInt(m, 10, 64)
	}
	return 0, fmt.Errorf("INTEGER %q", s)
}

func parseUint(s string, bits int) (uint64, error) {
	if m := intRE.FindString(s); m != "" && m[0] != '-' {
		return strconv.ParseUint(m, 10, bits)
	}
	return 0, fmt.Errorf("unsigned value %q", s)
}

func parseHex(s string) ([]byte, error) {
	h := strings.Join(strings.Fields(s), "")
	b, err := hex.DecodeString(h)
	if err != nil {
		return nil, fmt.Errorf("Hex-STRING %q", truncate(s))
	}
	return b, nil
}

// unquote reads a STRING value: quoted with backslash escapes (net-snmp
// escapes only `"` and `\`, and prints newlines raw), or bare.
func unquote(s string) ([]byte, error) {
	if !strings.HasPrefix(s, `"`) {
		return []byte(s), nil
	}
	s = s[1:]
	var b []byte
	esc := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case esc:
			b = append(b, c)
			esc = false
		case c == '\\':
			esc = true
		case c == '"':
			if rest := strings.TrimSpace(s[i+1:]); rest != "" {
				return nil, fmt.Errorf("text after the closing quote: %q", truncate(rest))
			}
			return b, nil
		default:
			b = append(b, c)
		}
	}
	return nil, fmt.Errorf("unterminated string")
}

// ── snmprec ────────────────────────────────────────────────────────────

// Snmprec renders the walk as snmpsim's data-file format ("OID|tag|value",
// tag 4x for a hex-encoded string) — how the foreign test feeds our
// recordings to somebody else's agent. variations replaces the tag|value
// column of named OIDs (snmpsim variation modules, e.g.
// "70:numeric|rate=125000,initial=5000000" for a counter that moves).
func (w Walk) Snmprec(variations map[string]string) []byte {
	var b strings.Builder
	for _, v := range w {
		if vv, ok := variations[v.OID]; ok {
			fmt.Fprintf(&b, "%s|%s\n", v.OID, vv)
			continue
		}
		switch v.Type {
		case Integer:
			fmt.Fprintf(&b, "%s|2|%d\n", v.OID, v.Int)
		case OctetString:
			if Printable(v.Bytes) && !bytes.ContainsAny(v.Bytes, "|\r\n\x00") && len(v.Bytes) > 0 {
				fmt.Fprintf(&b, "%s|4|%s\n", v.OID, v.Bytes)
			} else {
				fmt.Fprintf(&b, "%s|4x|%s\n", v.OID, hex.EncodeToString(v.Bytes))
			}
		case Null:
			fmt.Fprintf(&b, "%s|5|\n", v.OID)
		case ObjectID:
			fmt.Fprintf(&b, "%s|6|%s\n", v.OID, v.Str)
		case IPAddress:
			fmt.Fprintf(&b, "%s|64|%s\n", v.OID, v.Str)
		case Counter32:
			fmt.Fprintf(&b, "%s|65|%d\n", v.OID, v.Uint)
		case Gauge32:
			fmt.Fprintf(&b, "%s|66|%d\n", v.OID, v.Uint)
		case TimeTicks:
			fmt.Fprintf(&b, "%s|67|%d\n", v.OID, v.Uint)
		case Counter64:
			fmt.Fprintf(&b, "%s|70|%d\n", v.OID, v.Uint)
		case Opaque:
			fmt.Fprintf(&b, "%s|68x|%s\n", v.OID, hex.EncodeToString(v.Bytes))
		}
	}
	return []byte(b.String())
}
