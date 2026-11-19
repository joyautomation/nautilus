package retain

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/joyautomation/nautilus/internal/k8sapi"
)

// configMapStore retains State in a Kubernetes ConfigMap, via a *k8sapi.Client
// so tests can point it at an httptest server instead of a real API server.
//
// Writes are compare-and-swap on the ConfigMap's resourceVersion: Save
// sends the version this store last read or wrote, and a ConfigMap changed
// since (a deposed leader's late write, or another replica's) refuses it
// with 409. Save then re-reads the version and returns ErrConflict; the
// caller decides whether its state should still win (the leader's saver
// does, on its next save).
type configMapStore struct {
	client *k8sapi.Client
	name   string

	mu sync.Mutex
	rv string // the resourceVersion last read or written; "" when absent
}

// ErrConflict is a Save refused because the store changed since this
// replica last read or wrote it. The store has re-read its version: a
// Save after it writes over the change.
var ErrConflict = errors.New("the retain store changed since it was last read")

// NewConfigMap builds a Store backed by the named ConfigMap in the client's
// namespace. Exported alongside New so tests and callers that already have a
// *k8sapi.Client can skip the environment sniff.
func NewConfigMap(c *k8sapi.Client, name string) Store {
	return &configMapStore{client: c, name: name}
}

func (k *configMapStore) Kind() string { return "configmap" }

// configMap is the subset of the ConfigMap resource this store reads and
// writes. The whole State marshals to a single JSON string under
// Data["state"] rather than one ConfigMap key per field: State evolves
// (new tags, new task programs) without the store having to track a schema
// for the ConfigMap itself, and a single string is trivial to diff in
// `kubectl get -o yaml`.
type configMap struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name            string `json:"name"`
		Namespace       string `json:"namespace"`
		ResourceVersion string `json:"resourceVersion,omitempty"`
	} `json:"metadata"`
	Data map[string]string `json:"data"`
}

func (k *configMapStore) path() string {
	return "/api/v1/namespaces/" + k.client.Namespace + "/configmaps/" + k.name
}

// get reads the ConfigMap: its state string and resourceVersion, found
// false on a 404.
func (k *configMapStore) get() (data, rv string, found bool, err error) {
	resp, err := k.client.Do(http.MethodGet, k.path(), nil)
	if err != nil {
		return "", "", false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", "", false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", false, fmt.Errorf("get configmap: %s", resp.Status)
	}
	var cm configMap
	if err := json.NewDecoder(resp.Body).Decode(&cm); err != nil {
		return "", "", false, err
	}
	return cm.Data["state"], cm.Metadata.ResourceVersion, true, nil
}

// Load on a ConfigMap that doesn't exist yet means nothing has been
// retained yet, not a failure — same "nothing retained yet" semantics as
// the file store — so a 404 returns a zero State and a nil error. It
// records the version it read, which the next Save is conditional on.
func (k *configMapStore) Load() (State, error) {
	var s State
	data, rv, found, err := k.get()
	if err != nil {
		return s, err
	}
	k.mu.Lock()
	k.rv = rv
	k.mu.Unlock()
	if !found {
		return s, nil
	}
	return s, json.Unmarshal([]byte(data), &s)
}

// Save writes State to the ConfigMap, conditional on the version last read
// or written: a PUT carrying it, or a POST creating the ConfigMap when it
// was absent. A ConfigMap changed since is refused (ErrConflict, the
// version re-read). A request that fails in transit (a timeout after the
// server may have written) is settled by reading back: written, Save
// succeeds; not, it fails, and a write still in flight would carry the
// old version, so the next Save refuses it or writes over it.
func (k *configMapStore) Save(s State) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	cm := configMap{APIVersion: "v1", Kind: "ConfigMap"}
	cm.Metadata.Name = k.name
	cm.Metadata.Namespace = k.client.Namespace
	cm.Metadata.ResourceVersion = k.rv
	cm.Data = map[string]string{"state": string(b)}
	body, err := json.Marshal(cm)
	if err != nil {
		return err
	}

	var resp *http.Response
	if k.rv == "" {
		resp, err = k.client.Do(http.MethodPost, "/api/v1/namespaces/"+k.client.Namespace+"/configmaps", body)
	} else {
		resp, err = k.client.Do(http.MethodPut, k.path(), body)
	}
	if err != nil {
		if data, rv, found, gerr := k.get(); gerr == nil && found && data == string(b) {
			k.rv = rv // it landed
			return nil
		}
		return fmt.Errorf("save configmap: %w (not saved)", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		var got configMap
		if json.NewDecoder(resp.Body).Decode(&got) == nil && got.Metadata.ResourceVersion != "" {
			k.rv = got.Metadata.ResourceVersion
		} else if _, rv, _, gerr := k.get(); gerr == nil {
			k.rv = rv
		}
		return nil
	case http.StatusConflict, http.StatusNotFound:
		// Changed (or deleted, or created) since it was read: re-read the
		// version, so a Save that should still win can.
		if _, rv, _, gerr := k.get(); gerr == nil {
			k.rv = rv
		}
		return ErrConflict
	}
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	return fmt.Errorf("save configmap: %s: %s", resp.Status, msg)
}
