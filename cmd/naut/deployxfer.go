package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The upload half of naut deploy for a slow link (Ethernet radio): a built
// controller is [runtime][project zip][16-byte footer holding the zip's
// offset], and the box already holds the previous one, so what travels is
// the least the box can rebuild a controller from, gzipped, in a file named
// for what it is (so a dropped link resumes), and a hash checks the
// rebuilt file before it can become a release.
//
//	project  the box's runtime part is the new build's: send the project zip
//	         and footer, and the box joins the runtime it has with them
//	full     anything else (a first install, a new runtime, a box that cannot
//	         say): the whole controller, gzipped
//	legacy   a box whose frozen helper (naut deploy helper) cannot rebuild:
//	         the whole controller, uncompressed, as before

// transfer is the plan for one controller.
type transfer struct {
	kind     string // project, full, legacy
	wire     string // the file that goes over the wire (gzip), "" for legacy
	wireSize int64
	wireSha  string
	rsize    int64  // the runtime bytes the box supplies (0 for full)
	whole    string // SHA-256 of the whole controller
	size     int64  // the whole controller's size, for the summary
	note     string // why this kind
}

// boxBase is what box.sh base says about the box's current controller.
type boxBase struct {
	release string
	rsize   int64
	rsha    string
}

func parseBase(out string) (boxBase, bool) {
	var b boxBase
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "release":
			b.release = v
		case "runtime-size":
			b.rsize, _ = strconv.ParseInt(v, 10, 64)
		case "runtime-sha256":
			b.rsha = v
		}
	}
	return b, b.release != "" && b.rsize > 0 && len(b.rsha) == 64
}

// runtimePart is where a controller's runtime ends (its footer's offset) and
// the SHA-256 of those bytes. ok is false for a file with no footer.
func runtimePart(path string) (off int64, sha string, ok bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", false, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, "", false, err
	}
	off, ok = embeddedOffset(f, st.Size())
	if !ok {
		return 0, "", false, nil
	}
	h := sha256.New()
	if _, err := io.Copy(h, io.NewSectionReader(f, 0, off)); err != nil {
		return 0, "", false, err
	}
	return off, hex.EncodeToString(h.Sum(nil)), true, nil
}

// gzipLevel is how hard the wire file is compressed: the link is the slow
// part, so as hard as gzip goes (tests use a fast level).
var gzipLevel = gzip.BestCompression

// gzipRange writes bytes [from, to) of src to a gzip file at dst, and
// returns its size and SHA-256.
func gzipRange(src, dst string, from int64) (int64, string, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, "", err
	}
	defer in.Close()
	if _, err := in.Seek(from, io.SeekStart); err != nil {
		return 0, "", err
	}
	out, err := os.Create(dst)
	if err != nil {
		return 0, "", err
	}
	defer out.Close()
	h := sha256.New()
	zw, err := gzip.NewWriterLevel(io.MultiWriter(out, h), gzipLevel)
	if err != nil {
		return 0, "", err
	}
	if _, err := io.Copy(zw, in); err != nil {
		return 0, "", err
	}
	if err := zw.Close(); err != nil {
		return 0, "", err
	}
	st, err := out.Stat()
	if err != nil {
		return 0, "", err
	}
	return st.Size(), hex.EncodeToString(h.Sum(nil)), nil
}

// planTransfer picks what to send for the controller at ctl (whose whole
// SHA-256 is sum) given what the box has: the project part only when the
// box's runtime part is the same bytes, else all of it. dir is a scratch
// directory for the gzip file.
func planTransfer(ctl, sum, dir string, base boxBase, haveBase bool) (*transfer, error) {
	st, err := os.Stat(ctl)
	if err != nil {
		return nil, err
	}
	t := &transfer{whole: sum, size: st.Size(), wire: filepath.Join(dir, "wire.gz")}
	from := int64(0)
	t.kind, t.note = "full", "the whole controller, gzipped"
	if haveBase {
		off, rsha, ok, err := runtimePart(ctl)
		if err != nil {
			return nil, err
		}
		switch {
		case !ok:
			t.note = "the whole controller, gzipped (it has no project footer to split at)"
		case off == base.rsize && rsha == base.rsha:
			t.kind, t.rsize, from = "project", off, off
			t.note = "the project only, gzipped: the runtime on the box (" + base.release + ") is the one this was built on"
		default:
			t.note = "the whole controller, gzipped: the runtime on the box is not the one this was built on"
		}
	} else {
		t.note = "the whole controller, gzipped: the box has no controller to build on"
	}
	t.wireSize, t.wireSha, err = gzipRange(ctl, t.wire, from)
	return t, err
}

