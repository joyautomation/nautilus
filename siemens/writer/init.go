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
	u := strings.ToUpper(typ)
	real := u == "REAL" || u == "LREAL"
	integer := false
	switch u {
	case "SINT", "INT", "DINT", "LINT", "USINT", "UINT", "UDINT", "ULINT", "BYTE", "WORD", "DWORD", "LWORD":
		integer = true
	}
	if u == "TIME" {
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
		return numberLiteral(float64(x), integer, real)
	case int64:
		return numberLiteral(float64(x), integer, real)
	case float64:
		return numberLiteral(x, integer, real)
	case uint64: // above the int64 range: YAML hands it over unconverted
		s := strconv.FormatUint(x, 10)
		if real {
			s += ".0"
		}
		return s, nil
	case string:
		t := strings.ToUpper(strings.TrimSpace(x))
		if strings.HasPrefix(t, "T#") || strings.HasPrefix(t, "TIME#") {
			return strings.TrimSpace(x), nil
		}
		return st.QuoteString(x), nil
	}
	return "", fmt.Errorf("init %v is not a value the Siemens target can seed", v)
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
	if real && !strings.ContainsAny(s, ".eEn") { // n: NaN and Inf stay as they are, for the writer to refuse
		s += ".0"
	}
	return s, nil
}
