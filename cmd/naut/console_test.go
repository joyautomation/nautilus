package main

import (
	"bytes"
	"log/slog"
	"os"
	"strings"
	"testing"
)

func consoleLine(t *testing.T, width int, log func(*slog.Logger)) string {
	t.Helper()
	var buf bytes.Buffer
	log(slog.New(newConsoleHandler(&buf, func() int { return width })))
	return buf.String()
}

func TestConsoleHandlerDropsDateAndInfo(t *testing.T) {
	got := consoleLine(t, 0, func(l *slog.Logger) {
		l.Info("sparkplug: born", "group", "Plant", "node", "Line1", "bdSeq", 0)
	})
	// HH:MM:SS, then the message: no date, no INFO
	if len(got) < 9 || got[2] != ':' || got[5] != ':' || got[8] != ' ' {
		t.Fatalf("want a 15:04:05 time first, got %q", got)
	}
	if want := "sparkplug: born group=Plant node=Line1 bdSeq=0\n"; got[9:] != want {
		t.Fatalf("got %q, want %q after the time", got[9:], want)
	}
}

func TestConsoleHandlerKeepsWarnAndQuotes(t *testing.T) {
	got := consoleLine(t, 0, func(l *slog.Logger) {
		l.With("driver", "modbus").WithGroup("dev").Warn("read failed", "err", "i/o timeout", "addr", "")
	})
	if want := `WARN read failed driver=modbus dev.err="i/o timeout" dev.addr=""` + "\n"; got[9:] != want {
		t.Fatalf("got %q, want %q", got[9:], want)
	}
}

func TestConsoleHandlerWrapsAtWords(t *testing.T) {
	got := consoleLine(t, 55, func(l *slog.Logger) {
		l.Info("sparkplug: born", "group", "Plant", "node", "Line1", "bdSeq", 0, "nodeMetrics", 7, "devices", 0)
	})
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines at width 55, got %q", got)
	}
	if want := "sparkplug: born group=Plant node=Line1 bdSeq=0"; lines[0][9:] != want {
		t.Fatalf("line 1 %q, want %q after the time", lines[0], want)
	}
	if want := "         nodeMetrics=7 devices=0"; lines[1] != want {
		t.Fatalf("line 2 %q, want %q", lines[1], want)
	}
	for _, ln := range lines {
		if len(ln) > 55 {
			t.Fatalf("line %q is wider than 55", ln)
		}
	}
}

func TestWrapWordsLongWordGetsItsOwnLine(t *testing.T) {
	got := wrapWords([]string{"12:00:00", "url=" + strings.Repeat("x", 30)}, 20, 9)
	if want := "12:00:00\n         url=" + strings.Repeat("x", 30); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestWrapBanner(t *testing.T) {
	b := "nautilus · line1 — dashboard + tag API on http://localhost:18101 — sparkplug up — Ctrl+C to stop"
	if got := wrapBanner(b, 0); got != b {
		t.Fatalf("width 0 must leave the banner alone, got %q", got)
	}
	if got := wrapBanner(b, 200); got != b {
		t.Fatalf("a banner that fits must stay one line, got %q", got)
	}
	want := "nautilus · line1\n  dashboard + tag API on http://localhost:18101\n  sparkplug up — Ctrl+C to stop"
	if got := wrapBanner(b, 55); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

// Not a terminal (a test's stderr is a pipe or a file): the default logger
// stays as it is, so anything parsing naut's logs sees what it always did.
func TestUseConsoleLogLeavesNonTerminalsAlone(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if w := consoleWidth(f); w != 0 {
		t.Fatalf("a file reports width %d, want 0", w)
	}
	before := slog.Default()
	old := os.Stderr
	os.Stderr = f
	defer func() { os.Stderr = old }()
	useConsoleLog()
	if slog.Default() != before {
		t.Fatal("useConsoleLog replaced the default logger on a non-terminal")
	}
}
