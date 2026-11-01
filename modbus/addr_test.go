package modbus

import "testing"

// The address the driver dials, for every spelling of a source's host (#426).
func TestSourceAddr(t *testing.T) {
	cases := []struct {
		host string
		port int
		want string
	}{
		{"10.0.0.9", 0, "10.0.0.9:502"},
		{"plc.local", 5020, "plc.local:5020"},
		{"::1", 5021, "[::1]:5021"},
		{"::1", 0, "[::1]:502"},
		{"[::1]", 0, "[::1]:502"},
		{"[::1]", 5021, "[::1]:5021"},
	}
	for _, c := range cases {
		if got := (Source{Host: c.host, Port: c.port}).Addr(); got != c.want {
			t.Errorf("Source{%q,%d}.Addr() = %q, want %q", c.host, c.port, got, c.want)
		}
	}
}
