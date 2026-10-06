package writer

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/logix/logixd"
)

// The SDK import + build of logix-authoring.md §5.4: every fixture the
// writer emits is converted to an ACD and compiled by the real Logix
// Designer SDK, through logixd on the licensed Windows machine. This is
// the test that catches what the structural round trip cannot — a
// mnemonic the importer accepts and the compiler rejects, an operand the
// build types differently, an envelope attribute the SDK will not take.
//
//	NAUTILUS_LOGIXD_URL=http://127.0.0.1:18188 \
//	NAUTILUS_LOGIXD_TOKEN=... go test ./logix/writer/ -run TestSDK -v
//
// A failure here is a new rule for rules.go (§7a, "errors surface late"),
// never a workaround.
func TestSDKConvertAndBuild(t *testing.T) {
	url := os.Getenv("NAUTILUS_LOGIXD_URL")
	if url == "" {
		t.Skip("set NAUTILUS_LOGIXD_URL (and _TOKEN) to build the writer's output with the Studio 5000 SDK")
	}
	c := logixd.New(url, os.Getenv("NAUTILUS_LOGIXD_TOKEN"))
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	p, err := c.Probe(ctx)
	if err != nil {
		// A URL in the environment with no reachable or authorized agent
		// behind it is a skip, not a failure: this test proves the writer
		// against the SDK, not the session's plumbing.
		t.Skipf("logixd at %s not usable: %v", url, err)
	}
	if !p.Usable {
		var failed []string
		for _, g := range p.Gates {
			if !g.OK {
				failed = append(failed, g.Name+" ("+g.Detail+")")
			}
		}
		t.Skipf("SDK not usable: %s", strings.Join(failed, "; "))
	}

	cases := map[string]Options{
		"demoline":    demoOpts(),
		"subset":      {},
		"implicit-st": {}, // conformance/implicit-st with its manifest (#248)
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			var doc []byte
			var diags []Diag
			var err error
			if name == "implicit-st" {
				src, o := implicitSTOpts(t)
				doc, diags, err = WriteST(src, o)
			} else {
				doc, diags, err = Write(fixture(t, name+".ld"), opts)
			}
			if err != nil || len(diags) > 0 {
				t.Fatalf("write: %v %v", err, diags)
			}
			run := "writer-" + name + "-" + time.Now().UTC().Format("150405.000")
			l5x := path.Join(run, name+".L5X")
			acd := path.Join(run, name+".ACD")
			if err := c.PutFile(ctx, l5x, doc); err != nil {
				t.Fatalf("stage: %v", err)
			}
			if _, evs, err := c.Convert(ctx, l5x, acd, false); err != nil {
				t.Fatalf("L5X → ACD (the SDK import): %v\n%s", err, events(evs))
			}
			s, err := c.Open(ctx, acd)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer s.Close(context.Background())
			res, evs, err := s.Build(ctx, logixd.BuildDefault)
			if err != nil {
				t.Fatalf("build: %v\n%s", err, events(evs))
			}
			t.Logf("%s: imported and built in %dms", name, res.ElapsedMs)
		})
	}
}

func events(evs []logixd.Event) string {
	var b strings.Builder
	for _, e := range evs {
		if e.Kind != "progress" {
			fmt.Fprintf(&b, "  %s\n", e)
		}
	}
	return b.String()
}
