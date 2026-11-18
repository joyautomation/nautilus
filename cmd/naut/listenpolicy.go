package main

import (
	"net"
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

// openListenWarning is what naut run says when it listens beyond this
// machine with no token (#811): every client on the network can write
// tags, and a browser by any address but localhost, an IP or an allowed
// name is refused.
const openListenWarning = "WARNING: the tag API listens on %s with no NAUTILUS_TOKEN: any client that reaches it can write tags and drive outputs. Set NAUTILUS_TOKEN to require a token. Only requests to localhost, an IP address or a name in NAUTILUS_ALLOWED_HOSTS are answered, unless they carry the token.\n"
