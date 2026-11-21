// Package schemas embeds the manifest's JSON Schema so the controller can
// serve slices of it to the config screens (#711). The file stays the one
// contract: the editor, internal/project's schema test and the screens all
// read this copy.
package schemas

import _ "embed"

// Nautilus is nautilus.schema.json.
//
//go:embed nautilus.schema.json
var Nautilus []byte
