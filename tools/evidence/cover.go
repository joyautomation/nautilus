package main

import (
	"bufio"
	"io"
	"path"
	"strconv"
	"strings"
)

// readCover sums a `go test -coverprofile` file per package: package dir
// → {statements, covered}. A block listed more than once (profiles from
// several runs concatenated) counts once, covered if any run covered it.
func readCover(rd io.Reader) (map[string][2]int, error) {
	type block struct {
		stmts   int
		covered bool
	}
	blocks := map[string]*block{}
	sc := bufio.NewScanner(rd)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "mode:") {
			continue
		}
		// file.go:12.34,15.2 3 1
		f := strings.Fields(line)
		if len(f) != 3 {
			continue
		}
		stmts, err1 := strconv.Atoi(f[1])
		count, err2 := strconv.Atoi(f[2])
		if err1 != nil || err2 != nil {
			continue
		}
		b := blocks[f[0]]
		if b == nil {
			b = &block{stmts: stmts}
			blocks[f[0]] = b
		}
		b.covered = b.covered || count > 0
	}
	out := map[string][2]int{}
	for key, b := range blocks {
		file, _, _ := strings.Cut(key, ":")
		dir := pkgDir(path.Dir(file))
		c := out[dir]
		c[0] += b.stmts
		if b.covered {
			c[1] += b.stmts
		}
		out[dir] = c
	}
	return out, sc.Err()
}
