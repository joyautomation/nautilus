//go:build js

package runtime

// The playground's wasm runs no redundant pair: it carries no tags in a
// state image (imagetags_carry.go is the controller's).

func buildCarrySet(*Tags, []string, []string, []string) *carrySet { return nil }

func (cs *carrySet) idxOf(map[string]bool) []int { return nil }

func (t *Tags) copyTagsLocked(*carrySet, []int, *TagValues) {}

func rootOf(n string) string { return n }
