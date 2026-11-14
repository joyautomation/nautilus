// Package hmicheck renders a built operator screen in a headless browser
// and says what is wrong with it that a build can't see: a page that throws
// or comes up blank, a pipe that draws nothing, a drawing that lands far
// outside its own box, a page that scrolls sideways on a phone. `naut
// check` runs it whenever a browser is on the machine; the screen is served
// by naut's own server over a runtime that never scans, so nothing reaches
// a driver and the tags sit at their initial values.
//
// The browser is the one already installed (Chrome, Chromium or Edge),
// driven over the DevTools protocol on a loopback websocket: naut carries
// no browser of its own.
package hmicheck

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// FindBrowser returns a Chrome, Chromium or Edge to render with: the one
// NAUT_BROWSER names, else the first of the usual names on PATH or in the
// usual install places. "" when there is none.
func FindBrowser() string {
	if b := os.Getenv("NAUT_BROWSER"); b != "" {
		if b == "none" {
			return ""
		}
		return b
	}
	for _, n := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "microsoft-edge", "microsoft-edge-stable", "chrome"} {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	var places []string
	switch runtime.GOOS {
	case "darwin":
		places = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		}
	case "windows":
		for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LocalAppData"} {
			if d := os.Getenv(env); d != "" {
				places = append(places,
					filepath.Join(d, "Google", "Chrome", "Application", "chrome.exe"),
					filepath.Join(d, "Microsoft", "Edge", "Application", "msedge.exe"))
			}
		}
	}
	for _, p := range places {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// browser is one headless browser and its DevTools connection.
type browser struct {
	cmd     *exec.Cmd
	profile string
	ws      *websocket.Conn

	wmu     sync.Mutex
	mu      sync.Mutex
	next    int
	pending map[int]chan message
	events  map[string]func(message) // by session id
	closed  chan struct{}
	err     error
}

type message struct {
	ID        int             `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Error     *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

var listening = regexp.MustCompile(`DevTools listening on (ws://\S+)`)

func launch(ctx context.Context, path string) (*browser, error) {
	profile, err := os.MkdirTemp("", "naut-render-")
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(path,
		"--headless=new", "--remote-debugging-port=0", "--remote-debugging-address=127.0.0.1",
		"--user-data-dir="+profile, "--no-first-run", "--no-default-browser-check",
		"--disable-gpu", "--disable-extensions", "--disable-sync", "--hide-scrollbars",
		"--mute-audio", "--disable-background-networking", "--password-store=basic",
		"--use-mock-keychain", "about:blank")
	detach(cmd)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		os.RemoveAll(profile)
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		os.RemoveAll(profile)
		return nil, err
	}
	b := &browser{cmd: cmd, profile: profile, pending: map[int]chan message{}, events: map[string]func(message){}, closed: make(chan struct{})}
	found := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			if m := listening.FindStringSubmatch(sc.Text()); m != nil {
				select {
				case found <- m[1]:
				default:
				}
			}
		}
		close(found)
	}()
	var url string
	select {
	case u, ok := <-found:
		if !ok {
			b.close()
			return nil, errors.New("the browser exited before it was ready")
		}
		url = u
	case <-time.After(20 * time.Second):
		b.close()
		return nil, errors.New("the browser didn't start in 20 s")
	case <-ctx.Done():
		b.close()
		return nil, ctx.Err()
	}
	d := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	ws, _, err := d.DialContext(ctx, url, nil)
	if err != nil {
		b.close()
		return nil, err
	}
	ws.SetReadLimit(64 << 20)
	b.ws = ws
	go b.read()
	return b, nil
}

func (b *browser) read() {
	defer close(b.closed)
	for {
		_, data, err := b.ws.ReadMessage()
		if err != nil {
			b.mu.Lock()
			b.err = err
			for id, ch := range b.pending {
				close(ch)
				delete(b.pending, id)
			}
			b.mu.Unlock()
			return
		}
		var m message
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		b.mu.Lock()
		if m.ID != 0 {
			if ch, ok := b.pending[m.ID]; ok {
				delete(b.pending, m.ID)
				ch <- m
			}
		} else if f := b.events[m.SessionID]; f != nil {
			f(m)
		}
		b.mu.Unlock()
	}
}

// call sends one command (to a page when session is set) and waits for
// its answer.
func (b *browser) call(ctx context.Context, session, method string, params any, out any) error {
	ch := make(chan message, 1)
	b.mu.Lock()
	if b.err != nil {
		b.mu.Unlock()
		return b.err
	}
	b.next++
	id := b.next
	b.pending[id] = ch
	b.mu.Unlock()
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	if params == nil {
		raw = []byte("{}")
	}
	msg, _ := json.Marshal(message{ID: id, Method: method, Params: raw, SessionID: session})
	b.wmu.Lock()
	err = b.ws.WriteMessage(websocket.TextMessage, msg)
	b.wmu.Unlock()
	if err != nil {
		return err
	}
	select {
	case m, ok := <-ch:
		if !ok {
			return fmt.Errorf("%s: the browser went away", method)
		}
		if m.Error != nil {
			return fmt.Errorf("%s: %s", method, m.Error.Message)
		}
		if out != nil {
			return json.Unmarshal(m.Result, out)
		}
		return nil
	case <-ctx.Done():
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
		return ctx.Err()
	}
}

func (b *browser) on(session string, f func(message)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if f == nil {
		delete(b.events, session)
	} else {
		b.events[session] = f
	}
}

func (b *browser) close() {
	if b.ws != nil {
		_ = b.call(context.Background(), "", "Browser.close", nil, nil)
		b.ws.Close()
	}
	if b.cmd.Process != nil {
		done := make(chan struct{})
		go func() { _ = b.cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = b.cmd.Process.Kill()
			<-done
		}
	}
	os.RemoveAll(b.profile)
}
