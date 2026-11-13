package hmibuild

// Warning is one Svelte compiler warning.
type Warning struct {
	Code    string
	Message string
	Line    int // 1-based; 0 when the compiler gave no position
	Column  int // 1-based
}

// compiled is one .svelte (or .svelte.js) file, compiled.
type compiled struct {
	JS       string
	Warnings []Warning
}

// fatalWarnings throw when the component mounts, so the build stops on them
// with Svelte's own words: a prop or variable named `state` beside
// `$state(...)` makes `$state` a store subscription to it
// (store_rune_conflict), which throws "e.subscribe is not a function".
var fatalWarnings = map[string]bool{"store_rune_conflict": true}
