//go:build !js

package camera

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// boxes reads the top-level boxes of b.
func boxes(t *testing.T, b []byte) []struct {
	typ  string
	body []byte
} {
	t.Helper()
	var out []struct {
		typ  string
		body []byte
	}
	for len(b) > 0 {
		if len(b) < 8 {
			t.Fatalf("a box header is cut short: % x", b)
		}
		n := int(binary.BigEndian.Uint32(b))
		if n < 8 || n > len(b) {
			t.Fatalf("a box of %d bytes in %d", n, len(b))
		}
		out = append(out, struct {
			typ  string
			body []byte
		}{string(b[4:8]), b[8:n]})
		b = b[n:]
	}
	return out
}

func clipStream(t *testing.T) (*HLSStream, []AccessUnit) {
	t.Helper()
	aus := clipAUs(t)
	var sps, pps []byte
	for _, n := range aus[0].NALs {
		switch nalType(n[0]) {
		case nalSPS:
			sps = n
		case nalPPS:
			pps = n
		}
	}
	s := NewHLSStream(nil)
	if err := s.SetParams(sps, pps); err != nil {
		t.Fatal(err)
	}
	return s, aus
}

func TestTheClipBecomesAnHLSPlaylistOfWholeGOPSegments(t *testing.T) {
	s, aus := clipStream(t)
	if s.Playlist() != "" {
		t.Error("a playlist before any segment")
	}
	for _, au := range aus {
		s.Feed(au)
	}
	pl := s.Playlist()
	for _, want := range []string{"#EXTM3U", "#EXT-X-VERSION:7", "#EXT-X-INDEPENDENT-SEGMENTS", "#EXT-X-TARGETDURATION:", "#EXT-X-MEDIA-SEQUENCE:1", `#EXT-X-MAP:URI="init.mp4"`, "seg-1.m4s", "seg-2.m4s"} {
		if !strings.Contains(pl, want) {
			t.Errorf("the playlist lacks %q:\n%s", want, pl)
		}
	}
	// the clip has a keyframe a second and the target is two seconds: segments of
	// twenty pictures, the first being the first two seconds'
	init := s.Init()
	bs := boxes(t, init)
	if len(bs) != 2 || bs[0].typ != "ftyp" || bs[1].typ != "moov" || !bytes.HasPrefix(bs[0].body, []byte("iso5")) {
		t.Fatalf("the init segment's boxes: %v", bs)
	}
	if !bytes.Contains(init, []byte("avc1")) || !bytes.Contains(init, []byte("avcC")) {
		t.Error("the init segment has no avc1/avcC")
	}
	seg, ok := s.Segment(1)
	if !ok {
		t.Fatal("no segment 1")
	}
	fb := boxes(t, seg)
	if len(fb) != 4 || fb[0].typ != "styp" || fb[1].typ != "sidx" || fb[2].typ != "moof" || fb[3].typ != "mdat" {
		t.Fatalf("the segment's boxes: %v", fb)
	}
	// the sidx says what the segment is: one reference, its size and its duration
	if sx := fb[1].body; len(sx) != 44 || binary.BigEndian.Uint32(sx[4:8]) != 1 || binary.BigEndian.Uint32(sx[8:12]) != VideoTimescale ||
		binary.BigEndian.Uint32(sx[36:40]) != uint32(180000) || binary.BigEndian.Uint32(sx[32:36])&0x7fffffff != uint32(8+len(fb[2].body)+8+len(fb[3].body)) {
		t.Errorf("the sidx: % x", sx)
	}
	// the samples in the mdat are the clip's pictures, length-prefixed, parameter sets left out
	var want []byte
	n := 0
	for _, au := range aus {
		if n >= 20 {
			break
		}
		want = append(want, AVCC(au.NALs)...)
		n++
	}
	if !bytes.Equal(fb[3].body, want) {
		t.Errorf("the mdat holds %d bytes, want the first 20 pictures' %d", len(fb[3].body), len(want))
	}
	if _, ok := s.Segment(99); ok {
		t.Error("a segment that was never made")
	}
}

// Real tools open the segments: when ffprobe is on the machine (it is not needed
// by CI), it must read the init segment and a fragment as H.264 of the right size.
func TestFFprobeReadsTheSegmentsWhenItIsThere(t *testing.T) {
	ff, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("no ffprobe")
	}
	s, aus := clipStream(t)
	for _, au := range aus {
		s.Feed(au)
	}
	seg, _ := s.Segment(1)
	f := filepath.Join(t.TempDir(), "x.mp4")
	if err := os.WriteFile(f, append(append([]byte(nil), s.Init()...), seg...), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(ff, "-v", "error", "-count_frames", "-show_entries", "stream=codec_name,width,height,nb_read_frames", "-of", "csv=p=0", f).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "h264,160,120,20") {
		t.Errorf("ffprobe: %q %v, want h264,160,120 and 20 frames", out, err)
	}
}

