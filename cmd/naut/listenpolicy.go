package main

import (
	"fmt"
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
// plainAuthNote is what naut run says instead when an auth: block decides
// who may do what (#708), but the API listens beyond this machine over
// plain HTTP with no TLS proxy named: tokens and cookies would cross the
// network in clear, and browser sign-in is refused off loopback.
const plainAuthNote = "auth: the tag API listens on %s over plain HTTP: tokens cross the network in clear and browser sign-in works only from this machine. Set server.tls, or put a TLS proxy in front and name it in server.trusted-proxies.\n"

// listenNotice is naut run's warning about who can reach the tag API on
// addr, or "": without an auth: block (policy) and with no token, any
// client can write (openListenWarning); with one, the block guards reads
// and writes and NAUTILUS_TOKEN is no longer what protects it, so the
// warning is about the transport (plainAuthNote) when nothing encrypts it.
func listenNotice(addr string, token, policy, tls, proxied bool) string {
	if loopbackListen(addr) {
		return ""
	}
	switch {
	case policy && !tls && !proxied:
		return fmt.Sprintf(plainAuthNote, addr)
	case !policy && !token:
		return fmt.Sprintf(openListenWarning, addr)
	}
	return ""
}

const openListenWarning = "WARNING: the tag API listens on %s with no NAUTILUS_TOKEN: any client that reaches it can write tags and drive outputs. Set NAUTILUS_TOKEN to require a token. Only requests to localhost, an IP address or a name in NAUTILUS_ALLOWED_HOSTS are answered, unless they carry the token.\n"
