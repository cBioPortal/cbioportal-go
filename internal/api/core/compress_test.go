package core

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGzipResponse(t *testing.T) {
	body := strings.Repeat(`{"sampleId":"S1"},`, 1000)
	h := GzipResponse(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/json":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Length", "999")
			io.WriteString(w, body)
		case "/png":
			w.Header().Set("Content-Type", "image/png")
			io.WriteString(w, "png")
		case "/empty":
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	get := func(path, enc string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		if enc != "" {
			req.Header.Set("Accept-Encoding", enc)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	rec := get("/json", "gzip, deflate")
	if rec.Header().Get("Content-Encoding") != "gzip" || rec.Header().Get("Content-Length") != "" {
		t.Fatalf("headers = %v", rec.Header())
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(zr)
	if string(got) != body {
		t.Fatalf("decompressed body mismatch (%d bytes)", len(got))
	}

	if rec := get("/json", ""); rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != body {
		t.Fatal("compressed without Accept-Encoding")
	}
	if rec := get("/png", "gzip"); rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != "png" {
		t.Fatal("compressed an image")
	}
	if rec := get("/empty", "gzip"); rec.Code != http.StatusNoContent || rec.Header().Get("Content-Encoding") != "" || rec.Body.Len() != 0 {
		t.Fatalf("204 handling: code=%d headers=%v", rec.Code, rec.Header())
	}
}
