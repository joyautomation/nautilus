package apiclient

import "testing"

// --host is a host (http://host:port, as before) or the controller's URL,
// taken as it is: https for a controller with server.tls.
func TestBaseURL(t *testing.T) {
	for _, c := range []struct {
		host string
		port int
		want string
		bad  bool
	}{
		{"plc.example", 8080, "http://plc.example:8080", false},
		{"fe80::1", 8080, "http://[fe80::1]:8080", false},
		{"https://plc.example:8443", 8080, "https://plc.example:8443", false},
		{"https://plc.example/", 8080, "https://plc.example", false},
		{"http://10.0.0.5:8080", 9, "http://10.0.0.5:8080", false},
		{"ftp://plc.example", 0, "", true},
		{"https://plc.example/api", 0, "", true},
		{"https://user:pw@plc.example", 0, "", true},
		{"https://", 0, "", true},
	} {
		got, err := BaseURL(c.host, c.port)
		if (err != nil) != c.bad || got != c.want {
			t.Errorf("BaseURL(%q, %d) = %q, %v", c.host, c.port, got, err)
		}
	}
}
