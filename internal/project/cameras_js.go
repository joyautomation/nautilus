//go:build js

package project

import "gopkg.in/yaml.v3"

// The cameras (#724) are controller-only: the browser build keeps the rows as
// YAML nodes, unread, so none of the camera package is in the playground's wasm.
type CameraRows = []yaml.Node

type CameraSnapshotsCfg = *yaml.Node

func validateCameras(CameraRows, CameraSnapshotsCfg) error { return nil }
