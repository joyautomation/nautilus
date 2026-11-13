//go:build !js

package hmibuild

import _ "embed"

//go:embed packs/packs.tar.gz
var packTGZ []byte

func packBytes() ([]byte, error) { return packTGZ, nil }
