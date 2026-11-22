//go:build !js

package hmibuild

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
)

// The icon work lives here, and not in pwa.go, so the browser build (GOOS=js:
// the playground never builds an installable HMI, it passes NoPWA) does not
// link image/png. pwa_icons_js.go is its stand-in; the controller's behaviour
// is what it was.

// iconConfig reads a PNG's header: its size, or why it is not one.
func iconConfig(b []byte) (image.Config, error) {
	return png.DecodeConfig(bytes.NewReader(b))
}

// iconFiles adds the three icons of the installable app to files: the
// project's own scaled down, or one drawn from the theme color.
func iconFiles(files map[string][]byte, p *PWA, theme string) error {
	var src image.Image
	if len(p.Icon) > 0 {
		src, _ = png.Decode(bytes.NewReader(p.Icon)) // checked above
	} else {
		src = drawnIcon(512, theme)
	}
	icons := map[string]int{"icon-192.png": 192, "icon-512.png": 512, "apple-touch-icon.png": 180}
	for f, size := range icons {
		var buf bytes.Buffer
		if err := png.Encode(&buf, scale(src, size)); err != nil {
			return err
		}
		if _, own := files[f]; !own { // the app's own static file wins
			files[f] = buf.Bytes()
		}
	}
	return nil
}

// drawnIcon is the icon of a project that gave none: a rounded square in
// the theme color with a lighter mark, so two projects are told apart by
// their name under the icon and a project that cares gives pwa.icon.
func drawnIcon(n int, theme string) image.Image {
	var r, g, b uint8
	fmt.Sscanf(theme, "#%02x%02x%02x", &r, &g, &b)
	bg := color.NRGBA{r, g, b, 255}
	fg := color.NRGBA{uint8(min(255, int(r)+150)), uint8(min(255, int(g)+150)), uint8(min(255, int(b)+150)), 255}
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	c := float64(n) / 2
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			dx, dy := float64(x)+.5-c, float64(y)+.5-c
			d := dx*dx + dy*dy
			switch {
			case d < (c*.55)*(c*.55) && d > (c*.38)*(c*.38):
				img.SetNRGBA(x, y, fg) // a ring: a gauge
			default:
				img.SetNRGBA(x, y, bg)
			}
		}
	}
	return img
}

// scale is a box-filter resize to n x n (the stdlib has none).
func scale(src image.Image, n int) *image.NRGBA {
	sb := src.Bounds()
	out := image.NewNRGBA(image.Rect(0, 0, n, n))
	for y := 0; y < n; y++ {
		y0, y1 := sb.Min.Y+y*sb.Dy()/n, sb.Min.Y+(y+1)*sb.Dy()/n
		for x := 0; x < n; x++ {
			x0, x1 := sb.Min.X+x*sb.Dx()/n, sb.Min.X+(x+1)*sb.Dx()/n
			var sr, sg, sbl, sa, cnt uint32
			for yy := y0; yy < max(y1, y0+1); yy++ {
				for xx := x0; xx < max(x1, x0+1); xx++ {
					r, g, b, a := src.At(xx, yy).RGBA() // premultiplied
					sr += r >> 8
					sg += g >> 8
					sbl += b >> 8
					sa += a >> 8
					cnt++
				}
			}
			if sa == 0 {
				continue
			}
			// back to straight alpha
			out.SetNRGBA(x, y, color.NRGBA{uint8(sr * 255 / sa), uint8(sg * 255 / sa), uint8(sbl * 255 / sa), uint8(sa / cnt)})
		}
	}
	return out
}
