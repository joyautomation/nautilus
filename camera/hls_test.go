//go:build !js

package camera

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
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
	s := NewHLSStream()
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
	for _, want := range []string{"#EXTM3U", "#EXT-X-VERSION:7", "#EXT-X-TARGETDURATION:", "#EXT-X-MEDIA-SEQUENCE:1", `#EXT-X-MAP:URI="init.mp4"`, "seg-1.m4s", "seg-2.m4s"} {
		if !strings.Contains(pl, want) {
			t.Errorf("the playlist lacks %q:\n%s", want, pl)
		}
	}
	// the clip has a keyframe a second and the target is two seconds: segments of
	// twenty pictures, the first being the first two seconds'
	init := s.Init()
	bs := boxes(t, init)
	if len(bs) != 2 || bs[0].typ != "ftyp" || bs[1].typ != "moov" {
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
	if len(fb) != 2 || fb[0].typ != "moof" || fb[1].typ != "mdat" {
		t.Fatalf("the fragment's boxes: %v", fb)
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
	if !bytes.Equal(fb[1].body, want) {
		t.Errorf("the mdat holds %d bytes, want the first 20 pictures' %d", len(fb[1].body), len(want))
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

func TestPicturesBeforeTheFirstKeyframeAreDroppedAndAHugeRunIsBounded(t *testing.T) {
	s, aus := clipStream(t)
	// start in the middle of a GOP
	for _, au := range aus[5:] {
		s.Feed(au)
	}
	if _, _, dropped := s.Stats(); dropped == 0 {
		t.Error("pictures before a keyframe were kept")
	}
	// no keyframe for ever: the run is dropped at the bound, not grown
	s2, aus2 := clipStream(t)
	big := AccessUnit{NALs: [][]byte{append([]byte{nalIDR | 0x60}, make([]byte, 3<<20)...)}, Key: true}
	pic := AccessUnit{NALs: [][]byte{append([]byte{nalSlice | 0x60}, make([]byte, 3<<20)...)}}
	big.TS = aus2[0].TS
	s2.Feed(big)
	for i := 1; i < 50; i++ {
		pic.TS = uint32(i) * 9000
		s2.Feed(pic)
		if _, held, _ := s2.Stats(); held > hlsMaxSegBytes+8<<20 {
			t.Fatalf("held %d bytes of one segment", held)
		}
	}
	if _, _, dropped := s2.Stats(); dropped == 0 {
		t.Error("a run that cannot be segmented was never dropped")
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
