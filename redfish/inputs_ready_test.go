package redfish

import "testing"

// InputsReady forwards hw.Base's (io.InputsReadier, #786).

func TestInputsReadyAfterTheFirstPoll(t *testing.T) {
	t.Setenv("RF_DRIVER_PASSWORD", driverPassword)
	srv := standIn(t, "subsystem-1u")
	d, err := New(manifestFor(t, subsystemManifest, srv.URL(), ""), WithLogger(quiet()))
	if err != nil {
		t.Fatal(err)
	}
	if d.InputsReady() {
		t.Fatal("ready before Start")
	}
	startDriver(t, d)
	waitFor(t, "ready", d.InputsReady)
	if !online(d) {
		t.Fatal("ready before the first poll was delivered")
	}
}

func TestInputsReadyAfterAFailedFirstPoll(t *testing.T) {
	t.Setenv("RF_DRIVER_PASSWORD", driverPassword)
	srv := standIn(t, "subsystem-1u")
	m := manifestFor(t, subsystemManifest, srv.URL(), "")
	srv.Stop() // nothing answers
	d, err := New(m, WithLogger(quiet()))
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)
	waitFor(t, "ready after the failed first poll", d.InputsReady)
}
