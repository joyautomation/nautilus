package retain

import (
	"encoding/json"
	"os"
)

// fileStore retains State in a JSON file. Save writes to a sibling ".tmp"
// path and renames it over the target, so a crash or power loss mid-write
// never leaves a truncated or half-written file in place — the rename is
// atomic on the filesystems nautilus targets.
type fileStore struct{ path string }

func (f *fileStore) Kind() string { return "file" }

// Load reads State from disk. A missing file means nothing has been
// retained yet, not a failure, so it returns a zero State and a nil error;
// only a real I/O error (permissions) or a corrupt file (bad JSON) is
// reported.
func (f *fileStore) Load() (State, error) {
	var s State
	b, err := os.ReadFile(f.path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(b, &s)
}

func (f *fileStore) Save(s State) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, f.path)
}
