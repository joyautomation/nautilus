package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/camera"
)

func discoverWith(t *testing.T, found []camera.Found, err error, args ...string) (int, string, string) {
	t.Helper()
	old := discoverFn
	discoverFn = func(ctx context.Context, o camera.DiscoverOptions) ([]camera.Found, error) { return found, err }
	t.Cleanup(func() { discoverFn = old })
	var out, errb bytes.Buffer
	code := runCameraDiscover(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestCameraDiscoverListsWhatAnsweredWithARowToStartFromAndChangesNothing(t *testing.T) {
	code, out, _ := discoverWith(t, []camera.Found{
		{Endpoint: "urn:uuid:a", XAddrs: []string{"http://10.0.0.30/onvif/device_service"}, Name: "Pump house dome", Hardware: "XYZ-100", From: "10.0.0.30"},
		{Endpoint: "urn:uuid:b", XAddrs: []string{"http://10.9.9.9/onvif/device_service"}, From: "10.0.0.31", Elsewhere: true},
	}, nil)
	if code != 0 {
		t.Fatal(code)
	}
	for _, want := range []string{"2 ONVIF camera(s) answered", "Pump house dome", "XYZ-100", `url: "http://10.0.0.30/onvif/device_service"`, "kind: onvif", "(no name)", "not on 10.0.0.31", "naut alarms secrets set camera.<id>"} {
		if !strings.Contains(out, want) {
			t.Errorf("the output lacks %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "note:") != 1 {
		t.Errorf("only the device with an address elsewhere is noted:\n%s", out)
	}
}

func TestCameraDiscoverSaysWhatToDoWhenNothingAnswersAndWhenItCannotProbe(t *testing.T) {
	code, out, _ := discoverWith(t, nil, nil)
	if code != 0 || !strings.Contains(out, "does not cross routers") {
		t.Errorf("none: %d %s", code, out)
	}
	code, _, errs := discoverWith(t, nil, errors.New("no network interface that is up, multicast and has an IPv4 address"))
	if code != 1 || !strings.Contains(errs, "naut camera discover: no network interface") {
		t.Errorf("no interface: %d %s", code, errs)
	}
	code, out, _ = discoverWith(t, nil, nil, "--json")
	if code != 0 || strings.TrimSpace(out) != "[]" {
		t.Errorf("json, none: %d %q", code, out)
	}
	code, out, _ = discoverWith(t, []camera.Found{{Endpoint: "e", XAddrs: []string{"http://1.2.3.4/x"}}}, nil, "--json")
	if code != 0 || !strings.Contains(out, `"endpoint": "e"`) {
		t.Errorf("json: %d %s", code, out)
	}
	for _, bad := range [][]string{{"--timeout", "0s"}, {"--timeout", "2h"}, {"extra"}} {
		if code, _, _ := discoverWith(t, nil, nil, bad...); code != 2 {
			t.Errorf("%v: %d, want 2", bad, code)
		}
	}
}
