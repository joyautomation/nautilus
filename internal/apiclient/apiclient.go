// Package apiclient puts the controller's auth token on a request a
// nautilus tool makes to a controller's HTTP API: naut historian, naut pull,
// and the SNMP and Redfish plant agents. A controller answers a host name
// it does not trust only when the request carries its token (#819), so a
// client that reaches one by name sends NAUTILUS_TOKEN when it is set.
package apiclient

import (
	"net/http"
	"os"
)

// Authorize sets Authorization: Bearer $NAUTILUS_TOKEN on req, when the
// variable is set; otherwise it leaves req alone.
func Authorize(req *http.Request) {
	if tok := os.Getenv("NAUTILUS_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
}
