package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// A failing ClickHouse query answers with Spring Boot's generic 500, as
// Java does: the exception text (SQL, database names) goes to the server log
// and never into the response.
func TestFailHidesDatabaseErrors(t *testing.T) {
	const exception = "Code: 81. DB::Exception: Database dev_cbioportal_hub_go_ref does not exist. " +
		"Maybe you meant dev_cbioportal_public_go_blue?. (UNKNOWN_DATABASE) SELECT type_of_cancer_id FROM cancer_type"
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("X-ClickHouse-Exception-Code", "81")
		http.Error(w, exception, http.StatusNotFound)
	}))
	defer fake.Close()

	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	defer slog.SetDefault(prev)

	h := NewRouter(&State{CH: ch.New(fake.URL, "dev_cbioportal_hub_go_ref")})
	for _, path := range []string{"/api/cancer-types", "/api/studies?projection=META"} {
		logBuf.Reset()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Accept", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		body := rec.Body.String()
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("%s: status %d, body %q", path, rec.Code, body)
		}
		var got struct {
			Timestamp int64  `json:"timestamp"`
			Status    int    `json:"status"`
			Error     string `json:"error"`
			Path      string `json:"path"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("%s: body %q is not Spring's error JSON: %v", path, body, err)
		}
		wantPath, _, _ := strings.Cut(path, "?")
		if got.Status != 500 || got.Error != "Internal Server Error" || got.Path != wantPath || got.Timestamp == 0 {
			t.Errorf("%s: body %q", path, body)
		}
		for _, leak := range []string{"dev_cbioportal", "SELECT", "Exception", "cancer_type"} {
			if strings.Contains(body, leak) {
				t.Errorf("%s: response leaks %q: %q", path, leak, body)
			}
		}
		logged := logBuf.String()
		if !strings.Contains(logged, "dev_cbioportal_hub_go_ref does not exist") ||
			!strings.Contains(logged, "path="+wantPath) || !strings.Contains(logged, "method=GET") {
			t.Errorf("%s: error not logged: %q", path, logged)
		}
	}
}