func TestPicturesBeforeTheFirstKeyframeAreDroppedAndAGroupThatDoesNotFitEndsTheStream(t *testing.T) {
	s, aus := clipStream(t)
	// start in the middle of a GOP
	for _, au := range aus[5:] {
		s.Feed(au)
	}
	if _, _, dropped := s.Stats(); dropped == 0 {
		t.Error("pictures before a keyframe were kept")
	}
	// a group of pictures bigger than a segment, twice: the stream ends and says why
	s2, aus2 := clipStream(t)
	big := AccessUnit{NALs: [][]byte{append([]byte{nalIDR | 0x60}, make([]byte, 1<<20)...)}, Key: true}
	pic := AccessUnit{NALs: [][]byte{append([]byte{nalSlice | 0x60}, make([]byte, 1<<20)...)}}
	big.TS = aus2[0].TS
	for i := 0; i < 60 && s2.Err() == nil; i++ {
		if i%5 == 0 {
			big.TS = uint32(i) * 9000
			s2.Feed(big)
		} else {
			pic.TS = uint32(i) * 9000
			s2.Feed(pic)
		}
		if _, held, _ := s2.Stats(); held > hlsMaxSegBytes+2<<20 {
			t.Fatalf("held %d bytes of one segment", held)
		}
	}
	if err := s2.Err(); err != ErrKeyframes {
		t.Errorf("a camera whose groups never fit a segment: %v", err)
	}
}

// Pictures of one byte, a tick apart, never reach the two-second cut: the segment
// is bounded by its picture count, the work does not grow with the count, and the
// bookkeeping of each picture is counted.
func TestAFloodOfTinyPicturesIsBoundedInCountAndTime(t *testing.T) {
	s, aus := clipStream(t)
	b := NewMemBudget(DefaultLiveBudget)
	s.budget = b
	s.Feed(AccessUnit{TS: 0, Key: true, NALs: [][]byte{{nalIDR | 0x60, 0x88}}, Size: 2})
	start := time.Now()
	for i := 1; i < 200000; i++ {
		s.Feed(AccessUnit{TS: uint32(i), NALs: [][]byte{{nalSlice | 0x60, 1}}, Size: 2})
		if s.Err() != nil {
			break
		}
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("200,000 tiny pictures took %v: the work is not linear", time.Since(start))
	}
	_, held, _ := s.Stats()
	if held > hlsMaxSegBytes || held/sampleOverhead > hlsMaxSegSamples+1 {
		t.Errorf("%d bytes held for tiny pictures", held)
	}
	_ = aus
}

// The streams of a controller share a budget: one that would pass it ends with the
// reason, and what a stream held goes back when it ends.
func TestTheLiveStreamsShareOneMemoryBudget(t *testing.T) {
	aus := clipAUs(t)
	var sps, pps []byte
	for _, n := range aus[0].NALs {
		switch nalType(n[0]) {
		case nalSPS:
			sps = n
		case nalPPS:
			pps = n
		}
	}
	b := NewMemBudget(2*StreamFixedBytes + 1<<20)
	mk := func() *HLSStream {
		s := NewHLSStream(b)
		if s.Err() == nil {
			s.SetParams(sps, pps)
		}
		return s
	}
	a, c := mk(), mk()
	if a.Err() != nil || c.Err() != nil {
		t.Fatalf("two streams in a budget of two: %v %v", a.Err(), c.Err())
	}
	if third := mk(); third.Err() != ErrStreamTooLarge {
		t.Errorf("a third stream: %v, want ErrStreamTooLarge", third.Err())
	}
	if b.Used() != 2*StreamFixedBytes {
		t.Errorf("the budget holds %d, want the two streams' %d", b.Used(), 2*StreamFixedBytes)
	}
	// pictures count too: feed one until it passes what is left
	for i := 0; i < 300 && a.Err() == nil; i++ {
		a.Feed(AccessUnit{TS: uint32(i) * 9000, Key: i == 0, NALs: [][]byte{append([]byte{nalIDR | 0x60}, make([]byte, 400<<10)...)}, Size: 400 << 10})
	}
	if a.Err() != ErrStreamTooLarge {
		t.Errorf("a stream that outgrew the budget: %v", a.Err())
	}
	a.Close(nil)
	c.Close(nil)
	if b.Used() != 0 {
		t.Errorf("%d bytes still reserved after both ended", b.Used())
	}
}

func TestTheWindowAndItsBytesAreBounded(t *testing.T) {
	s, aus := clipStream(t)
	// many GOPs: repeat the clip with rising timestamps
	var ts uint32
	for round := 0; round < 40; round++ {
		for _, au := range aus {
			au.TS = ts
			ts += 9000
			s.Feed(au)
		}
	}
	segs, held, _ := s.Stats()
	if segs > hlsWindow || held > hlsMaxWindowBytes {
		t.Errorf("%d segments, %d bytes held after 40 clips", segs, held)
	}
	pl := s.Playlist()
	if strings.Count(pl, "#EXTINF") != segs || strings.Contains(pl, "MEDIA-SEQUENCE:1\n") {
		t.Errorf("the playlist does not slide:\n%s", pl)
	}
	// the sequence a player asks for next is always one the window still has or a newer one
	first := strings.Split(strings.Split(pl, "MEDIA-SEQUENCE:")[1], "\n")[0]
	if _, ok := s.Segment(1); ok || first == "1" {
		t.Error("segment 1 is still served after the window slid")
	}
}

