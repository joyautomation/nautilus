package main

import "testing"

func TestBrowseAddr(t *testing.T) {
	cases := []struct {
		host string
		port int
		want string
	}{
		{"127.0.0.1", 502, "127.0.0.1:502"},
		{"127.0.0.1:5020", 502, "127.0.0.1:5020"},
		{"[::1]:5021", 502, "[::1]:5021"},
		{"::1", 5021, "[::1]:5021"},
		{"[::1]", 502, "[::1]:502"},
	}
	for _, c := range cases {
		got, err := browseAddr(c.host, c.port)
		if err != nil || got != c.want {
			t.Errorf("browseAddr(%q,%d) = %q, %v; want %q", c.host, c.port, got, err, c.want)
		}
	}
	if _, err := browseAddr("h:notaport", 502); err == nil {
		t.Error("an invalid port in --host was accepted")
	}
}
