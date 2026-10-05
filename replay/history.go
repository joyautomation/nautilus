package replay

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
)

// History is a recording: n samples every Step seconds from T0 (unix
// seconds), one series per "TAG.Member". Gaps (a scrape that failed, a
// device that was dark) are filled at load: a gap holds the last value
// before it, and a series that starts late takes its first value back to
// T0 — a baseline has no holes.
type History struct {
	T0     int64
	Step   int64
	N      int
	Series map[string][]float64
	Bool   map[string]bool
	// First is each series' first real sample: before it the series is
	// back-filled, a flat line the recording never saw.
	First map[string]int
}

type historyJSON struct {
	T0     int64                 `json:"t0"`
	Step   int64                 `json:"step"`
	N      int                   `json:"n"`
	Bool   []string              `json:"bool"`
	Series map[string][]*float64 `json:"series"`
}

// LoadHistory reads a history.json(.gz) — gzip is detected, not assumed.
func LoadHistory(r io.Reader) (*History, error) {
	br := &peekReader{r: r}
	var src io.Reader = br
	if br.gzip() {
		zr, err := gzip.NewReader(br)
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		src = zr
	}
	var y historyJSON
	if err := json.NewDecoder(src).Decode(&y); err != nil {
		return nil, fmt.Errorf("history: %w", err)
	}
	if y.Step <= 0 || y.N <= 0 {
		return nil, fmt.Errorf("history: step %d, n %d: nothing to replay", y.Step, y.N)
	}
	h := &History{T0: y.T0, Step: y.Step, N: y.N, Series: map[string][]float64{}, Bool: map[string]bool{}, First: map[string]int{}}
	for _, b := range y.Bool {
		h.Bool[b] = true
	}
	for name, s := range y.Series {
		if len(s) != y.N {
			return nil, fmt.Errorf("history: series %s has %d samples, want %d", name, len(s), y.N)
		}
		out := make([]float64, y.N)
		first := math.NaN()
		last := math.NaN()
		for i, v := range s {
			if v != nil {
				last = *v
				if math.IsNaN(first) {
					first = *v
					h.First[name] = i
				}
			}
			out[i] = last
		}
		if math.IsNaN(first) {
			continue // never sampled: as good as absent
		}
		for i := 0; i < y.N && math.IsNaN(out[i]); i++ {
			out[i] = first
		}
		h.Series[name] = out
	}
	return h, nil
}

// End is the time of the last sample, unix seconds.
func (h *History) End() int64 { return h.T0 + int64(h.N-1)*h.Step }

// At is series name at unix time t (seconds, fractional): linear between
// samples for a number, the sample at or before t for a bool — a link does
// not come half up. Clamped to the recording.
func (h *History) At(name string, t float64) (float64, bool) {
	s, ok := h.Series[name]
	if !ok {
		return 0, false
	}
	f := (t - float64(h.T0)) / float64(h.Step)
	if f <= 0 {
		return s[0], true
	}
	i := int(f)
	if i >= h.N-1 {
		return s[h.N-1], true
	}
	if h.Bool[name] {
		return s[i], true
	}
	frac := f - float64(i)
	return s[i] + (s[i+1]-s[i])*frac, true
}

// Tags lists the recorded tag names (the part before the dot).
func (h *History) Tags() map[string]bool {
	out := map[string]bool{}
	for k := range h.Series {
		tag, _, _ := strings.Cut(k, ".")
		out[tag] = true
	}
	return out
}

// peekReader lets LoadHistory sniff the gzip magic without consuming it.
type peekReader struct {
	r    io.Reader
	head []byte
}

func (p *peekReader) gzip() bool {
	b := make([]byte, 2)
	n, _ := io.ReadFull(p.r, b)
	p.head = b[:n]
	return n == 2 && b[0] == 0x1f && b[1] == 0x8b
}

func (p *peekReader) Read(b []byte) (int, error) {
	if len(p.head) > 0 {
		n := copy(b, p.head)
		p.head = p.head[n:]
		return n, nil
	}
	return p.r.Read(b)
}
