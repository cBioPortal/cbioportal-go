package api

import (
	"bytes"
	"encoding/base64"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// The frontend reaches OncoKB through an obfuscated proxy: the path, query
// parameters and body are base64-encoded, and the response is returned as a
// base64-encoded JSON string. The proxy adds the portal's OncoKB token, if any.

const oncokbProxyPrefix = "/proxy/A8F74CD7851BDEE8DCD2E86AB4E2A711/"

const oncokbUserAgreement = "I/We do NOT use this obfuscated proxy to programmatically obtain private OncoKB data. " +
	"I/We know that I/we should get a valid data access token by registering at https://www.oncokb.org/account/register."

func (s *State) routeProxy(mux *http.ServeMux) {
	mux.HandleFunc(oncokbProxyPrefix, s.oncokbProxy)
	mux.HandleFunc("/proxy/dev/oncokb/", s.oncokbDevProxy)
}

func oncokbBaseURL() string {
	if u := os.Getenv("ONCOKB_URL"); u != "" {
		return strings.TrimSuffix(u, "/")
	}
	return "https://public.api.oncokb.org/api/v1"
}

// decodeB64 decodes leniently like commons-codec: characters outside the
// base64 alphabet (quotes, whitespace) are ignored and padding is optional.
func decodeB64(s string) string {
	var clean strings.Builder
	for _, c := range s {
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '+' || c == '/' {
			clean.WriteRune(c)
		} else if c == '-' {
			clean.WriteRune('+')
		} else if c == '_' {
			clean.WriteRune('/')
		}
	}
	b, _ := base64.RawStdEncoding.DecodeString(clean.String())
	return string(b)
}

func (s *State) oncokbProxy(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Proxy-User-Agreement") != oncokbUserAgreement {
		readBody(r, nil)
		http.Error(w, "Fair Usage Agreement is missing", http.StatusBadRequest)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	var body []byte
	if len(raw) > 0 {
		body = []byte(decodeB64(string(raw)))
	}
	path := decodeB64(strings.TrimPrefix(r.URL.Path, oncokbProxyPrefix))
	query := url.Values{}
	for k, vs := range r.URL.Query() {
		for _, v := range vs {
			query.Add(decodeB64(k), decodeB64(v))
		}
	}
	resp, status, err := oncokbExchange(r, path, query.Encode(), body, os.Getenv("ONCOKB_TOKEN"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if status >= 400 {
		http.Error(w, string(resp), status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`"` + base64.StdEncoding.EncodeToString(resp) + `"`))
}

// oncokbDevProxy forwards plain (unencoded) requests with the caller's token.
func (s *State) oncokbDevProxy(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	token := r.Header.Get("X-Proxy-User-Agreement")
	if token == "" {
		token = "NA"
	}
	resp, status, err := oncokbExchange(r, strings.TrimPrefix(r.URL.Path, "/proxy/dev/oncokb"), r.URL.RawQuery, body, token)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(resp)
}

var oncokbClient = &http.Client{Timeout: 60 * time.Second}

func oncokbExchange(r *http.Request, path, query string, body []byte, token string) ([]byte, int, error) {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	target := oncokbBaseURL() + path
	if query != "" {
		target += "?" + query
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, rd)
	if err != nil {
		return nil, 0, err
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := oncokbClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	return out, resp.StatusCode, err
}
