package snmp

import (
	"context"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/snmp/walk"
)

// #1040 review: a SET goes out as ONE datagram. An agent that never
// answers (the answer lost, or the agent busy rebooting the outlet) sees
// one request, not one per retry: an action-valued object must not run
// twice. A GET keeps its retries. set_retried turns it red.
func TestSetIsSentOnce(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	var got atomic.Int32
	go func() {
		buf := make([]byte, 2048)
		for {
			if _, _, err := pc.ReadFrom(buf); err != nil {
				return
			}
			got.Add(1) // counted, never answered
		}
	}()
	t.Setenv("NAUT_TEST_COMMUNITY", "private")
	_, port, _ := net.SplitHostPort(pc.LocalAddr().String())
	p, _ := strconv.Atoi(port)
	retries := 2
	src := Source{ID: "pdu1", Host: "127.0.0.1", Port: p, Timeout: 100 * time.Millisecond, Retries: &retries, CommunityEnv: "NAUT_TEST_COMMUNITY"}
	g, err := Dial(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	oid, _ := walk.ParseOID("1.3.6.1.4.1.318.1.1.12.3.3.1.1.4.1")
	if err := g.Set(context.Background(), walk.Varbind{OID: oid, Type: walk.Integer, Int: 3}); err == nil {
		t.Fatal("a SET nobody answered succeeded")
	}
	time.Sleep(300 * time.Millisecond)
	if n := got.Load(); n != 1 {
		t.Fatalf("the agent received %d SET datagram(s); want exactly 1 (no retransmit)", n)
	}
	got.Store(0)
	_, _ = g.Get(context.Background(), []string{"1.3.6.1.2.1.1.3.0"})
	time.Sleep(300 * time.Millisecond)
	if n := got.Load(); n != int32(retries+1) {
		t.Fatalf("a GET went out %d time(s); want %d (its retries kept)", n, retries+1)
	}
}
