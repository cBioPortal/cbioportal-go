package chdb

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

// Version is the chDB engine release Library downloads.
const Version = "v26.11.1"

// releases are the libchdb archives of Version, by GOOS/GOARCH, with their
// SHA-256 digests as GitHub publishes them.
var releases = map[string]struct{ asset, sha256 string }{
	"linux/amd64":  {"linux-x86_64-libchdb.tar.gz", "210f34d85596f8580176bee4c58b688e05d9389d87eb51f4ff94ae08294c65a7"},
	"linux/arm64":  {"linux-aarch64-libchdb.tar.gz", "86a618b092036ceca5f41b2bfdb6b38253ab2d704a1dd577a3cbb351a0256698"},
	"darwin/arm64": {"macos-arm64-libchdb.tar.gz", "c0a4df14c10369ce86b5e58f6b9cfade30b155cea6908cc7556c2a73ef18cd08"},
	"darwin/amd64": {"macos-x86_64-libchdb.tar.gz", "ace512c37c673075b59b551574d3c77edf159a8bf2d1b9d829ef0721a9ececd8"},
}

// Progress, when set, is called as Library downloads the engine, with the
// bytes so far and the whole size (-1 when the server does not say).
var Progress func(done, total int64)

type progressReader struct {
	r           io.Reader
	done, total int64
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.done += int64(n)
	Progress(p.done, p.total)
	return n, err
}

// Library returns the path of libchdb Version in cacheDir, downloading it
// from chDB's GitHub releases the first time (about 120-180 MB, checked
// against its published SHA-256). logf reports the download.
func Library(cacheDir string, logf func(format string, args ...any)) (string, error) {
	key := runtime.GOOS + "/" + runtime.GOARCH
	rel, ok := releases[key]
	if !ok {
		return "", fmt.Errorf("chdb has no engine for %s; pass the path of a libchdb with --chdb-lib", key)
	}
	dir := filepath.Join(cacheDir, "chdb", Version)
	lib := filepath.Join(dir, "libchdb.so")
	if _, err := os.Stat(lib); err == nil {
		return lib, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	url := "https://github.com/chdb-io/chdb-core/releases/download/" + Version + "/" + rel.asset
	logf("downloading the ClickHouse engine (chDB %s, %s) once, from %s", Version, key, url)
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading %s: %s", url, resp.Status)
	}
	archive, err := os.CreateTemp(dir, "download-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(archive.Name())
	defer archive.Close()
	h := sha256.New()
	var body io.Reader = resp.Body
	if Progress != nil {
		body = &progressReader{r: resp.Body, total: resp.ContentLength}
	}
	if _, err := io.Copy(io.MultiWriter(archive, h), body); err != nil {
		return "", fmt.Errorf("downloading %s: %w", url, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != rel.sha256 {
		return "", fmt.Errorf("downloading %s: SHA-256 %s, want %s", url, got, rel.sha256)
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	if err := extractLibrary(archive, lib); err != nil {
		return "", err
	}
	return lib, nil
}

// extractLibrary writes the libchdb shared library of a release archive to
// dest.
func extractLibrary(archive io.Reader, dest string) error {
	gz, err := gzip.NewReader(archive)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return errors.New("the chdb archive has no libchdb library")
		}
		if err != nil {
			return err
		}
		name := path.Base(hdr.Name)
		if hdr.Typeflag != tar.TypeReg || !strings.HasPrefix(name, "libchdb.") {
			continue
		}
		tmp := dest + ".tmp"
		f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
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
		return os.Rename(tmp, dest)
	}
}
