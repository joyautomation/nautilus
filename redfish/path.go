// path.go is the member locator's second half: where, inside one fetched
// Redfish resource, a member's value lives. docs/design/it-drivers.md §3.2.
//
// The grammar is dotted property names with ONE selector form on arrays:
//
//	Status.Health
//	Fans[MemberId=0].Reading
//	Temperatures[Name=Inlet Temp].ReadingCelsius
//	Temperatures[*].ReadingCelsius          (only under agg:, see MemberBinding)
//
// A selector picks the array element whose property equals the literal, by
// the property's string form (a JSON number 0 matches "0"). Positional
// indexes are refused on purpose: BMCs reorder arrays across firmware
// updates, and a binding that silently starts reading the neighbouring fan
// is worse than one that stops resolving. [*] fans out over every element;
// it exists for one member (Server.MaxTempC, the max over a chassis'
// temperatures) and the manifest only accepts it together with agg:.
package redfish

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Path is a parsed member path.
type Path struct {
	src  string
	segs []segment
}

type segment struct {
	name string
	// sel is the array selector on this segment: key/value, or wildcard.
	sel      bool
	wildcard bool
	key, val string
}

// ParsePath compiles a path. Errors name the offending column.
func ParsePath(src string) (*Path, error) {
	if strings.TrimSpace(src) == "" {
		return nil, errors.New("path: empty")
	}
	p := &Path{src: src}
	i := 0
	for {
		start := i
		for i < len(src) && src[i] != '.' && src[i] != '[' {
			if src[i] == ']' {
				return nil, fmt.Errorf("path %q: unexpected ']' at column %d", src, i+1)
			}
			i++
		}
		name := src[start:i]
		if name == "" {
			return nil, fmt.Errorf("path %q: empty property name at column %d", src, start+1)
		}
		if strings.TrimSpace(name) != name {
			return nil, fmt.Errorf("path %q: property %q has surrounding spaces", src, name)
		}
		seg := segment{name: name}
		if i < len(src) && src[i] == '[' {
			end := strings.IndexByte(src[i:], ']')
			if end < 0 {
				return nil, fmt.Errorf("path %q: unclosed '[' at column %d", src, i+1)
			}
			body := src[i+1 : i+end]
			switch {
			case body == "*":
				seg.sel, seg.wildcard = true, true
			case isDigits(body):
				return nil, fmt.Errorf("path %q: positional index [%s] — select by a property instead, e.g. [MemberId=%s] (BMCs reorder arrays across firmware)", src, body, body)
			default:
				eq := strings.IndexByte(body, '=')
				if eq <= 0 {
					return nil, fmt.Errorf("path %q: selector [%s] — want [Key=Value] or [*]", src, body)
				}
				seg.sel = true
				seg.key, seg.val = body[:eq], body[eq+1:]
				if strings.ContainsAny(seg.key, " .[") {
					return nil, fmt.Errorf("path %q: selector key %q must be one property name", src, seg.key)
				}
			}
			i += end + 1
		}
		p.segs = append(p.segs, seg)
		if i == len(src) {
			break
		}
		if src[i] != '.' {
			return nil, fmt.Errorf("path %q: expected '.' at column %d", src, i+1)
		}
		i++
		if i == len(src) {
			return nil, fmt.Errorf("path %q: trailing '.'", src)
		}
	}
	return p, nil
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// String returns the source text.
func (p *Path) String() string { return p.src }

// Wildcard reports whether the path fans out ([*]).
func (p *Path) Wildcard() bool {
	for _, s := range p.segs {
		if s.wildcard {
			return true
		}
	}
	return false
}

// ErrAmbiguous is returned when a [Key=Value] selector matches more than
// one element: the binding cannot know which one it meant.
var ErrAmbiguous = errors.New("selector matches more than one element")

// Eval resolves the path in a decoded resource (json.Unmarshal into any,
// with UseNumber). It returns every non-null value the path reaches — one
// for a plain path, any number for a [*] path — and nil when the path
// resolves to nothing: a missing property, a null, a selector that matches
// no element. Only an ambiguous selector or a type clash (a selector on a
// non-array) is an error, because those say the binding is wrong, not that
// the device is quiet.
func (p *Path) Eval(doc any) ([]any, error) {
	cur := []any{doc}
	for _, s := range p.segs {
		var next []any
		for _, v := range cur {
			obj, ok := v.(map[string]any)
			if !ok {
				continue
			}
			child, ok := obj[s.name]
			if !ok || child == nil {
				continue
			}
			if !s.sel {
				next = append(next, child)
				continue
			}
			arr, ok := child.([]any)
			if !ok {
				return nil, fmt.Errorf("path %q: %s is not an array", p.src, s.name)
			}
			if s.wildcard {
				for _, e := range arr {
					if e != nil {
						next = append(next, e)
					}
				}
				continue
			}
			var hit any
			n := 0
			for _, e := range arr {
				eo, ok := e.(map[string]any)
				if !ok {
					continue
				}
				if kv, ok := eo[s.key]; ok && scalarString(kv) == s.val {
					hit = e
					n++
				}
			}
			if n > 1 {
				return nil, fmt.Errorf("path %q: [%s=%s]: %w", p.src, s.key, s.val, ErrAmbiguous)
			}
			if n == 1 {
				next = append(next, hit)
			}
		}
		cur = next
		if len(cur) == 0 {
			return nil, nil
		}
	}
	return cur, nil
}

// scalarString is a JSON scalar's comparison form: strings as themselves,
// numbers as written, bools as true/false.
func scalarString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	}
	return fmt.Sprint(v)
}

