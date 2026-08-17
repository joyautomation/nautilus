// Package retain is a controller's retained memory: operator setpoints and
// online-edited programs survive restarts, failovers, and redeploys. In a
// cluster the backing store is a ConfigMap, read and written through
// internal/k8sapi (the same pattern the leader package uses for its Lease);
// standalone it's a JSON file next to the project.
package retain

import "github.com/joyautomation/nautilus/internal/k8sapi"

// State is everything a controller retains across power cycles.
type State struct {
	// Tags are retained tag values (operator setpoints), JSON-native scalars.
	Tags map[string]any `json:"tags,omitempty"`
	// Programs are online-edited IEC sources keyed by task name, so an
	// edit made through the runtime API survives a pod restart until
	// `nautilus pull` lands it in git.
	Programs map[string]string `json:"programs,omitempty"`
}

// Store persists and recalls State. Implementations must treat "nothing has
// been saved yet" as success: Load on a store with no prior Save returns a
// zero State and a nil error, not a not-found error, so callers can Load
// unconditionally on startup.
type Store interface {
	Load() (State, error)
	Save(State) error
	Kind() string // "file" | "configmap", for logs
}

// New picks the store for the environment: a ConfigMap when running in a
// cluster, falling back to the file store if the service account can't be
// read (e.g. the pod's token isn't mounted yet, or RBAC is misconfigured —
// better to retain to a file than to not retain at all); the file store
// outside a cluster.
func New(configMapName, filePath string) Store {
	if k8sapi.InCluster() {
		if c, err := k8sapi.New(); err == nil {
			return NewConfigMap(c, configMapName)
		}
	}
	return &fileStore{path: filePath}
}
