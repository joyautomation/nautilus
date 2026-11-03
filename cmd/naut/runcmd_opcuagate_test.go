package main

import (
	"slices"
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/runtime"
)

// The OPC UA server answers to the same elector as the drivers: wired by
// wireRedundancy, a standby's server refuses writes (Status.Standby; the
// Write service answers Bad_InvalidState, opcua/server TestStandbyRefusesWrites)
// and the leader's takes them. opcua.writable reaches the server.
func TestWireRedundancyGatesOPCUAWrites(t *testing.T) {
	files := fstest.MapFS{
		"nautilus.yaml": &fstest.MapFile{Data: []byte(`name: lift-station
redundancy: {}
opcua:
  endpoint: opc.tcp://localhost:0
  writable: ["*SP"]
tasks:
  - program: main.st
tags:
  - { name: LevelSP, role: setpoint, init: 5.0 }
`)},
		"main.st": &fstest.MapFile{Data: []byte(`PROGRAM Main
VAR_EXTERNAL LevelSP : REAL; END_VAR
LevelSP := LevelSP;
END_PROGRAM
`)},
	}
	proj, err := project.Load(files, "")
	if err != nil {
		t.Fatal(err)
	}
	coord := &flagCoordinator{}
	wireRedundancy(proj, coord)
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	opc, err := proj.OPCUAServer(rt)
	if err != nil {
		t.Fatal(err)
	}
	if st := opc.Status(); !st.Standby {
		t.Error("a standby replica's OPC UA server takes writes")
	}
	coord.lead.Store(true)
	st := opc.Status()
	if st.Standby {
		t.Error("the leader's OPC UA server refuses writes")
	}
	if !slices.Equal(st.Writable, []string{"*SP"}) {
		t.Errorf("opcua.writable reached the server as %v", st.Writable)
	}
}
