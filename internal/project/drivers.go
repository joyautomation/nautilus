package project

import (
	"fmt"
	"time"

	"github.com/joyautomation/nautilus/eip"
	"github.com/joyautomation/nautilus/server"
	"github.com/joyautomation/nautilus/sparkplug"
)

// DriverStatus adapts the field driver's and Sparkplug node's health into the
// generic server.DriverStatus envelope the HMI components render. Wired as
// server.Options.Drivers so /api/drivers and the stream frame carry it, with
// the server package staying free of any driver dependency.
func (p *Project) DriverStatus(node *sparkplug.Node) func() []server.DriverStatus {
	eipDrv, hasEIP := p.Runtime.Driver.(*eip.Driver)
	if !hasEIP && node == nil {
		return nil
	}
	return func() []server.DriverStatus {
		var out []server.DriverStatus
		if hasEIP {
			out = append(out, eipStatus(eipDrv.Health()))
		}
		if node != nil {
			out = append(out, sparkplugStatus(node.Status()))
		}
		return out
	}
}

func eipStatus(h eip.Health) server.DriverStatus {
	s := server.DriverStatus{
		Kind:      "ethernet-ip",
		Name:      h.Host,
		Detail:    fmt.Sprintf("%s · slot %d", h.Host, h.Slot),
		LastError: h.LastError,
		SinceMs:   h.SinceMs,
	}
	switch {
	case h.Connected:
		s.State = "connected"
		s.Message = fmt.Sprintf("Polling %d tags", h.Tags)
	case h.LastError != "":
		s.State = "error"
		s.Message = "Connect failed — retrying"
	default:
		s.State = "connecting"
		s.Message = "Connecting to controller"
	}
	// A connected driver whose polls are all failing is degraded, not healthy.
	if h.Connected && h.Polls == 0 && h.PollErrors > 0 {
		s.State = "degraded"
		s.Message = "Connected, but reads are failing"
	}
	// Volatile marks the readouts that free-run with traffic rather than
	// with anything an operator would call a change: they ride the block
	// whenever it goes out, but do not by themselves push a driver status
	// onto a delta stream (see server.DriverStatus). Tag count is not one
	// of them — it changing means the subscription changed.
	s.Metrics = []server.DriverMetric{
		{Label: "tags", Value: float64(h.Tags)},
		{Label: "polls", Value: float64(h.Polls), Volatile: true},
		{Label: "poll errors", Value: float64(h.PollErrors), Volatile: true},
		{Label: "reconnects", Value: float64(h.Reconnects), Volatile: true},
	}
	if h.LeafMode > 0 {
		s.Metrics = append(s.Metrics, server.DriverMetric{Label: "leaf structs", Value: float64(h.LeafMode)})
	}
	if h.LastPollMs > 0 {
		// AtMs is the freshness a reader renders; Text is the same age
		// pre-rendered for readers that predate AtMs. The moment itself is
		// what travels, so a delta client can render "0.4s" against the
		// block's own AsOfMs instead of watching a healthy driver's age
		// creep up between blocks.
		s.Metrics = append(s.Metrics, server.DriverMetric{
			Label: "last poll",
			AtMs:  h.LastPollMs,
			Text:  agoText(h.LastPollMs),
		})
	}
	return s
}

func sparkplugStatus(st sparkplug.Status) server.DriverStatus {
	s := server.DriverStatus{
		Kind:      "sparkplug",
		Name:      st.Group + "/" + st.EdgeNode,
		Detail:    st.Broker,
		SinceMs:   st.BornMs,
		LastError: "",
	}
	// State machine over the real Sparkplug flags — the human sentence
	// mirrors tentacle's store-forward banner reasoning.
	switch {
	case !st.Connected:
		s.State = "error"
		s.Message = "Broker unreachable"
		s.SinceMs = 0
	case st.PrimaryHost != "" && !st.HostOnline:
		s.State = "waiting"
		if st.PrimaryHostSeen {
			s.Message = fmt.Sprintf("Waiting for primary host %q (offline)", st.PrimaryHost)
		} else {
			s.Message = fmt.Sprintf("Waiting for primary host %q to announce", st.PrimaryHost)
		}
	case !st.Born:
		s.State = "connecting"
		s.Message = "Connected, publishing birth"
	default:
		s.State = "connected"
		s.Message = fmt.Sprintf("Publishing · bdSeq %d", st.BdSeq)
	}
	// messages and seq free-run with every publish; bdSeq does not — it
	// steps on a rebirth, which is exactly a change worth a frame.
	s.Metrics = []server.DriverMetric{
		{Label: "messages", Value: float64(st.Msgs), Volatile: true},
		{Label: "bdSeq", Value: float64(st.BdSeq)},
		{Label: "seq", Value: float64(st.Seq), Volatile: true},
	}
	if st.LastPubMs > 0 {
		s.Metrics = append(s.Metrics, server.DriverMetric{
			Label: "last publish",
			AtMs:  st.LastPubMs,
			Text:  agoText(st.LastPubMs),
		})
	}
	if sf := st.StoreForward; sf != nil {
		s.Metrics = append(s.Metrics, server.DriverMetric{Label: "buffered", Value: float64(sf.Buffered), Text: fmt.Sprintf("%d / %d", sf.Buffered, sf.Max)})
		if sf.Buffered > 0 && s.State == "connected" {
			s.State = "degraded"
			s.Message = fmt.Sprintf("Draining %d buffered messages", sf.Buffered)
		}
	}
	for _, d := range st.Devices {
		detail := "offline"
		if d.Online {
			detail = fmt.Sprintf("%d tags", d.Tags)
		}
		s.Devices = append(s.Devices, server.DriverDevice{ID: d.ID, Online: d.Online, Detail: detail})
	}
	s.Extra = map[string]any{
		"born":        st.Born,
		"primaryHost": st.PrimaryHost,
	}
	return s
}

// agoText renders an epoch-ms timestamp as a COMPACT, fixed-width-ish
// freshness for a status tile — no "ago" (the tile's LAST POLL / LAST
// PUBLISH label already means that), so it fits one line and never wraps
// (a wrapping value would reflow the card every time it ticked).
func agoText(ms int64) string {
	if ms <= 0 {
		return "—"
	}
	d := time.Since(time.UnixMilli(ms))
	switch {
	case d < 10*time.Second:
		return fmt.Sprintf("%.1fs", d.Seconds()) // 0.4s
	case d < time.Minute:
		return fmt.Sprintf("%.0fs", d.Seconds()) // 42s
	case d < time.Hour:
		return fmt.Sprintf("%.0fm", d.Minutes()) // 5m
	default:
		return fmt.Sprintf("%.0fh", d.Hours()) // 2h
	}
}
