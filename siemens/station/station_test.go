package station

import (
	"strings"
	"testing"
)

func TestCheckAgentURL(t *testing.T) {
	for _, ok := range []string{"http://127.0.0.1:8190", "https://tiad.example:8190/"} {
		if err := CheckAgentURL(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	// Every bad input carries "s3cret": no error may contain it, whatever
	// shape the mistake takes.
	for _, bad := range []string{
		"tiad-s3cret", "ftp://u:s3cret@h", "http://", "https://u:s3cret@h", "https://s3cret@h",
		"admin:s3cret@10.0.0.5:8080", "http//u:s3cret@h", "u:s3cret@h", "//u:s3cret@h:8190",
		"http://h:8190/?token=s3cret", "https://u:s3cret@h/?token=s3cret", "http://h/#s3cret", "http://h/?",
		"http://h:s3cret", "http:s3cret@h", "mailto:s3cret@h",
	} {
		err := CheckAgentURL(bad)
		if err == nil {
			t.Errorf("%s: want an error", bad)
		} else if strings.Contains(err.Error(), "s3cret") {
			t.Errorf("%s: the error echoes the credential: %v", bad, err)
		}
	}
}

func TestCheckCertSHA256(t *testing.T) {
	good := strings.Repeat("aB", 32)
	if err := CheckCertSHA256(good); err != nil {
		t.Error(err)
	}
	if err := CheckCertSHA256(strings.Repeat("ab:", 31) + "ab"); err != nil {
		t.Error(err)
	}
	for _, bad := range []string{"", "abc", strings.Repeat("zz", 32), strings.Repeat("ab", 33)} {
		if CheckCertSHA256(bad) == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

func TestStationNames(t *testing.T) {
	for _, n := range []string{"plc-1", "a.b-c", "port-1234", "port-x", "1000.1.1.1"} {
		if err := CheckStationName(n); err != nil {
			t.Errorf("%q: %v", n, err)
		}
	}
	for _, n := range []string{"", "PLC", "-a", "a-", "a..b", "300.1.1.1", "01.2.3.4", "port-001", "port-001-00002", "port-001.x", strings.Repeat("a", 64)} {
		if CheckStationName(n) == nil {
			t.Errorf("%q: want an error", n)
		}
	}
}
