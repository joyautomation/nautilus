package facade

import (
	"context"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/siemens/webapi"
	"github.com/joyautomation/nautilus/siemens/webapi/webapitest"
)

// InputsReady (io.InputsReadier, #786): false until the first poll is over,
// merged or failed; sticky.

var readyBindings = []Binding{{Name: "Level", Symbol: `"Tags".Level`, Type: "REAL"}}

func TestInputsReadyAfterTheFirstPollThenSticky(t *testing.T) {
	s, c := cpu(t)
	d := NewDriver(c, readyBindings, time.Second)
	if d.InputsReady() {
		t.Fatal("ready before the first poll")
	}
	d.PollOnce(context.Background())
	if !d.InputsReady() {
		t.Fatal("not ready after the first poll")
	}
	if v, err := d.ReadInputs(); err != nil || v["Level"] != 12.5 {
		t.Fatalf("ready before the first poll was delivered: %v %v", v, err)
	}
	s.StopAnswering()
	d.PollOnce(context.Background())
	if d.Health().Connected {
		t.Fatal("the later poll did not fail")
	}
	if !d.InputsReady() {
		t.Fatal("InputsReady went false after a later failed poll")
	}
}

func TestInputsReadyAfterAFailedFirstPoll(t *testing.T) {
	s := webapitest.New()
	s.Users["naut"] = webapitest.User{Password: "pw", Permissions: []string{"read_value"}}
	s.Vars[`"Tags".Level`] = &webapitest.Var{Datatype: "real", Value: 1.0}
	url, pin := s.Start(t)
	c, err := webapi.New(url, webapi.Options{User: "naut", Password: "wrong", CertSHA256: pin})
	if err != nil {
		t.Fatal(err)
	}
	d := NewDriver(c, readyBindings, time.Second)
	if d.InputsReady() {
		t.Fatal("ready before the first poll")
	}
	d.PollOnce(context.Background())
	if d.Health().Connected {
		t.Fatal("the poll should have failed (wrong password)")
	}
	if !d.InputsReady() {
		t.Fatal("a failed first poll must count")
	}
}

func TestInputsReadyWithNoBindings(t *testing.T) {
	_, c := cpu(t)
	if !NewDriver(c, nil, time.Second).InputsReady() {
		t.Fatal("a driver with no bindings must be ready at once")
	}
}
