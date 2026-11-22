//go:build linux

package floatip

import (
	"net"
	"os"
	"os/exec"
	"testing"
)

// The kernel side, on a dummy interface. It needs CAP_NET_ADMIN, so it runs
// only inside a network namespace the test may change:
//
//	unshare -Urn go test ./internal/floatip -run TestSystem
func TestSystemAddsAndRemovesTheAddress(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs CAP_NET_ADMIN: run under `unshare -Urn`")
	}
	if _, err := exec.LookPath("ip"); err != nil {
		t.Skip("needs the ip command to make a dummy interface")
	}
	for _, args := range [][]string{{"link", "add", "ftest0", "type", "dummy"}, {"link", "set", "ftest0", "up"}} {
		if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
			t.Skipf("ip %v: %v %s", args, err, out)
		}
	}
	c, err := Parse("10.77.0.50/24", "ftest0")
	if err != nil {
		t.Fatal(err)
	}
	has := func() bool {
		ifc, _ := net.InterfaceByName("ftest0")
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ip, _, err := net.ParseCIDR(a.String()); err == nil && ip.String() == "10.77.0.50" {
				return true
			}
		}
		return false
	}
	var s System
	if err := s.Add(c); err != nil || !has() {
		t.Fatalf("Add: %v, present %v", err, has())
	}
	if err := s.Add(c); err != nil {
		t.Errorf("a second Add (already there): %v", err)
	}
	if err := s.Announce(c); err != nil {
		t.Errorf("Announce: %v", err)
	}
	if err := s.Del(c); err != nil || has() {
		t.Fatalf("Del: %v, present %v", err, has())
	}
	if err := s.Del(c); err != nil {
		t.Errorf("a second Del (not there): %v", err)
	}
}
