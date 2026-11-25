package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The reference data a portal without a datahub checkout downloads once:
// datahub's seed file and shared gene panels, at a fixed datahub commit.
const (
	datahubCommit = "448af88f1e792f684a6e1d5128e3d3aa844f1135"
	seedName      = "seed-cbioportal_hg19_hg38_v2.14.5.sql.gz"
	seedSHA256    = "da619680669e5b2a8deb2967ebdccaf6369d98336b666b991efcfe47e1960a7a"
)

var httpClient = &http.Client{Timeout: 10 * time.Minute}

var errNotFound = errors.New("not found")

// downloadSeed returns datahub's seed file, downloaded once into cacheDir
// and checked against its SHA-256.
func downloadSeed(cacheDir string, logf func(string, ...any)) (string, error) {
	dir := filepath.Join(cacheDir, "reference", datahubCommit[:12])
	path := filepath.Join(dir, seedName)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	url := "https://raw.githubusercontent.com/cBioPortal/datahub/" + datahubCommit + "/seedDB/" + seedName
	logf("downloading the reference genes (%s, 14 MB) once", seedName)
	if err := download(url, path, seedSHA256); err != nil {
		return "", err
	}
	return path, nil
}

// downloadGenePanels returns a folder of datahub's shared gene panels,
// downloaded once into cacheDir.
func downloadGenePanels(cacheDir string, logf func(string, ...any)) (string, error) {
	dir := filepath.Join(cacheDir, "reference", datahubCommit[:12], "gene_panels")
	if done, _ := filepath.Glob(filepath.Join(dir, ".complete")); len(done) > 0 {
		return dir, nil
	}
	resp, err := httpClient.Get("https://api.github.com/repos/cBioPortal/datahub/contents/reference_data/gene_panels?ref=" + datahubCommit)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("listing datahub's gene panels: %s", resp.Status)
	}
	var files []struct {
		Name        string `json:"name"`
		Type        string `json:"type"`
		DownloadURL string `json:"download_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&files); err != nil {
		return "", err
	}
	logf("downloading datahub's %d shared gene panels once", len(files))
	for _, f := range files {
		if f.Type != "file" || strings.Contains(f.Name, "/") {
			continue
		}
		// Most panel files are kept in Git LFS, which this address
		// serves; it answers 404 for the others, kept in git itself.
		url := "https://media.githubusercontent.com/media/cBioPortal/datahub/" + datahubCommit + "/reference_data/gene_panels/" + f.Name
		err := download(url, filepath.Join(dir, f.Name), "")
		if errors.Is(err, errNotFound) && f.DownloadURL != "" {
			err = download(f.DownloadURL, filepath.Join(dir, f.Name), "")
		}
		if err != nil {
			return "", err
		}
	}
	return dir, os.WriteFile(filepath.Join(dir, ".complete"), nil, 0o644)
}

// download saves url to path, checking its SHA-256 when sha is set.
func download(url, path, sha string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	resp, err := httpClient.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("downloading %s: %w", url, errNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading %s: %s", url, resp.Status)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".download-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), resp.Body); err != nil {
		tmp.Close()
		return fmt.Errorf("downloading %s: %w", url, err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); sha != "" && got != sha {
		return fmt.Errorf("downloading %s: SHA-256 %s, want %s", url, got, sha)
	}
	return os.Rename(tmp.Name(), path)
}
