//go:build !js

package devbind

import (
	"context"
	"net"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/modbus"
	"github.com/joyautomation/nautilus/modbus/slave"
	"github.com/joyautomation/nautilus/opcua"
)

// The wrapped drivers answer InputsReady (io.InputsReadier, #786) as the
// driver under them does: the runtime reaches it through the wrapper.

func readyWithin(t *testing.T, d nio.Driver, what string) {
	t.Helper()
	r, ok := d.(nio.InputsReadier)
	if !ok {
		t.Fatalf("%T hides InputsReady", d)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if r.InputsReady() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("InputsReady never became true: %s", what)
}

func TestWrappedModbusInputsReady(t *testing.T) {
	srv := slave.NewServer("", nil)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	srv.Unit(1).SetHolding(3, 0x237)
	s, err := Resolve(vendored(t, "abb-acs355-fmbt21"), []Device{{Name: "CONV", Profile: "abb-acs355-fmbt21", Endpoint: srv.Addr()}})
	if err != nil {
		t.Fatal(err)
	}
	mm, err := s.MergeModbus(modbus.Manifest{})
	if err != nil {
		t.Fatal(err)
	}
	md, err := modbus.New(mm, modbus.WithScanRate(10*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	w := s.WrapModbus(md)
	if w.InputsReady() {
		t.Fatal("ready before Start")
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	t.Cleanup(func() { cancel(); w.Stop() })
	readyWithin(t, w, "after the first read")
}

func TestWrappedOPCUAInputsReady(t *testing.T) {
	fsys := vendored(t, "pepperl-fuchs-ice3")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing listens: the first connect is refused
	s, err := Resolve(fsys, []Device{{Name: "PF1", Profile: "pepperl-fuchs-ice3", Endpoint: "opc.tcp://" + addr, Points: []string{"Port1_SioInput"}}})
	if err != nil {
		t.Fatal(err)
	}
	od := s.OPCUADevices()[0]
	drv, err := opcua.New(od.Endpoint, od.Manifest, opcua.WithSecurity("None", "None"), opcua.WithRetry(20*time.Millisecond, 50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.WrapOPCUA("PF1", drv, "None", "None")
	if err != nil {
		t.Fatal(err)
	}
	if w.InputsReady() {
		t.Fatal("ready before Start")
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	t.Cleanup(func() { cancel(); w.Stop() })
	readyWithin(t, w, "after the first connect was refused")
}
