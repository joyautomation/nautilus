package node

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
)

// UnpinPath is where a controller asks the witness to drop, or lift the
// refusal of, its peer's pinned key (#710 2b, `naut redundancy unpin` on
// the surviving controller): Prefix + cluster + UnpinPath. The witness
// serves it (cmd/naut), behind peer TLS's per-request check: it takes the
// ask only from a controller of that cluster, by its certificate, and
// only about the other one.
const UnpinPath = "/unpin"

// UnpinAsk is an unpin's body: Op "drop" or "lift", Name the controller,
// Key the fingerprint of the key the asking controller drops (or whose
// refusal it lifts). The witness acts only on that same key: one it has
// already re-pinned since (a replacement that reached it first, or an
// unpin on the witness box) is never dropped by a later ask.
type UnpinAsk struct {
	Op   string `json:"op"`
	Name string `json:"name"`
	Key  string `json:"key"`
}

// UnpinOther is the witness's outcome when the key it holds for the name
// (pinned or refused) is not the asking controller's: it changed nothing.
const UnpinOther = "other-key"

// UnpinAnswer is the witness's answer: Outcome is what it found
// (peertls.DropOutcome for a drop; "lifted" or "not-refused" for a lift;
// UnpinOther), Pinned the key it has pinned for the name now ("" none).
type UnpinAnswer struct {
	Outcome string `json:"outcome"`
	Pinned  string `json:"pinned,omitempty"`
}

// WitnessUnpin asks the witness to drop (op "drop") the pin of name, the
// other controller, or to lift (op "lift") the refusal of its dropped
// key, over this controller's own pinned connection, under the
// certificate of the incarnation the pair counts (caller.callCert). A
// refusal is an error saying why.
func (e *Elector) WitnessUnpin(ctx context.Context, op, name, key string) (UnpinAnswer, error) {
	b, _ := json.Marshal(UnpinAsk{Op: op, Name: name, Key: key})
	body, err := e.call.call(ctx, 2*e.o.Lease, http.MethodPost, e.wit+Prefix+e.o.Cluster+UnpinPath, b)
	if err != nil {
		if se, ok := err.(*statusError); ok && se.body != "" {
			return UnpinAnswer{}, fmt.Errorf("the witness refused: %s", se.said())
		}
		return UnpinAnswer{}, err
	}
	var a UnpinAnswer
	if err := json.Unmarshal(body, &a); err != nil || a.Outcome == "" {
		return UnpinAnswer{}, fmt.Errorf("the witness's answer is unreadable: %q", body)
	}
	return a, nil
}

// LockWitnessDir takes the witness's state directory lock, as a running
// witness holds it: an offline command (naut witness unpin) runs only
// while no witness does.
func LockWitnessDir(dir string) (release func(), err error) {
	return lock(filepath.Join(dir, ".lock"))
}
