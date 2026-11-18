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
	for _, bad := range []string{"tiad", "ftp://h", "http://", "https://u:secret@h", "https://token@h"} {
		err := CheckAgentURL(bad)
		if err == nil {
			t.Errorf("%s: want an error", bad)
		} else if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "token@") {
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
