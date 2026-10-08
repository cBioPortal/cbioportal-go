// Package github is the small part of the GitHub REST API that
// cBioPortal.org's jobs use: reading pull requests, and reporting a job's
// result on a commit as a Check Run and a Deployment, signed in as the
// team's GitHub App.
package github

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Client calls the GitHub API, as the App when ClientID, InstallationID and
// KeyFile are set, else anonymously (reads only).
type Client struct {
	API            string // https://api.github.com
	ClientID       string // the App's client id (the JWT issuer)
	InstallationID string
	KeyFile        string // the App's private key (PEM)
	HTTP           *http.Client

	mu      sync.Mutex
	token   string
	tokenAt time.Time
	now     func() time.Time
}

// FromEnv configures a Client from GITHUB_API, GH_CLIENT_ID,
// GH_INSTALLATION_ID and GH_APP_KEY (default /etc/gh-app/key.pem).
func FromEnv() *Client {
	return &Client{
		API:            envOr("GITHUB_API", "https://api.github.com"),
		ClientID:       os.Getenv("GH_CLIENT_ID"),
		InstallationID: os.Getenv("GH_INSTALLATION_ID"),
		KeyFile:        envOr("GH_APP_KEY", "/etc/gh-app/key.pem"),
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// ErrNoApp is returned for writes when the App is not configured.
var ErrNoApp = errors.New("GitHub App not configured")

// HasApp reports whether the App's credentials are configured.
func (c *Client) HasApp() bool {
	if c.ClientID == "" || c.InstallationID == "" || c.KeyFile == "" {
		return false
	}
	_, err := os.Stat(c.KeyFile)
	return err == nil
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: time.Minute}
}

func (c *Client) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// tokenLifetime is how long an installation token is reused; GitHub's
// last an hour.
const tokenLifetime = 50 * time.Minute

// Token returns an installation token of the App, reused for 50 minutes.
func (c *Client) Token(ctx context.Context) (string, error) {
	if !c.HasApp() {
		return "", ErrNoApp
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && c.clock().Sub(c.tokenAt) < tokenLifetime {
		return c.token, nil
	}
	jwt, err := c.appJWT()
	if err != nil {
		return "", err
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := c.do(ctx, http.MethodPost, "/app/installations/"+c.InstallationID+"/access_tokens", "Bearer "+jwt, nil, &out); err != nil {
		return "", fmt.Errorf("installation token: %w", err)
	}
	if out.Token == "" {
		return "", errors.New("installation token: empty answer")
	}
	c.token, c.tokenAt = out.Token, c.clock()
	return c.token, nil
}

// appJWT is the App's own credential: a JWT signed with its key, valid for
// nine minutes (GitHub allows ten), issued a minute early against clock
// drift.
func (c *Client) appJWT() (string, error) {
	pemBytes, err := os.ReadFile(c.KeyFile)
	if err != nil {
		return "", err
	}
	key, err := parseKey(pemBytes)
	if err != nil {
		return "", err
	}
	now := c.clock().Unix()
	return signJWT(key, map[string]any{"iat": now - 60, "exp": now + 540, "iss": c.ClientID})
}

func parseKey(b []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, errors.New("GitHub App key: no PEM block")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("GitHub App key: %w", err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("GitHub App key: not an RSA key")
	}
	return rk, nil
}

func signJWT(key *rsa.PrivateKey, claims map[string]any) (string, error) {
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signed := header + "." + enc.EncodeToString(payload)
	sum := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signed + "." + enc.EncodeToString(sig), nil
}

// Get reads path into out, with the App's token when there is one (a
// higher rate limit, private repositories), else anonymously.
func (c *Client) Get(ctx context.Context, path string, out any) error {
	auth := ""
	if tok, err := c.Token(ctx); err == nil {
		auth = "Bearer " + tok
	}
	return c.do(ctx, http.MethodGet, path, auth, nil, out)
}

// Send writes body to path as the App.
func (c *Client) Send(ctx context.Context, method, path string, body, out any) error {
	tok, err := c.Token(ctx)
	if err != nil {
		return err
	}
	return c.do(ctx, method, path, "Bearer "+tok, body, out)
}

// HTTPError is a non-2xx answer.
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("HTTP %d: %.300s", e.Status, e.Body) }

func (c *Client) do(ctx context.Context, method, path, auth string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.API, "/")+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: %w", method, path, &HTTPError{resp.StatusCode, string(b)})
	}
	if out == nil || len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, out)
}

// PullRequest is the part of a pull request the jobs read.
type PullRequest struct {
	Number int    `json:"number"`
	State  string `json:"state"`
	Body   string `json:"body"`
	Head   struct {
		SHA  string `json:"sha"`
		Repo *struct {
			CloneURL string `json:"clone_url"`
		} `json:"repo"`
	} `json:"head"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

// HasLabel reports whether the pull request has label name.
func (p *PullRequest) HasLabel(name string) bool {
	for _, l := range p.Labels {
		if l.Name == name {
			return true
		}
	}
	return false
}

// HeadCloneURL is the clone URL of the head repository ("" when deleted).
func (p *PullRequest) HeadCloneURL() string {
	if p.Head.Repo == nil {
		return ""
	}
	return p.Head.Repo.CloneURL
}

// PR reads pull request n of repo (owner/name).
func (c *Client) PR(ctx context.Context, repo string, n int) (*PullRequest, error) {
	var pr PullRequest
	if err := c.Get(ctx, fmt.Sprintf("/repos/%s/pulls/%d", repo, n), &pr); err != nil {
		return nil, err
	}
	return &pr, nil
}

// PRFiles lists the paths a pull request changes (GitHub lists at most
// 3000).
func (c *Client) PRFiles(ctx context.Context, repo string, n int) ([]string, error) {
	var out []string
	for page := 1; page <= 30; page++ {
		var files []struct {
			Filename string `json:"filename"`
		}
		if err := c.Get(ctx, fmt.Sprintf("/repos/%s/pulls/%d/files?per_page=100&page=%d", repo, n, page), &files); err != nil {
			return nil, err
		}
		if len(files) == 0 {
			break
		}
		for _, f := range files {
			out = append(out, f.Filename)
		}
	}
	return out, nil
}
