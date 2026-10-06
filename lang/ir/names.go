package ir

import "strings"

// Identifiers are case-insensitive (IEC 61131-3 §6.1.2): `Level`, `LEVEL`
// and `level` name the same variable, tag, POU, type, FB instance or
// member. Nautilus keeps every name AS DECLARED — maps are keyed by the
// declaration's spelling and that spelling is what diagnostics, the tag
// store, the API and the L5X writer show — and folds only where a name is
// COMPARED. These helpers are the one place that rule lives for the IR and
// the lowerer; other packages use them (or strings.EqualFold) rather than
// growing their own.

// NameKey folds an identifier to its comparison key. Two names are the
// same identifier exactly when their keys are equal. Use it to key a
// lookup table; never show a key to a user.
func NameKey(name string) string { return strings.ToUpper(name) }

// SameName reports whether a and b name the same identifier.
func SameName(a, b string) bool { return strings.EqualFold(a, b) }

// Lookup finds name in m: an exact hit first (the common case, and free),
// then a case-insensitive match. It returns the value, the key it was
// stored under (the declared spelling), and whether it was found. A map
// that holds two keys differing only in case is a duplicate declaration
// the caller should have rejected; Lookup then returns either.
func Lookup[V any](m map[string]V, name string) (V, string, bool) {
	if v, ok := m[name]; ok {
		return v, name, true
	}
	for k, v := range m {
		if strings.EqualFold(k, name) {
			return v, k, true
		}
	}
	var zero V
	return zero, "", false
}

// FieldOf resolves a member name to its index on the struct,
// case-insensitively.
func (d *StructDef) FieldOf(name string) (int, bool) {
	if d == nil {
		return 0, false
	}
	i, _, ok := Lookup(d.FieldIndex, name)
	return i, ok
}

// SlotOf resolves a pin or member name to its slot index on the function
// block, case-insensitively.
func (d *FBDef) SlotOf(name string) (int, bool) {
	if d == nil {
		return 0, false
	}
	i, _, ok := Lookup(d.SlotIndex, name)
	return i, ok
}
