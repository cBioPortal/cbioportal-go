package github

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testKey(t *testing.T) (*rsa.PrivateKey, string) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "key.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return key, path
}

func TestAppJWT(t *testing.T) {
	key, path := testKey(t)
	now := time.Unix(1_700_000_000, 0)
	c := &Client{ClientID: "Iv1.abc", InstallationID: "1", KeyFile: path, now: func() time.Time { return now }}
	jwt, err := c.appJWT()
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("%q", jwt)
	}
	enc := base64.RawURLEncoding
	header, _ := enc.DecodeString(parts[0])
	payload, _ := enc.DecodeString(parts[1])
	sig, _ := enc.DecodeString(parts[2])
	if string(header) != `{"alg":"RS256","typ":"JWT"}` {
		t.Errorf("header %s", header)
	}
	var claims struct {
		Iat, Exp int64
		Iss      string
	}
	json.Unmarshal(payload, &claims)
	if claims.Iss != "Iv1.abc" || claims.Iat != now.Unix()-60 || claims.Exp != now.Unix()+540 {
		t.Errorf("claims %+v", claims)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
		t.Errorf("signature: %v", err)
	}

	// A PKCS#8 key works too.
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600)
	if _, err := c.appJWT(); err != nil {
		t.Errorf("PKCS#8: %v", err)
	}
}

// fakeGitHub records requests and answers them.
type fakeGitHub struct {
	mu       sync.Mutex
	requests []string // "METHOD path auth body"
	tokens   int
}

func (f *fakeGitHub) serve(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.requests = append(f.requests, fmt.Sprintf("%s %s %s %s", r.Method, r.URL.RequestURI(), r.Header.Get("Authorization"), body))
		f.mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			f.tokens++
			fmt.Fprintf(w, `{"token":"inst-%d"}`, f.tokens)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/check-runs"):
			fmt.Fprint(w, `{"id":11}`)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deployments"):
			fmt.Fprint(w, `{"id":22}`)
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/deployments"):
			fmt.Fprint(w, `[{"id":31},{"id":32}]`)
		case strings.Contains(r.URL.Path, "/pulls/5/files"):
			if r.URL.Query().Get("page") == "1" {
				fmt.Fprint(w, `[{"filename":"public/a/x.txt"},{"filename":"public/b/y.txt"}]`)
			} else {
				fmt.Fprint(w, `[]`)
			}
		case strings.HasSuffix(r.URL.Path, "/pulls/5"):
			fmt.Fprint(w, `{"number":5,"state":"open","body":"hi","head":{"sha":"abc","repo":{"clone_url":"https://github.com/x/datahub.git"}},"labels":[{"name":"preview"}]}`)
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestStatusReporting(t *testing.T) {
	_, path := testKey(t)
	f := &fakeGitHub{}
	srv := f.serve(t)
	c := &Client{API: srv.URL, ClientID: "app", InstallationID: "7", KeyFile: path}
	ctx := context.Background()

	pr, err := c.PR(ctx, "o/r", 5)
	if err != nil || pr.Head.SHA != "abc" || !pr.HasLabel("preview") || pr.HasLabel("x") || pr.HeadCloneURL() != "https://github.com/x/datahub.git" {
		t.Fatalf("PR: %+v, %v", pr, err)
	}
	files, err := c.PRFiles(ctx, "o/r", 5)
	if err != nil || strings.Join(files, ",") != "public/a/x.txt,public/b/y.txt" {
		t.Fatalf("PRFiles: %q, %v", files, err)
	}

	s := &Status{C: c, Repo: "o/r", SHA: "abc", CheckName: "Preview", Environment: "pr-5-preview", Description: "d", LogURL: "https://logs"}
	s.Start(ctx, "Building...", "summary", "https://env/")
	s.Progress(ctx, "Half", "more")
	s.Finish(ctx, "skipped", "Superseded", "final", "https://details", "https://env/")
	if err := c.DeactivateEnvironment(ctx, "o/r", "pr-5 preview"); err != nil {
		t.Fatal(err)
	}

	want := []string{
		`POST /app/installations/7/access_tokens Bearer `,
		`GET /repos/o/r/pulls/5 Bearer inst-1`,
		`GET /repos/o/r/pulls/5/files?per_page=100&page=1 Bearer inst-1`,
		`GET /repos/o/r/pulls/5/files?per_page=100&page=2 Bearer inst-1`,
		`POST /repos/o/r/check-runs Bearer inst-1 {"details_url":"https://logs","head_sha":"abc","name":"Preview","output":{"summary":"summary","title":"Building..."},"status":"in_progress"}`,
		`POST /repos/o/r/deployments Bearer inst-1 {"auto_merge":false,"description":"d","environment":"pr-5-preview","ref":"abc","required_contexts":[],"transient_environment":true}`,
		`POST /repos/o/r/deployments/22/statuses Bearer inst-1 {"environment_url":"https://env/","log_url":"https://logs","state":"in_progress"}`,
		`PATCH /repos/o/r/check-runs/11 Bearer inst-1 {"details_url":"https://logs","output":{"summary":"more","title":"Half"},"status":"in_progress"}`,
		`PATCH /repos/o/r/check-runs/11 Bearer inst-1 {"conclusion":"skipped","details_url":"https://details","output":{"summary":"final","title":"Superseded"},"status":"completed"}`,
		`POST /repos/o/r/deployments/22/statuses Bearer inst-1 {"environment_url":"https://env/","log_url":"https://logs","state":"inactive"}`,
		`GET /repos/o/r/deployments?environment=pr-5+preview&per_page=100 Bearer inst-1`,
		`POST /repos/o/r/deployments/31/statuses Bearer inst-1 {"state":"inactive"}`,
		`POST /repos/o/r/deployments/32/statuses Bearer inst-1 {"state":"inactive"}`,
	}
	if len(f.requests) != len(want) {
		t.Fatalf("%d requests:\n%s", len(f.requests), strings.Join(f.requests, "\n"))
	}
	for i, w := range want {
		got := strings.TrimSpace(f.requests[i])
		if i == 0 {
			got = strings.TrimSpace(got[:len(w)]) // the JWT varies
		}
		if got != strings.TrimSpace(w) {
			t.Errorf("request %d:\n got %s\nwant %s", i, got, w)
		}
	}
	if f.tokens != 1 {
		t.Errorf("%d installation tokens, want 1 (reused)", f.tokens)
	}
}

func TestNoApp(t *testing.T) {
	f := &fakeGitHub{}
	srv := f.serve(t)
	c := &Client{API: srv.URL, KeyFile: "/nonexistent"}
	if pr, err := c.PR(context.Background(), "o/r", 5); err != nil || pr.Number != 5 {
		t.Fatalf("anonymous read: %v", err)
	}
	s := &Status{C: c, Repo: "o/r", SHA: "abc"}
	s.Start(context.Background(), "t", "s", "u")
	s.Finish(context.Background(), "success", "t", "s", "u", "u")
	if len(f.requests) != 1 || strings.Contains(f.requests[0], "Bearer") {
		t.Errorf("requests without the App: %q", f.requests)
	}
}

func TestDeploymentState(t *testing.T) {
	for concl, want := range map[string]string{"success": "success", "skipped": "inactive", "neutral": "failure", "failure": "failure"} {
		if got := DeploymentState(concl); got != want {
			t.Errorf("%s: %s, want %s", concl, got, want)
		}
	}
}
