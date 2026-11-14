package hmicheck

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"math"
	"regexp"
	"strconv"
	"strings"
)

//go:embed text.js
var textJS string

// MinTextPx is the size under which text drawn on a desktop window is too
// small to read at an operator's distance: the kit's smallest (12 px, all
// caps labels only) drawn at 75%.
const MinTextPx = 9.0

// textItem is one piece of text the page shows (text.js).
type textItem struct {
	T    string  `json:"t"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	W    float64 `json:"w"`
	H    float64 `json:"h"`
	FG   string  `json:"fg"`
	Bold bool    `json:"bold"`
	O    float64 `json:"o"`
	PX   float64 `json:"px"`
}

type textResult struct {
	Theme string     `json:"theme"`
	Items []textItem `json:"items"`
}

// legibility looks at the text on the page as it is drawn: each piece's
// contrast against the pixels behind it in a screenshot, and its size. A
// page on the kit's theme (a data-theme on <html>) is looked at in the
// light and the dark theme both, since an operator picks either.
func (b *browser) legibility(ctx context.Context, s string, v view, reload func() error) []string {
	var out []string
	first, err := b.textAndShot(ctx, s)
	if err != nil {
		return nil
	}
	themes := []*textShot{first}
	if t := first.res.Theme; t != "" {
		// the other theme as an operator gets it: chosen (the kit keeps the
		// choice in localStorage "theme"), then the page loaded again
		other := "light"
		if t == "light" {
			other = "dark"
		}
		if err := b.eval(ctx, s, fmt.Sprintf("localStorage.setItem('theme', %q)", other), nil); err == nil && reload() == nil {
			if second, err := b.textAndShot(ctx, s); err == nil && second.res.Theme == other {
				themes = append(themes, second)
			}
			// leave the choice as it was, for the next page
			_ = b.eval(ctx, s, fmt.Sprintf("localStorage.setItem('theme', %q)", t), nil)
		}
	}
	for _, ts := range themes {
		if line := ts.contrast(); line != "" {
			out = append(out, line)
		}
	}
	if line := first.small(v); line != "" {
		out = append(out, line)
	}
	return out
}

type textShot struct {
	res textResult
	img image.Image
}

func (b *browser) eval(ctx context.Context, s, expr string, into any) error {
	var res struct {
		Result           remoteObject `json:"result"`
		ExceptionDetails *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := b.call(ctx, s, "Runtime.evaluate", map[string]any{"expression": expr, "returnByValue": true}, &res); err != nil {
		return err
	}
	if res.ExceptionDetails != nil {
		return fmt.Errorf("%s", res.ExceptionDetails.Text)
	}
	if into != nil {
		return json.Unmarshal(res.Result.Value, into)
	}
	return nil
}

func (b *browser) textAndShot(ctx context.Context, s string) (*textShot, error) {
	var ts textShot
	if err := b.eval(ctx, s, textJS, &ts.res); err != nil {
		return nil, err
	}
	var shot struct {
		Data string `json:"data"`
	}
	if err := b.call(ctx, s, "Page.captureScreenshot", map[string]any{"format": "png"}, &shot); err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(shot.Data)
	if err != nil {
		return nil, err
	}
	if ts.img, err = png.Decode(bytes.NewReader(raw)); err != nil {
		return nil, err
	}
	return &ts, nil
}

type rgb [3]float64

// background is the colour a text sits on: the commonest pixel in a ring
// two to four pixels outside its box. Not inside it, where a heavy face
// can cover more of the box than the ground does.
func background(img image.Image, it textItem) (rgb, bool) {
	r := img.Bounds()
	ix0, iy0 := int(math.Floor(it.X))-2, int(math.Floor(it.Y))-2
	ix1, iy1 := int(math.Ceil(it.X+it.W))+2, int(math.Ceil(it.Y+it.H))+2
	count := map[[3]uint8]int{}
	best, n := [3]uint8{}, 0
	for y := iy0 - 2; y < iy1+2; y++ {
		for x := ix0 - 2; x < ix1+2; x++ {
			if x >= ix0 && x < ix1 && y >= iy0 && y < iy1 {
				continue // inside the ring
			}
			if !(image.Point{x, y}).In(r) {
				continue
			}
			cr, cg, cb, _ := img.At(x, y).RGBA()
			k := [3]uint8{uint8(cr >> 8), uint8(cg >> 8), uint8(cb >> 8)}
			count[k]++
			if count[k] > n {
				best, n = k, count[k]
			}
		}
	}
	if n == 0 {
		return rgb{}, false
	}
	return rgb{float64(best[0]), float64(best[1]), float64(best[2])}, true
}

var (
	rgbRe   = regexp.MustCompile(`^rgba?\(\s*([\d.]+)[,\s]+([\d.]+)[,\s]+([\d.]+)(?:\s*[,/]\s*([\d.]+%?))?\s*\)$`)
	srgbRe  = regexp.MustCompile(`^color\(srgb\s+([\d.]+)\s+([\d.]+)\s+([\d.]+)(?:\s*/\s*([\d.]+%?))?\s*\)$`)
	alphaOf = func(s string) float64 {
		if s == "" {
			return 1
		}
		if strings.HasSuffix(s, "%") {
			f, _ := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64)
			return f / 100
		}
		f, _ := strconv.ParseFloat(s, 64)
		return f
	}
)

// parseColor reads a computed colour: rgb(), rgba() or color(srgb ...).
// A fill that is a gradient or none isn't a colour (false).
func parseColor(s string) (rgb, float64, bool) {
	s = strings.TrimSpace(s)
	if m := rgbRe.FindStringSubmatch(s); m != nil {
		var c rgb
		for i := range c {
			c[i], _ = strconv.ParseFloat(m[i+1], 64)
		}
		return c, alphaOf(m[4]), true
	}
	if m := srgbRe.FindStringSubmatch(s); m != nil {
		var c rgb
		for i := range c {
			f, _ := strconv.ParseFloat(m[i+1], 64)
			c[i] = f * 255
		}
		return c, alphaOf(m[4]), true
	}
	return rgb{}, 0, false
}

func luminance(c rgb) float64 {
	var l [3]float64
	for i, v := range c {
		v /= 255
		if v <= 0.04045 {
			l[i] = v / 12.92
		} else {
			l[i] = math.Pow((v+0.055)/1.055, 2.4)
		}
	}
	return 0.2126*l[0] + 0.7152*l[1] + 0.0722*l[2]
}

// Contrast is the WCAG contrast ratio of two colours, 1 to 21.
func Contrast(a, b rgb) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func hex(c rgb) string {
	return fmt.Sprintf("#%02x%02x%02x", uint8(math.Round(c[0])), uint8(math.Round(c[1])), uint8(math.Round(c[2])))
}

// contrast says how much of the page's text reads under WCAG AA: 4.5:1,
// or 3:1 for large text (24 px, or 18.7 px bold).
func (ts *textShot) contrast() string {
	type low struct {
		t      string
		fg, bg rgb
		ratio  float64
	}
	var lows []low
	seen := map[string]bool{}
	total := 0
	for _, it := range ts.res.Items {
		fg, a, ok := parseColor(it.FG)
		if !ok {
			continue
		}
		bg, ok := background(ts.img, it)
		if !ok {
			continue
		}
		total++
		if a *= it.O; a < 1 { // drawn see-through: what shows is the blend
			for i := range fg {
				fg[i] = fg[i]*a + bg[i]*(1-a)
			}
		}
		need := 4.5
		if it.PX >= 24 || (it.Bold && it.PX >= 18.66) {
			need = 3
		}
		if r := Contrast(fg, bg); r < need {
			lows = append(lows, low{it.T, fg, bg, r})
		}
	}
	if len(lows) == 0 {
		return ""
	}
	var ex []string
	for _, l := range lows {
		k := hex(l.fg) + hex(l.bg)
		if seen[k] || len(ex) == 4 {
			continue
		}
		seen[k] = true
		ex = append(ex, fmt.Sprintf("%q %s on %s (%.1f:1)", l.t, hex(l.fg), hex(l.bg), l.ratio))
	}
	where := ""
	advice := "colour text from the kit's theme tokens (var(--ink), var(--ink-2), var(--muted), on var(--bg), var(--surface)), never a hex, so it reads in the light and the dark theme"
	if ts.res.Theme != "" {
		where = fmt.Sprintf("in the %s theme, ", ts.res.Theme)
	}
	return fmt.Sprintf("%s%d of %d texts are hard to read (under 4.5:1 contrast, 3:1 for large text): %s: %s", where, len(lows), total, strings.Join(ex, ", "), advice)
}

// small says how much of the page's text is drawn too small to read.
func (ts *textShot) small(v view) string {
	var smalls []textItem
	least := math.Inf(1)
	for _, it := range ts.res.Items {
		if it.PX > 0 && it.PX < MinTextPx {
			smalls = append(smalls, it)
			least = math.Min(least, it.PX)
		}
	}
	if len(smalls) == 0 {
		return ""
	}
	var ex []string
	seen := map[string]bool{}
	for _, it := range smalls {
		if seen[it.T] || len(ex) == 4 {
			continue
		}
		seen[it.T] = true
		ex = append(ex, fmt.Sprintf("%q", it.T))
	}
	return fmt.Sprintf("%d of %d texts are drawn under %.0f px in a %d×%d window (the smallest %.1f px): %s: write text at 12 px or more, and draw the canvas for the window so it isn't scaled down", len(smalls), len(ts.res.Items), MinTextPx, v.width, v.height, least, strings.Join(ex, ", "))
}
