//go:build !unix

package main

import (
	"io/fs"
	"os"
)

func keepOwner(*os.File, fs.FileInfo) error { return nil }
