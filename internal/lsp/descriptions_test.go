package lsp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestVarCommentsTrailingCommentsInVarBlocksOnly(t *testing.T) {
	const src = `PROGRAM P
(* not a declaration : BOOL; (* nor this *) *)
VAR_EXTERNAL
    LeadFailed : BOOL; (* the lead pump's FailToRun *)
    LeadReq    : BOOL;
    Hi, Lo     : REAL; // both alarm limits
    Spd AT %QW0 : INT := 0; (*   speed ref   *)
    Empty      : BOOL; (**)
END_VAR
var
    tmr : TON; // the post-run timer
end_var
Outside : BOOL; (* after END_VAR: not a declaration *)
END_PROGRAM
`
	got := VarComments(src)
	want := []VarComment{
		{"LeadFailed", "the lead pump's FailToRun", 4},
		{"Hi", "both alarm limits", 6},
		{"Lo", "both alarm limits", 6},
		{"Spd", "speed ref", 7},
		{"tmr", "the post-run timer", 11},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("VarComments\n got  %+v\n want %+v", got, want)
	}
}

func TestDescriptionsForCommentWinsCaseInsensitively(t *testing.T) {
	tags := []ProjectTag{
		{Name: "TempC", Desc: "Tank temperature"},
		{Name: "PumpRun", Desc: "Pump run command"},
		{Name: "LevelPct"}, // no desc: not listed
	}
	src := "PROGRAM P\nVAR_EXTERNAL\n  pumprun : BOOL; (* local note *)\n  TempC : REAL;\nEND_VAR\nEND_PROGRAM\n"
	got := descriptionsFor(tags, src)
	want := map[string]string{"TempC": "Tank temperature", "pumprun": "local note"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("descriptionsFor\n got  %v\n want %v", got, want)
	}
}

// End to end through the JSON-RPC framing, the way the extension asks: a
// ladder file in a manifest project — its open buffer (not the disk copy)
// supplies the VAR comments, the manifest the tag descriptions.
func TestDescriptionsRequest(t *testing.T) {
	prog := writeProject(t, "")
	ld := filepath.Join(filepath.Dir(prog), "interlock.ld")
	if err := os.WriteFile(ld, []byte("PROGRAM Interlock\nEND_PROGRAM\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const buffer = "PROGRAM Interlock\nVAR_EXTERNAL\n  PumpRun : BOOL;\n  TempC : REAL; (* as the operator sees it *)\nEND_VAR\nRUNG r1: TempC ( PumpRun )\nEND_PROGRAM\n"
	s := startSession(t)
	s.recvResponse(s.send("initialize", map[string]any{}, true))
	uri := pathToURI(ld)
	s.send("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{"uri": uri, "languageId": "iec-ld", "version": 1, "text": buffer},
	}, false)
	s.recv() // its diagnostics
	id := s.send("nautilus/descriptions", map[string]any{"textDocument": map[string]any{"uri": uri}}, true)
	var res DescriptionsResult
	if err := json.Unmarshal(s.recvResponse(id), &res); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"TempC":   "as the operator sees it",
		"PumpRun": "Pump run command",
		"Status":  "Operator status line",
	}
	if !reflect.DeepEqual(res.Descriptions, want) {
		t.Fatalf("descriptions\n got  %v\n want %v", res.Descriptions, want)
	}

	// A file the server has not opened is read from disk; outside a
	// project there is nothing to describe, and the answer is {} not null.
	stray := filepath.Join(t.TempDir(), "x.ld")
	if err := os.WriteFile(stray, []byte("PROGRAM X\nEND_PROGRAM\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	id = s.send("nautilus/descriptions", map[string]any{"textDocument": map[string]any{"uri": pathToURI(stray)}}, true)
	raw := s.recvResponse(id)
	if string(raw) != `{"descriptions":{}}` {
		t.Fatalf("stray file: %s", raw)
	}
}

// A description edited in a tag-file (tags/io.yaml), not nautilus.yaml,
// must reach the next request too: the manifest cache also follows the
// modtimes of the tag-files it composed.
func TestProjectTagsCacheFollowsTagFiles(t *testing.T) {
	root := t.TempDir()
	manifest := "name: tank\ntag-files: [tags/io.yaml]\ntasks:\n  - program: program.st\n    scan: 100ms\ndriver:\n  type: memory\n"
	if err := os.WriteFile(filepath.Join(root, "nautilus.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "tags"), 0o755); err != nil {
		t.Fatal(err)
	}
	io := filepath.Join(root, "tags", "io.yaml")
	write := func(desc string) {
		t.Helper()
		if err := os.WriteFile(io, []byte("- { name: M1_StartPB, role: input, init: false, desc: \""+desc+"\" }\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	prog := filepath.Join(root, "program.ld")
	write("start")
	if got := descriptionsFor(projectTags(prog), ""); got["M1_StartPB"] != "start" {
		t.Fatalf("first read: %v", got)
	}
	write("M1 start pushbutton (momentary)")
	ahead := time.Now().Add(2 * time.Second) // a distinct modtime, as TestProjectTagsCacheInvalidates
	if err := os.Chtimes(io, ahead, ahead); err != nil {
		t.Fatal(err)
	}
	if got := descriptionsFor(projectTags(prog), ""); got["M1_StartPB"] != "M1 start pushbutton (momentary)" {
		t.Fatalf("after editing the tag-file: %v — the cache did not follow it", got)
	}
}
