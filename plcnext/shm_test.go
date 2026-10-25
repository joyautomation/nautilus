package plcnext

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
)

// The page has one definition, rt/plcnext-io/nautilus_io_shm.h; this is
// the guard that shm.go says the same thing.
func TestShmLayoutMatchesTheShimsHeader(t *testing.T) {
	f, err := os.Open("../rt/plcnext-io/nautilus_io_shm.h")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got := map[string]uint64{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 || fields[0] != "#define" || !strings.HasPrefix(fields[1], "NIO_") {
			continue
		}
		v := strings.TrimSuffix(fields[2], "ULL")
		if fields[1] == "NIO_HIST_LE_US" {
			list := strings.Trim(strings.Join(fields[2:], ""), "{}")
			if list != strings.Trim(strings.ReplaceAll(fmt.Sprint(ShimHistLeUs), " ", ","), "[]") {
				t.Errorf("NIO_HIST_LE_US {%s}, ShimHistLeUs %v", list, ShimHistLeUs)
			}
			continue
		}
		if n, err := strconv.ParseUint(v, 0, 64); err == nil {
			got[fields[1]] = n
		} else if strings.HasPrefix(v, `"`) && fields[1] == "NIO_SHM_PATH" {
			if s, _ := strconv.Unquote(v); s != DefaultShm {
				t.Errorf("NIO_SHM_PATH %s, DefaultShm %q", v, DefaultShm)
			}
		}
	}
	want := map[string]uint64{
		"NIO_SHM_SIZE": shmSize, "NIO_MAGIC": shmMagic, "NIO_VERSION": shmVersion, "NIO_FRAME_LEN": shmFrameLen,
		"NIO_OFF_MAGIC": shmOffMagic, "NIO_OFF_VERSION": shmOffVersion, "NIO_OFF_PERIOD_NS": shmOffPeriodNs,
		"NIO_OFF_FRAME_LEN": shmOffFrameLen, "NIO_OFF_PID": shmOffPID, "NIO_OFF_STARTED_NS": shmOffStartedNs,
		"NIO_OFF_LATE_US": shmOffLateUs,
		"NIO_OFF_IN_SEQ":  shmOffInSeq, "NIO_OFF_STATUS": shmOffStatus, "NIO_OFF_STAMP_NS": shmOffStampNs,
		"NIO_OFF_FRAME_NS": shmOffFrameNs, "NIO_OFF_CYCLES": shmOffCycles, "NIO_OFF_LATE": shmOffLate,
		"NIO_OFF_WORST_US": shmOffWorstUs, "NIO_OFF_LAST_US": shmOffLastUs, "NIO_OFF_APPLIED": shmOffApplied,
		"NIO_OFF_TORN": shmOffTorn, "NIO_OFF_INPUTS": shmOffInputs, "NIO_OFF_ECHO": shmOffEcho,
		"NIO_OFF_HIST": shmOffHist, "NIO_HIST_N": shmHistN, "NIO_OFF_WORST_WIN_US": shmOffWorstWinUs, "NIO_OFF_PRIO": shmOffPrio, "NIO_OFF_CPU": shmOffCPU,
		"NIO_OFF_OUT_SEQ": shmOffOutSeq, "NIO_OFF_OUT_LEN": shmOffOutLen, "NIO_OFF_OUT_NS": shmOffOutNs,
		"NIO_OFF_OUT_GEN": shmOffOutGen, "NIO_OFF_WATCHDOG_MS": shmOffWatchdogMs, "NIO_OFF_OUTPUTS": shmOffOutputs,
		"NIO_ST_PLC": shmStPLC, "NIO_ST_FRAMES": shmStFrames, "NIO_ST_RUN_KNOWN": shmStRunKnown,
		"NIO_ST_RUN": shmStRun, "NIO_ST_FIFO": shmStFIFO, "NIO_ST_WATCHDOG": shmStWatchdog,
	}
	for k, w := range want {
		if g, ok := got[k]; !ok {
			t.Errorf("%s is not in the header", k)
		} else if g != w {
			t.Errorf("%s: header %#x, shm.go %#x", k, g, w)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("%s is in the header but not checked against shm.go", k)
		}
	}
}

func TestManifestTransport(t *testing.T) {
	m, err := ParseManifest([]byte(benchManifest))
	if err != nil {
		t.Fatal(err)
	}
	if m.Transport != TransportGRPC || m.Shm != DefaultShm || m.watchdog() != DefaultOutputWatchdog {
		t.Fatalf("defaults: transport %q shm %q watchdog %v", m.Transport, m.Shm, m.watchdog())
	}
	m, err = ParseManifest([]byte("transport: shm\noutput-watchdog: \"0\"\n" + benchManifest))
	if err != nil || m.Transport != TransportShm || m.watchdog() != 0 {
		t.Fatalf("shm with the watchdog off: %+v %v", m, err)
	}
	_, err = ParseManifest([]byte("transport: udp\noutput-watchdog: soon\n" + benchManifest))
	if err == nil || !strings.Contains(err.Error(), `"udp"`) || !strings.Contains(err.Error(), `"soon"`) {
		t.Fatalf("bad transport and watchdog not both reported: %v", err)
	}
}