// parseRate reads a link speed: bits per second, with an optional k or M
// ("256k", "1M", "64000").
func parseRate(s string) (int64, error) {
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "k"), strings.HasSuffix(s, "K"):
		mult, s = 1000, s[:len(s)-1]
	case strings.HasSuffix(s, "M"), strings.HasSuffix(s, "m"):
		mult, s = 1_000_000, s[:len(s)-1]
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%q is not a link speed: bits per second, like 256k or 1M", s)
	}
	return int64(n * float64(mult)), nil
}

func humanBytes(n int64) string {
	switch {
	case n >= 10<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

func humanDuration(d time.Duration) string {
	switch {
	case d < 90*time.Second:
		return fmt.Sprintf("%d s", int(d.Round(time.Second)/time.Second))
	case d < 90*time.Minute:
		return fmt.Sprintf("%d min", int((d+30*time.Second)/time.Minute))
	}
	return fmt.Sprintf("%.1f h", d.Hours())
}

// defaultRate is the link speed the estimate assumes when the person gives
// none: the slowest radio links the deploy is meant for.
const defaultRate = 256_000

// slowSend is the estimate beyond which the summary suggests -limit.
const slowSend = 3 * time.Minute

// sendEstimate is the time to send n bytes at rate bits/s.
func sendEstimate(n, rate int64) time.Duration {
	return time.Duration(float64(n) * 8 / float64(rate) * float64(time.Second))
}

// summary is what is printed before anything is sent.
func (t *transfer) summary(rate, limit int64) string {
	n := t.wireSize
	if t.kind == "legacy" {
		n = t.size
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "sending %s (%s): %s", humanBytes(n), t.kind, t.note)
	r := rate
	if r == 0 {
		r = defaultRate
	}
	if limit > 0 && limit < r {
		r = limit
	}
	est := sendEstimate(n, r)
	fmt.Fprintf(&sb, "\n  about %s at %s", humanDuration(est), rateText(r))
	if rate == 0 && limit == 0 {
		fmt.Fprintf(&sb, " (-bandwidth sets the link speed assumed)")
	}
	if limit > 0 {
		fmt.Fprintf(&sb, ", limited to %s", rateText(limit))
	} else if est > slowSend {
		fmt.Fprintf(&sb, "\n  a send this long can crowd the link's other traffic: -limit 128k holds it to 128 kbit/s")
	}
	return sb.String()
}

func rateText(bps int64) string {
	switch {
	case bps >= 1_000_000 && bps%1_000_000 == 0:
		return fmt.Sprintf("%d Mbit/s", bps/1_000_000)
	case bps >= 1000:
		return fmt.Sprintf("%d kbit/s", bps/1000)
	}
	return fmt.Sprintf("%d bit/s", bps)
}

// throttle limits the bytes per second a reader hands out, so a deploy does
// not take a shared link whole. It sleeps in the writer's path (the ssh
// process reads stdin at the pace it is given).
type throttle struct {
	r     io.Reader
	bps   float64 // bytes per second; 0: none
	start time.Time
	n     int64
	sleep func(time.Duration)
	now   func() time.Time
}

func newThrottle(r io.Reader, bitsPerSecond int64) io.Reader {
	if bitsPerSecond <= 0 {
		return r
	}
	return &throttle{r: r, bps: float64(bitsPerSecond) / 8, sleep: time.Sleep, now: time.Now}
}

func (t *throttle) Read(p []byte) (int, error) {
	if t.start.IsZero() {
		t.start = t.now()
	}
	// small reads, so the pace holds without bursts of a whole buffer
	if max := int(t.bps/10) + 1; len(p) > max {
		p = p[:max]
	}
	n, err := t.r.Read(p)
	t.n += int64(n)
	due := time.Duration(float64(t.n) / t.bps * float64(time.Second))
	if wait := due - t.now().Sub(t.start); wait > 0 {
		t.sleep(wait)
	}
	return n, err
}

// progress prints how far a send has got, at most every few seconds.
type progress struct {
	r     io.Reader
	w     io.Writer
	total int64
	done  int64
	t0    time.Time
	last  time.Time
	every time.Duration
	now   func() time.Time
}

func newProgress(r io.Reader, w io.Writer, already, total int64) *progress {
	return &progress{r: r, w: w, done: already, total: total, every: 5 * time.Second, now: time.Now}
}

func (p *progress) Read(b []byte) (int, error) {
	if p.t0.IsZero() {
		p.t0, p.last = p.now(), p.now()
	}
	n, err := p.r.Read(b)
	p.done += int64(n)
	if t := p.now(); t.Sub(p.last) >= p.every {
		p.last = t
		rate := float64(p.done) / t.Sub(p.t0).Seconds()
		fmt.Fprintf(p.w, "naut deploy: sent %s of %s (%d%%)\n", humanBytes(p.done), humanBytes(p.total), p.done*100/max(p.total, 1))
		_ = rate
	}
	return n, err
}

// deployRetries and deployRetryWait are how often and how long a send is
// retried over a flaky link; tests shorten the wait.
var (
	deployRetries   = 8
	deployRetryWait = 2 * time.Second
)

// uploadResumable sends the file at path (size bytes) as incoming/<name> on
// the box, whole or not at all, resuming from what the box already holds
// after a dropped link: each attempt asks the box how many bytes it has and
// sends the rest. It gives up after deployRetries failed attempts.
func (b *sshBox) uploadResumable(name, path string, size, limit int64) error {
	if cleanID(name) != name {
		return fmt.Errorf("%q cannot go to the box unquoted", name)
	}
	var lastErr error
	wait := deployRetryWait
	for attempt := 0; attempt <= deployRetries; attempt++ {
		if attempt > 0 {
			fmt.Fprintf(os.Stderr, "naut deploy: the link dropped (%v); resuming in %s\n", lastErr, humanDuration(wait))
			time.Sleep(wait)
			if wait < 30*time.Second {
				wait *= 2
			}
		}
		have, err := b.partSize(name)
		if err != nil {
			lastErr = err
			continue
		}
		if have > size {
			if code := b.root(io.Discard, "partreset", name); code != 0 {
				lastErr = fmt.Errorf("could not clear a longer partial upload (exit %d)", code)
				continue
			}
			have = 0
		}
		if have == size {
			return nil
		}
		if have > 0 {
			fmt.Fprintf(os.Stderr, "naut deploy: the box already holds %s of %s; sending the rest\n", humanBytes(have), humanBytes(size))
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		if _, err := f.Seek(have, io.SeekStart); err != nil {
			f.Close()
			return err
		}
		r := newThrottle(newProgress(f, os.Stderr, have, size), limit)
		code := b.appendPart(r, name)
		f.Close()
		if code == 0 {
			// the box says what it holds: sent and received are not the same
			// thing over a link that dies at the last byte
			if got, err := b.partSize(name); err == nil && got == size {
				return nil
			}
			lastErr = errors.New("the box holds fewer bytes than were sent")
			continue
		}
		lastErr = fmt.Errorf("ssh exit %d", code)
	}
	return fmt.Errorf("gave up after %d attempts: %v (run the same command again to resume; the box keeps what it has)", deployRetries+1, lastErr)
}

// partSize is how many bytes of upload <name> the box holds.
func (b *sshBox) partSize(name string) (int64, error) {
	var out bytes.Buffer
	if code := b.root(&out, "partsize", name); code != 0 {
		return 0, fmt.Errorf("could not ask the box (exit %d)", code)
	}
	n, err := strconv.ParseInt(strings.TrimSpace(out.String()), 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("the box answered %q", strings.TrimSpace(out.String()))
	}
	return n, nil
}

// appendPart adds r to the end of incoming/<name>.part.
func (b *sshBox) appendPart(r io.Reader, name string) int {
	if b.helper {
		return b.run(r, os.Stdout, "sudo -n "+helperPath+" append "+name)
	}
	in := b.boxRoot() + "/incoming/" + name
	return b.run(r, os.Stdout, b.priv("mkdir -p "+b.boxRoot()+"/incoming")+" && "+
		b.priv(fmt.Sprintf("sh -c 'cat >> %s.part'", in)))
}

// base asks the box what its current controller offers (box.sh base).
func (b *sshBox) base() (boxBase, bool, error) {
	var out bytes.Buffer
	if code := b.root(&out, "base"); code != 0 {
		return boxBase{}, false, fmt.Errorf("could not ask the box what it has (exit %d)", code)
	}
	bb, ok := parseBase(out.String())
	return bb, ok, nil
}

// legacyHelperVersions are the helper versions that predate the slow-link
// upload: a helper from one of them still runs every older command, but
// cannot rebuild a controller, so a deploy to it sends the whole file as it
// always did, and says how to get the faster path.
var legacyHelperVersions = map[string]bool{
	"177ab940e0359f6e": true, // main before the slow-link upload
}
