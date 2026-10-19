package molecular

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
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
		core.WriteJSON(want, rows[:n])
		got := httptest.NewRecorder()
		a := core.NewJSONArray(got, req)
		for _, r := range rows[:n] {
			if err := a.Add(r); err != nil {
				t.Fatal(err)
			}
		}
		a.Finish(nil)
		if got.Body.String() != want.Body.String() || got.Header().Get("Content-Type") != want.Header().Get("Content-Type") {
			t.Errorf("%d rows: %q (%s), want %q (%s)", n, got.Body, got.Header().Get("Content-Type"), want.Body, want.Header().Get("Content-Type"))
		}
	}
}

func TestJSONArrayErrors(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/molecular-data", nil)
	// Before the first element: the error response Fail writes.
	got, want := httptest.NewRecorder(), httptest.NewRecorder()
	core.NewJSONArray(got, req).Finish(errors.New("boom"))
	core.Fail(want, req, errors.New("boom"))
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
	a := core.NewJSONArray(httptest.NewRecorder(), req)
	a.Add(1)
	a.Finish(errors.New("boom"))
}
