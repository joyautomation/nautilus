package project

import (
	"testing"

	"github.com/joyautomation/nautilus/hw"
)

func TestHwStatus(t *testing.T) {
	src := func(id, state string, fresh bool, bad int) hw.SourceHealth {
		return hw.SourceHealth{ID: id, Addr: id + ":161", State: state, Fresh: fresh, Tags: 29, BadTags: bad}
	}
	cases := []struct {
		name        string
		h           hw.Health
		state, kind string
	}{
		{"all fresh", hw.Health{Kind: "snmp", Sources: []hw.SourceHealth{src("SW1", "connected", true, 0)}}, "connected", "snmp"},
		{"silent", hw.Health{Kind: "snmp", Sources: []hw.SourceHealth{src("SW1", "connected", false, 0)}}, "degraded", "snmp"},
		{"refused tag", hw.Health{Kind: "redfish", Sources: []hw.SourceHealth{src("N1", "connected", true, 1)}}, "degraded", "redfish"},
		{"one down", hw.Health{Kind: "prometheus", Sources: []hw.SourceHealth{src("A", "connected", true, 0), src("B", "error", false, 0)}}, "degraded", "prometheus"},
		{"all down", hw.Health{Kind: "snmp", Sources: []hw.SourceHealth{{ID: "SW1", State: "error", LastError: "timeout"}}}, "error", "snmp"},
		{"parked", hw.Health{Kind: "snmp", Sources: []hw.SourceHealth{src("SW1", "parked", false, 0)}}, "waiting", "snmp"},
		{"connecting", hw.Health{Kind: "snmp", Sources: []hw.SourceHealth{{ID: "SW1", State: "connecting"}}}, "connecting", "snmp"},
	}
	for _, c := range cases {
		s := hwStatus(c.h)
		if s.State != c.state || s.Kind != c.kind {
			t.Errorf("%s: state=%s kind=%s (%s)", c.name, s.State, s.Kind, s.Message)
		}
		if len(s.Devices) != len(c.h.Sources) {
			t.Errorf("%s: %d device rows", c.name, len(s.Devices))
		}
	}
	s := hwStatus(cases[1].h)
	if s.Devices[0].Online || s.Devices[0].Detail != "29 tags · silent" {
		t.Errorf("silent row: %+v", s.Devices[0])
	}
	if s.Metrics[0].Text != "0 / 1" {
		t.Errorf("sources metric text = %q", s.Metrics[0].Text)
	}
}
