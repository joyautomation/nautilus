//go:build js

package hmibuild

import (
	"encoding/binary"
	"errors"
	"image"
)

// The browser build links no image/png (see pwa_icons.go). It builds HMIs
// with NoPWA, so no icons are made here; a project's pwa.icon is still checked
// for being a PNG of a sensible size, from its header alone.

var pngSignature = []byte("\x89PNG\r\n\x1a\n")

func iconConfig(b []byte) (image.Config, error) {
	if len(b) < 24 || string(b[:8]) != string(pngSignature) || string(b[12:16]) != "IHDR" {
		return image.Config{}, errors.New("png: invalid format: not a PNG header")
	}
	return image.Config{Width: int(binary.BigEndian.Uint32(b[16:20])), Height: int(binary.BigEndian.Uint32(b[20:24]))}, nil
}

func iconFiles(map[string][]byte, *PWA, string) error {
	return errors.New("an installable HMI (PWA icons) is not built in the browser: build with NoPWA")
}
