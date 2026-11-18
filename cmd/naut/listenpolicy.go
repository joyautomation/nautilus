package main

import (
	"net"
	"os"
	"strings"
)

// loopbackListen reports a listen address only this machine can reach:
// localhost or a loopback IP. ":8080" (every interface) and a LAN address
// are not.
func loopbackListen(addr string) bool {
	h, _, err := net.SplitHostPort(addr)
	if err != nil {
		h = addr
	}
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// allowedHosts is NAUTILUS_ALLOWED_HOSTS: host names, comma-separated, a
// browser page may write from when no token is set (#811).
func allowedHosts() []string {
	var out []string
	for _, h := range strings.Split(os.Getenv("NAUTILUS_ALLOWED_HOSTS"), ",") {
		if h = strings.TrimSpace(h); h != "" {
			out = append(out, h)
		}
	}
	return out
}

// openListenWarning is what naut run says when it listens beyond this
// machine with no token (#811): every client on the network can write
// tags, and a browser by any address but localhost, an IP or an allowed
// name is refused.
const openListenWarning = "WARNING: the tag API listens on %s with no NAUTILUS_TOKEN: any client that reaches it can write tags and drive outputs. Set NAUTILUS_TOKEN to require a token; without one, only requests to localhost, an IP address or a name in NAUTILUS_ALLOWED_HOSTS are answered.\n"
