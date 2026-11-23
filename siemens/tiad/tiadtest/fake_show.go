package tiadtest

import (
	"net/http"
	"strings"

	"github.com/joyautomation/nautilus/siemens/tiad"
)

// The agent's refusals of an agent's UI mode (TIAD_UI), word for word
// (tools/tiad/core/Show.cs, DownloadCalls.cs).
const (
	// ShowNeedsUI refuses a show on an agent without TIA's window.
	ShowNeedsUI = `this agent runs TIA Portal without its user interface, so there is no editor to show a block in: start the agent with TIAD_UI=1 (install-tiad.ps1 -Ui) to show one; such an agent refuses every download`
	// UIRefusesDownload refuses every download on an agent with it.
	UIRefusesDownload = `this agent runs TIA Portal with its user interface (TIAD_UI=1), for viewing only, and refuses every download: a click or a dialog in the visible Portal could change or stall it. Restart the agent without TIAD_UI to download; nothing was sent to the CPU`
)

// show is POST /v1/sessions/{id}/show: the agent's Show.
func (f *Fake) show(r *http.Request) (any, *failure) {
	if !f.UI {
		return nil, fail(http.StatusBadRequest, tiad.KindBadRequest, "%s", ShowNeedsUI)
	}
	s, fl := f.session(r)
	if fl != nil {
		return nil, fl
	}
	var req struct {
		Block string `json:"block"`
	}
	if fl := decode(r, &req); fl != nil {
		return nil, fl
	}
	if req.Block == "" {
		return nil, fail(http.StatusBadRequest, tiad.KindBadRequest, "block: a program block's name is required")
	}
	for _, b := range s.projectBlocks() {
		if !strings.EqualFold(b.name, req.Block) {
			continue
		}
		name := b.name
		if b.group != "" {
			name = b.group + "/" + b.name
		}
		f.shown = append(f.shown, name)
		return tiad.Shown{Block: b.name, Type: b.kind, Number: b.number, Group: b.group}, nil
	}
	return nil, fail(http.StatusNotFound, tiad.KindNotFound, "no program block %q in %s", req.Block, s.project)
}
