// Package blackbox runs the independent tester's checks against a running
// naut: scripts written by a foreign client against the public docs and the
// specs, imported here byte for byte (README.md). The package has no
// production code; its one test, TestBlackbox, is the runner, so each
// check's verdict lands in the runtime evidence like any other test's.
package blackbox
