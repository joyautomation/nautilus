package snmp_test

import "testing"

// InputsReady forwards hw.Base's (io.InputsReadier, #786).

func TestInputsReadyAfterTheFirstPoll(t *testing.T) {
	r := newRig(t, "switch.snmpwalk", "SW1")
	d := r.start(t)
	waitFor(t, "ready", d.InputsReady)
	if !online(d, "SW1") {
		t.Fatal("ready before the first poll was delivered")
	}
}

func TestInputsReadyAfterAFailedFirstPoll(t *testing.T) {
	r := newRig(t, "switch.snmpwalk", "SW1")
	r.agent.Stop() // nothing answers
	d := r.start(t)
	waitFor(t, "ready after the failed first poll", d.InputsReady)
}
