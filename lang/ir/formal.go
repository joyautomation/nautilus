package ir

import "strconv"

// FormalNames names a standard function's (or FBD operator block's) n
// inputs the way IEC 61131-3 does: IN for a unary function, IN1..INn for
// the extensible and binary ones, and the standard's own names for the
// few that have them (LIMIT's MN/IN/MX, SEL's G/IN0/IN1, MUX's K/IN0.., …).
//
// One table for every consumer: the FBD diagram's pin labels, the ST
// lowering's formal (named-argument) calls of a standard function —
// `LIMIT(MN := 0.0, IN := x, MX := 10.0)` — and the language server's
// signature help, so the three never disagree.
func FormalNames(fn string, n int) []string {
	switch fn {
	case "LIMIT":
		if n == 3 {
			return []string{"MN", "IN", "MX"}
		}
	case "SEL":
		if n == 3 {
			return []string{"G", "IN0", "IN1"}
		}
	case "MUX":
		if n >= 2 {
			pins := []string{"K"}
			for i := 0; i < n-1; i++ {
				pins = append(pins, "IN"+strconv.Itoa(i))
			}
			return pins
		}
	case "SHL", "SHR", "ROL", "ROR":
		if n == 2 {
			return []string{"IN", "N"}
		}
	case "LEFT", "RIGHT":
		if n == 2 {
			return []string{"IN", "L"}
		}
	case "MID":
		if n == 3 {
			return []string{"IN", "L", "P"}
		}
	case "INSERT":
		if n == 3 {
			return []string{"IN1", "IN2", "P"}
		}
	case "DELETE":
		if n == 3 {
			return []string{"IN", "L", "P"}
		}
	case "REPLACE":
		if n == 4 {
			return []string{"IN1", "IN2", "L", "P"}
		}
	}
	if n == 1 {
		return []string{"IN"}
	}
	pins := make([]string, n)
	for i := range pins {
		pins[i] = "IN" + strconv.Itoa(i+1)
	}
	return pins
}
