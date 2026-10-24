package optommp

// stream.go is the processor-side configuration a manifest can ask for,
// written on every connect, and the loop that receives a stream:
//
//   - the communication watchdog (F0380010), when the manifest sets one;
//   - the custom data area's pointers (F0D50000), one per word the scan
//     needs, checked again every few seconds — another client rewriting
//     them would otherwise hand the driver someone else's data;
//   - the stream (F03FFFC0): the custom data area, every interval, to this
//     host's address on the connection and a local UDP port.
//
// The driver records the pointers and the streaming configuration (which
// is stored to flash) as it first found them and puts both back on a
// clean stop. The watchdog it leaves set: a controller that has stopped
// is what it is for. A stream that misses five
// intervals (at least 100 ms) makes every tag Stale until the next one
// arrives; none for the request timeout ends the session, and the
// reconnect configures the stream again. While a stream runs, the only
// TCP traffic is writes, latch clears and the chassis check — so with a
// watchdog set the driver also reads at a third of the watchdog time, to
// keep it fed.

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"time"

	nio "github.com/joyautomation/nautilus/io"
)

// configure writes the watchdog, the custom data area and the stream the
// manifest asks for. It returns the stream's socket, or nil when polling.
func (d *Driver) configure(c *Client) (*net.UDPConn, error) {
	if err := d.configureWatchdog(c); err != nil {
		return nil, err
	}
	if !d.custom {
		return nil, nil
	}
	if err := d.configureCustom(c); err != nil {
		return nil, err
	}
	if d.m.Stream == 0 {
		return nil, nil
	}
	return d.configureStream(c)
}

func (d *Driver) configureWatchdog(c *Client) error {
	cur, err := c.ReadUint32(addrWatchdog)
	if err != nil {
		return err
	}
	if d.m.Watchdog != nil {
		want := uint32(time.Duration(*d.m.Watchdog) / time.Millisecond)
		if cur != want {
			if err := c.Write(addrSetWatchdog, be32(want)); err != nil {
				return fmt.Errorf("optommp: could not set the watchdog: %w", err)
			}
			got, err := c.ReadUint32(addrWatchdog)
			if err != nil {
				return err
			}
			if got != want {
				return fmt.Errorf("optommp: wrote watchdog %d ms, the unit reads %d ms", want, got)
			}
			d.log.Info("optommp: set the communication watchdog", "host", d.m.Host, "from_ms", cur, "to_ms", want)
			cur = want
		}
	}
	armed, outputs := 0, 0
	if cur > 0 {
		for _, b := range d.m.Tags {
			if b.Point != PointDO && b.Point != PointAO {
				continue
			}
			outputs++
			on, err := c.ReadUint32(addrChannelConfig + uint32(b.Slot)*configModuleStride + uint32(b.Channel)*configChannelStride + cfgWatchdogEnable)
			if err != nil {
				return err
			}
			if on != 0 {
				armed++
			}
		}
		if armed == 0 && outputs > 0 {
			d.log.Warn("optommp: the watchdog is on but no bound output has a watchdog value enabled; set them in groov Manage",
				"host", d.m.Host, "watchdog_ms", cur)
		}
	}
	d.mu.Lock()
	d.watchdogMs, d.armed, d.armable = int64(cur), armed, outputs
	d.mu.Unlock()
	return nil
}

// customPointers is the custom configuration the scan needs.
func (d *Driver) customPointers() []byte {
	b := make([]byte, 4*len(d.plan.words))
	for i, w := range d.plan.words {
		binary.BigEndian.PutUint32(b[i*4:], w)
	}
	return b
}

func (d *Driver) configureCustom(c *Client) error {
	want := d.customPointers()
	have, err := c.Read(addrCustomConfig, len(want))
	if err != nil {
		return err
	}
	d.mu.Lock()
	if d.customOrig == nil {
		d.customOrig = have
	}
	d.mu.Unlock()
	if bytes.Equal(have, want) {
		return nil
	}
	if !allZero(have) {
		d.log.Warn("optommp: the custom data area held other pointers; replacing them", "host", d.m.Host, "pointers", len(d.plan.words))
	}
	for off := 0; off < len(want); off += MaxBlock {
		end := min(off+MaxBlock, len(want))
		if err := c.Write(addrCustomConfig+uint32(off), want[off:end]); err != nil {
			return fmt.Errorf("optommp: could not configure the custom data area: %w", err)
		}
	}
	if err := d.verifyCustom(c); err != nil {
		return err
	}
	d.log.Info("optommp: configured the custom data area", "host", d.m.Host, "pointers", len(d.plan.words))
	return nil
}

