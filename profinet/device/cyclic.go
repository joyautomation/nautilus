package device

import (
	"context"
	"encoding/binary"
	"runtime"
	"time"

	"github.com/joyautomation/nautilus/profinet/transport"
	"github.com/joyautomation/nautilus/profinet/wire"
)

// startCyclic starts the AR's provider loop once (d.mu held).
func (d *Device) startCyclic(a *ar) {
	if a.cyclic || d.runCtx == nil {
		return
	}
	a.cyclic = true
	ctx, cancel := context.WithCancel(d.runCtx)
	a.cancel = cancel
	if a.dataAt.IsZero() {
		a.dataAt = time.Now()
	}
	go d.provide(ctx, a)
}

// provide sends the input frame every cycle and watches the controller's
// output frames. It runs on a locked OS thread so the Go scheduler does
// not migrate it mid-cycle (the realtime work's placement applies to the
// process; see docs/design/realtime.md).
func (d *Device) provide(ctx context.Context, a *ar) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	period := a.in.period()
	if period <= 0 {
		period = time.Millisecond
	}
	t := time.NewTicker(period)
	defer t.Stop()
	csdu := make([]byte, max(int(a.in.dataLen), wire.MinCSDU))
	var frame []byte
	var cycle uint16
	var fails int
	var skipUntil time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if d.watchdogExpired(a, now) {
				return
			}
			if now.Before(skipUntil) {
				continue
			}
			ds := d.fillInputs(a, csdu)
			// The counter advances by the cycle in units of 31.25 µs.
			cycle += a.in.scf * a.in.rr
			frame = wire.AppendCyclic(frame[:0], a.in.frameID, csdu[:a.in.dataLen], cycle, ds)
			if err := d.cfg.Port.Send(a.ctrlMAC, wire.EtherTypePNIO, frame); err != nil {
				if err == transport.ErrClosed {
					return
				}
				// A dead NIC must not cost a core: skip cycles, doubling
				// from 100 ms to 3.2 s, until a send gets through.
				fails++
				skipUntil = now.Add(100 * time.Millisecond << min(fails-1, 5))
				if fails == 1 || fails%100 == 0 {
					d.log.Warn("profinet device: input frame send failed", "error", err, "consecutive", fails)
				}
				continue
			}
			fails = 0
		}
	}
}

// fillInputs lays out this cycle's C-SDU at the controller's FrameOffsets
// and returns the DataStatus to send.
func (d *Device) fillInputs(a *ar, csdu []byte) uint8 {
	clear(csdu)
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, o := range a.in.objs {
		addr := Addr{o.SlotNumber, o.SubslotNumber}
		n := int(a.expected[addr].inLen)
		off := int(o.FrameOffset)
		if off+n+1 > len(csdu) {
			continue
		}
		good := d.inValid && !a.wrong[addr]
		if _, known := d.subs[addr]; !known {
			good = false
		}
		if good {
			copy(csdu[off:off+n], d.in[addr])
		}
		csdu[off+n] = iops(good)
	}
	// Our consumer status for each output subslot: Good unless it does
	// not match the layout.
	for _, c := range a.in.iocs {
		if off := int(c.FrameOffset); off < len(csdu) {
			csdu[off] = iops(!a.wrong[Addr{c.SlotNumber, c.SubslotNumber}])
		}
	}
	if a.appReady.Load() {
		return wire.DataStatusRunNormal
	}
	return wire.DataStatusNormal
}

func iops(good bool) uint8 {
	if good {
		return wire.IOxSGood
	}
	return wire.IOxSBad
}

// watchdogExpired ends the AR when the controller's output frames have
// stopped: watchdog after the last one, or firstOutputGrace after DATA
// began when none came at all.
func (d *Device) watchdogExpired(a *ar, now time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ar != a {
		return true
	}
	if a.out == nil {
		return false
	}
	switch {
	case !a.lastOut.IsZero() && now.Sub(a.lastOut) > a.watchdog:
	case a.lastOut.IsZero() && !a.dataAt.IsZero() && now.Sub(a.dataAt) > firstOutputGrace:
	default:
		return false
	}
	d.counters.aborts++
	d.endAR(a, "the controller's output frames stopped (watchdog "+a.watchdog.String()+")")
	return true
}

// handleOutputFrame takes the controller's output frame into the image.
func (d *Device) handleOutputFrame(f transport.Frame) {
	id := binary.BigEndian.Uint16(f.Payload)
	d.mu.Lock()
	defer d.mu.Unlock()
	a := d.ar
	if a == nil || a.out == nil || id != a.out.frameID || !macEqual(f.Src, a.ctrlMAC) {
		return
	}
	fr, err := wire.ParseCyclic(f.Payload, int(a.out.dataLen))
	if err != nil {
		return
	}
	a.lastOut = time.Now()
	d.counters.outputFrames++
	d.snapInfo.fresh = true
	d.snapInfo.dataStatus = fr.DataStatus
	if wire.MeaningOf(fr.DataStatus) == wire.StatusInvalid {
		return // the frame carries no usable data; keep the last image
	}
	for _, o := range a.out.objs {
		addr := Addr{o.SlotNumber, o.SubslotNumber}
		buf, ok := d.out[addr]
		if !ok {
			continue
		}
		n := int(a.expected[addr].outLen)
		off := int(o.FrameOffset)
		if off+n+1 > len(fr.CSDU) {
			continue
		}
		good := wire.IOxSGoodState(fr.CSDU[off+n]) && !a.wrong[addr] && n == len(buf)
		d.outGood[addr] = good
		if good {
			copy(buf, fr.CSDU[off:off+n])
		}
	}
}
