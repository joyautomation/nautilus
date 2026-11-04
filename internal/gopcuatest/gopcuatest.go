// Package gopcuatest connects gopcua clients for tests that subscribe.
//
// gopcua's Client (v0.9.1, and its main branch as of 2026-11) can lose its
// publish loop for good when it subscribes right after Connect. NewClient
// queues a pause for the loop and Connect starts the loop's goroutine;
// Subscribe queues a resume. If the goroutine has not run by the time the
// resume is queued, both are waiting when it starts, and its select picks
// one at random: the pause first is harmless (the resume ends it), but the
// resume first is dropped ("ignore since not paused") and the pause that
// follows is never ended. No Publish request is sent again, so the
// subscription's notifications never reach the client, while every other
// service still answers (#570).
//
// Connect closes that window: it returns once the loop has taken its
// pause, after which Subscribe's resume always ends it. Only tests import
// this package; the OPC UA client driver (opcua/client.go) does not
// subscribe through gopcua.
package gopcuatest

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/gopcua/opcua"
)

// Connect connects c and waits until its publish loop is running, so that
// a Subscribe that follows is heard.
func Connect(ctx context.Context, c *opcua.Client) error {
	if err := c.Connect(ctx); err != nil {
		return err
	}
	if err := AwaitPublishLoop(ctx, c); err != nil {
		// Callers register their Close after a successful Connect, so a
		// client left open here would leak.
		c.Close(ctx)
		return err
	}
	return nil
}

// AwaitPublishLoop returns once c's publish loop has taken the pause
// NewClient queued for it: c's pause channel is empty. It reads the
// channel's length through reflection, which needs no access to the
// unexported field's value; a gopcua that no longer has the field is an
// error, so an upgrade is made to look at #570 again rather than lose the
// wait silently.
func AwaitPublishLoop(ctx context.Context, c *opcua.Client) error {
	pause := reflect.ValueOf(c).Elem().FieldByName("pausech")
	if !pause.IsValid() || pause.Kind() != reflect.Chan {
		return fmt.Errorf("gopcua's Client has no pausech channel any more: recheck #570 (the publish-loop start race) against this version")
	}
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	timeout := time.After(10 * time.Second)
	for pause.Len() > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout:
			return fmt.Errorf("gopcua's publish loop did not start within 10s of Connect")
		case <-tick.C:
		}
	}
	return nil
}