// Deleted, returned from a Map function, removes the property.
var Deleted = &struct{ deleted bool }{true}

// ErrNoTarget is Map's answer when the path's container is not there to
// write into: a selector matching no element, a parent that is not an
// object.
var ErrNoTarget = errors.New("path reaches nothing to write")

// Map is Eval's inverse, for a stand-in serving a plant: it replaces every
// value the path reaches with fn(i, old) — i counts the reached values in
// Eval's order, old is nil for a property that is missing (and is then
// created: a sensor whose Reading went null gets it back). fn returning
// Deleted removes the property. Selectors must match exactly as Eval's do;
// a missing intermediate object is ErrNoTarget, never invented.
func (p *Path) Map(doc any, fn func(i int, old any) any) error {
	n := 0
	var walk func(cur any, segs []segment) error
	walk = func(cur any, segs []segment) error {
		obj, ok := cur.(map[string]any)
		if !ok {
			return fmt.Errorf("path %q: %w", p.src, ErrNoTarget)
		}
		s := segs[0]
		last := len(segs) == 1
		if !s.sel {
			if last {
				v := fn(n, obj[s.name])
				n++
				if v == Deleted {
					delete(obj, s.name)
				} else {
					obj[s.name] = v
				}
				return nil
			}
			return walk(obj[s.name], segs[1:])
		}
		arr, ok := obj[s.name].([]any)
		if !ok {
			return fmt.Errorf("path %q: %s is not an array: %w", p.src, s.name, ErrNoTarget)
		}
		var hits []int
		for i, e := range arr {
			if s.wildcard {
				if e != nil {
					hits = append(hits, i)
				}
				continue
			}
			if eo, ok := e.(map[string]any); ok {
				if kv, ok := eo[s.key]; ok && scalarString(kv) == s.val {
					hits = append(hits, i)
				}
			}
		}
		switch {
		case !s.wildcard && len(hits) > 1:
			return fmt.Errorf("path %q: [%s=%s]: %w", p.src, s.key, s.val, ErrAmbiguous)
		case len(hits) == 0:
			return fmt.Errorf("path %q: [%s]: %w", p.src, s.key+"="+s.val, ErrNoTarget)
		}
		for _, i := range hits {
			if last {
				// A selector names an element, not a value in it: replacing
				// the whole element is never what a binding means.
				return fmt.Errorf("path %q ends in a selector: %w", p.src, ErrNoTarget)
			}
			if err := walk(arr[i], segs[1:]); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(doc, p.segs)
}
