//go:build !js

package camera

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"strings"
	"time"
)

// The virtual camera (#724): a picture drawn by the controller, so a demo, a
// test and the guide have a camera with no hardware: a moving bar (so a frozen
// picture is visible), the camera's name, the lines it is given (an asset's
// live values) and the time. It uses only the standard library: the text is a
// 5x7 bitmap font of the characters a label and a reading need.

// Frame size of the virtual camera.
const (
	VirtualW = 320
	VirtualH = 180
)

// VirtualFrame is one JPEG frame at time t, with the camera's label and up to
// four lines of text under it.
func VirtualFrame(label string, lines []string, t time.Time) []byte {
	img := image.NewRGBA(image.Rect(0, 0, VirtualW, VirtualH))
	for y := 0; y < VirtualH; y++ {
		g := uint8(24 + y*40/VirtualH)
		for x := 0; x < VirtualW; x++ {
			img.SetRGBA(x, y, color.RGBA{R: g / 2, G: g, B: g + 20, A: 255})
		}
	}
	// the moving bar: one full pass in ten seconds
	pos := int((t.UnixNano() / int64(time.Second/20)) % 200)
	bx := pos * (VirtualW - 20) / 200
	for y := VirtualH - 24; y < VirtualH-12; y++ {
		for x := bx; x < bx+20; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 240, G: 190, B: 40, A: 255})
		}
	}
	text(img, 8, 8, 2, strings.ToUpper(label), color.RGBA{R: 235, G: 240, B: 245, A: 255})
	for i, l := range lines {
		if i == 4 {
			break
		}
		text(img, 8, 32+i*12, 1, strings.ToUpper(l), color.RGBA{R: 190, G: 215, B: 235, A: 255})
	}
	text(img, 8, VirtualH-10, 1, t.UTC().Format("2006-01-02 15:04:05")+" UTC", color.RGBA{R: 150, G: 170, B: 190, A: 255})
	var b bytes.Buffer
	_ = jpeg.Encode(&b, img, &jpeg.Options{Quality: 70})
	return b.Bytes()
}

func text(img *image.RGBA, x, y, scale int, s string, c color.RGBA) {
	for _, r := range s {
		g, ok := glyphs[r]
		if !ok {
			g = glyphs[' ']
		}
		for row := 0; row < 7; row++ {
			for col := 0; col < 5; col++ {
				if g[row]&(1<<(4-col)) == 0 {
					continue
				}
				for dy := 0; dy < scale; dy++ {
					for dx := 0; dx < scale; dx++ {
						px, py := x+col*scale+dx, y+row*scale+dy
						if px >= 0 && px < VirtualW && py >= 0 && py < VirtualH {
							img.SetRGBA(px, py, c)
						}
					}
				}
			}
		}
		x += 6 * scale
	}
}

// glyphs are 5x7: one byte a row, the low five bits.
var glyphs = map[rune][7]byte{
	' ': {}, '.': {0, 0, 0, 0, 0, 12, 12}, ':': {0, 12, 12, 0, 12, 12, 0}, '-': {0, 0, 0, 31, 0, 0, 0},
	'/': {1, 2, 2, 4, 8, 8, 16}, '%': {25, 25, 2, 4, 8, 19, 19}, '_': {0, 0, 0, 0, 0, 0, 31}, '+': {0, 4, 4, 31, 4, 4, 0},
	'0': {14, 17, 19, 21, 25, 17, 14}, '1': {4, 12, 4, 4, 4, 4, 14}, '2': {14, 17, 1, 2, 4, 8, 31},
	'3': {30, 1, 1, 14, 1, 1, 30}, '4': {2, 6, 10, 18, 31, 2, 2}, '5': {31, 16, 30, 1, 1, 17, 14},
	'6': {6, 8, 16, 30, 17, 17, 14}, '7': {31, 1, 2, 4, 8, 8, 8}, '8': {14, 17, 17, 14, 17, 17, 14},
	'9': {14, 17, 17, 15, 1, 2, 12},
	'A': {14, 17, 17, 31, 17, 17, 17}, 'B': {30, 17, 17, 30, 17, 17, 30}, 'C': {14, 17, 16, 16, 16, 17, 14},
	'D': {30, 17, 17, 17, 17, 17, 30}, 'E': {31, 16, 16, 30, 16, 16, 31}, 'F': {31, 16, 16, 30, 16, 16, 16},
	'G': {14, 17, 16, 23, 17, 17, 15}, 'H': {17, 17, 17, 31, 17, 17, 17}, 'I': {14, 4, 4, 4, 4, 4, 14},
	'J': {7, 2, 2, 2, 2, 18, 12}, 'K': {17, 18, 20, 24, 20, 18, 17}, 'L': {16, 16, 16, 16, 16, 16, 31},
	'M': {17, 27, 21, 21, 17, 17, 17}, 'N': {17, 17, 25, 21, 19, 17, 17}, 'O': {14, 17, 17, 17, 17, 17, 14},
	'P': {30, 17, 17, 30, 16, 16, 16}, 'Q': {14, 17, 17, 17, 21, 18, 13}, 'R': {30, 17, 17, 30, 20, 18, 17},
	'S': {15, 16, 16, 14, 1, 1, 30}, 'T': {31, 4, 4, 4, 4, 4, 4}, 'U': {17, 17, 17, 17, 17, 17, 14},
	'V': {17, 17, 17, 17, 17, 10, 4}, 'W': {17, 17, 17, 21, 21, 27, 17}, 'X': {17, 17, 10, 4, 10, 17, 17},
	'Y': {17, 17, 10, 4, 4, 4, 4}, 'Z': {31, 1, 2, 4, 8, 16, 31},
}
