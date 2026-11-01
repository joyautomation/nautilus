package codegen

import (
	"strconv"
	"strings"
)

// yamlHost renders a host as a YAML scalar that reads back as the same
// string: an IPv6 address would otherwise read as a list ("[::1]") or a map.
func yamlHost(h string) string {
	if strings.ContainsAny(h, ":[]#{}&*!|>'\"%@`,?") {
		return strconv.Quote(h)
	}
	return h
}
