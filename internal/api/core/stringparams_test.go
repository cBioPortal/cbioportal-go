package core

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestJoinRepeatedStringParams(t *testing.T) {
	for _, c := range []struct {
		in, want string
		changed  bool
	}{
		{"", "", false},
		{"attributeId=A", "attributeId=A", false},
		{"attributeId=A&attributeId=B&pageSize=2", "attributeId=A%2CB&pageSize=2", true},
		{"pageSize=1&attributeId=A&projection=ID&attributeId=B%20C", "pageSize=1&attributeId=A%2CB+C&projection=ID", true},
		{"projection=ID&projection=SUMMARY", "projection=ID&projection=SUMMARY", false},
		{"keyword=a&sampleListId=x&keyword=b&sampleListId=y", "keyword=a%2Cb&sampleListId=x%2Cy", true},
	} {
		got, changed := joinRepeatedStringParams(c.in)
		if got != c.want || changed != c.changed {
			t.Errorf("%q: %q %v, want %q %v", c.in, got, changed, c.want, c.changed)
		}
	}
}

func TestSpringStringParams(t *testing.T) {
	var seen string
	h := SpringStringParams(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.Query().Get("attributeId") + "|" + r.URL.Query().Get("projection")
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET",
		"/api/studies/s/clinical-data?attributeId=A&attributeId=B&projection=ID&projection=SUMMARY", nil))
	if seen != "A,B|ID" {
		t.Errorf("handler saw %q, want the joined String parameter and the first projection", seen)
	}
}