func TestWaitReturnsWhenTheFirstSegmentsAreReadyOrTheStreamEnds(t *testing.T) {
	s, aus := clipStream(t)
	done := make(chan error, 1)
	go func() { done <- s.Wait(context.Background()) }()
	select {
	case <-done:
		t.Fatal("Wait returned before any segment")
	case <-time.After(50 * time.Millisecond):
	}
	for _, au := range aus {
		s.Feed(au)
	}
	if err := <-done; err != nil {
		t.Errorf("Wait: %v", err)
	}
	// a stream that ends early says why
	s2, _ := clipStream(t)
	go func() { time.Sleep(30 * time.Millisecond); s2.Close(os.ErrDeadlineExceeded) }()
	if err := s2.Wait(context.Background()); err == nil {
		t.Error("a stream that failed did not say so")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	s3, _ := clipStream(t)
	if err := s3.Wait(ctx); err == nil {
		t.Error("Wait ignored its context")
	}
	if s2.Playlist() != "" {
		t.Error("a closed stream still serves a playlist")
	}
}

func TestSegmentNamesAreOnlyTheOnesThePlaylistMakes(t *testing.T) {
	for in, want := range map[string]uint64{"seg-1.m4s": 1, "seg-42.m4s": 42, "seg-0.m4s": 0} {
		if n, ok := ParseSegName(in); !ok || n != want {
			t.Errorf("%s: %d %v", in, n, ok)
		}
	}
	for _, bad := range []string{"", "seg-.m4s", "seg-01.m4s", "seg-1.mp4", "seg--1.m4s", "seg-1.m4s/..", "../seg-1.m4s", "seg-99999999999999999999.m4s", "seg-1 .m4s", "SEG-1.m4s", "seg-1.m4s?x"} {
		if _, ok := ParseSegName(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}

// The sets in a camera's description and the ones in its stream may differ: before
// any picture the stream's are taken, after one the stream ends (a player's buffers
// cannot follow a change of settings).
func TestParameterSetsFromTheStreamReplaceTheDescriptionsBeforeAnyPicture(t *testing.T) {
	s, aus := clipStream(t)
	var sps, pps []byte
	for _, n := range aus[0].NALs {
		switch nalType(n[0]) {
		case nalSPS:
			sps = n
		case nalPPS:
			pps = n
		}
	}
	other := append([]byte(nil), sps...)
	other[3]++ // another level: a different, valid set
	if err := s.SetParams(other, pps); err != nil {
		t.Fatalf("before any picture: %v", err)
	}
	if !bytes.Contains(s.Init(), other[1:4]) {
		t.Error("the init segment was not remade from the stream's sets")
	}
	for _, au := range aus[:3] {
		s.Feed(au)
	}
	s.SetParams(other, pps) // the same again: nothing
	if s.Err() != nil {
		t.Fatalf("the same sets again ended the stream: %v", s.Err())
	}
	s.SetParams(sps, pps) // changed after pictures were taken
	if s.Err() == nil || !strings.Contains(s.Err().Error(), "changed its picture settings") {
		t.Errorf("a change after the first pictures: %v", s.Err())
	}
}

// What the budget counts is what the heap holds: pictures made of megabyte
// delimiters (which the segment leaves out) and one byte of slice must not keep the
// delimiters' capacity alive behind a count of 70 bytes a picture.
func TestTheBudgetCountsWhatTheHeapHolds(t *testing.T) {
	s, aus := clipStream(t)
	b := NewMemBudget(DefaultLiveBudget)
	s.budget = b
	d := &Depacketizer{}
	_ = aus
	var ms0, ms1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&ms0)
	aud := append([]byte{nalAUD | 0x60}, make([]byte, 1<<20-2)...)
	seq := uint16(1)
	for i := 0; i < 400 && s.Err() == nil; i++ {
		ts := uint32(i) * 9000
		d.Push(RTPPacket{Seq: seq, TS: ts, Payload: aud}, func(AccessUnit) {})
		seq++
		d.Push(RTPPacket{Seq: seq, TS: ts, Payload: aud}, func(AccessUnit) {})
		seq++
		slice := []byte{nalSlice | 0x60, 0x88}
		if i%10 == 0 {
			slice = []byte{nalIDR | 0x60, 0x88}
		}
		d.Push(RTPPacket{Seq: seq, TS: ts, Marker: true, Payload: slice}, s.Feed)
		seq++
	}
	runtime.GC()
	runtime.ReadMemStats(&ms1)
	grew := int64(ms1.HeapAlloc) - int64(ms0.HeapAlloc)
	if grew > b.Used()+StreamMemoryBound+8<<20 {
		t.Errorf("the heap grew %d MB while the budget counts %d MB", grew>>20, b.Used()>>20)
	}
	if s.Err() == nil && grew > 80<<20 {
		t.Errorf("the heap grew %d MB with the stream healthy", grew>>20)
	}
}
