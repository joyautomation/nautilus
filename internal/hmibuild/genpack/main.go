// genpack writes internal/hmibuild/packs/packs.tar.gz from an npm install
// of the closed package set (packs/generate.sh runs it).
//
//	go run ./internal/hmibuild/genpack <packs dir> <out.tar.gz>
package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/joyautomation/nautilus/internal/hmibuild"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: genpack <packs dir> <out.tar.gz>")
		os.Exit(2)
	}
	var buf bytes.Buffer
	dir, err := filepath.Abs(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "genpack:", err)
		os.Exit(1)
	}
	if err := hmibuild.Pack(dir, &buf); err != nil {
		if e, ok := err.(*hmibuild.Error); ok {
			for _, m := range e.Messages {
				fmt.Fprintln(os.Stderr, "genpack:", m)
			}
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "genpack:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(os.Args[2], buf.Bytes(), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "genpack:", err)
		os.Exit(1)
	}
	fmt.Printf("%s: %d bytes\n", os.Args[2], buf.Len())
}
