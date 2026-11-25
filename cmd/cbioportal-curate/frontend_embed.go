//go:build embedfrontend

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// frontendArchive is the website build (frontend/dist) as a .tar.gz, put
// here by `make curate`.
//
//go:embed frontend.tar.gz
var frontendArchive []byte

// frontendDir is --frontend-dir, or else the built-in website, unpacked
// once into the cache folder.
func frontendDir(o *options) (string, error) {
	if o.frontendDir != "" {
		return o.frontendDir, nil
	}
	sum := sha256.Sum256(frontendArchive)
	dir := filepath.Join(o.cacheDir, "frontend", hex.EncodeToString(sum[:8]))
	if _, err := os.Stat(filepath.Join(dir, "index.html")); err == nil {
		return dir, nil
	}
	tmp := dir + ".tmp"
	os.RemoveAll(tmp)
	gz, err := gzip.NewReader(bytes.NewReader(frontendArchive))
	if err != nil {
		return "", err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		name := filepath.Clean(hdr.Name)
		if name == "." || strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			continue
		}
		p := filepath.Join(tmp, name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(p, 0o755); err != nil {
				return "", err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return "", err
			}
			f, err := os.Create(p)
			if err != nil {
				return "", err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return "", err
			}
			if err := f.Close(); err != nil {
				return "", err
			}
		}
	}
	os.RemoveAll(dir)
	return dir, os.Rename(tmp, dir)
}
