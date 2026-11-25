package runtime

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"math"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
)

// The wire form of an Image (EncodeImage, DecodeImage), version 4:
//
//	"nsi" format
//	uvarint term, uvarint seq, str build, bytes retain
//	uvarint tasks, then per task:
//	  str task, str layout, varint nowMs, uvarint scans,
//	  uvarint leaves, then per leaf: kind byte, payload
//	  (format 2) str source, tags
//	(format 2) str tagsLayout, varint tagsNowMs, tags
//	(format 3) uvarint hold: 0 none, else the takeover hold's age in ms + 1
//	(format 4) uvarint edge session: Image.EdgeSession
//	uint32 CRC-32C (Castagnoli) of everything before it, little endian
//
// tags is a uvarint count, then per tag a uvarint place in the carried set
// and its leaves (a uvarint count, then each leaf). Format 1 is the same
// without the parts marked format 2 (#710 S1b): a leader sends it to a
// standby that does not ask for 2, so a pair mid rolling update still
// carries its retained values; such an image is never taken over hot.
// Format 2 is format 3 without the hold (#710 (b2)): a hot takeover needs
// the same build anyway, so a format 2 image inherits no hold. Format 3 is
// format 4 without the edge session (#798): its standby learns none.
//
// A str or bytes is a uvarint length and the bytes. A leaf's payload by
// kind: BOOL one byte (0 or 1); an integer a varint and a str (an
// enumeration member's name, empty otherwise); TIME a varint; REAL eight
// bytes, the IEEE 754 bits little endian; STRING a str; a value with no
// kind nothing.
//
// The checksum is end to end, over what the leader encoded: peer TLS
// already protects the link, so a mismatch means a bug or a torn buffer,
// and the standby asks for a full image again.
const imageFormat = 4

// ImageFormats are the wire formats this build reads, oldest first; a
// standby names them when it asks for the stream, and the leader sends
// the newest one both read (ImageFormatFor).
var ImageFormats = []byte{1, 2, 3, imageFormat}

// ImageFormatFor is the format a leader of this build sends a standby that
// reads formats (as its stream request names them): the newest both read,
// and 1 for a standby that names none (a build before #710 S2).
func ImageFormatFor(formats []byte) byte {
	best := byte(1)
	for _, f := range formats {
		if f > best && f <= imageFormat {
			best = f
		}
	}
	return best
}

var imageMagic = [3]byte{'n', 's', 'i'}

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// ErrImage is returned by DecodeImage for bytes that are not an image it
// can read: another format, a bad checksum, a truncated or malformed body.
var ErrImage = errors.New("runtime: not a readable state image")

// MaxImageBytes bounds a whole image on the wire, and maxImageString one
// string in it (a task or build name, a layout, a STRING leaf, an
// enumeration member). EncodeImage refuses to make a bigger one and
// DecodeImage to read one, so a decode allocates at most 32 bytes a byte
// received (a leaf is 32 bytes in memory and at least one on the wire):
// 256 MiB at worst, from a peer that holds the pair's token.
const (
	MaxImageBytes  = 8 << 20
	maxImageString = 1 << 20
)

// ErrImageTooBig is returned by EncodeImage for an image over the limits:
// the stream reports it rather than send what no standby would read.
var ErrImageTooBig = errors.New("runtime: the state image is over its size limit")

// ImageTooBigError is EncodeImage's ErrImageTooBig with its numbers:
// What is over (the image, or one string in it), Bytes how big, Limit the
// limit. errors.Is(err, ErrImageTooBig) holds for it.
type ImageTooBigError struct {
	What         string
	Bytes, Limit int
}

func (e *ImageTooBigError) Error() string {
	return fmt.Sprintf("%v: %s of %d bytes, the limit is %d", ErrImageTooBig, e.What, e.Bytes, e.Limit)
}

func (e *ImageTooBigError) Is(target error) bool { return target == ErrImageTooBig }

// EncodeImage appends img's wire form to dst and returns it. With a dst
// whose capacity a previous image filled, it allocates nothing. An image
// over MaxImageBytes, or with a string over maxImageString (a long STRING
// variable), is refused with ErrImageTooBig and dst comes back unchanged.
func EncodeImage(dst []byte, img *Image) ([]byte, error) {
	return EncodeImageFormat(dst, img, imageFormat)
}

