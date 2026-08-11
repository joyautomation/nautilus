package project

// tag-files: is how a generated tag set stays a separate, reviewable file
// instead of a 500-line smear through the manifest a person edits — and how
// one project ships to several sites. Both uses depend on the same two
// properties: composition is deterministic, and a name declared twice is an
// error rather than a silent override.

import (
	"strings"
	"testing"
	"testing/fstest"
)

// tagProject builds a project whose files are given as name → contents,
// on top of the shared program fixtures.
func tagProject(files map[string]string) fstest.MapFS {
	m := fsys("")
	for name, body := range files {
		m[name] = &fstest.MapFile{Data: []byte(body)}
	}
	return m
}

const oneTask = "tasks:\n  - program: program.fbd\n"

func names(t *testing.T, m *Manifest) []string {
	t.Helper()
	out := make([]string, 0, len(m.Tags))
	for _, tg := range m.Tags {
		out = append(out, tg.Name)
	}
	return out
}

func TestTagFilesComposeInOrder(t *testing.T) {
	fs := tagProject(map[string]string{
		"nautilus.yaml": oneTask + "tag-files: [tags/a.yaml, tags/b.yaml]\n" +
			"tags:\n  - { name: Inline, role: state, init: 0.0 }\n",
		"tags/a.yaml": "- { name: A1, role: input }\n- { name: A2, role: input }\n",
		"tags/b.yaml": "- { name: B1, role: input }\n",
	})
	m, err := ReadManifest(fs, "")
	if err != nil {
		t.Fatal(err)
	}
	// Listed order, then the manifest's own tags last — so a reader scanning
	// the composed list sees generated bulk first and hand-authored last.
	want := []string{"A1", "A2", "B1", "Inline"}
	if got := names(t, m); !equalStrings(got, want) {
		t.Errorf("composed tags = %v, want %v", got, want)
	}
}

func TestTagFilesRejectDuplicates(t *testing.T) {
	for _, tc := range []struct {
		what  string
		files map[string]string
		// both sources must appear in the message: the whole point is
		// knowing WHERE the two declarations are.
		mentions []string
	}{
		{
			what: "across two tag files",
			files: map[string]string{
				"nautilus.yaml": oneTask + "tag-files: [tags/a.yaml, tags/b.yaml]\n",
				"tags/a.yaml":   "- { name: Dup, role: input }\n",
				"tags/b.yaml":   "- { name: Dup, role: input }\n",
			},
			mentions: []string{"Dup", "tags/a.yaml", "tags/b.yaml"},
		},
		{
			what: "between a tag file and the manifest",
			files: map[string]string{
				"nautilus.yaml": oneTask + "tag-files: [tags/a.yaml]\n" +
					"tags:\n  - { name: Dup, role: state, init: 0.0 }\n",
				"tags/a.yaml": "- { name: Dup, role: input }\n",
			},
			mentions: []string{"Dup", "tags/a.yaml", "nautilus.yaml"},
		},
		{
			what: "twice inside one file",
			files: map[string]string{
				"nautilus.yaml": oneTask + "tags:\n" +
					"  - { name: Dup, role: input }\n" +
					"  - { name: Dup, role: input }\n",
			},
			mentions: []string{"Dup", "nautilus.yaml"},
		},
	} {
		t.Run(tc.what, func(t *testing.T) {
			_, err := ReadManifest(tagProject(tc.files), "")
			if err == nil {
				t.Fatal("a tag declared twice loaded cleanly — last-wins is exactly " +
					"the silent override this must not have")
			}
			for _, want := range tc.mentions {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
		})
	}
}

// A tag file that escapes the project would load in development and vanish
// the moment `nautilus build` archives the directory.
func TestTagFilesStayInsideTheProject(t *testing.T) {
	for _, bad := range []string{"../outside.yaml", "/etc/tags.yaml", "a/../../b.yaml"} {
		_, err := ReadManifest(tagProject(map[string]string{
			"nautilus.yaml": oneTask + "tag-files: [" + bad + "]\n",
		}), "")
		if err == nil {
			t.Errorf("tag-files accepted %q, which is outside the project", bad)
		}
	}
}

func TestTagFileErrors(t *testing.T) {
	for _, tc := range []struct{ what, file, mentions string }{
		{"a typo'd key", "- { name: T, role: input, unti: psi }\n", "unti"},
		{"a mapping instead of a list", "tags:\n  - { name: T, role: input }\n", "bare YAML list"},
	} {
		t.Run(tc.what, func(t *testing.T) {
			_, err := ReadManifest(tagProject(map[string]string{
				"nautilus.yaml": oneTask + "tag-files: [tags/a.yaml]\n",
				"tags/a.yaml":   tc.file,
			}), "")
			if err == nil {
				t.Fatalf("%s loaded cleanly", tc.what)
			}
			if !strings.Contains(err.Error(), tc.mentions) {
				t.Errorf("error %q does not mention %q", err, tc.mentions)
			}
		})
	}

	if _, err := ReadManifest(tagProject(map[string]string{
		"nautilus.yaml": oneTask + "tag-files: [tags/missing.yaml]\n",
	}), ""); err == nil {
		t.Error("a tag-files entry naming no file loaded cleanly")
	}

	// An empty generated file is normal: a generator whose input has no rows
	// yet should not break the project it is wired into.
	m, err := ReadManifest(tagProject(map[string]string{
		"nautilus.yaml":   oneTask + "tag-files: [tags/empty.yaml]\n",
		"tags/empty.yaml": "# generated — no rows yet\n",
	}), "")
	if err != nil {
		t.Fatalf("an empty tag file should compose to nothing, got %v", err)
	}
	if len(m.Tags) != 0 {
		t.Errorf("empty tag file produced %d tags", len(m.Tags))
	}
}

// One project, one set of programs, several manifests: the site chooses
// which tag files it composes. This is the whole per-site variation story.
func TestManifestSelection(t *testing.T) {
	fs := tagProject(map[string]string{
		"nautilus.yaml":      oneTask + "tag-files: [tags/common.yaml]\n",
		"sites/hayward.yaml": oneTask + "tag-files: [tags/common.yaml, tags/dampers.yaml]\n",
		"tags/common.yaml":   "- { name: Common, role: input }\n",
		"tags/dampers.yaml":  "- { name: Damper, role: input }\n",
	})
	for _, tc := range []struct {
		manifest string
		want     []string
	}{
		{"", []string{"Common"}},
		{"nautilus.yaml", []string{"Common"}},
		{"sites/hayward.yaml", []string{"Common", "Damper"}},
	} {
		m, err := ReadManifest(fs, tc.manifest)
		if err != nil {
			t.Fatalf("manifest %q: %v", tc.manifest, err)
		}
		if got := names(t, m); !equalStrings(got, tc.want) {
			t.Errorf("manifest %q composed %v, want %v", tc.manifest, got, tc.want)
		}
	}

	// The programs are shared, so a site manifest is a full project.
	if _, err := Load(fs, "sites/hayward.yaml"); err != nil {
		t.Errorf("a site manifest should load like any other: %v", err)
	}

	if _, err := ReadManifest(fs, "../elsewhere.yaml"); err == nil {
		t.Error("a manifest outside the project loaded cleanly")
	}
	if _, err := ReadManifest(fs, "sites/nope.yaml"); err == nil {
		t.Error("a manifest that does not exist loaded cleanly")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
