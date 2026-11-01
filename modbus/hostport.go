package modbus

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// SplitHost normalizes a host as a user writes it: "10.0.0.9",
// "10.0.0.9:5020", "::1", "[::1]" or "[::1]:5020". It returns the bare host
// (no brackets) and the port, 0 when the text carries none.
func SplitHost(s string) (host string, port int, err error) {
	if h, ps, serr := net.SplitHostPort(s); serr == nil {
		p, perr := strconv.Atoi(ps)
		if perr != nil || p <= 0 || p > 65535 {
			return "", 0, fmt.Errorf("host %q has an invalid port", s)
		}
		return h, p, nil
	}
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		return s[1 : len(s)-1], 0, nil
	}
	return s, 0, nil
}
