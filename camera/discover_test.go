//go:build !js

package camera

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func probeMatch(endpoint, scopes, xaddrs string) string {
	return `<?xml version="1.0"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery" xmlns:a="http://schemas.xmlsoap.org/ws/2004/08/addressing"><s:Body><d:ProbeMatches><d:ProbeMatch><a:EndpointReference><a:Address>` + endpoint + `</a:Address></a:EndpointReference><d:Types>dn:NetworkVideoTransmitter</d:Types><d:Scopes>` + scopes + `</d:Scopes><d:XAddrs>` + xaddrs + `</d:XAddrs></d:ProbeMatch></d:ProbeMatches></s:Body></s:Envelope>`
}

// responder answers a probe sent to it with the given datagrams, and records the probe.
func responder(t *testing.T, answers func() []string) (addr string, probes <-chan string) {
	t.Helper()
	c, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	ch := make(chan string, 4)
	go func() {
		buf := make([]byte, 65536)
		for {
			n, from, err := c.ReadFrom(buf)
			if err != nil {
				return
			}
			ch <- string(buf[:n])
			for _, a := range answers() {
				c.WriteTo([]byte(a), from)
			}
		}
	}()
	return c.LocalAddr().String(), ch
}

func TestDiscoveryAsksForVideoTransmittersAndListsWhatAnswers(t *testing.T) {
	addr, probes := responder(t, func() []string {
		return []string{
			probeMatch("urn:uuid:aaa", "onvif://www.onvif.org/name/Pump%20house%20dome onvif://www.onvif.org/hardware/XYZ-100", "http://127.0.0.1:80/onvif/device_service"),
			probeMatch("urn:uuid:aaa", "onvif://www.onvif.org/name/dup", "http://127.0.0.1/x"), // the same device again
			probeMatch("urn:uuid:bbb", "", "http://10.1.2.3/onvif/device_service"),             // an address that is not where it answered from
			probeMatch("urn:uuid:ccc", "", "http://127.0.0.1/a http://10.1.2.3/b"),             // one of its addresses is elsewhere: flagged
		}
	})
	got, err := Discover(context.Background(), DiscoverOptions{Target: addr, Timeout: 600 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	p := <-probes
	if !strings.Contains(p, "Probe") || !strings.Contains(p, "NetworkVideoTransmitter") || !strings.Contains(p, "uuid:") {
		t.Errorf("the probe: %s", p)
	}
	if len(got) != 3 {
		t.Fatalf("found %+v", got)
	}
	a, b, c := got[0], got[1], got[2]
	if a.Endpoint != "urn:uuid:aaa" || a.Name != "Pump house dome" || a.Hardware != "XYZ-100" || a.Elsewhere || len(a.XAddrs) != 1 || a.From != "127.0.0.1" {
		t.Errorf("first: %+v", a)
	}
	if b.Endpoint != "urn:uuid:bbb" || !b.Elsewhere {
		t.Errorf("an address off the sender's host is flagged: %+v", b)
	}
	if c.Endpoint != "urn:uuid:ccc" || !c.Elsewhere || len(c.XAddrs) != 2 {
		t.Errorf("any address off the sender's host flags the device: %+v", c)
	}
}

func TestWhatAnswersIsNeverTrustedOrAllowedToMoveATerminal(t *testing.T) {
	// a valid ProbeMatch padded past the datagram bound: only the bound keeps it out
	big := probeMatch("urn:uuid:big", "", "http://127.0.0.1/big") + "<!--" + strings.Repeat("a", maxDatagram) + "-->"
	answers := []string{
		"not xml at all",
		big,
		probeMatch("urn:uuid:esc", "onvif://www.onvif.org/name/Dome%1B%5D0%3Bpwned%07%0A onvif://www.onvif.org/hardware/H%1B%5B2J", "http://127.0.0.1/ok"),
		probeMatch("urn:uuid:file", "", "file:///etc/passwd ftp://127.0.0.1/x javascript:alert(1)"), // no usable address: not listed
		probeMatch("urn:uuid:user", "", "http://admin:pw@127.0.0.1/x"),                              // user information: not listed
		probeMatch("urn:uuid:long", "", "http://127.0.0.1/"+strings.Repeat("p", 600)),
		probeMatch("", "", "http://127.0.0.1/x"), // no endpoint
		`<?xml version="1.0"?><!DOCTYPE x [<!ENTITY a "aaaa">]><s:Envelope xmlns:s="x"><s:Body>&a;</s:Body></s:Envelope>`,
	}
	addr, _ := responder(t, func() []string { return answers })
	got, err := Discover(context.Background(), DiscoverOptions{Target: addr, Timeout: 600 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Endpoint != "urn:uuid:esc" {
		t.Fatalf("found %+v", got)
	}
	for _, s := range []string{got[0].Name, got[0].Hardware} {
		if strings.ContainsAny(s, "\x1b\x07\n") {
			t.Errorf("a control character reached the output: %q", s)
		}
	}
	if got[0].Name != "Dome]0;pwned" || got[0].Hardware != "H[2J" {
		t.Errorf("cleaned text: %q %q", got[0].Name, got[0].Hardware)
	}
}

func TestNoCharacterThatCanReorderOrRewriteATerminalSurvives(t *testing.T) {
	for _, s := range []string{"evil\u202Ereversed", "a\u009B2Jb", "zero\u200Bwidth", "line\u2028sep", "bidi\u2066x\u2069", "esc\x1b[31m"} {
		got := cleanText(s, 64)
		for _, r := range got {
			if r == 0x202e || r == 0x9b || r == 0x200b || r == 0x2028 || r == 0x2066 || r == 0x2069 || r < 0x20 {
				t.Errorf("%q survived as %q", s, got)
			}
		}
	}
	if cleanText("Pump house dome — 1ª câmera", 64) != "Pump house dome — 1ª câmera" {
		t.Error("ordinary text, accents and dashes were cleaned away")
	}
}

func TestOneAddressCannotFillTheTableWithDevices(t *testing.T) {
	var many []string
	for i := 0; i < 100; i++ {
		many = append(many, probeMatch(fmt.Sprintf("urn:uuid:%03d", i), "", "http://127.0.0.1/x"))
	}
	addr, _ := responder(t, func() []string { return many })
	got, err := Discover(context.Background(), DiscoverOptions{Target: addr, Timeout: 800 * time.Millisecond})
	if err != nil || len(got) != MaxPerSource {
		t.Errorf("%d found from one address, want %d: %v", len(got), MaxPerSource, err)
	}
}

func TestAFloodOfAnswersKeepsAtMostMaxFound(t *testing.T) {
	old := MaxPerSource
	MaxPerSource = 1 << 20
	t.Cleanup(func() { MaxPerSource = old })
	var many []string
	for i := 0; i < MaxFound+100; i++ {
		many = append(many, probeMatch(fmt.Sprintf("urn:uuid:%04d", i), "", "http://127.0.0.1/x"))
	}
	addr, _ := responder(t, func() []string { return many })
	got, err := Discover(context.Background(), DiscoverOptions{Target: addr, Timeout: 800 * time.Millisecond})
	if err != nil || len(got) != MaxFound {
		t.Errorf("%d found, %v", len(got), err)
	}
}

func TestDiscoveryEndsWhenAskedToAndWithNoAnswersIsAnEmptyList(t *testing.T) {
	addr, _ := responder(t, func() []string { return nil })
	start := time.Now()
	got, err := Discover(context.Background(), DiscoverOptions{Target: addr, Timeout: 300 * time.Millisecond})
	if err != nil || len(got) != 0 || time.Since(start) > 2*time.Second {
		t.Errorf("%v %v after %s", got, err, time.Since(start))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start = time.Now()
	if _, err := Discover(ctx, DiscoverOptions{Target: addr, Timeout: 30 * time.Second}); err != nil || time.Since(start) > 2*time.Second {
		t.Errorf("a cancelled probe kept waiting: %v after %s", err, time.Since(start))
	}
	if _, err := Discover(context.Background(), DiscoverOptions{Interface: "no-such-interface0", Timeout: time.Second}); err == nil || !strings.Contains(err.Error(), "no-such-interface0") {
		t.Errorf("an interface that does not exist: %v", err)
	}
}
