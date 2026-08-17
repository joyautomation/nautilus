package retain

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/joyautomation/nautilus/internal/k8sapi"
)

// configMapStore retains State in a Kubernetes ConfigMap, via a *k8sapi.Client
// so tests can point it at an httptest server instead of a real API server.
type configMapStore struct {
	client *k8sapi.Client
	name   string
}

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
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Data map[string]string `json:"data"`
}

func (k *configMapStore) path() string {
	return "/api/v1/namespaces/" + k.client.Namespace + "/configmaps/" + k.name
}

// Load on a ConfigMap that doesn't exist yet means nothing has been
// retained yet, not a failure — same "nothing retained yet" semantics as
// the file store — so a 404 returns a zero State and a nil error.
func (k *configMapStore) Load() (State, error) {
	var s State
	resp, err := k.client.Do(http.MethodGet, k.path(), nil)
	if err != nil {
		return s, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return s, nil
	}
	if resp.StatusCode != http.StatusOK {
		return s, fmt.Errorf("get configmap: %s", resp.Status)
	}
	var cm configMap
	if err := json.NewDecoder(resp.Body).Decode(&cm); err != nil {
		return s, err
	}
	return s, json.Unmarshal([]byte(cm.Data["state"]), &s)
}

// Save writes State to the ConfigMap. It PUTs to the resource URL to update
// an existing ConfigMap, and on a 404 falls back to POSTing the collection
// URL to create one.
//
// This deliberately sends no resourceVersion, so there is no compare-and-swap
// against a concurrent writer: last writer wins. That's safe here because
// the leader package's election guarantees only the elected replica ever
// calls Save — a follower that thinks it's the leader is exactly the failure
// mode leader election exists to prevent, not one this store needs to guard
// against too.
func (k *configMapStore) Save(s State) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	cm := configMap{APIVersion: "v1", Kind: "ConfigMap"}
	cm.Metadata.Name = k.name
	cm.Metadata.Namespace = k.client.Namespace
	cm.Data = map[string]string{"state": string(b)}
	body, err := json.Marshal(cm)
	if err != nil {
		return err
	}

	resp, err := k.client.Do(http.MethodPut, k.path(), body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		collection := "/api/v1/namespaces/" + k.client.Namespace + "/configmaps"
		resp2, err := k.client.Do(http.MethodPost, collection, body)
		if err != nil {
			return err
		}
		defer resp2.Body.Close()
		if resp2.StatusCode != http.StatusCreated {
			msg, _ := io.ReadAll(io.LimitReader(resp2.Body, 256))
			return fmt.Errorf("create configmap: %s: %s", resp2.Status, msg)
		}
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("update configmap: %s: %s", resp.Status, msg)
	}
	return nil
}
