package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/joyautomation/nautilus/lang/ir"
	"gopkg.in/yaml.v3"
)

// manifestLoc places a manifest diagnostic. A gcc-style line starts with the
// file it is about so a terminal or an editor can jump there; the project
// directory is not a file, so printing it (as ".") told the reader nothing.
type manifestLoc struct {
	// file is the manifest's path as the person typed the project's: bare
	// "nautilus.yaml" when checking ".", "plant/nautilus.yaml" otherwise.
	file string
	// tags maps a tag's name key to where its `name:` value sits in the
	// manifest, so a diagnostic about one tag can carry its line.
	tags map[string][2]int
}

func newManifestLoc(dir, manifestName string) manifestLoc {
	label := manifestLabel(manifestName)
	l := manifestLoc{file: filepath.Join(dir, filepath.FromSlash(label)), tags: map[string][2]int{}}
	raw, err := os.ReadFile(l.file)
	if err != nil {
		return l
	}
	var doc yaml.Node
	if yaml.Unmarshal(raw, &doc) != nil || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return l
	}
	top := doc.Content[0].Content
	for i := 0; i+1 < len(top); i += 2 {
		if top[i].Value != "tags" || top[i+1].Kind != yaml.SequenceNode {
			continue
		}
		for _, entry := range top[i+1].Content {
			if entry.Kind != yaml.MappingNode {
				continue
			}
			for j := 0; j+1 < len(entry.Content); j += 2 {
				if entry.Content[j].Value == "name" {
					v := entry.Content[j+1]
					l.tags[ir.NameKey(v.Value)] = [2]int{v.Line, v.Column}
				}
			}
		}
	}
	return l
}

// tag is the location of one tag's entry: file:line:col when the manifest
// itself declares it, else just the file (a tag composed from a tag file, or
// a name the decoder could not place, has no line here to give).
func (l manifestLoc) tag(name string) string {
	if p, ok := l.tags[ir.NameKey(name)]; ok && name != "" {
		return fmt.Sprintf("%s:%d:%d", l.file, p[0], p[1])
	}
	return l.file
}

var tagInError = regexp.MustCompile(`^tag (\S+)`)

// tagNamedInError pulls the tag out of a runtime error that begins "tag X",
// or "" when the error names none.
func tagNamedInError(err error) string {
	if m := tagInError.FindStringSubmatch(err.Error()); m != nil {
		return m[1]
	}
	return ""
}

// say is a manifest error as one diagnostic line. The decoder's own errors
// already begin with the manifest's name, so repeating the file in front of
// them would print it twice.
func (l manifestLoc) say(err error) string {
	if strings.HasPrefix(err.Error(), filepath.Base(l.file)+":") {
		if d := filepath.Dir(l.file); d != "." {
			return d + string(filepath.Separator) + err.Error()
		}
		return err.Error()
	}
	return l.file + ": " + err.Error()
}
