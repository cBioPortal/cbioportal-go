package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Public studies come from cBioPortal's study archives, the files behind
// the Download links of www.cbioportal.org/datasets; their names and sizes
// from www.cbioportal.org's API.
const (
	studyArchives = "https://datahub.assets.cbioportal.org/"
	publicAPI     = "https://www.cbioportal.org/api/studies?projection=SUMMARY"
)

var studyIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

type publicStudy struct {
	StudyID      string `json:"studyId"`
	Name         string `json:"name"`
	CancerTypeID string `json:"cancerTypeId"`
	Samples      int    `json:"allSampleCount"`
	Description  string `json:"description"`
}

// publicCatalog is the list of public studies, fetched once.
type publicCatalog struct {
	once    sync.Once
	studies []publicStudy
	err     error
}

func (c *publicCatalog) list() ([]publicStudy, error) {
	c.once.Do(func() { c.studies, c.err = fetchPublicStudies() })
	return c.studies, c.err
}

func fetchPublicStudies() ([]publicStudy, error) {
	var ids []string
	if err := getJSON(studyArchives+"study_list.json", &ids); err != nil {
		return nil, err
	}
	var all []publicStudy
	if err := getJSON(publicAPI, &all); err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, id := range ids {
		have[id] = true
	}
	var out []publicStudy
	seen := map[string]bool{}
	for _, s := range all {
		if have[s.StudyID] {
			out = append(out, s)
			seen[s.StudyID] = true
		}
	}
	// Archives without a study on www still download; they show by id.
	for _, id := range ids {
		if !seen[id] {
			out = append(out, publicStudy{StudyID: id, Name: id})
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

func getJSON(url string, v any) error {
	resp, err := httpClient.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// downloadStudy downloads public study id into dir/<id> (replacing an
// earlier download) and returns that folder.
func downloadStudy(dir, id string) (string, error) {
	if !studyIDPattern.MatchString(id) {
		return "", fmt.Errorf("%q is not a study id", id)
	}
	resp, err := httpClient.Get(studyArchives + id + ".tar.gz")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading %s: %s", id, resp.Status)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(dir, ".download-"+id+"-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	if err := untar(resp.Body, tmp); err != nil {
		return "", fmt.Errorf("unpacking %s: %w", id, err)
	}
	src := filepath.Join(tmp, id)
	if !isStudy(src) {
		return "", fmt.Errorf("the %s archive has no %s/meta_study.txt", id, id)
	}
	dest := filepath.Join(dir, id)
	old := dest + ".old"
	os.RemoveAll(old)
	if _, err := os.Stat(dest); err == nil {
		if err := os.Rename(dest, old); err != nil {
			return "", err
		}
	}
	if err := os.Rename(src, dest); err != nil {
		return "", err
	}
	os.RemoveAll(old)
	return dest, nil
}

// untar unpacks a .tar.gz stream into dir, refusing paths outside it.
func untar(r io.Reader, dir string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(hdr.Name)
		if name == "." || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			continue
		}
		p := filepath.Join(dir, name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
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
}
