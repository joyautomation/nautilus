package writer

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/lang/st"
)

// InitLiteral prints a manifest init: of an elementary type as the ST
// literal the writer parses (a struct's start value is Global.Start). The loader hands every number over as a float64
// (project.TagDefs), so the tag's type decides the spelling: a whole
// number is an integer literal for an integer type, keeps a decimal point
// for REAL and LREAL, and is milliseconds for a TIME (as the runtime reads
// a numeric TIME seed); a fraction on an integer type or a TIME is refused.
func InitLiteral(v any, typ string) (string, error) {
	s, err := initLiteral(v, typ)
	if err != nil {
		return "", err
	}
	// The value must fit the type: TIA refuses an out-of-range start
	// value at import, and nautilus would not hold the same value.
	u := strings.ToUpper(strings.TrimSpace(typ))
	if isInt(u) || isBits(u) {
		if why := intFits(s, u); why != "" {
			return "", fmt.Errorf("init %v %s", v, why)
		}
	}
	if u == "REAL" {
		if f, err := strconv.ParseFloat(s, 64); err == nil && math.Abs(f) > math.MaxFloat32 {
			return "", fmt.Errorf("init %v is beyond REAL's range", v)
		}
	}
	return s, nil
}

// intFits says why integer literal s does not fit integer or bit string
// type u, or "".
func intFits(s, u string) string {
	bits, signed := uint(0), false
	if w, ok := intWidth[u]; ok {
		bits, signed = w.bits, w.signed
	} else {
		bits = bitStrings[u]
	}
	if signed {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || bits < 64 && (n < -(int64(1)<<(bits-1)) || n >= int64(1)<<(bits-1)) {
			return "is out of " + u + "'s range"
		}
		return ""
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil || bits < 64 && n >= uint64(1)<<bits {
		return "is out of " + u + "'s range"
	}
	return ""
}

func initLiteral(v any, typ string) (string, error) {
	u := strings.ToUpper(typ)
	real := u == "REAL" || u == "LREAL"
	integer := false
	switch u {
	case "SINT", "INT", "DINT", "LINT", "USINT", "UINT", "UDINT", "ULINT", "BYTE", "WORD", "DWORD", "LWORD":
		integer = true
	}
	if u == "TIME" {
		switch x := v.(type) {
		case int:
			return "T#" + strconv.Itoa(x) + "ms", nil
		case int64:
			return "T#" + strconv.FormatInt(x, 10) + "ms", nil
		}
		if x, ok := v.(float64); ok {
			if x != math.Trunc(x) || math.Abs(x) >= 1<<53 {
				return "", fmt.Errorf("init %v is not a whole number of milliseconds", x)
			}
			return "T#" + strconv.FormatInt(int64(x), 10) + "ms", nil
		}
	}
	switch x := v.(type) {
	case bool:
		if x {
			return "TRUE", nil
		}
		return "FALSE", nil
	case int:
		return intLiteral(int64(x), integer, real)
	case int64:
		return intLiteral(x, integer, real)
	case float64:
		return numberLiteral(x, integer, real)
	case uint64: // above the int64 range: YAML hands it over unconverted
		s := strconv.FormatUint(x, 10)
		if real {
			s += ".0"
		}
		return s, nil
	case string:
		if u == "STRING" {
			// Before the TIME spellings: 'T#5s' is text in a STRING.
			if len(x) > maxString {
				return "", fmt.Errorf("init is %d bytes; TIA's String holds at most %d", len(x), maxString)
			}
			if !ascii(x) {
				return "", fmt.Errorf("init %q: %s", x, gapMessage(gapString, "a STRING with characters outside ASCII", "how TIA stores them is not measured yet"))
			}
			return st.QuoteString(x), nil
		}
		t := strings.ToUpper(strings.TrimSpace(x))
		if strings.HasPrefix(t, "T#") || strings.HasPrefix(t, "TIME#") {
			return strings.TrimSpace(x), nil
		}
		if u == "TIME" {
			// nautilus seeds a TIME from the duration without its prefix
			// too (`3s`, `500ms`).
			return "T#" + strings.TrimSpace(x), nil
		}
		if !strings.HasPrefix(u, "STRING") && !strings.HasPrefix(u, "WSTRING") && u != "CHAR" && u != "WCHAR" {
			return "", fmt.Errorf("init %q is not a %s", x, typ)
		}
		return st.QuoteString(x), nil
	}
	return "", fmt.Errorf("init %v is not a value the Siemens target can seed", v)
}

// intLiteral writes an integer init: exactly on an integer type (a float64
// holds none above 2^53 exactly, #1432), through numberLiteral otherwise.
func intLiteral(x int64, integer, real bool) (string, error) {
	if integer {
		return strconv.FormatInt(x, 10), nil
	}
	return numberLiteral(float64(x), integer, real)
}

func numberLiteral(x float64, integer, real bool) (string, error) {
	if integer {
		if x != math.Trunc(x) || math.IsInf(x, 0) {
			return "", fmt.Errorf("init %v is not a whole number", x)
		}
		if math.Abs(x) < 1<<63 {
			return strconv.FormatInt(int64(x), 10), nil
		}
		return strconv.FormatFloat(x, 'f', 0, 64), nil
	}
	s := strconv.FormatFloat(x, 'g', -1, 64)
	if real && !strings.ContainsAny(s, ".n") { // n: NaN and Inf stay as they are, for the writer to refuse
		// A REAL literal keeps its decimal point: 45.0, 1.0e+40.
		if m, e, ok := strings.Cut(s, "e"); ok {
			s = m + ".0e" + e
		} else {
			s += ".0"
		}
	}
	return s, nil
}