// EncodeImageFormat is EncodeImage in a given wire format: 1 leaves out
// the tags and the sources (for a standby of a build before #710 S2).
func EncodeImageFormat(dst []byte, img *Image, format byte) ([]byte, error) {
	if format < 1 || format > imageFormat {
		return dst, fmt.Errorf("runtime: no image format %d, this build writes 1 to %d", format, imageFormat)
	}
	start := len(dst)
	if err := imageStringsFit(img); err != nil {
		return dst, err
	}
	dst = append(dst, imageMagic[:]...)
	dst = append(dst, format)
	dst = binary.AppendUvarint(dst, img.Term)
	dst = binary.AppendUvarint(dst, img.Seq)
	dst = appendStr(dst, img.Build)
	dst = binary.AppendUvarint(dst, uint64(len(img.Retain)))
	dst = append(dst, img.Retain...)
	dst = binary.AppendUvarint(dst, uint64(len(img.Tasks)))
	for i := range img.Tasks {
		t := &img.Tasks[i]
		dst = appendStr(dst, t.Task)
		dst = appendStr(dst, t.Layout)
		dst = binary.AppendVarint(dst, t.NowMs)
		dst = binary.AppendUvarint(dst, t.Scans)
		dst = binary.AppendUvarint(dst, uint64(len(t.Leaves)))
		for j := range t.Leaves {
			dst = appendLeaf(dst, &t.Leaves[j])
		}
		if format >= 2 {
			dst = appendStr(dst, t.Source)
			dst = appendTags(dst, &t.Tags)
		}
	}
	if format >= 2 {
		dst = appendStr(dst, img.TagsLayout)
		dst = binary.AppendVarint(dst, img.TagsNowMs)
		dst = appendTags(dst, &img.Tags)
	}
	if format >= 3 {
		// The takeover hold: 0 for none, else its age in ms plus one.
		var hold uint64
		if img.Holding {
			hold = uint64(img.HoldAge.Milliseconds()) + 1
		}
		dst = binary.AppendUvarint(dst, hold)
	}
	if format >= 4 {
		dst = binary.AppendUvarint(dst, img.EdgeSession)
	}
	if n := len(dst) - start + 4; n > MaxImageBytes && !mutRuntime("image_size_unbounded") {
		return dst[:start], &ImageTooBigError{What: "the image", Bytes: n, Limit: MaxImageBytes}
	}
	sum := crc32.Checksum(dst[start:], castagnoli)
	if mutRuntime("image_crc_unchecked") {
		sum = 0
	}
	return binary.LittleEndian.AppendUint32(dst, sum), nil
}

// imageStringsFit checks every string against maxImageString before
// anything is written.
func imageStringsFit(img *Image) error {
	if mutRuntime("image_size_unbounded") {
		return nil
	}
	long := func(what string, n int) error {
		if n > maxImageString {
			return &ImageTooBigError{What: what, Bytes: n, Limit: maxImageString}
		}
		return nil
	}
	if err := long("the build name", len(img.Build)); err != nil {
		return err
	}
	for i := range img.Tasks {
		t := &img.Tasks[i]
		if err := long("a task name", len(t.Task)); err != nil {
			return err
		}
		if err := long("a layout", len(t.Layout)); err != nil {
			return err
		}
		for j := range t.Leaves {
			if n := len(t.Leaves[j].S); n > maxImageString {
				return long("task "+t.Task+": a STRING value", n)
			}
		}
		if err := long("a source hash", len(t.Source)); err != nil {
			return err
		}
		for j := range t.Tags.Leaves {
			if n := len(t.Tags.Leaves[j].S); n > maxImageString {
				return long("task "+t.Task+": a STRING tag", n)
			}
		}
	}
	if err := long("the tags' layout", len(img.TagsLayout)); err != nil {
		return err
	}
	for j := range img.Tags.Leaves {
		if n := len(img.Tags.Leaves[j].S); n > maxImageString {
			return long("a STRING tag", n)
		}
	}
	return nil
}

