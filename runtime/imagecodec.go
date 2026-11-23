package runtime

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"

	"github.com/joyautomation/nautilus/lang/ir"
)

// The wire form of an Image (EncodeImage, DecodeImage), version 1:
//
//	"nsi" imageFormat
//	uvarint term, uvarint seq, str build, bytes retain
//	uvarint tasks, then per task:
//	  str task, str layout, varint nowMs, uvarint scans,
//	  uvarint leaves, then per leaf: kind byte, payload
//	uint32 CRC-32C (Castagnoli) of everything before it, little endian
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
const imageFormat = 1

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

// EncodeImage appends img's wire form to dst and returns it. With a dst
// whose capacity a previous image filled, it allocates nothing. An image
// over MaxImageBytes, or with a string over maxImageString (a long STRING
// variable), is refused with ErrImageTooBig and dst comes back unchanged.
func EncodeImage(dst []byte, img *Image) ([]byte, error) {
	start := len(dst)
	if err := imageStringsFit(img); err != nil {
		return dst, err
	}
	dst = append(dst, imageMagic[:]...)
	dst = append(dst, imageFormat)
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
	}
	if n := len(dst) - start + 4; n > MaxImageBytes && !mutRuntime("image_size_unbounded") {
		return dst[:start], fmt.Errorf("%w: %d bytes, the limit is %d", ErrImageTooBig, n, MaxImageBytes)
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
			return fmt.Errorf("%w: %s of %d bytes, the limit is %d", ErrImageTooBig, what, n, maxImageString)
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
	}
	return nil
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
	if body[3] != imageFormat {
		return fmt.Errorf("%w: format %d, this build reads %d", ErrImage, body[3], imageFormat)
	}
	if got, want := crc32.Checksum(body, castagnoli), binary.LittleEndian.Uint32(tail); got != want && !mutRuntime("image_crc_unchecked") {
		return fmt.Errorf("%w: checksum %08x, the image says %08x", ErrImage, got, want)
	}
	d := imageReader{b: body[4:]}
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
