package prom_test

import (
	"context"
	"testing"

	"github.com/joyautomation/nautilus/prom"
)

// InputsReady forwards hw.Base's (io.InputsReadier, #786).

func TestInputsReadyAfterTheFirstPoll(t *testing.T) {
	srv := startStandIn(t, baseBody)
	d, err := prom.New(testManifest(srv.URL()))
	if err != nil {
		t.Fatal(err)
	}
	if d.InputsReady() {
		t.Fatal("ready before Start")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Start(ctx)
	defer d.Stop()
	waitFor(t, "ready", d.InputsReady)
	if v, _ := d.ReadInputs(); v["NODE1__Online"] != true {
		t.Fatalf("ready before the first poll was delivered: %v", v["NODE1__Online"])
	}
}

func TestInputsReadyAfterAFailedFirstPoll(t *testing.T) {
	srv := startStandIn(t, baseBody)
	m := testManifest(srv.URL())
	srv.Stop() // nothing answers
	d, err := prom.New(m)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Start(ctx)
	defer d.Stop()
	waitFor(t, "ready after the failed first poll", d.InputsReady)
}
