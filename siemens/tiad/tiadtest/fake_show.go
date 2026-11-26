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

// OnlineNeedsUI refuses an online call on an agent without TIA's window,
// word for word (tools/tiad/core/Online.cs).
const OnlineNeedsUI = `this agent runs TIA Portal without its user interface, so there is no window to hold a project online in: start the agent with TIAD_UI=1 (install-tiad.ps1 -Ui) to go online in TIA's window; such an agent refuses every download`

// heldOnlineRefuses is the agent's HeldOnlineRefuses.
func heldOnlineRefuses(s *session, call string) *failure {
	if s.heldOnline == "" {
		return nil
	}
	return fail(http.StatusConflict, tiad.KindBusy, "the session is held online to %s in TIA's window: take it offline first (POST /v1/sessions/%s/online {\"online\": false}), then %s", s.heldOnline, s.id, call)
}

func (f *Fake) releaseOnline(s *session) (string, *failure) {
	was := s.heldOnline
	if was == "" {
		return "", nil
	}
	if f.ReleaseOnlineFails != "" {
		return "", fail(http.StatusInternalServerError, tiad.KindOperationFailed, "TIA: %s", f.ReleaseOnlineFails)
	}
	f.online = append(f.online, "offline "+was)
	s.heldOnline = ""
	return was, nil
}

// SetReleaseOnlineFails is the ReleaseOnlineFails knob ("" releases again).
func (f *Fake) SetReleaseOnlineFails(msg string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ReleaseOnlineFails = msg
}

// OnlineEvents is every hold and release, "online <address>" and
// "offline <address>", in order.
func (f *Fake) OnlineEvents() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.online...)
}

// goOnline is POST /v1/sessions/{id}/online: the agent's Online. The fake
// does not model credentials or certificates going online.
func (f *Fake) goOnline(r *http.Request) (any, *failure) {
	if !f.UI {
		return nil, fail(http.StatusBadRequest, tiad.KindBadRequest, "%s", OnlineNeedsUI)
	}
	s, fl := f.session(r)
	if fl != nil {
		return nil, fl
	}
	var req tiad.OnlineRequest
	if fl := decode(r, &req); fl != nil {
		return nil, fl
	}
	if fl := checkDeviceAttestation(req.DeviceAttestation); fl != nil {
		return nil, fl
	}
	if !req.Online {
		was, fl := f.releaseOnline(s)
		if fl != nil {
			return nil, fl
		}
		return tiad.OnlineResult{Online: false, Address: was}, nil
	}
	addr := ""
	switch {
	case s.station != nil:
		addr = s.station.CPU.Address
	case s.cpu != nil && s.node.read && s.node.address != "":
		addr = s.node.address
	default:
		return nil, fail(http.StatusBadRequest, tiad.KindBadRequest, "the project has no station spec, and its CPU no address to go online to")
	}
	if !contains(f.Targets, addr) {
		return nil, fail(http.StatusForbidden, tiad.KindTargetRefused, "%s is not in TIAD_TARGETS", addr)
	}
	if _, fl := f.checkDevicePin(addr, "online", req.DeviceAttestation); fl != nil {
		return nil, fl
	}
	switch s.heldOnline {
	case addr:
		return tiad.OnlineResult{Online: true, Address: addr, State: "Online"}, nil
	case "":
	default:
		return nil, fail(http.StatusConflict, tiad.KindBusy, "the session is held online to %s, not %s: take it offline first", s.heldOnline, addr)
	}
	if f.blindTrust != nil {
		return nil, fail(http.StatusBadRequest, tiad.KindBadRequest, "%s; nothing went online", loginBlocked(f.blindTrust))
	}
	s.heldOnline = addr
	f.online = append(f.online, "online "+addr)
	return tiad.OnlineResult{Online: true, Address: addr, State: "Online"}, nil
}
