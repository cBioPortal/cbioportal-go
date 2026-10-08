package studies

import (
	"net/http"
	"net/url"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
)

// StudiesMeta serves GET /api/studies/meta (ColumnStoreStudyController
// .getAllStudiesMeta): an empty body with the number of studies matching
// keyword / sortBy / direction in the total-count and X-Total-Count headers.
// An unknown sortBy or direction is a 400, as Spring converts them.
func (s Server) StudiesMeta(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if msg := core.CheckParams(core.StudiesMetaParams, q); msg != "" {
		core.WriteError(w, http.StatusBadRequest, msg)
		return
	}
	meta := url.Values{"projection": {"META"}}
	for _, k := range []string{"keyword", "sortBy", "direction"} {
		if v, ok := q[k]; ok {
			meta[k] = v
		}
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/api/studies"
	r2.URL.RawPath = ""
	r2.URL.RawQuery = meta.Encode()
	rec := core.NewBufferedResponse()
	s.studies(rec, r2)
	if rec.Status != http.StatusOK {
		w.WriteHeader(rec.Status)
		w.Write(rec.Body.Bytes())
		return
	}
	n := rec.Header().Get("total-count")
	if n == "" {
		n = "0"
	}
	w.Header().Set("total-count", n)
	w.Header().Set("X-Total-Count", n)
	w.WriteHeader(http.StatusOK)
}
