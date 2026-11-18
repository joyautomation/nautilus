package testmqtt

import (
	"sync"
	"testing"

	mqttsrv "github.com/mochi-mqtt/server/v2"
)

// expires reads only the fields mochi sets at connect. mochi writes the
// client's Properties.Will when a clean session ends (attachClient), on that
// client's own goroutine, while SameIDSerializer reads expires on the new
// connection's: copying the whole Properties raced with that write (caught
// by -race in sparkplug's TestBdSeqFilePersistsAcrossRestarts). Under -race
// (CI's race job), this fails if expires reads Will again.
func TestExpiresReadsOnlyConnectTimeFields(t *testing.T) {
	cl := &mqttsrv.Client{}
	cl.Properties.ProtocolVersion = 4
	cl.Properties.Clean = true
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // mochi, ending the session
		defer wg.Done()
		for range 1000 {
			cl.Properties.Will = mqttsrv.Will{}
		}
	}()
	go func() { // SameIDSerializer, on the new connection
		defer wg.Done()
		for range 1000 {
			if !expires(cl) {
				t.Error("a clean MQTT 3.1.1 session must expire")
				return
			}
		}
	}()
	wg.Wait()
}
