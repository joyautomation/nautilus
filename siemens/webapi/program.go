package webapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Var is one entry of a PlcProgram.Browse reply.
type Var struct {
	Name        string `json:"name"`
	Address     string `json:"address,omitempty"`
	HasChildren bool   `json:"has_children,omitempty"`
	DBNumber    int    `json:"db_number,omitempty"`
	Datatype    string `json:"datatype,omitempty"` // "bool", "int", "real", "struct", …
	ReadOnly    bool   `json:"read_only,omitempty"`
	MaxLength   int    `json:"max_length,omitempty"`
	// ArrayDimensions are the bounds of an array variable.
	ArrayDimensions []struct {
		StartIndex int `json:"start_index"`
		Count      int `json:"count"`
	} `json:"array_dimensions,omitempty"`
}

// Browse lists the children of v ("" for the top level: global DBs and
// tag-table tags), or with self set describes v itself.
func (c *Client) Browse(ctx context.Context, v string, self bool) ([]Var, error) {
	mode := "children"
	if self {
		mode = "var"
	}
	params := map[string]string{"mode": mode}
	if v != "" {
		params["var"] = v
	}
	var out []Var
	err := c.call(ctx, "PlcProgram.Browse", params, &out)
	return out, err
}

// Read reads one elementary variable by its symbol, e.g. `"Tags".Level`.
func (c *Client) Read(ctx context.Context, v string) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.call(ctx, "PlcProgram.Read", map[string]string{"var": v}, &out)
	return out, err
}

// Write writes one elementary variable.
func (c *Client) Write(ctx context.Context, v string, value any) error {
	var ok bool
	if err := c.call(ctx, "PlcProgram.Write", map[string]any{"var": v, "value": value}, &ok); err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("webapi: PlcProgram.Write %s: the CPU answered false", v)
	}
	return nil
}

// Result is one variable's outcome in ReadMany or WriteMany.
type Result struct {
	Var   string
	Value json.RawMessage // ReadMany only
	Err   error
}

// ReadMany reads many variables in as few bulk requests as the body limit
// allows. A failing variable fails only its own Result.
func (c *Client) ReadMany(ctx context.Context, vars []string) ([]Result, error) {
	calls := make([]Call, len(vars))
	for i, v := range vars {
		calls[i] = Call{Method: "PlcProgram.Read", Params: map[string]string{"var": v}}
	}
	replies, err := c.chunked(ctx, calls)
	if err != nil {
		return nil, err
	}
	out := make([]Result, len(vars))
	for i, r := range replies {
		out[i] = Result{Var: vars[i], Value: r.Result, Err: r.Err}
	}
	return out, nil
}

// Write is one variable and value for WriteMany.
type Write struct {
	Var   string
	Value any
}

// WriteMany writes many variables, batched like ReadMany. The writes of
// one batch are not atomic: each Result says whether its own write landed.
func (c *Client) WriteMany(ctx context.Context, ws []Write) ([]Result, error) {
	calls := make([]Call, len(ws))
	for i, w := range ws {
		calls[i] = Call{Method: "PlcProgram.Write", Params: map[string]any{"var": w.Var, "value": w.Value}}
	}
	replies, err := c.chunked(ctx, calls)
	if err != nil {
		return nil, err
	}
	out := make([]Result, len(ws))
	for i, r := range replies {
		out[i] = Result{Var: ws[i].Var, Err: r.Err}
		if r.Err == nil && string(r.Result) != "true" {
			out[i].Err = fmt.Errorf("webapi: PlcProgram.Write %s: the CPU answered %s", ws[i].Var, r.Result)
		}
	}
	return out, nil
}

// chunked splits calls into bulk requests under MaxBody and MaxBatch.
func (c *Client) chunked(ctx context.Context, calls []Call) ([]Reply, error) {
	out := make([]Reply, 0, len(calls))
	// Envelope overhead per call: {"jsonrpc":"2.0","method":"","params":,"id":NNNNNNN},
	const perCall = 64
	start, size := 0, 2
	flush := func(end int) error {
		if end == start {
			return nil
		}
		rs, err := c.Do(ctx, calls[start:end]...)
		if err != nil {
			return err
		}
		out = append(out, rs...)
		start, size = end, 2
		return nil
	}
	for i, cl := range calls {
		p, err := json.Marshal(cl.Params)
		if err != nil {
			return nil, fmt.Errorf("webapi: encoding %s: %w", cl.Method, err)
		}
		n := len(cl.Method) + len(p) + perCall
		if n+2 > c.opt.MaxBody {
			return nil, fmt.Errorf("webapi: one %s call is %d bytes, over the %d-byte limit", cl.Method, n, c.opt.MaxBody)
		}
		if size+n > c.opt.MaxBody || i-start >= c.opt.MaxBatch {
			if err := flush(i); err != nil {
				return nil, err
			}
		}
		size += n
	}
	if err := flush(len(calls)); err != nil {
		return nil, err
	}
	return out, nil
}

// Operating modes as the CPU spells them.
const (
	ModeRun     = "run"
	ModeStop    = "stop"
	ModeStartup = "startup"
	ModeHold    = "hold"
)

// OperatingMode returns the CPU's mode: "run", "stop", "startup", "hold",
// or another state of a redundant system.
func (c *Client) OperatingMode(ctx context.Context) (string, error) {
	var m string
	err := c.call(ctx, "Plc.ReadOperatingMode", nil, &m)
	return strings.ToLower(m), err
}

// RequestOperatingMode asks the CPU to change to "run" or "stop". The user
// needs the change_operating_mode permission.
func (c *Client) RequestOperatingMode(ctx context.Context, mode string) error {
	if mode != ModeRun && mode != ModeStop {
		return fmt.Errorf("webapi: mode %q: only %q or %q can be requested", mode, ModeRun, ModeStop)
	}
	var ok bool
	if err := c.call(ctx, "Plc.RequestChangeOperatingMode", map[string]string{"mode": mode}, &ok); err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("webapi: the CPU refused the change to %s", mode)
	}
	return nil
}

// Symbol builds a variable's symbol from a DB (or tag) name and member
// path: Symbol("Tags", "Pump", "Speed") is `"Tags".Pump.Speed`. Members
// that are not plain identifiers are quoted. Names may not contain a
// double quote.
func Symbol(root string, members ...string) (string, error) {
	if root == "" || strings.Contains(root, `"`) {
		return "", fmt.Errorf("webapi: bad root name %q", root)
	}
	var b strings.Builder
	b.WriteString(`"` + root + `"`)
	for _, m := range members {
		if m == "" || strings.Contains(m, `"`) {
			return "", fmt.Errorf("webapi: bad member name %q", m)
		}
		b.WriteByte('.')
		if plainIdent(m) {
			b.WriteString(m)
		} else {
			b.WriteString(`"` + m + `"`)
		}
	}
	return b.String(), nil
}

func plainIdent(s string) bool {
	for i, r := range s {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
