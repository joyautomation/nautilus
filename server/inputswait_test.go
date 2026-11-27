package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

// notYet is a driver whose inputs have not arrived (io.InputsReadier).
type notYet struct{ *nio.Memory }

func (*notYet) InputsReady() bool { return false }

// #786: while the first scan waits for the drivers' inputs, GET /api/state
// says so under scan.inputsWait, naming who it waits for.
func TestStateShowsTheInputsWait(t *testing.T) {
	rt, err := runtime.New(runtime.Options{
		Program:    "PROGRAM P\nVAR_EXTERNAL Out : REAL; END_VAR\nOut := 1.0;\nEND_PROGRAM\n",
		Driver:     &notYet{Memory: nio.NewMemory()},
		Outputs:    []string{"Out"},
		Seed:       nio.Values{"Out": 0.0},
		InputsWait: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	rt.Scan()
	rec := httptest.NewRecorder()
	New(rt, Options{}).Handler().ServeHTTP(rec, httptest.NewRequest("GET", "http://localhost/api/state", nil))
	var got struct {
		Scan struct {
			InputsWait *struct {
				Since      time.Time `json:"since"`
				WaitingFor []string  `json:"waitingFor"`
			} `json:"inputsWait"`
		} `json:"scan"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("GET /api/state: %v: %s", err, rec.Body.String())
	}
	w := got.Scan.InputsWait
	if w == nil || w.Since.IsZero() || len(w.WaitingFor) != 1 || w.WaitingFor[0] != "server" {
		t.Fatalf("scan.inputsWait = %+v, want since set and waitingFor [server]: %s", w, rec.Body.String())
	}
}