// verifyCustom reads the pointers back.
func (d *Driver) verifyCustom(c *Client) error {
	want := d.customPointers()
	have, err := c.Read(addrCustomConfig, len(want))
	if err != nil {
		return err
	}
	if !bytes.Equal(have, want) {
		return errors.New("optommp: the custom data area's pointers changed under the driver (another client?)")
	}
	return nil
}

func (d *Driver) configureStream(c *Client) (*net.UDPConn, error) {
	local, ok1 := c.LocalAddr().(*net.TCPAddr)
	remote, ok2 := c.RemoteAddr().(*net.TCPAddr)
	if !ok1 || !ok2 || local.IP.To4() == nil {
		return nil, fmt.Errorf("optommp: stream: need an IPv4 TCP connection to learn where to stream to (have %v)", c.LocalAddr())
	}
	uc, err := net.ListenUDP("udp4", &net.UDPAddr{Port: d.m.StreamPort})
	if err != nil {
		return nil, fmt.Errorf("optommp: stream: %w", err)
	}
	port := uc.LocalAddr().(*net.UDPAddr).Port
	size := uint32(4 * len(d.plan.words))
	interval := uint32(time.Duration(d.m.Stream) / time.Millisecond)
	fail := func(what string, err error) (*net.UDPConn, error) {
		uc.Close()
		return nil, fmt.Errorf("optommp: could not configure the stream %s: %w", what, err)
	}

	cur, err := c.Read(addrStreamConfig, streamConfigLen)
	if err != nil {
		return fail("(reading it)", err)
	}
	d.mu.Lock()
	if d.streamOrig == nil {
		orig := bytes.Clone(cur)
		if binary.BigEndian.Uint32(orig[0x08:]) == addrCustomData && net.IP(orig[0x20:0x24]).Equal(local.IP.To4()) {
			// Left streaming here by a nautilus that did not stop cleanly.
			binary.BigEndian.PutUint32(orig[0x10:], 0)
		}
		d.streamOrig = orig
	}
	d.mu.Unlock()
	if t := net.IP(cur[0x20:0x24]); !t.IsUnspecified() && !t.Equal(local.IP.To4()) {
		d.log.Warn("optommp: replacing stream target #1", "host", d.m.Host, "was", t.String())
	}
	for i := 1; i < 8; i++ {
		if t := net.IP(cur[0x20+4*i : 0x24+4*i]); !t.IsUnspecified() {
			d.log.Warn("optommp: another stream target receives this stream too", "host", d.m.Host, "target", i+1, "ip", t.String())
		}
	}

	if err := c.Write(addrStreamOn, be32(0)); err != nil {
		return fail("(disabling it)", err)
	}
	if err := c.Write(addrStreamStart, append(be32(addrCustomData), be32(size)...)); err != nil {
		return fail("start address and data size", err)
	}
	if err := c.Write(addrStreamTiming, append(be32(interval), be32(uint32(port))...)); err != nil {
		return fail("interval and target port", err)
	}
	if err := c.Write(addrStreamTarget, local.IP.To4()); err != nil {
		return fail("target address", err)
	}
	if err := c.Write(addrStreamOn, be32(1)); err != nil {
		return fail("(enabling it)", err)
	}
	d.streamFrom = remote.IP
	d.log.Info("optommp: streaming", "host", d.m.Host, "to", net.JoinHostPort(local.IP.String(), fmt.Sprint(port)),
		"words", len(d.plan.words), "every", time.Duration(d.m.Stream))
	return uc, nil
}

// restore puts the custom data area's pointers and the streaming
// configuration back as the driver first found them.
func (d *Driver) restore(c *Client) {
	if d.m.Stream > 0 {
		d.unstream(c)
	}
	d.mu.Lock()
	orig := d.customOrig
	d.mu.Unlock()
	if orig == nil {
		return
	}
	for off := 0; off < len(orig); off += MaxBlock {
		if err := c.Write(addrCustomConfig+uint32(off), orig[off:min(off+MaxBlock, len(orig))]); err != nil {
			d.log.Warn("optommp: could not put the custom data area back", "host", d.m.Host, "err", err)
			return
		}
	}
}

// unstream puts the streaming configuration back as it was found.
func (d *Driver) unstream(c *Client) {
	d.mu.Lock()
	orig := d.streamOrig
	d.mu.Unlock()
	if orig == nil {
		return
	}
	err := c.Write(addrStreamOn, be32(0))
	if err == nil {
		err = c.Write(addrStreamStart, orig[0x08:0x10])
	}
	if err == nil {
		err = c.Write(addrStreamTiming, orig[0x14:0x1C])
	}
	if err == nil {
		err = c.Write(addrStreamTarget, orig[0x20:0x24])
	}
	if on := orig[0x10:0x14]; err == nil && !allZero(on) {
		err = c.Write(addrStreamOn, on)
	}
	if err != nil {
		d.log.Warn("optommp: could not put the streaming configuration back", "host", d.m.Host, "err", err)
		return
	}
	d.log.Info("optommp: stream stopped, configuration restored", "host", d.m.Host)
}

