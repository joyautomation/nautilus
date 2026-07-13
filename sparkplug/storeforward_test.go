package sparkplug

import "testing"

func TestStoreForwardRingAndDrain(t *testing.T) {
	sf := &storeForward{max: 3}
	for i := 0; i < 5; i++ {
		sf.enqueue(sfRecord{topic: "t", ts: uint64(i)})
	}
	if sf.len() != 3 || sf.dropped != 2 {
		t.Fatalf("len=%d dropped=%d, want 3/2", sf.len(), sf.dropped)
	}
	// Oldest two dropped → remaining ts should be 2,3,4.
	b := sf.drainBatch(2)
	if len(b) != 2 || b[0].ts != 2 || b[1].ts != 3 {
		t.Fatalf("drain batch = %+v", b)
	}
	if sf.len() != 1 {
		t.Fatalf("remaining %d, want 1", sf.len())
	}
}

func TestParseState(t *testing.T) {
	cases := []struct {
		in     string
		online bool
		ts     int64
		ok     bool
	}{
		{"ONLINE", true, 0, true},
		{"OFFLINE", false, 0, true},
		{`{"online":true,"timestamp":123}`, true, 123, true},
		{`{"online":false,"timestamp":9}`, false, 9, true},
		{"garbage", false, 0, false},
	}
	for _, c := range cases {
		on, ts, ok := parseState([]byte(c.in))
		if on != c.online || ts != c.ts || ok != c.ok {
			t.Errorf("parseState(%q) = %v,%d,%v; want %v,%d,%v", c.in, on, ts, ok, c.online, c.ts, c.ok)
		}
	}
}
