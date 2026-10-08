package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJavaFirewall(t *testing.T) {
	h := JavaFirewall(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) }))
	serve := func(method, target, accept string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/", nil)
		r.RequestURI = target
		if accept != "" {
			r.Header.Set("Accept", accept)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for target, want := range map[string]string{
		"/api/genes/TP53":           "pass",
		"/api/genes/TP53?x=a;b":     "pass",
		"/api/genes/TP53?x=%zz":     "pass",
		"/api/genes/.TP53":          "pass",
		"/api/genes/TP%7C53":        "pass",
		"/api/genes/TP%C3%A953":     "pass",
		"http://h/api/genes/TP53":   "pass",
		"*":                         "pass",
		"/api/genes/lookup/ACSL1;":  "spring",
		"/api/genes/TP%3b53":        "spring",
		"/api/genes//TP53":          "spring",
		"/api/genes/../genes/TP53":  "spring",
		"/api/genes/.":              "spring",
		"/api/genes/TP%2E53":        "spring",
		"/api/genes/TP%2553":        "spring",
		"/api/genes/TP%0A53":        "spring",
		"/api/genes/TP%E2%80%A953":  "spring",
		"/api/..;/sendgrid/.env":    "spring",
		"/api/genes/TP%2F53":        "tomcat",
		"/api/genes/TP%5c53":        "tomcat",
		"/api/genes/TP%0053":        "tomcat",
		"/api/genes/TP%FF53":        "tomcat",
		"/api/genes/TP%2":           "tomcat",
		"/api/genes/TP|53":          "tomcat",
		"/api/genes/TP53?x={":       "tomcat",
		"/../api/genes/TP53":        "tomcat",
		"/..;/api/genes/TP53":       "tomcat",
		"/api/%2e%2e/%2e%2e/x":      "tomcat",
		"http://h/api/genes/TP53;x": "spring",
	} {
		w := serve("GET", target, "")
		got := "pass"
		switch {
		case w.Code == http.StatusTeapot:
		case w.Code == 400 && strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") && w.Body.String() == tomcatBadRequestPage:
			got = "tomcat"
		case w.Code == 400 && w.Header().Get("Content-Type") == "application/json":
			got = "spring"
			var e struct {
				Status int
				Error  string
				Path   string
			}
			json.Unmarshal(w.Body.Bytes(), &e)
			path, _, _ := strings.Cut(strings.TrimPrefix(target, "http://h"), "?")
			if e.Status != 400 || e.Error != "Bad Request" || e.Path != path || strings.HasSuffix(w.Body.String(), "\n") {
				t.Errorf("%s: body %s", target, w.Body)
			}
		default:
			got = w.Header().Get("Content-Type") + " " + w.Body.String()
		}
		if got != want {
			t.Errorf("%s: %s, want %s", target, got, want)
		}
	}
	for accept, want := range map[string]string{
		"":                                  "application/json",
		"*/*":                               "application/json",
		"application/json, text/plain, */*": "application/json",
		"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8": "text/html;charset=UTF-8",
		"TEXT/*":    "text/html;charset=UTF-8",
		"image/png": "",
	} {
		if w := serve("GET", "/x;", accept); w.Code != 400 || w.Header().Get("Content-Type") != want {
			t.Errorf("Accept %q: %d %q, want %q", accept, w.Code, w.Header().Get("Content-Type"), want)
		}
	}
	if w := serve("TRACE", "/api/genes/TP53", ""); w.Code != 400 || w.Body.Len() != 0 || w.Header().Get("Allow") == "" {
		t.Errorf("TRACE: %d %q %v", w.Code, w.Body, w.Header())
	}
	if w := serve("PROPFIND", "/api/genes/TP53", ""); w.Code != 400 || w.Body.Len() != 0 {
		t.Errorf("PROPFIND: %d %q", w.Code, w.Body)
	}
}
