//go:build js && wasm

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall/js"

	"github.com/joyautomation/nautilus/internal/composecmd"
	"github.com/joyautomation/nautilus/internal/diagram"
)

// exec runs the `naut` verbs the VS Code extension spawns, with the CLI's
// arguments and the CLI's output, so the extension's web build can hand its
// execFile calls here unchanged (tools/vscode-iec/src/web/child_process.ts).
// Relative paths resolve against cwd (the browser has one process, so no
// chdir). A verb the browser cannot run says so, exit 2.
func exec(args []string, stdin, cwd string) (stdout, stderr string, code int) {
	var out, errb bytes.Buffer
	abs := func(p string) string {
		if p == "" || p == "-" || filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(cwd, p)
	}
	if len(args) == 0 {
		return "", "naut: no verb\n", 2
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "version":
		return "naut " + version() + " (browser playground)\n", "", 0
	case "compose":
		// --overrides takes a value; every other non-flag is the target.
		var a []string
		for i := 0; i < len(rest); i++ {
			switch r := rest[i]; {
			case r == "--overrides" || r == "-overrides":
				a = append(a, r)
				if i+1 < len(rest) {
					i++
					a = append(a, abs(rest[i]))
				}
			case strings.HasPrefix(r, "-"):
				a = append(a, r)
			default:
				a = append(a, abs(r))
			}
		}
		c := composecmd.Run(a, strings.NewReader(stdin), &out, &errb)
		return out.String(), errb.String(), c
	case "ld", "fbd", "sfc":
		if len(rest) == 0 {
			return "", fmt.Sprintf("naut %s: graph or edit\n", verb), 2
		}
		switch rest[0] {
		case "graph":
			if len(rest) < 2 || len(rest) > 3 {
				return "", fmt.Sprintf("naut %s graph: <file|-> [file]\n", verb), 2
			}
			src, at := stdin, ""
			if rest[1] == "-" {
				if len(rest) == 3 {
					at = abs(rest[2])
				}
			} else {
				at = abs(rest[1])
				b, err := os.ReadFile(at)
				if err != nil {
					j, _ := json.Marshal(map[string]string{"error": err.Error()})
					return string(j) + "\n", "", 2
				}
				src = string(b)
			}
			m, err := diagram.Graph(verb, src, at)
			if err != nil {
				j, _ := json.Marshal(map[string]string{"error": err.Error()})
				return string(j) + "\n", "", 1
			}
			j, _ := json.Marshal(m)
			return string(j) + "\n", "", 0
		case "edit":
			// A request's file names its project, for library pins.
			var req map[string]any
			body := stdin
			if json.Unmarshal([]byte(stdin), &req) == nil {
				if f, ok := req["file"].(string); ok && f != "" {
					req["file"] = abs(f)
					b, _ := json.Marshal(req)
					body = string(b)
				}
			}
			v, c := diagram.Edit(verb, []byte(body))
			j, _ := json.Marshal(v)
			return string(j) + "\n", "", c
		}
	case "test":
		list, asJSON, verbose, pattern, dir := false, false, false, "", "."
		for i := 0; i < len(rest); i++ {
			switch r := strings.TrimPrefix(rest[i], "-"); {
			case r == "list" || r == "-list":
				list = true
			case r == "json" || r == "-json":
				asJSON = true
			case r == "v" || r == "-v":
				verbose = true
			case r == "run" || r == "-run":
				if i+1 < len(rest) {
					i++
					pattern = rest[i]
				}
			case !strings.HasPrefix(rest[i], "-"):
				dir = rest[i]
			default:
				return "", fmt.Sprintf("naut test: %s is not available in the browser playground\n", rest[i]), 2
			}
		}
		s, failed, err := runSuitesAs(abs(dir), pattern, list, !list && !asJSON, verbose)
		if err != nil {
			return "", "naut test: " + err.Error() + "\n", 1
		}
		if failed > 0 {
			return s, "", 1
		}
		return s, "", 0
	}
	return "", fmt.Sprintf("naut %s: not available in the browser playground (install naut to use it)\n", verb), 2
}

func jsExec(_ js.Value, a []js.Value) any {
	var args []string
	for i := 0; i < a[0].Length(); i++ {
		args = append(args, a[0].Index(i).String())
	}
	stdin, cwd := "", "/project"
	if len(a) > 1 && a[1].Type() == js.TypeString {
		stdin = a[1].String()
	}
	if len(a) > 2 && a[2].Type() == js.TypeString && a[2].String() != "" {
		cwd = a[2].String()
	}
	return promise(func() any {
		o, e, c := exec(args, stdin, cwd)
		return js.ValueOf(map[string]any{"stdout": o, "stderr": e, "code": c})
	})
}

func version() string {
	return lspVersion()
}
