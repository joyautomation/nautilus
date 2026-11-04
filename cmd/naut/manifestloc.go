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
	// tags maps a tag's name key to where its `name:` value sits — in the
	// manifest, or in the tag file (tag-files:) that declares it — so a
	// diagnostic about one tag can carry its file and line.
	tags map[string]tagPos
	// dir and label are file's two halves: the project directory, and the
	// manifest's name relative to it as the loader's own errors spell it
	// ("nautilus.yaml", or "sites/bad.yaml" under -m).
	dir, label string
}

// tagPos is where one tag's `name:` value sits.
type tagPos struct {
	file      string
	line, col int
}

func newManifestLoc(dir, manifestName string) manifestLoc {
	label := manifestLabel(manifestName)
	l := manifestLoc{file: filepath.Join(dir, filepath.FromSlash(label)), tags: map[string]tagPos{}, dir: dir, label: label}
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
		if top[i+1].Kind != yaml.SequenceNode {
			continue
		}
		switch top[i].Value {
		case "tags":
			l.placeTags(top[i+1], l.file)
		case "tag-files":
			// A tag file is a bare YAML list of tags, its paths relative
			// to the project directory. A tag may be declared once
			// (composeTags), so each name lands in one file.
			for _, f := range top[i+1].Content {
				path := filepath.Join(dir, filepath.FromSlash(f.Value))
				raw, err := os.ReadFile(path)
				if err != nil {
					continue
				}
				var tf yaml.Node
				if yaml.Unmarshal(raw, &tf) != nil || len(tf.Content) == 0 || tf.Content[0].Kind != yaml.SequenceNode {
					continue
				}
				l.placeTags(tf.Content[0], path)
			}
		}
	}
	return l
}

// placeTags records where each `name:` in a sequence of tag entries sits.
func (l manifestLoc) placeTags(seq *yaml.Node, file string) {
	for _, entry := range seq.Content {
		if entry.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j+1 < len(entry.Content); j += 2 {
			if entry.Content[j].Value == "name" {
				v := entry.Content[j+1]
				l.tags[ir.NameKey(v.Value)] = tagPos{file, v.Line, v.Column}
			}
		}
	}
}

// tag is the location of one tag's entry: file:line:col in the manifest or
// the tag file that declares it, else just the manifest (a name the decoder
// could not place has no line here to give).
func (l manifestLoc) tag(name string) string {
	if p, ok := l.tags[ir.NameKey(name)]; ok && name != "" {
		return fmt.Sprintf("%s:%d:%d", p.file, p.line, p.col)
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
	if strings.HasPrefix(err.Error(), l.label+":") {
		if l.dir != "" && l.dir != "." {
			return filepath.Join(l.dir, err.Error()) // the label already names the file under dir
		}
		return err.Error()
	}
	return l.file + ": " + err.Error()
}
