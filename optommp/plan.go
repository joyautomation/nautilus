package optommp

// plan.go is which 4-byte words of the memory map a scan needs, and the
// three ways the driver fetches them:
//
//   - packed: the words grouped into as few block reads as the areas allow
//     (one per area that carries a binding — two on an ordinary chassis);
//   - custom area: each word a pointer in the processor's custom data area
//     (F0D50000), so the scan is one read of F0D60000;
//   - stream: the processor pushes that same custom data area over UDP.
//
// Every binding is decoded from the same word image whichever way it was
// filled, so the three modes deliver identical values and quality.
//
// Through a pointer an analog or counter word comes from the channel's
// own expanded read area, not the packed values: on the bench EPIC
// (firmware 4.1.2.0, 2026-10-24) a pointer into the packed values costs
// the processor ≈ 0.5 ms each time it is resolved — 36 analog pointers
// streamed every 20 ms held mmpServer at 90 % of a core — while one into
// the expanded areas costs next to nothing and reads the same value
// (NaN included).

import "sort"

// planWords lists, in order, the unique addresses the bindings read;
// custom picks the addresses a pointer should name.
func planWords(tags []Binding, custom bool) []uint32 {
	seen := map[uint32]bool{}
	var words []uint32
	add := func(a uint32) {
		if !seen[a] {
			seen[a] = true
			words = append(words, a)
		}
	}
	for _, b := range tags {
		if b.isStatus() {
			continue // the driver's own, no word of the map
		}
		r := wordsFor(b, custom)
		add(r.val)
		if r.qual != 0 {
			add(r.qual)
		}
	}
	sort.Slice(words, func(i, j int) bool { return words[i] < words[j] })
	return words
}

// tagWords is the addresses one binding decodes from: its value word, the
// bit within it for masks, and (for digital channels) the word holding the
// channel's quality bit; 0 when none.
type tagWords struct {
	val, qual uint32
	bit       uint
}

func wordsFor(b Binding, custom bool) tagWords {
	slot, ch := uint32(b.Slot), uint32(b.Channel)
	digital := addrDigitalPacked + slot*digitalModuleStride
	value := addrPackedValues + slot*valuesModuleStride + ch*4
	if custom {
		switch b.Point {
		case PointAI, PointAO:
			value = addrAnalogRead + slot*pointModuleStride + ch*pointChannelStride
		case PointCounter:
			value = addrDigitalRead + slot*clearModuleStride + ch*clearChannelStride + digitalReadFeature
		}
	}
	switch b.Point {
	case PointDI, PointDO:
		return tagWords{val: digital, qual: digital + 4, bit: uint(ch)}
	case PointAI, PointAO:
		return tagWords{val: value}
	case PointCounter:
		return tagWords{val: value, qual: digital + 4, bit: uint(ch)}
	case PointOnLatch:
		return tagWords{val: digital + 8, qual: digital + 4, bit: uint(ch)}
	case PointOffLatch:
		return tagWords{val: digital + 12, qual: digital + 4, bit: uint(ch)}
	case PointQuality:
		return tagWords{val: addrQualityCodes + slot*qualityModuleStride + ch*4}
	case PointScratchBit:
		// The 64-bit mask is big-endian: bits 32..63 in the first word.
		i := uint32(b.Index)
		if i >= 32 {
			return tagWords{val: addrScratchBits, bit: uint(i - 32)}
		}
		return tagWords{val: addrScratchBits + 4, bit: uint(i)}
	case PointScratchInt:
		return tagWords{val: addrScratchInts + uint32(b.Index)*4}
	case PointScratchFloat:
		return tagWords{val: addrScratchFloats + uint32(b.Index)*4}
	}
	return tagWords{}
}

// area is one block read in packed mode.
type area struct {
	base uint32
	n    int // bytes
}

// packedAreas groups sorted words into block reads: consecutive words
// share a read while they sit in the same 4 KiB page of the map (one
// memory-map area) and the read stays within MaxBlock. It also reports,
// per word, which area holds it and where.
func packedAreas(words []uint32) (areas []area, src []wordSrc) {
	src = make([]wordSrc, len(words))
	for i, w := range words {
		if k := len(areas) - 1; k >= 0 && w>>12 == areas[k].base>>12 && int(w-areas[k].base)+4 <= MaxBlock {
			areas[k].n = int(w-areas[k].base) + 4
			src[i] = wordSrc{k, int(w - areas[k].base)}
			continue
		}
		areas = append(areas, area{w, 4})
		src[i] = wordSrc{len(areas) - 1, 0}
	}
	return areas, src
}

type wordSrc struct{ area, off int }
