//go:build linux

package optommp

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// #1047 review (1), the RT path (fdConn: raw syscalls on its own
// descriptor): a fenced write the unit does not answer is reset IN PLACE
// (the unit reads ECONNRESET) and the descriptor stays open until the run
// loop closes it, so a thread still blocked on it never retries onto a
// reused number; and closing twice closes it once, never an unrelated
// file that took the number since.
func TestOptoMMPRTFencedWriteAbortsInPlaceAndClosesOnce(t *testing.T) {
	c, done, _ := silentUnit(t)
	fc, ok := c.conn.(*fdConn)
	if !ok {
		t.Skip("no raw descriptor on this build")
	}
	fd := fc.fd
	if err := c.WriteBy(0xF0000000, []byte{0, 0, 0, 1}, time.Now().Add(200*time.Millisecond)); err == nil {
		t.Fatal("an unanswered write succeeded")
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		t.Fatalf("the descriptor was closed by the fence (%v); want it aborted in place and left to its owner", err)
	}
	if err := <-done; !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("the unit's side read to %v; want ECONNRESET", err)
	}
	c.conn.Close() // the run loop's close
	// Open files until one takes the closed descriptor's number.
	dir := t.TempDir()
	var reuser *os.File
	for i := 0; i < 256 && reuser == nil; i++ {
		f, err := os.Create(filepath.Join(dir, "f"+strconv.Itoa(i)))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if int(f.Fd()) == fd {
			reuser = f
		}
	}
	if reuser == nil {
		t.Skip("no file took the closed descriptor's number")
	}
	c.conn.Close() // a second close (a late timer, a deferred reset)
	if _, err := reuser.Write([]byte("still mine")); err != nil {
		t.Fatalf("a file that took the descriptor's number after the close lost it to a second close: %v", err)
	}
}
