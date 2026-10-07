package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJSONArrayMatchesWriteJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/molecular-data", nil)
	e := int64(7157)
	rows := []NumericGeneMolecularData{
		{UniqueSampleKey: "a<b>&", SampleID: "S1", Value: json.Number("1.5"), EntrezGeneID: &e},
		{SampleID: "S2 \"x\"", Value: json.Number("-0.25"), Gene: &Gene{HugoGeneSymbol: "TP53"}},
	}
	for _, n := range []int{0, 1, 2} {
		want := httptest.NewRecorder()
		writeJSON(want, rows[:n])
		got := httptest.NewRecorder()
		a := newJSONArray(got, req)
		for _, r := range rows[:n] {
			if err := a.add(r); err != nil {
				t.Fatal(err)
			}
		}
		a.finish(nil)
		if got.Body.String() != want.Body.String() || got.Header().Get("Content-Type") != want.Header().Get("Content-Type") {
			t.Errorf("%d rows: %q (%s), want %q (%s)", n, got.Body, got.Header().Get("Content-Type"), want.Body, want.Header().Get("Content-Type"))
		}
	}
}

func TestJSONArrayErrors(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/molecular-data", nil)
	// Before the first element: the error response fail writes.
	got, want := httptest.NewRecorder(), httptest.NewRecorder()
	newJSONArray(got, req).finish(errors.New("boom"))
	fail(want, req, errors.New("boom"))
	if got.Code != http.StatusInternalServerError || got.Code != want.Code ||
		got.Header().Get("Content-Type") != want.Header().Get("Content-Type") || strings.Contains(got.Body.String(), "boom") {
		t.Errorf("got %d %q, want %d %q", got.Code, got.Body, want.Code, want.Body)
	}
	// After it: the response is aborted.
	defer func() {
		if v := recover(); v != http.ErrAbortHandler {
			t.Errorf("recovered %v, want ErrAbortHandler", v)
		}
	}()
	a := newJSONArray(httptest.NewRecorder(), req)
	a.add(1)
	a.finish(errors.New("boom"))
}
