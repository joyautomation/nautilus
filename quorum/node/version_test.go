package node

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/quorum"
)

// A pair that mixes releases across a vote protocol version can't vote,
// and must say so (redundancy.version-mismatch, #1079 review), not fail
// to elect in silence.

// asVersion re-seals frame b as protocol version v.
func asVersion(b []byte, v byte) []byte {
	body := append([]byte(nil), b[:len(b)-4]...)
	body[0] = v
	return binary.BigEndian.AppendUint32(body, crc32.Checksum(body, crc32.MakeTable(crc32.Castagnoli)))
}

// A frame of another version is refused as a version mismatch (409,
// "version"), naming both versions, never as a bad frame.
func TestWitnessRefusesAnotherVersionAsSuch(t *testing.T) {
	w, stop := startWitness(t, WitnessOptions{Dir: t.TempDir(), Lease: time.Second})
	defer stop()
	b := asVersion(voteBody(t, quorum.Request{From: "a", Term: 1, Nonce: 1, ID: 1}), 1)
	r := httptest.NewRequest(http.MethodPost, Prefix+testCluster+"/vote", bytes.NewReader(b))
	r.Header.Set(LeaseHeader, strconv.FormatInt(int64(time.Second), 10))
	r.Header.Set(MembersHeader, "a,b")
	rec := httptest.NewRecorder()
	w.Handler().ServeHTTP(rec, r)
	if rec.Code != http.StatusConflict || rec.Header().Get(RefusedHeader) != "version" ||
		!bytes.Contains(rec.Body.Bytes(), []byte("version 1 and this witness 2")) {
		t.Fatalf("an old frame: %d %q %q; want 409 \"version\" naming both", rec.Code, rec.Header().Get(RefusedHeader), rec.Body.String())
	}
}

// The sender hears it either way: a voter of this release refuses with
// "version", and one of an older release, which can't, answers 400.
func TestVersionMismatchReportedToTheSender(t *testing.T) {
	for _, c := range []struct {
		name   string
		answer func(w http.ResponseWriter)
	}{
		{"refused as a version", func(w http.ResponseWriter) { refuse(w, "version", "a speaks vote protocol version 3") }},
		{"an older release's bad frame", func(w http.ResponseWriter) { http.Error(w, "bad frame", http.StatusBadRequest) }},
		{"a reply of another version", func(w http.ResponseWriter) {
			b, _ := quorum.Reply{From: WitnessID, To: "a", Term: 9, Nonce: 1, ID: 99}.MarshalBinary()
			writeFrame(w, asVersion(b, 1))
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { c.answer(w) }))
			defer srv.Close()
			u, _ := url.Parse(srv.URL)
			o := testOptions(t, "a", "b")
			o.WitnessAddr = u.Host
			var mu sync.Mutex
			var evs []Event
			o.OnEvent = func(ev Event) { mu.Lock(); evs = append(evs, ev); mu.Unlock() }
			e, err := New(o)
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			running(t, e.Run)
			waitFor(t, time.Second, e.running.Load)
			e.vote(WitnessID, e.wit, quorum.Request{From: "a", Term: 9, Nonce: e.nonce, ID: 99})
			waitFor(t, 2*time.Second, func() bool {
				mu.Lock()
				defer mu.Unlock()
				for _, ev := range evs {
					if ev.Kind == EventVersionMismatch {
						return true
					}
				}
				return false
			})
		})
	}
}

// A controller refuses its peer's frame of another version the same way,
// and raises the event on its own side too.
func TestControllerRefusesAnotherVersionAsSuch(t *testing.T) {
	o := testOptions(t, "a", "b")
	var mu sync.Mutex
	var evs []Event
	o.OnEvent = func(ev Event) { mu.Lock(); evs = append(evs, ev); mu.Unlock() }
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	running(t, e.Run)
	waitFor(t, time.Second, e.running.Load)
	for _, path := range []string{"/vote", "/adopt"} {
		var b []byte
		if path == "/vote" {
			b = voteBody(t, quorum.Request{From: "b", Term: 1, Nonce: 1, ID: 1})
		} else {
			b, _ = quorum.Adopt{From: "b", Kind: quorum.State}.MarshalBinary()
		}
		r := httptest.NewRequest(http.MethodPost, Prefix+testCluster+path, bytes.NewReader(asVersion(b, 1)))
		r.Header.Set(LeaseHeader, strconv.FormatInt(int64(o.Lease), 10))
		rec := httptest.NewRecorder()
		e.Handler().ServeHTTP(rec, r)
		if rec.Code != http.StatusConflict || rec.Header().Get(RefusedHeader) != "version" {
			t.Fatalf("%s, an old frame: %d %q; want 409 \"version\"", path, rec.Code, rec.Header().Get(RefusedHeader))
		}
	}
	waitFor(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, ev := range evs {
			if ev.Kind == EventVersionMismatch {
				return true
			}
		}
		return false
	})
}
