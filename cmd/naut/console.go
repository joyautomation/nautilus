package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/term"
)

// On a terminal, `naut run` logs for the person watching it: the time of
// day, the message and its attributes, wrapped at word boundaries to the
// terminal's width with a hanging indent. The default logger's date and
// INFO prefix take 25 columns of every line, which a person at a bench does
// not need and a screen recording cannot afford: in a split screen at a
// readable size the terminal is ~55 columns wide. Anywhere else (a pipe,
// a file, systemd, a pod's log) nothing changes, because something may be
// parsing it.

// consoleWidth is the terminal width of f, or 0 when f is not a terminal.
func consoleWidth(f *os.File) int {
	fd := int(f.Fd())
	if !term.IsTerminal(fd) {
		return 0
	}
	w, _, err := term.GetSize(fd)
	if err != nil {
		return 0
	}
	return w
}

// useConsoleLog makes the console handler the default logger when stderr
// (where the default logger writes) is a terminal.
func useConsoleLog() {
	if consoleWidth(os.Stderr) == 0 {
		return
	}
	slog.SetDefault(slog.New(newConsoleHandler(os.Stderr, func() int { return consoleWidth(os.Stderr) })))
}

// consoleHandler is a slog.Handler writing "15:04:05 message k=v ...".
// WARN and ERROR keep their level word; INFO and DEBUG don't. width is
// asked on every record so a resized terminal wraps to its new size; 0
// means don't wrap.
type consoleHandler struct {
	mu    *sync.Mutex
	w     io.Writer
	width func() int
	level slog.Leveler
	attrs []string // preformatted k=v from WithAttrs, group-qualified
	group string   // key prefix from WithGroup, "a.b."
}

func newConsoleHandler(w io.Writer, width func() int) *consoleHandler {
	return &consoleHandler{mu: &sync.Mutex{}, w: w, width: width, level: slog.LevelInfo}
}

func (h *consoleHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level.Level()
}

func (h *consoleHandler) Handle(_ context.Context, r slog.Record) error {
	t := r.Time
	if t.IsZero() {
		t = time.Now()
	}
	words := []string{t.Format("15:04:05")}
	if r.Level >= slog.LevelWarn {
		words = append(words, r.Level.String())
	}
	words = append(words, strings.Fields(r.Message)...)
	words = append(words, h.attrs...)
	r.Attrs(func(a slog.Attr) bool {
		words = appendAttr(words, h.group, a)
		return true
	})
	line := wrapWords(words, h.width(), len("15:04:05 "))
	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, line+"\n")
	return err
}

func (h *consoleHandler) WithAttrs(as []slog.Attr) slog.Handler {
	h2 := *h
	h2.attrs = slices.Clone(h.attrs)
	for _, a := range as {
		h2.attrs = appendAttr(h2.attrs, h.group, a)
	}
	return &h2
}

func (h *consoleHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	h2 := *h
	h2.group = h.group + name + "."
	return &h2
}

// appendAttr formats a as k=v, the way slog's text handler quotes values:
// a value with a space, a quote or an equals sign in it is quoted, so it
// stays one word for the wrap.
func appendAttr(words []string, group string, a slog.Attr) []string {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return words
	}
	if a.Value.Kind() == slog.KindGroup {
		g := group
		if a.Key != "" {
			g += a.Key + "."
		}
		for _, ga := range a.Value.Group() {
			words = appendAttr(words, g, ga)
		}
		return words
	}
	v := a.Value.String()
	if v == "" || strings.ContainsAny(v, " \t\n\"=") {
		v = strconv.Quote(v)
	}
	return append(words, group+a.Key+"="+v)
}

// wrapWords joins words with single spaces, starting a new line, indented
// by indent spaces, before any word that would run past width. A word
// longer than a line gets a line of its own (the terminal breaks it; there
// is nowhere better). width 0 joins everything on one line.
func wrapWords(words []string, width, indent int) string {
	if width <= 0 || indent >= width {
		return strings.Join(words, " ")
	}
	var b strings.Builder
	col := 0
	for i, w := range words {
		switch {
		case i == 0:
		case col+1+utf8.RuneCountInString(w) > width:
			b.WriteString("\n" + strings.Repeat(" ", indent))
			col = indent
		default:
			b.WriteByte(' ')
			col++
		}
		b.WriteString(w)
		col += utf8.RuneCountInString(w)
	}
	return b.String()
}

// wrapBanner breaks runProject's banner between its " — " sections when it
// is wider than width, continuation lines indented two spaces. width 0
// leaves it as it is.
func wrapBanner(banner string, width int) string {
	if width <= 0 || utf8.RuneCountInString(banner) <= width {
		return banner
	}
	parts := strings.Split(banner, " — ")
	var b strings.Builder
	col := 0
	for i, p := range parts {
		switch {
		case i == 0:
		case col+3+utf8.RuneCountInString(p) > width:
			b.WriteString("\n  ")
			col = 2
		default:
			b.WriteString(" — ")
			col += 3
		}
		// a part longer than the line (a URL with its label) wraps at
		// its own spaces rather than mid-word
		for j, w := range strings.Split(p, " ") {
			n := utf8.RuneCountInString(w)
			if j > 0 {
				if col+1+n > width {
					b.WriteString("\n  ")
					col = 2
				} else {
					b.WriteString(" ")
					col++
				}
			}
			b.WriteString(w)
			col += n
		}
	}
	return b.String()
}
