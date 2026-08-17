package retain

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/joyautomation/nautilus/internal/k8sapi"
)

// ---- file store ----

func TestFileStoreSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "retain.json")
	store := &fileStore{path: path}

	want := State{
		Tags: map[string]any{
			"Enable":   true,
			"Setpoint": 72.5,
			"Recipe":   "batch-3",
		},
		Programs: map[string]string{
			"Main": "PROGRAM Main\nEND_PROGRAM\n",
		},
	}

	if err := store.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("round trip mismatch:\n got  %s\n want %s", gotJSON, wantJSON)
	}
}

func TestFileStoreLoadMissingFile(t *testing.T) {
	dir := t.TempDir()
	store := &fileStore{path: filepath.Join(dir, "does-not-exist.json")}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load on missing file returned error: %v", err)
	}
	if len(got.Tags) != 0 || len(got.Programs) != 0 {
		t.Errorf("Load on missing file returned non-empty state: %+v", got)
	}
}

func TestFileStoreLoadCorruptJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "retain.json")
	if err := os.WriteFile(path, []byte("{not valid json"), 0o644); err != nil {
		t.Fatalf("setup WriteFile: %v", err)
	}
	store := &fileStore{path: path}

	if _, err := store.Load(); err == nil {
		t.Error("Load on corrupt JSON returned nil error, want an error")
	}
}

func TestFileStoreSaveLeavesNoTmpFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "retain.json")
	store := &fileStore{path: path}

	if err := store.Save(State{Tags: map[string]any{"x": true}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("expected .tmp file to be gone after Save, stat err = %v", err)
	}
}

// ---- ConfigMap store ----

// fakeAPIServer is a minimal stand-in for the Kubernetes API server's
// ConfigMap endpoints, backed by a map keyed by "namespace/name".
type fakeAPIServer struct {
	mu               sync.Mutex
	cms              map[string]configMap
	lastAuthHeader   string
	sawAuthorization bool
}

func newFakeAPIServer() *fakeAPIServer {
	return &fakeAPIServer{cms: map[string]configMap{}}
}

func (f *fakeAPIServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()

		if auth := r.Header.Get("Authorization"); auth != "" {
			f.sawAuthorization = true
			f.lastAuthHeader = auth
		}

		const prefix = "/api/v1/namespaces/"
		path := r.URL.Path[len(prefix):] // "{ns}/configmaps[/{name}]"

		// Collection endpoint: POST creates.
		if r.Method == http.MethodPost {
			var cm configMap
			if err := json.NewDecoder(r.Body).Decode(&cm); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			key := cm.Metadata.Namespace + "/" + cm.Metadata.Name
			f.cms[key] = cm
			w.WriteHeader(http.StatusCreated)
			return
		}

		// Resource endpoint: {ns}/configmaps/{name}
		ns, name, ok := strings.Cut(path, "/configmaps/")
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		key := ns + "/" + name

		switch r.Method {
		case http.MethodGet:
			cm, ok := f.cms[key]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(cm)
		case http.MethodPut:
			if _, ok := f.cms[key]; !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			var cm configMap
			if err := json.NewDecoder(r.Body).Decode(&cm); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f.cms[key] = cm
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

func newTestClient(t *testing.T, srv *httptest.Server) *k8sapi.Client {
	t.Helper()
	return &k8sapi.Client{Base: srv.URL, Namespace: "testns", HTTP: srv.Client()}
}

func TestConfigMapStoreLoadMissing(t *testing.T) {
	fake := newFakeAPIServer()
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	store := NewConfigMap(newTestClient(t, srv), "retain")
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load on missing configmap returned error: %v", err)
	}
	if len(got.Tags) != 0 || len(got.Programs) != 0 {
		t.Errorf("Load on missing configmap returned non-empty state: %+v", got)
	}
}

func TestConfigMapStoreSaveCreatesWhenAbsent(t *testing.T) {
	fake := newFakeAPIServer()
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	store := NewConfigMap(newTestClient(t, srv), "retain")
	want := State{Tags: map[string]any{"Enable": true}}
	if err := store.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	fake.mu.Lock()
	_, ok := fake.cms["testns/retain"]
	fake.mu.Unlock()
	if !ok {
		t.Fatal("Save did not create the configmap via the POST fallback")
	}
}

func TestConfigMapStoreSaveUpdatesWhenPresent(t *testing.T) {
	fake := newFakeAPIServer()
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	store := NewConfigMap(newTestClient(t, srv), "retain")
	if err := store.Save(State{Tags: map[string]any{"Enable": true}}); err != nil {
		t.Fatalf("initial Save: %v", err)
	}
	updated := State{Tags: map[string]any{"Enable": false, "Setpoint": 12.0}}
	if err := store.Save(updated); err != nil {
		t.Fatalf("update Save: %v", err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(updated)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("round trip mismatch after update:\n got  %s\n want %s", gotJSON, wantJSON)
	}
}

func TestConfigMapStoreRoundTrip(t *testing.T) {
	fake := newFakeAPIServer()
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	store := NewConfigMap(newTestClient(t, srv), "retain")
	want := State{
		Tags: map[string]any{
			"Enable":   true,
			"Setpoint": 72.5,
			"Recipe":   "batch-3",
		},
		Programs: map[string]string{
			"Main": "PROGRAM Main\nEND_PROGRAM\n",
		},
	}
	if err := store.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("round trip mismatch:\n got  %s\n want %s", gotJSON, wantJSON)
	}
}

func TestConfigMapStoreNoAuthHeaderWhenTokenPathEmpty(t *testing.T) {
	fake := newFakeAPIServer()
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	store := NewConfigMap(newTestClient(t, srv), "retain")
	if err := store.Save(State{Tags: map[string]any{"x": true}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := store.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	fake.mu.Lock()
	sawAuth := fake.sawAuthorization
	fake.mu.Unlock()
	if sawAuth {
		t.Error("request carried an Authorization header despite an empty tokenPath")
	}
}
