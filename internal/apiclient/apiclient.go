// Package apiclient puts the controller's auth token on a request a
// nautilus tool makes to a controller's HTTP API: naut historian, naut pull,
// and the SNMP and Redfish plant agents. A controller answers a host name
// it does not trust only when the request carries its token (#819), so a
// client that reaches one by name sends NAUTILUS_TOKEN when it is set.
package apiclient

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Authorize sets Authorization: Bearer $NAUTILUS_TOKEN on req, when the
// variable is set; otherwise it leaves req alone.
func Authorize(req *http.Request) {
	if tok := os.Getenv("NAUTILUS_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
}

// RefusalHint is what a tool adds to a controller's 401 or 403, so the
// person knows the credential it sent (or did not): the only one these
// tools send is NAUTILUS_TOKEN. Any other status gets "".
func RefusalHint(status int) string {
	if status != http.StatusUnauthorized && status != http.StatusForbidden {
		return ""
	}
	if os.Getenv("NAUTILUS_TOKEN") == "" {
		return " (no credential was sent: set NAUTILUS_TOKEN to a token the controller accepts, such as a personal access token from naut users token <you>, or a service token)"
	}
	return " (sent NAUTILUS_TOKEN: the controller does not accept it, or its holder lacks the permission)"
}

// BaseURL is the controller's API base for a tool's --host and --port:
// a URL given as --host ("https://plc.example:8443") is taken as it is,
// its port included (or its scheme's), and its certificate checked
// against the system's roots like any HTTPS client's; a bare host name or
// address is http://host:port, as before.
func BaseURL(host string, port int) (string, error) {
	if !strings.Contains(host, "://") {
		return "http://" + net.JoinHostPort(host, strconv.Itoa(port)), nil
	}
	u, err := url.Parse(host)
	if err != nil {
		return "", fmt.Errorf("--host %q: %w", host, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("--host %q: the scheme is http or https", host)
	}
	if u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.User != nil {
		return "", fmt.Errorf("--host %q: give the controller's address only (https://plc.example:8443)", host)
	}
	return u.Scheme + "://" + u.Host, nil
}
