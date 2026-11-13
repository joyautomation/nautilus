package hmibuild

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
)

// The closed package set, packed (packs/generate.sh): the files of Svelte,
// the HMI kit, the 3D kit, three and Threlte that their public entry points
// reach, as node_modules/<package>/..., and svelte-compiler.js (that
// Svelte's compiler, bundled for goja). Committed, so `go install` gets a
// naut that builds HMIs. naut embeds it (packs_embed.go); naut.wasm
// fetches it when a project first needs it (packs_js.go).

var (
	rootOnce sync.Once
	rootDir  string
	rootErr  error
)

// packRoot unpacks the pack once into the user's cache (keyed by its
// hash, so a new naut gets its own) and returns that directory: esbuild
// resolves packages from real files, like any node_modules.
// NAUT_HMI_PACKS names an unpacked one instead (developing the pack).
func packRoot() (string, error) {
	rootOnce.Do(func() {
		if dir := os.Getenv("NAUT_HMI_PACKS"); dir != "" {
			rootDir = dir
			return
		}
		rootDir, rootErr = unpack()
	})
	return rootDir, rootErr
}

func unpack() (string, error) {
	packTGZ, err := packBytes()
	if err != nil {
		return "", err
	}
	if len(packTGZ) < 64 {
		return "", errors.New("this naut was built without its HMI packages (internal/hmibuild/packs/packs.tar.gz is empty); build naut from a full checkout")
	}
	sum := sha256.Sum256(packTGZ)
	base, err := os.UserCacheDir()
	if err != nil || base == "" {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "nautilus", "hmi-packs", hex.EncodeToString(sum[:8]))
	if _, err := os.Stat(filepath.Join(dir, ".complete")); err == nil {
		return dir, nil
	}
	tmp := fmt.Sprintf("%s.tmp-%d", dir, os.Getpid())
	_ = os.RemoveAll(tmp)
	if err := extract(packTGZ, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return "", fmt.Errorf("unpacking naut's HMI packages into %s: %w", filepath.Dir(dir), err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".complete"), nil, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dir); err != nil {
		// another naut unpacked the same pack first: theirs is as good
		_ = os.RemoveAll(tmp)
		if _, statErr := os.Stat(filepath.Join(dir, ".complete")); statErr != nil {
			return "", err
		}
	}
	return dir, nil
}

func extract(tgz []byte, dir string) error {
	zr, err := gzip.NewReader(bytes.NewReader(tgz))
	if err != nil {
		return err
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := path.Clean(h.Name)
		if strings.HasPrefix(name, "../") || path.IsAbs(name) {
			return fmt.Errorf("pack entry %q leaves the pack", h.Name)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		dst := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, tr); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
}
