package api

import (
	"bytes"
	"encoding/json"
	"maps"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// selfPagedPaths answer with their own paged envelope (not a bare JSON array),
// so the generic paging below leaves their query params untouched.
var selfPagedPaths = map[string]bool{
	"/api/clinical-data-table/fetch": true,
}

// pageList implements the pageSize / pageNumber / sortBy / direction query
// params of the Java list endpoints for every handler returning a JSON array:
// the array is stably sorted by the sortBy field (ASC unless direction=DESC)
// and then sliced to the requested page. pageSize / pageNumber are removed
// from the request before the handler runs so handlers return the full list.
// META requests pass through: their total-count covers all items.
func pageList(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		sizeParam, numberParam, sortBy := q.Get("pageSize"), q.Get("pageNumber"), q.Get("sortBy")
		if (sizeParam == "" && numberParam == "" && sortBy == "") || selfPagedPaths[r.URL.Path] ||
			strings.EqualFold(q.Get("projection"), "META") {
			next.ServeHTTP(w, r)
			return
		}
		size, sizeErr := strconv.Atoi(sizeParam)
		number, _ := strconv.Atoi(numberParam)
		q.Del("pageSize")
		q.Del("pageNumber")
		r2 := r.Clone(r.Context())
		r2.URL.RawQuery = q.Encode()

		rec := &bufferedResponse{header: http.Header{}, status: http.StatusOK}
		next.ServeHTTP(rec, r2)
		maps.Copy(w.Header(), rec.header)
		var items []json.RawMessage
		if rec.status != http.StatusOK || json.Unmarshal(rec.body.Bytes(), &items) != nil {
			w.WriteHeader(rec.status)
			w.Write(rec.body.Bytes())
			return
		}
		if sortBy != "" {
			sortItems(items, sortBy, strings.EqualFold(q.Get("direction"), "DESC"))
		}
		if sizeErr == nil && size >= 0 {
			if number < 0 {
				number = 0
			}
			start := min(number*size, len(items))
			items = items[start:min(start+size, len(items))]
		}
		var buf bytes.Buffer
		buf.WriteByte('[')
		for i, it := range items {
			if i > 0 {
				buf.WriteByte(',')
			}
			buf.Write(it)
		}
		buf.WriteString("]\n")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Del("Content-Length")
		w.WriteHeader(http.StatusOK)
		w.Write(buf.Bytes())
	})
}

// sortItems stably sorts JSON objects by one top-level field: numbers compare
// numerically, everything else as strings; a missing or null field sorts
// first (MySQL NULL ordering).
func sortItems(items []json.RawMessage, field string, desc bool) {
	type keyed struct {
		raw   json.RawMessage
		null  bool
		num   *float64
		str   string
		index int
	}
	ks := make([]keyed, len(items))
	for i, it := range items {
		k := keyed{raw: it, index: i, null: true}
		var obj map[string]json.RawMessage
		if json.Unmarshal(it, &obj) == nil {
			if v, ok := obj[field]; ok && string(v) != "null" {
				k.null = false
				var f float64
				var s string
				switch {
				case json.Unmarshal(v, &f) == nil:
					k.num = &f
				case json.Unmarshal(v, &s) == nil:
					k.str = s
				default:
					k.str = string(v)
				}
			}
		}
		ks[i] = k
	}
	less := func(a, b keyed) bool {
		if a.null || b.null {
			return a.null && !b.null
		}
		if a.num != nil && b.num != nil {
			return *a.num < *b.num
		}
		return a.str < b.str
	}
	sort.SliceStable(ks, func(i, j int) bool {
		if desc {
			return less(ks[j], ks[i])
		}
		return less(ks[i], ks[j])
	})
	for i, k := range ks {
		items[i] = k.raw
	}
}
