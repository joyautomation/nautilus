//go:build !js

package camera

import (
	"errors"
)

// H.264 (ITU-T H.264 / ISO 14496-10) as far as a relay needs it: NAL unit
// types, the picture size from a sequence parameter set, and which access units
// start a picture a player can begin at. The controller never decodes video.

// NAL unit types.
const (
	nalSlice    = 1
	nalIDR      = 5
	nalSEI      = 6
	nalSPS      = 7
	nalPPS      = 8
	nalAUD      = 9
	nalSTAPA    = 24
	nalFUA      = 28
	maxNALBytes = 1 << 20 // one NAL unit; a camera that sends more is refused
	maxAUBytes  = 2 << 20 // one access unit (a picture)
)

func nalType(b byte) int { return int(b & 0x1f) }

// unescape removes emulation-prevention bytes (00 00 03 -> 00 00): an SPS is
// read as the raw bit string.
func unescape(b []byte) []byte {
	out := make([]byte, 0, len(b))
	zeros := 0
	for _, c := range b {
		if zeros >= 2 && c == 3 {
			zeros = 0
			continue
		}
		out = append(out, c)
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}

type bitReader struct {
	b   []byte
	pos int // in bits
	err error
}

func (r *bitReader) bit() uint {
	if r.pos>>3 >= len(r.b) {
		r.err = errors.New("h264: the parameter set is cut short")
		return 0
	}
	v := uint(r.b[r.pos>>3]>>(7-uint(r.pos&7))) & 1
	r.pos++
	return v
}

func (r *bitReader) bits(n int) uint {
	var v uint
	for i := 0; i < n && r.err == nil; i++ {
		v = v<<1 | r.bit()
	}
	return v
}

// ue reads an unsigned Exp-Golomb code, bounded so a hostile run of zeros ends it.
func (r *bitReader) ue() uint {
	zeros := 0
	for r.err == nil && r.bit() == 0 {
		zeros++
		if zeros > 31 {
			r.err = errors.New("h264: a code in the parameter set is too long")
			return 0
		}
	}
	if r.err != nil {
		return 0
	}
	return (1 << uint(zeros)) - 1 + r.bits(zeros)
}

func (r *bitReader) se() int {
	k := r.ue()
	if k&1 == 1 {
		return int((k + 1) >> 1)
	}
	return -int(k >> 1)
}

// SPSInfo is what the muxer needs of a sequence parameter set.
type SPSInfo struct {
	Profile, Compat, Level byte
	Width, Height          int
}

// ParseSPS reads the picture size (cropping applied) from a sequence parameter set
// NAL unit (header byte included). It refuses a set it cannot read and sizes a
// player could not take (zero, or over 8192 either way).
func ParseSPS(nal []byte) (SPSInfo, error) {
	if len(nal) < 5 || nalType(nal[0]) != nalSPS {
		return SPSInfo{}, errors.New("h264: not a sequence parameter set")
	}
	if len(nal) > 4096 {
		return SPSInfo{}, errors.New("h264: the sequence parameter set is far too long")
	}
	info := SPSInfo{Profile: nal[1], Compat: nal[2], Level: nal[3]}
	r := &bitReader{b: unescape(nal[4:])}
	r.ue() // seq_parameter_set_id
	chroma := uint(1)
	switch info.Profile {
	case 100, 110, 122, 244, 44, 83, 86, 118, 128, 138, 139, 134, 135:
		chroma = r.ue()
		if chroma == 3 {
			r.bit() // separate_colour_plane_flag
		}
		r.ue()            // bit_depth_luma_minus8
		r.ue()            // bit_depth_chroma_minus8
		r.bit()           // qpprime_y_zero_transform_bypass_flag
		if r.bit() == 1 { // seq_scaling_matrix_present_flag
			n := 8
			if chroma == 3 {
				n = 12
			}
			for i := 0; i < n && r.err == nil; i++ {
				if r.bit() == 1 {
					size := 16
					if i >= 6 {
						size = 64
					}
					last, next := 8, 8
					for j := 0; j < size && r.err == nil; j++ {
						if next != 0 {
							next = (last + r.se() + 256) % 256
						}
						if next != 0 {
							last = next
						}
					}
				}
			}
		}
	}
	r.ue()          // log2_max_frame_num_minus4
	switch r.ue() { // pic_order_cnt_type
	case 0:
		r.ue()
	case 1:
		r.bit()
		r.se()
		r.se()
		n := r.ue()
		if n > 255 {
			return SPSInfo{}, errors.New("h264: the parameter set lists too many reference offsets")
		}
		for i := uint(0); i < n && r.err == nil; i++ {
			r.se()
		}
	}
	r.ue()  // max_num_ref_frames
	r.bit() // gaps_in_frame_num_value_allowed_flag
	wMbs := r.ue() + 1
	hMapUnits := r.ue() + 1
	frameMbsOnly := r.bit()
	if frameMbsOnly == 0 {
		r.bit() // mb_adaptive_frame_field_flag
	}
	r.bit() // direct_8x8_inference_flag
	var cl, cr, ct, cb uint
	if r.bit() == 1 { // frame_cropping_flag
		cl, cr, ct, cb = r.ue(), r.ue(), r.ue(), r.ue()
	}
	if r.err != nil {
		return SPSInfo{}, r.err
	}
	if wMbs > 512 || hMapUnits > 512 {
		return SPSInfo{}, errors.New("h264: the picture is larger than 8192 pixels")
	}
	cropX, cropY := uint(1), uint(2-frameMbsOnly)
	if chroma == 1 || chroma == 2 {
		cropX = 2
	}
	if chroma == 1 {
		cropY *= 2
	}
	w := int(wMbs*16) - int((cl+cr)*cropX)
	h := int((2-frameMbsOnly)*hMapUnits*16) - int((ct+cb)*cropY)
	if w <= 0 || h <= 0 {
		return SPSInfo{}, errors.New("h264: the picture has no size")
	}
	if w > 8192 || h > 8192 { // an interlaced picture is twice the map units' height
		return SPSInfo{}, errors.New("h264: the picture is larger than 8192 pixels")
	}
	info.Width, info.Height = w, h
	return info, nil
}

// sliceFirstMB is first_mb_in_slice of a slice NAL unit (header byte included): 0
// starts a picture. A NAL too short to say returns a number that is not 0.
func sliceFirstMB(nal []byte) uint {
	if len(nal) < 2 {
		return 1 << 31
	}
	r := &bitReader{b: unescape(nal[1:])}
	v := r.ue()
	if r.err != nil {
		return 1 << 31
	}
	return v
}

// startsCleanly reports whether a picture's first NAL unit is where a picture
// begins (a parameter set, delimiter or SEI, or the first slice of an IDR): a
// keyframe that lost its first packets is not one a player can begin at.
func (a AccessUnit) startsCleanly() bool {
	if len(a.NALs) == 0 || len(a.NALs[0]) == 0 {
		return false
	}
	n := a.NALs[0]
	switch nalType(n[0]) {
	case nalSPS, nalPPS, nalSEI, nalAUD:
		return true
	case nalIDR:
		return sliceFirstMB(n) == 0
	}
	return false
}
