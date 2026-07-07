package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

// JSON-RPC 2.0 message framing per the LSP base protocol: each message is
// preceded by an RFC-822-style header block terminated by \r\n\r\n, of which
// only Content-Length matters.

// message is a decoded JSON-RPC request, notification, or response.
// Requests carry an ID; notifications don't.
type message struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	// Result/Error only appear on responses; the server never receives
	// those (it makes no server→client requests) but tests do.
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

// responseError is a JSON-RPC error object.
type responseError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// JSON-RPC error codes (the subset used).
const (
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

// readMessage reads one Content-Length-framed JSON-RPC message.
func readMessage(r *bufio.Reader) (*message, error) {
	contentLength := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break // end of headers
		}
		if name, value, ok := strings.Cut(line, ":"); ok {
			if strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
				contentLength, err = strconv.Atoi(strings.TrimSpace(value))
				if err != nil {
					return nil, fmt.Errorf("bad Content-Length: %w", err)
				}
			}
		}
	}
	if contentLength < 0 {
		return nil, fmt.Errorf("missing Content-Length header")
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	var m message
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("bad JSON-RPC payload: %w", err)
	}
	return &m, nil
}

// writer serializes outgoing messages; safe for concurrent use so request
// handlers and diagnostic pushes never interleave frames.
type writer struct {
	mu  sync.Mutex
	out io.Writer
}

func (w *writer) write(v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := fmt.Fprintf(w.out, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return err
	}
	_, err = w.out.Write(body)
	return err
}

// respond sends a success result for a request.
func (w *writer) respond(id *json.RawMessage, result any) error {
	return w.write(struct {
		JSONRPC string           `json:"jsonrpc"`
		ID      *json.RawMessage `json:"id"`
		Result  any              `json:"result"`
	}{"2.0", id, result})
}

// respondError sends an error result for a request.
func (w *writer) respondError(id *json.RawMessage, code int, msg string) error {
	return w.write(struct {
		JSONRPC string           `json:"jsonrpc"`
		ID      *json.RawMessage `json:"id"`
		Error   responseError    `json:"error"`
	}{"2.0", id, responseError{code, msg}})
}

// notify sends a server-initiated notification (e.g. publishDiagnostics).
func (w *writer) notify(method string, params any) error {
	return w.write(struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  any    `json:"params"`
	}{"2.0", method, params})
}
