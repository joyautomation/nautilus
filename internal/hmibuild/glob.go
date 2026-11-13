package hmibuild

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// import.meta.glob, as Vite has it: a literal pattern or array of them
// ("/src/..." from the app's root, or relative to the file), with
// { eager: true } for modules imported up front instead of loaders. A
// "!" pattern excludes. `**` matches any depth.
var (
	globCall  = regexp.MustCompile(`import\.meta\.glob\(\s*(\[[^\]]*\]|'[^']*'|"[^"]*"|` + "`[^`]*`" + `)\s*(?:,\s*(\{[^}]*\}))?\s*\)`)
	strLit    = regexp.MustCompile(`'([^']*)'|"([^"]*)"|` + "`([^`]*)`")
	eagerOpt  = regexp.MustCompile(`eager\s*:\s*(!0|true)`)
	importOpt = regexp.MustCompile(`import\s*:\s*['"]([\w$]+)['"]`)
)

func expandGlobs(js, appRoot, dir string) (string, error) {
	if !strings.Contains(js, "import.meta.glob") {
		return js, nil
	}
	var head []string
	var firstErr error
	n := 0
	out := globCall.ReplaceAllStringFunc(js, func(call string) string {
		m := globCall.FindStringSubmatch(call)
		eager := eagerOpt.MatchString(m[2])
		named := ""
		if im := importOpt.FindStringSubmatch(m[2]); im != nil {
			named = im[1]
		}
		var include, exclude []string
		absKeys := false
		for i, lm := range strLit.FindAllStringSubmatch(m[1], -1) {
			pat := lm[1] + lm[2] + lm[3]
			neg := strings.HasPrefix(pat, "!")
			pat = strings.TrimPrefix(pat, "!")
			if i == 0 {
				absKeys = strings.HasPrefix(pat, "/")
			}
			base := dir
			if strings.HasPrefix(pat, "/") {
				base, pat = appRoot, strings.TrimPrefix(pat, "/")
			}
			full := filepath.ToSlash(filepath.Join(base, pat))
			if neg {
				exclude = append(exclude, full)
			} else {
				include = append(include, full)
			}
		}
		files, err := globFiles(include, exclude)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		var entries []string
		for _, f := range files {
			// keys as Vite gives them: from the app root for a "/" pattern,
			// else relative to the importing file
			key := filepath.ToSlash(f)
			if absKeys {
				if rel, err := filepath.Rel(appRoot, f); err == nil && !strings.HasPrefix(rel, "..") {
					key = "/" + filepath.ToSlash(rel)
				}
			} else if rel, err := filepath.Rel(dir, f); err == nil {
				key = filepath.ToSlash(rel)
				if !strings.HasPrefix(key, "../") {
					key = "./" + key
				}
			}
			if eager {
				id := fmt.Sprintf("__nx_glob_%d", n)
				n++
				head = append(head, fmt.Sprintf("import * as %s from %s;", id, jsq(f)))
				val := id
				if named != "" {
					val = id + "." + named
				}
				entries = append(entries, fmt.Sprintf("%s: %s", jsq(key), val))
			} else {
				val := fmt.Sprintf("() => import(%s)", jsq(f))
				if named != "" {
					val = fmt.Sprintf("() => import(%s).then((m) => m.%s)", jsq(f), named)
				}
				entries = append(entries, fmt.Sprintf("%s: %s", jsq(key), val))
			}
		}
		return "{" + strings.Join(entries, ", ") + "}"
	})
	if firstErr != nil {
		return "", firstErr
	}
	if len(head) == 0 {
		return out, nil
	}
	return strings.Join(head, "\n") + "\n" + out, nil
}

// globFiles matches slash patterns with *, ? and ** (any depth).
func globFiles(include, exclude []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, pat := range include {
		re, err := globRegexp(pat)
		if err != nil {
			return nil, err
		}
		root := pat
		if i := strings.IndexAny(root, "*?["); i >= 0 {
			root = root[:strings.LastIndex(root[:i], "/")+1]
		}
		_ = filepath.WalkDir(filepath.FromSlash(root), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			sp := filepath.ToSlash(p)
			if !re.MatchString(sp) || seen[sp] {
				return nil
			}
			for _, ex := range exclude {
				if xr, err := globRegexp(ex); err == nil && xr.MatchString(sp) {
					return nil
				}
			}
			seen[sp] = true
			out = append(out, filepath.FromSlash(sp))
			return nil
		})
	}
	sort.Strings(out)
	return out, nil
}

func globRegexp(pat string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pat); i++ {
		c := pat[i]
		switch {
		case c == '*' && i+1 < len(pat) && pat[i+1] == '*':
			i++
			if i+1 < len(pat) && pat[i+1] == '/' {
				i++
				b.WriteString("(?:.*/)?")
			} else {
				b.WriteString(".*")
			}
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}
