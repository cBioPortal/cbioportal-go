package datahub

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Pointer is a Git LFS pointer file: the object a large file stands for.
type Pointer struct {
	OID  string // sha256, hex
	Size int64
}

const pointerVersion = "version https://git-lfs"

// maxPointerSize is the largest file taken for a pointer file.
const maxPointerSize = 2048

// ParsePointer parses the content of an LFS pointer file.
func ParsePointer(b []byte) (Pointer, bool) {
	if !bytes.HasPrefix(b, []byte(pointerVersion)) {
		return Pointer{}, false
	}
	var p Pointer
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		k, v, _ := strings.Cut(sc.Text(), " ")
		switch k {
		case "oid":
			p.OID, _ = strings.CutPrefix(v, "sha256:")
		case "size":
			p.Size, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	if len(p.OID) != 64 || p.Size < 0 {
		return Pointer{}, false
	}
	if _, err := hex.DecodeString(p.OID); err != nil {
		return Pointer{}, false
	}
	return p, true
}

// pointerFiles lists the LFS pointer files under root.
func pointerFiles(root string) (map[string]Pointer, error) {
	out := map[string]Pointer{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > maxPointerSize {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if p, ok := ParsePointer(b); ok {
			out[path] = p
		}
		return nil
	})
	return out, err
}

// PullLFS replaces the LFS pointer files under Dir/sub with their content
// from the LFS store (LFSURL), and those it lacks from FallbackLFSURL when
// set. It returns the number of files downloaded.
func (c *Checkout) PullLFS(ctx context.Context, sub string) (int, error) {
	files, err := pointerFiles(filepath.Join(c.Dir, sub))
	if err != nil {
		return 0, err
	}
	if len(files) == 0 {
		return 0, nil
	}
	store := c.LFSURL
	if store == "" {
		store = LFSURL()
	}
	missing, err := c.download(ctx, store, files)
	if err != nil {
		return 0, err
	}
	if len(missing) > 0 && c.FallbackLFSURL != "" {
		for path := range missing {
			c.logf("  %s: not in datahub's LFS store, trying %s", c.rel(path), c.FallbackLFSURL)
		}
		missing, err = c.download(ctx, c.FallbackLFSURL, missing)
		if err != nil {
			return 0, err
		}
	}
	if len(missing) > 0 {
		var names []string
		for path := range missing {
			names = append(names, c.rel(path))
		}
		return 0, fmt.Errorf("LFS objects not found: %s", strings.Join(names, ", "))
	}
	c.logf("  %d LFS files downloaded", len(files))
	return len(files), nil
}

func (c *Checkout) rel(path string) string {
	if r, err := filepath.Rel(c.Dir, path); err == nil {
		return r
	}
	return path
}

// lfsHTTP is the client for LFS requests; downloads of large objects may
// take long, so only the batch requests have a deadline.
var lfsHTTP = &http.Client{}

// batchObject is an object of a Git LFS batch request or response.
type batchObject struct {
	OID     string `json:"oid"`
	Size    int64  `json:"size"`
	Actions *struct {
		Download *struct {
			Href   string            `json:"href"`
			Header map[string]string `json:"header"`
		} `json:"download"`
	} `json:"actions,omitempty"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// download fetches the objects of files from the LFS store at base (the
// lfs.url form: the batch endpoint is <base>/objects/batch) and returns the
// files whose objects the store does not have.
func (c *Checkout) download(ctx context.Context, base string, files map[string]Pointer) (map[string]Pointer, error) {
	byOID := map[string][]string{}
	var objs []batchObject
	for path, p := range files {
		if _, seen := byOID[p.OID]; !seen {
			objs = append(objs, batchObject{OID: p.OID, Size: p.Size})
		}
		byOID[p.OID] = append(byOID[p.OID], path)
	}
	missing := map[string]Pointer{}
	const batchSize = 100
	type job struct {
		obj   batchObject
		paths []string
	}
	var jobs []job
	for start := 0; start < len(objs); start += batchSize {
		resp, err := lfsBatch(ctx, base, objs[start:min(start+batchSize, len(objs))])
		if err != nil {
			return nil, err
		}
		for _, o := range resp {
			if o.Error != nil || o.Actions == nil || o.Actions.Download == nil {
				for _, path := range byOID[o.OID] {
					missing[path] = files[path]
				}
				continue
			}
			jobs = append(jobs, job{o, byOID[o.OID]})
		}
	}

	// A few downloads at a time.
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		firstEr error
		sem     = make(chan struct{}, 4)
	)
	for _, j := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			err := fetchObject(ctx, j.obj, j.paths)
			if err != nil {
				mu.Lock()
				firstEr = errors.Join(firstEr, fmt.Errorf("%s: %w", c.rel(j.paths[0]), err))
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return missing, firstEr
}

// lfsBatch asks the store for download links of objs.
func lfsBatch(ctx context.Context, base string, objs []batchObject) ([]batchObject, error) {
	body, _ := json.Marshal(map[string]any{
		"operation": "download",
		"transfers": []string{"basic"},
		"objects":   objs,
	})
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	url := strings.TrimRight(base, "/") + "/objects/batch"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.git-lfs+json")
	req.Header.Set("Content-Type", "application/vnd.git-lfs+json")
	resp, err := lfsHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("LFS batch request to %s: %w", base, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode == http.StatusNotFound {
		// The whole repository is unknown to this store.
		out := make([]batchObject, len(objs))
		for i, o := range objs {
			out[i] = batchObject{OID: o.OID, Error: &struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			}{404, "not found"}}
		}
		return out, nil
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("LFS batch request to %s: HTTP %d: %.300s", base, resp.StatusCode, b)
	}
	var r struct {
		Objects []batchObject `json:"objects"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("LFS batch answer of %s: %w", base, err)
	}
	return r.Objects, nil
}

// retryDelay is the wait before a download's second attempt (twice that
// before the third).
var retryDelay = 5 * time.Second

// fetchObject downloads one object, checks its size and hash, and writes
// it over each of its pointer files.
func fetchObject(ctx context.Context, o batchObject, paths []string) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * retryDelay):
			}
		}
		lastErr = fetchObjectOnce(ctx, o, paths)
		if lastErr == nil {
			return nil
		}
	}
	return lastErr
}

func fetchObjectOnce(ctx context.Context, o batchObject, paths []string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.Actions.Download.Href, nil)
	if err != nil {
		return err
	}
	for k, v := range o.Actions.Download.Header {
		req.Header.Set(k, v)
	}
	resp, err := lfsHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}
	first := paths[0]
	tmp := first + ".lfs"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n != o.Size {
		err = fmt.Errorf("download: %d bytes, expected %d", n, o.Size)
	}
	if err == nil && hex.EncodeToString(h.Sum(nil)) != o.OID {
		err = fmt.Errorf("download: content does not match oid %s", o.OID)
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	for _, p := range paths[1:] {
		if err := copyFile(tmp, p); err != nil {
			os.Remove(tmp)
			return err
		}
	}
	return os.Rename(tmp, first)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
