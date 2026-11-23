//go:build !js

package camera

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"sync/atomic"
	"time"
)

const boundary = "nautframe"

// MJPEGContentType is the type a live camera answers with.
const MJPEGContentType = "multipart/x-mixed-replace; boundary=" + boundary

// MaxFrameBytes bounds one frame of a relayed stream.
const MaxFrameBytes = MaxSnapshotBytes

// Relay writes the camera's live motion-JPEG to w until ctx ends or the camera
// stops, counting the bytes in m. A virtual camera is drawn here (two frames a
// second); an mjpeg camera is read frame by frame and written as frames (not
// passed through byte for byte: a frame past MaxFrameBytes, or a part that is
// not a JPEG, ends the stream rather than reaching the browser).
func (f *Fetcher) Relay(ctx context.Context, c Camera, lines func() []string, w io.Writer, flush func(), m *Meter) error {
	switch c.Kind {
	case KindVirtual:
		t := time.NewTicker(500 * time.Millisecond)
		defer t.Stop()
		for {
			var ls []string
			if lines != nil {
				ls = lines()
			}
			if err := writeFrame(w, VirtualFrame(c.Label, ls, time.Now()), m); err != nil {
				return err
			}
			flush()
			select {
			case <-ctx.Done():
				return nil
			case <-t.C:
			}
		}
	case KindMJPEG:
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		resp, err := f.get(ctx, c, c.URL)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		// a camera that stops sending ends the stream, not holds the place
		idle := &idleBody{r: resp.Body, d: StreamIdle}
		idle.t = time.AfterFunc(StreamIdle, func() { idle.fired.Store(true); cancel() })
		defer idle.t.Stop()
		resp.Body = idle
		mr, err := mjpegReader(resp)
		if err != nil {
			return err
		}
		for {
			frame, err := nextFrame(mr)
			if err != nil {
				if idle.fired.Load() {
					return errors.New("the camera stopped sending")
				}
				if ctx.Err() != nil || errors.Is(err, io.EOF) {
					return nil
				}
				return err
			}
			if err := writeFrame(w, frame, m); err != nil {
				return err
			}
			flush()
		}
	}
	return fmt.Errorf("a %s camera is not relayed live in this build", c.Kind)
}

func mjpegReader(resp *http.Response) (*multipart.Reader, error) {
	mt, params, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/x-mixed-replace" || params["boundary"] == "" {
		return nil, errors.New("the camera's URL is not a motion-JPEG stream (multipart/x-mixed-replace)")
	}
	return multipart.NewReader(bufio.NewReader(resp.Body), params["boundary"]), nil
}

// idleBody restarts a timer on every read: the camera has StreamIdle to send
// the next byte.
type idleBody struct {
	r     io.ReadCloser
	d     time.Duration
	t     *time.Timer
	fired atomic.Bool
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if n > 0 {
		b.t.Reset(b.d)
	}
	return n, err
}
func (b *idleBody) Close() error { return b.r.Close() }

// nextFrame reads one frame. What it returns is safe to show a person: the
// transport's and the parser's own errors carry the camera's address and its own
// bytes, so they are replaced by fixed words.
func nextFrame(mr *multipart.Reader) ([]byte, error) {
	p, err := mr.NextPart()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, io.EOF
		}
		if re := readErr(err); re != nil && re.Error() != "the camera's picture could not be read" {
			return nil, re
		}
		return nil, errors.New("the camera sent a stream the controller could not read")
	}
	b, err := io.ReadAll(io.LimitReader(p, MaxFrameBytes+1))
	if err != nil {
		return nil, readErr(err)
	}
	if len(b) > MaxFrameBytes {
		return nil, fmt.Errorf("a frame is larger than %d bytes", MaxFrameBytes)
	}
	if !isJPEG(b) {
		return nil, errors.New("a part of the stream is not a JPEG")
	}
	return b, nil
}

func writeFrame(w io.Writer, jpg []byte, m *Meter) error {
	var h bytes.Buffer
	fmt.Fprintf(&h, "--%s\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", boundary, len(jpg))
	if _, err := w.Write(h.Bytes()); err != nil {
		return err
	}
	if _, err := w.Write(jpg); err != nil {
		return err
	}
	if _, err := io.WriteString(w, "\r\n"); err != nil {
		return err
	}
	if m != nil {
		m.Add(h.Len() + len(jpg) + 2)
	}
	return nil
}

// frameFromStream cuts one frame from an mjpeg camera.
func (f *Fetcher) frameFromStream(ctx context.Context, c Camera) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resp, err := f.get(ctx, c, c.URL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	mr, err := mjpegReader(resp)
	if err != nil {
		return nil, err
	}
	return nextFrame(mr)
}
