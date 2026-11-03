package uaserver

import (
	"testing"

	"github.com/gopcua/opcua/ua"
	"github.com/joyautomation/nautilus/opcua/internal/uasc"
)

// A handler that panics answers its request with Bad_InternalError; the
// panic never reaches the message loop, where it would end the controller.
func TestHandlerPanicIsAFaultNotACrash(t *testing.T) {
	s := &Server{cfg: &serverConfig{}}
	h := func(*uasc.SecureChannel, ua.Request, uint32) (ua.Response, error) {
		var refs []int
		_ = refs[3] // index out of range
		return nil, nil
	}
	resp, err := s.callHandler(h, nil, &ua.BrowseRequest{}, 1)
	if err != ua.StatusBadInternalError || resp != nil {
		t.Fatalf("a panicking handler gave (%v, %v), want (nil, Bad_InternalError)", resp, err)
	}
	ok := func(*uasc.SecureChannel, ua.Request, uint32) (ua.Response, error) {
		return &ua.BrowseResponse{}, nil
	}
	if resp, err := s.callHandler(ok, nil, &ua.BrowseRequest{}, 1); err != nil || resp == nil {
		t.Fatalf("a good handler gave (%v, %v)", resp, err)
	}
}