func appendTags(dst []byte, tv *TagValues) []byte {
	dst = binary.AppendUvarint(dst, uint64(len(tv.Idx)))
	for i, place := range tv.Idx {
		leaves := tv.leavesOf(i)
		dst = binary.AppendUvarint(dst, uint64(place))
		dst = binary.AppendUvarint(dst, uint64(len(leaves)))
		for j := range leaves {
			dst = appendLeaf(dst, &leaves[j])
		}
	}
	return dst
}

func appendStr(dst []byte, s string) []byte {
	dst = binary.AppendUvarint(dst, uint64(len(s)))
	return append(dst, s...)
}

func appendLeaf(dst []byte, l *ir.Leaf) []byte {
	dst = append(dst, byte(l.Kind))
	switch l.Kind {
	case ir.TypeBool:
		return append(dst, byte(l.Bits&1))
	case ir.TypeInt:
		dst = binary.AppendVarint(dst, int64(l.Bits))
		return appendStr(dst, l.S)
	case ir.TypeTime:
		return binary.AppendVarint(dst, int64(l.Bits))
	case ir.TypeReal:
		return binary.LittleEndian.AppendUint64(dst, l.Bits)
	case ir.TypeString:
		return appendStr(dst, l.S)
	}
	return dst
}

// DecodeImage reads one image's wire form into img, reusing its Tasks and
// their leaf buffers. On an error img is left partly written; the caller
// discards it. It checks the checksum before it reads anything else.
func DecodeImage(b []byte, img *Image) error {
	if len(b) < len(imageMagic)+1+4 {
		return fmt.Errorf("%w: %d bytes", ErrImage, len(b))
	}
	if len(b) > MaxImageBytes && !mutRuntime("image_size_unbounded") {
		return fmt.Errorf("%w: %d bytes, the limit is %d", ErrImage, len(b), MaxImageBytes)
	}
	body, tail := b[:len(b)-4], b[len(b)-4:]
	if [3]byte(body[:3]) != imageMagic {
		return fmt.Errorf("%w: no image header", ErrImage)
	}
	format := body[3]
	if format < 1 || format > imageFormat {
		return fmt.Errorf("%w: format %d, this build reads 1 to %d", ErrImage, format, imageFormat)
	}
	if got, want := crc32.Checksum(body, castagnoli), binary.LittleEndian.Uint32(tail); got != want && !mutRuntime("image_crc_unchecked") {
		return fmt.Errorf("%w: checksum %08x, the image says %08x", ErrImage, got, want)
	}
	d := imageReader{b: body[4:]}
	img.Format = format
	img.TagsLayout, img.TagsNowMs = "", 0
	img.Tags.reset()
	img.Holding, img.HoldAge = false, 0
	img.EdgeSession = 0
	img.Term = d.uvarint()
	img.Seq = d.uvarint()
	img.Build = d.str()
	n := d.uvarint()
	if d.err == nil && n > uint64(len(d.b)) {
		d.fail("retain section of %d bytes", n)
	}
	if d.err == nil {
		img.Retain = append(img.Retain[:0], d.b[:n]...)
		d.b = d.b[n:]
	}
	tasks := d.uvarint()
	if d.err == nil && tasks > uint64(len(d.b)) {
		d.fail("%d tasks", tasks)
	}
	if d.err != nil {
		return d.err
	}
	if uint64(cap(img.Tasks)) < tasks {
		img.Tasks = append(img.Tasks[:cap(img.Tasks)], make([]TaskImage, int(tasks)-cap(img.Tasks))...)
	}
	img.Tasks = img.Tasks[:tasks]
	for i := range img.Tasks {
		t := &img.Tasks[i]
		t.Task = d.str()
		t.Layout = d.str()
		t.NowMs = d.varint()
		t.Scans = d.uvarint()
		leaves := d.uvarint()
		if d.err == nil && leaves > uint64(len(d.b)) { // a leaf is at least its kind byte
			d.fail("%d leaves", leaves)
		}
		if d.err != nil {
			return d.err
		}
		if uint64(cap(t.Leaves)) < leaves {
			t.Leaves = make([]ir.Leaf, leaves)
		}
		t.Leaves = t.Leaves[:leaves]
		for j := range t.Leaves {
			d.leaf(&t.Leaves[j])
			if d.err != nil {
				return d.err
			}
		}
		t.Source = ""
		t.Tags.reset()
		if format >= 2 {
			t.Source = d.str()
			d.tags(&t.Tags)
			if d.err != nil {
				return d.err
			}
		}
	}
	if format >= 2 {
		img.TagsLayout = d.str()
		img.TagsNowMs = d.varint()
		d.tags(&img.Tags)
	}
	if format >= 3 {
		if hold := d.uvarint(); d.err == nil && hold > 0 {
			if hold-1 > uint64(math.MaxInt64/time.Millisecond) {
				d.fail("a hold age of %d ms", hold-1)
			} else {
				img.Holding, img.HoldAge = true, time.Duration(hold-1)*time.Millisecond
			}
		}
	}
	if format >= 4 {
		img.EdgeSession = d.uvarint()
	}
	if d.err == nil && len(d.b) != 0 {
		d.fail("%d bytes after the last task", len(d.b))
	}
	return d.err
}

