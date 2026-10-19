package contract

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"testing"
	"time"
)

// Shared helpers of the contract tests: sending requests, decoding replies,
// building unique keys and comparing floats.

var client = &http.Client{Timeout: 30 * time.Second}

// send sends a request to the API and returns the response with its body
// read and closed. A nil body sends no body.
func send(t *testing.T, method, path string, body []byte, h http.Header) (*http.Response, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, apiBase()+path, r)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	if h != nil {
		req.Header = h
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func jsonHeader() http.Header { return http.Header{"Content-Type": {"application/json"}} }

// rawDo sends a JSON body given as text ("" for none) and returns the
// response and its body.
func rawDo(t *testing.T, method, path, body string) (*http.Response, []byte) {
	t.Helper()
	var b []byte
	if body != "" {
		b = []byte(body)
	}
	return send(t, method, path, b, jsonHeader())
}

// statusOf returns the HTTP status of a GET.
func statusOf(t *testing.T, path string) int {
	t.Helper()
	resp, _ := send(t, http.MethodGet, path, nil, nil)
	return resp.StatusCode
}

// decodeOK fails the test unless the reply is 200, then decodes it into out.
// useNumber keeps JSON numbers as written.
func decodeOK(t *testing.T, method, path string, resp *http.Response, b []byte, out any, useNumber bool) {
	t.Helper()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s %s -> %d: %s", method, path, resp.StatusCode, b)
	}
	d := json.NewDecoder(bytes.NewReader(b))
	if useNumber {
		d.UseNumber()
	}
	if err := d.Decode(out); err != nil {
		t.Fatalf("decode %s %s: %v", method, path, err)
	}
}

// do sends an optional JSON body (gzip-compressed when gz) and decodes the JSON
// response into out.
func do(t *testing.T, method, path string, body any, gz bool, out any) {
	t.Helper()
	h := jsonHeader()
	var raw []byte
	if body != nil {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		if gz {
			var buf bytes.Buffer
			zw := gzip.NewWriter(&buf)
			zw.Write(raw)
			zw.Close()
			raw = buf.Bytes()
			h.Set("Content-Encoding", "gzip")
		}
	}
	resp, b := send(t, method, path, raw, h)
	decodeOK(t, method, path, resp, b, out, false)
}

func post(t *testing.T, path string, body any, out any) { do(t, "POST", path, body, false, out) }
func get(t *testing.T, path string, out any)            { do(t, "GET", path, nil, false, out) }

// postNumbers posts body and decodes the JSON reply keeping numbers as
// written (BigDecimal bin bounds keep their scale).
func postNumbers(t *testing.T, path string, body any, out any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, b := send(t, http.MethodPost, path, raw, jsonHeader())
	decodeOK(t, http.MethodPost, path, resp, b, out, true)
}

// list requests a JSON list and decodes its items as T.
func list[T any](t *testing.T, method, path, body string) []T {
	t.Helper()
	resp, b := rawDo(t, method, path, body)
	var out []T
	decodeOK(t, method, path, resp, b, &out, false)
	return out
}

// listOf requests a list and decodes it.
func listOf(t *testing.T, method, path, body string) []map[string]any {
	t.Helper()
	return list[map[string]any](t, method, path, body)
}

// rawList requests a list and returns its items as raw JSON.
func rawList(t *testing.T, method, path, body string) []json.RawMessage {
	t.Helper()
	return list[json.RawMessage](t, method, path, body)
}

// javaKey is Java's uniqueSampleKey / uniquePatientKey: base64 (no padding)
// of "<id>:<studyId>".
func javaKey(id, study string) string {
	return base64.RawStdEncoding.EncodeToString([]byte(id + ":" + study))
}

// wantNear reports an error unless got equals want within the relative
// tolerance rel.
func wantNear(t *testing.T, what string, got, want, rel float64) {
	t.Helper()
	if got != want && math.Abs(got-want) > rel*math.Abs(want) {
		t.Errorf("%s = %v, want %v (rel %g)", what, got, want, rel)
	}
}
