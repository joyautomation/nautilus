package tiadtest

import (
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/joyautomation/nautilus/siemens/tiad"
)

// contractShow: an agent started with TIAD_UI=1 shows a block in TIA's
// editor, and refuses every download; any other agent refuses a show
// before TIA is asked anything.
func contractShow(t *testing.T, start Starter) {
	t.Run("RefusedWithoutUI", func(t *testing.T) { contractShowRefusedWithoutUI(t, start) })
	t.Run("Shown", func(t *testing.T) { contractShowShown(t, start) })
	t.Run("UIRefusesDownload", func(t *testing.T) { contractShowUIRefusesDownload(t, start) })
}

func contractShowRefusedWithoutUI(t *testing.T, start Starter) {
	a, s := compiledBench(t, start)
	h, err := a.Client().Health(ctx)
	if err != nil || h.UI {
		t.Fatalf("health: %+v %v, want ui false", h, err)
	}
	_, err = s.Show(ctx, "naut_Main")
	var e *tiad.Error
	if !errors.As(err, &e) || e.Kind != tiad.KindBadRequest || e.Message != ShowNeedsUI || statusOf(err) != http.StatusBadRequest {
		t.Fatalf("show without UI: %v, want bad_request %q", err, ShowNeedsUI)
	}
	if got := a.Shown(t); len(got) != 0 {
		t.Fatalf("shown without UI: %v, want none", got)
	}
}

func contractShowShown(t *testing.T, start Starter) {
	a, s := compiledBenchWith(t, start, Config{UI: true})
	h, err := a.Client().Health(ctx)
	if err != nil || !h.UI {
		t.Fatalf("health: %+v %v, want ui true", h, err)
	}
	// TIA's block names compare without case; the reply names the block
	// as the project holds it.
	r, err := s.Show(ctx, "NAUT_MAIN")
	if err != nil || r != (tiad.Shown{Block: "naut_Main", Type: "FB", Number: 1, Group: "nautilus"}) {
		t.Fatalf("show naut_Main: %+v %v", r, err)
	}
	// A block at the root: Main [OB1], which TIA made with the CPU.
	r, err = s.Show(ctx, "Main")
	if err != nil || r != (tiad.Shown{Block: "Main", Type: "OB", Number: 1, Group: ""}) {
		t.Fatalf("show Main: %+v %v", r, err)
	}
	_, err = s.Show(ctx, "naut_Missing")
	if tiad.Kind(err) != tiad.KindNotFound || statusOf(err) != http.StatusNotFound {
		t.Fatalf("show of a block the project lacks: %v, want not_found", err)
	}
	_, err = s.Show(ctx, "")
	if tiad.Kind(err) != tiad.KindBadRequest {
		t.Fatalf("show of no block: %v, want bad_request", err)
	}
	if got, want := a.Shown(t), []string{"nautilus/naut_Main", "Main"}; !slices.Equal(got, want) {
		t.Fatalf("shown: %v, want %v", got, want)
	}
	// Showing changed nothing: the project still saves.
	if _, err := s.Save(ctx, false); err != nil {
		t.Fatalf("save after a show: %v", err)
	}
}

func contractShowUIRefusesDownload(t *testing.T, start Starter) {
	a, s := compiledBenchWith(t, start, Config{UI: true})
	for _, dry := range []bool{false, true} {
		req := benchSoftwareLoad
		req.DryRun = dry
		_, err := s.Download(ctx, req)
		var e *tiad.Error
		if !errors.As(err, &e) || e.Kind != tiad.KindBadRequest || e.Message != UIRefusesDownload {
			t.Fatalf("download (dry run %v) on a UI agent: %v, want bad_request %q", dry, err, UIRefusesDownload)
		}
	}
	if l := a.Loaded(t); len(l) != 0 {
		t.Fatalf("loaded: %+v, want nothing", l)
	}
	// Refused before the deploy record's intent line: no record.
	list, err := a.Client().Deploys(ctx, benchCPU)
	if err != nil || len(list.Deploys) != 0 {
		t.Fatalf("deploys: %+v %v, want none", list, err)
	}
}
