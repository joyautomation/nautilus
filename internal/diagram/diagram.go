// Package diagram is the graph/edit half of the diagram editors: what
// `naut ld|fbd|sfc graph` and `naut ld|fbd|sfc edit` answer, without the
// process around it. The CLI wraps it in stdin/stdout for the VS Code
// extension; the browser playground (cmd/naut-wasm) calls it directly, so
// both editors are handed exactly the same models and edits.
package diagram

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/joyautomation/nautilus/internal/lsp"
	"github.com/joyautomation/nautilus/internal/stproject"
	"github.com/joyautomation/nautilus/lang/fbd"
	"github.com/joyautomation/nautilus/lang/ld"
	"github.com/joyautomation/nautilus/lang/sfc"
	"github.com/joyautomation/nautilus/lang/st"
)

// Graph is the model `naut <lang> graph` prints for src. at is the file the
// source belongs to, for its project's libraries and tags ("" for a buffer
// with no file). An error is the one the CLI prints as {"error": ...}.
func Graph(lang, src, at string) (any, error) {
	switch lang {
	case "ld":
		// The project's library sources are in scope, so an FB element shows
		// the pins the rung's power really uses (a user block's, not IN/Q).
		libs := libraries(at)
		model, err := ld.Graph(src, libs...)
		if err != nil {
			return nil, err
		}
		// The manifest's tags ride along (with a path to find the project
		// by): a retag to a tag this file doesn't declare can then offer to
		// declare it, typed, in VAR_EXTERNAL.
		return struct {
			*ld.Model
			Tags  []lsp.ProjectTag `json:"tags,omitempty"`
			Known []string         `json:"known,omitempty"`
		}{model, Tags(at), ProjectNames(libs)}, nil
	case "fbd":
		model, err := fbd.GraphWithLibs(src, libraries(at))
		if err != nil {
			return nil, err
		}
		// The manifest's tags ride along, as for ld: every program has them
		// in scope without a declaration (#177/#210), so the palette and a
		// chip's retag offer them beside the file's own variables.
		return struct {
			*fbd.Model
			Tags []lsp.ProjectTag `json:"tags,omitempty"`
		}{model, Tags(at)}, nil
	case "sfc":
		model, err := sfc.Graph(src)
		if err != nil {
			return nil, err
		}
		// The manifest's tags ride along: an action association naming a
		// tag is a tag, not a new action waiting for its body (#177/#210).
		return struct {
			*sfc.Model
			Tags []lsp.ProjectTag `json:"tags,omitempty"`
		}{model, Tags(at)}, nil
	}
	return nil, fmt.Errorf("unknown diagram language %q", lang)
}

// Edit answers one `naut <lang> edit` request (the JSON the extension writes
// to its stdin) with the value the CLI prints and its exit code: 0 edits,
// 1 an op that could not apply, 2 a request that did not decode.
func Edit(lang string, request []byte) (any, int) {
	var req struct {
		Source string          `json:"source"`
		Op     json.RawMessage `json:"op"`
		// File, when given, names the source on disk so the project's
		// library sources are in scope for a user block's pins.
		File string `json:"file,omitempty"`
	}
	bad := map[string]string{"error": "expected {\"source\": ..., \"op\": {...}} on stdin"}
	if err := json.Unmarshal(request, &req); err != nil {
		return bad, 2
	}
	switch lang {
	case "ld":
		var op ld.EditOp
		if err := unmarshalOp(req.Op, &op); err != nil {
			return bad, 2
		}
		edits, err := ld.ApplyEdit(req.Source, op, libraries(req.File)...)
		if err != nil {
			return map[string]string{"error": err.Error()}, 1
		}
		return map[string]any{"edits": edits}, 0
	case "fbd":
		var op fbd.EditOp
		if err := unmarshalOp(req.Op, &op); err != nil {
			return bad, 2
		}
		edits, err := fbd.ApplyEdit(req.Source, op, libraries(req.File)...)
		if err != nil {
			return map[string]string{"error": err.Error()}, 1
		}
		if edits == nil {
			edits = []fbd.TextEdit{}
		}
		return map[string]any{"edits": edits}, 0
	case "sfc":
		var op sfc.EditOp
		if err := unmarshalOp(req.Op, &op); err != nil {
			return bad, 2
		}
		edits, err := sfc.ApplyEdit(req.Source, op)
		if err != nil {
			return map[string]string{"error": err.Error()}, 1
		}
		if edits == nil {
			edits = []sfc.TextEdit{}
		}
		return map[string]any{"edits": edits}, 0
	}
	return map[string]string{"error": fmt.Sprintf("unknown diagram language %q", lang)}, 2
}

// unmarshalOp decodes an op the way decoding it as a struct field would: an
// absent or null op is the zero op, not an error.
func unmarshalOp(raw json.RawMessage, op any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, op)
}

// libraries is the project's library sources for the file at, or none for
// a buffer with no file.
func libraries(at string) []string {
	if at == "" {
		return nil
	}
	_, libs, _ := stproject.PreludeSources(at, nil)
	return libs
}

// Tags is the manifest's tags for the project a diagram file belongs to;
// nil for a buffer with no file.
func Tags(at string) []lsp.ProjectTag {
	if at == "" {
		return nil
	}
	abs, err := filepath.Abs(at)
	if err != nil {
		return nil
	}
	return lsp.ProjectTags(abs)
}

// ProjectNames are the names the project's libraries put in scope that are
// neither tags nor variables: VAR_GLOBAL CONSTANT constants (#176) and
// enumeration members (#238). A rung naming one needs no declaration, so
// the ladder's declare offer leaves it out.
func ProjectNames(libs []string) []string {
	var out []string
	for _, src := range libs {
		prog, err := st.Parse(src)
		if err != nil {
			continue
		}
		for _, vb := range prog.GlobalConsts {
			for _, vd := range vb.Variables {
				out = append(out, vd.Name)
			}
		}
		for _, td := range prog.TypeDecls {
			if e, ok := td.Type.(*st.EnumType); ok {
				for _, m := range e.Members {
					out = append(out, m.Name)
				}
			}
		}
	}
	return out
}
