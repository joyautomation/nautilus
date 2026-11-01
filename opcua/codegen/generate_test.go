package codegen

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/opcua"
)

func v(path, typ string, scalar bool, rank int32, writable bool) opcua.Variable {
	p := strings.Split(path, "/")
	return opcua.Variable{
		Path: p, NodeID: "ns=2;s=" + path, Namespace: "urn:x", Type: typ,
		Scalar: scalar, ValueRank: rank, Readable: true, Writable: writable, DisplayName: p[len(p)-1],
	}
}

// Names are the browse path joined with "_" and sanitized; arrays and
// non-scalar types are skipped with a reason; --writable never makes a node
// the server marks read-only writable; a collision is an error.
func TestGenerate(t *testing.T) {
	br := opcua.BrowseResult{
		Namespaces: []string{"http://opcfoundation.org/UA/", "urn:x"},
		Variables: []opcua.Variable{
			v("Plant/Pump 1/Speed", "Double", true, -1, true),
			v("Plant/Pump 1/Run", "Boolean", true, -1, false),
			v("Plant/Trend", "Double", true, 1, false),
			v("Plant/Recipe", "Structure", false, -1, false),
		},
	}
	out, err := Generate(br, Options{WritablePatterns: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]opcua.TagBinding{}
	for _, b := range out.Manifest.Tags {
		got[b.Name] = b
	}
	if len(got) != 2 || !got["Plant_Pump_1_Speed"].Writable || got["Plant_Pump_1_Run"].Writable {
		t.Errorf("bindings = %+v", out.Manifest.Tags)
	}
	skipped := strings.Join(out.Skipped, "\n")
	if !strings.Contains(skipped, "Plant_Trend: array") || !strings.Contains(skipped, "Plant_Recipe: type Structure is not a scalar") {
		t.Errorf("skipped = %q", out.Skipped)
	}

	none, err := Generate(br, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range none.Manifest.Tags {
		if b.Writable {
			t.Errorf("%s is writable with no --writable", b.Name)
		}
	}

	br.Variables = append(br.Variables, v("Plant/Pump_1/Speed", "Double", true, -1, false))
	if _, err := Generate(br, Options{}); err == nil || !strings.Contains(err.Error(), "Plant_Pump_1_Speed") {
		t.Errorf("a name collision was not an error: %v", err)
	}
}