// streamLoop receives the stream until ctx ends or it goes quiet for the
// request timeout. Commands and the chassis check go over c meanwhile.
func (d *Driver) streamLoop(ctx context.Context, c *Client, uc *net.UDPConn) (polled bool, err error) {
	interval := time.Duration(d.m.Stream)
	staleAfter := max(5*interval, 100*time.Millisecond)
	deadAfter := max(time.Duration(d.m.Timeout), 2*staleAfter)
	keepEvery := d.chassis
	d.mu.Lock()
	if wd := time.Duration(d.watchdogMs) * time.Millisecond; wd > 0 {
		keepEvery = min(keepEvery, wd/3)
	}
	d.mu.Unlock()

	pkts := make(chan []byte, 4)
	go func() {
		defer close(pkts)
		buf := make([]byte, 2048)
		for {
			n, from, err := uc.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if !from.IP.Equal(d.streamFrom) {
				continue
			}
			select {
			case pkts <- bytes.Clone(buf[:n]):
			default: // the loop is behind; the next datagram is newer anyway
			}
		}
	}()
	defer uc.Close()

	check := time.NewTicker(staleAfter / 2)
	defer check.Stop()
	keep := time.NewTicker(keepEvery)
	defer keep.Stop()
	last, stale := time.Now(), false
	nextChassis := last.Add(d.chassis)
	for {
		select {
		case <-ctx.Done():
			return polled, nil
		case p, ok := <-pkts:
			if !ok {
				return polled, errors.New("optommp: stream socket closed")
			}
			img, err := d.decodeStream(p)
			if err != nil {
				d.fail(err)
				continue
			}
			now := time.Now()
			if polled {
				if gap := now.Sub(last).Microseconds(); gap > d.gapMaxUs.Load() {
					d.gapMaxUs.Store(gap)
				}
			}
			last, stale = now, false
			n, err := d.deliver(c, img)
			if err != nil {
				return polled, err
			}
			polled = true
			d.requests.Store(int64(n))
			d.scanUs.Store(time.Since(now).Microseconds())
		case <-check.C:
			idle := time.Since(last)
			if idle > deadAfter {
				return polled, fmt.Errorf("optommp: communication timeout: missed incoming stream (none for %s)", idle.Round(time.Millisecond))
			}
			if idle > staleAfter && !stale {
				stale = true
				d.late.Add(1)
				d.markStale()
				d.log.Warn("optommp: stream late; tags Stale until it resumes", "host", d.m.Host, "idle", idle.Round(time.Millisecond))
			}
		case <-keep.C:
			if time.Now().Before(nextChassis) {
				// Feed the watchdog: the stream itself is not a request.
				if _, err := c.ReadUint32(addrWatchdog); err != nil {
					return polled, err
				}
				continue
			}
			if err := d.checkChassis(c); err != nil {
				return polled, err
			}
			if err := d.verifyCustom(c); err != nil {
				return polled, err
			}
			nextChassis = time.Now().Add(d.chassis)
		case <-d.kick:
			if err := d.flush(c); err != nil {
				return polled, err
			}
		}
	}
}

// decodeStream checks one datagram's header and returns its words.
func (d *Driver) decodeStream(p []byte) ([]uint32, error) {
	want := streamHeaderLen + 4*len(d.plan.words)
	switch {
	case len(p) != want:
		return nil, fmt.Errorf("optommp: stream datagram of %d bytes, want %d", len(p), want)
	case int(binary.BigEndian.Uint16(p)) != len(p) || p[2] != 0 || p[3]>>4 != 0xA:
		return nil, fmt.Errorf("optommp: stream datagram header % x", p[:4])
	case binary.BigEndian.Uint32(p[4:]) != addrCustomData:
		return nil, fmt.Errorf("optommp: stream of 0x%08X, want the custom data area", binary.BigEndian.Uint32(p[4:]))
	}
	img := make([]uint32, len(d.plan.words))
	for i := range img {
		img[i] = binary.BigEndian.Uint32(p[streamHeaderLen+4*i:])
	}
	return img, nil
}

// markStale demotes every delivered tag to Stale.
func (d *Driver) markStale() {
	d.mu.Lock()
	for name, q := range d.quality {
		if q != nio.NotConnected {
			d.quality[name] = nio.Stale
		}
	}
	d.mu.Unlock()
}

func be32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func allZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}