type imageReader struct {
	b   []byte
	err error
}

func (d *imageReader) fail(format string, a ...any) {
	if d.err == nil {
		d.err = fmt.Errorf("%w: "+format, append([]any{ErrImage}, a...)...)
	}
}

func (d *imageReader) uvarint() uint64 {
	if d.err != nil {
		return 0
	}
	v, n := binary.Uvarint(d.b)
	if n <= 0 {
		d.fail("truncated")
		return 0
	}
	d.b = d.b[n:]
	return v
}

func (d *imageReader) varint() int64 {
	if d.err != nil {
		return 0
	}
	v, n := binary.Varint(d.b)
	if n <= 0 {
		d.fail("truncated")
		return 0
	}
	d.b = d.b[n:]
	return v
}

func (d *imageReader) str() string {
	n := d.uvarint()
	if d.err != nil {
		return ""
	}
	if n > maxImageString || n > uint64(len(d.b)) {
		d.fail("string of %d bytes", n)
		return ""
	}
	s := string(d.b[:n])
	d.b = d.b[n:]
	return s
}

// tags reads a tags section into tv, reusing its storage.
func (d *imageReader) tags(tv *TagValues) {
	tv.reset()
	n := d.uvarint()
	if d.err == nil && n > uint64(len(d.b)) { // a tag is at least two bytes
		d.fail("%d tags", n)
	}
	for range n {
		if d.err != nil {
			return
		}
		place := d.uvarint()
		leaves := d.uvarint()
		if d.err == nil && (leaves > uint64(len(d.b)) || place > uint64(MaxImageBytes)) {
			d.fail("a tag at place %d of %d leaves", place, leaves)
		}
		if d.err != nil {
			return
		}
		for range leaves {
			tv.Leaves = append(tv.Leaves, ir.Leaf{})
			d.leaf(&tv.Leaves[len(tv.Leaves)-1])
			if d.err != nil {
				return
			}
		}
		tv.Idx = append(tv.Idx, int(place))
		tv.Ends = append(tv.Ends, len(tv.Leaves))
	}
}

func (d *imageReader) byte1() byte {
	if d.err != nil {
		return 0
	}
	if len(d.b) == 0 {
		d.fail("truncated")
		return 0
	}
	c := d.b[0]
	d.b = d.b[1:]
	return c
}

func (d *imageReader) leaf(l *ir.Leaf) {
	*l = ir.Leaf{Kind: ir.TypeKind(d.byte1())}
	switch l.Kind {
	case ir.TypeVoid:
	case ir.TypeBool:
		switch d.byte1() {
		case 0:
		case 1:
			l.Bits = 1
		default:
			d.fail("a BOOL that is neither 0 nor 1")
		}
	case ir.TypeInt:
		l.Bits = uint64(d.varint())
		l.S = d.str()
	case ir.TypeTime:
		l.Bits = uint64(d.varint())
	case ir.TypeReal:
		if d.err == nil && len(d.b) < 8 {
			d.fail("truncated")
			return
		}
		if d.err == nil {
			l.Bits = binary.LittleEndian.Uint64(d.b)
			d.b = d.b[8:]
		}
	case ir.TypeString:
		l.S = d.str()
	default:
		d.fail("a leaf of kind %d", l.Kind)
	}
}
