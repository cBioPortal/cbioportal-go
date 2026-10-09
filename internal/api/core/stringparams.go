package core

import (
	"net/http"
	"net/url"
	"strings"
)

// springStringParams are the query parameters the Java controllers declare
// as a single String. Spring binds a repeated String parameter to its
// values joined with commas (ArrayToStringConverter), so
// attributeId=A&attributeId=B asks for the attribute "A,B"; parameters of
// other types take the first value, as url.Values.Get does.
var springStringParams = map[string]bool{
	"attributeId": true, "categoricalAttributeId": true, "geneticProfileId": true, "keyword": true,
	"molecularProfileIdA": true, "molecularProfileIdB": true, "numericalAttributeId": true, "sampleId": true,
	"sampleListId": true, "searchTerm": true, "xAxisAttributeId": true, "yAxisAttributeId": true,
}

// SpringStringParams rewrites a request's repeated String parameters
// (springStringParams) to one parameter holding the values joined with
// commas, where the first of them stood. The rest of the query is left as
// it is.
func SpringStringParams(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if q, ok := joinRepeatedStringParams(r.URL.RawQuery); ok {
			r2 := r.Clone(r.Context())
			r2.URL.RawQuery = q
			r = r2
		}
		next.ServeHTTP(w, r)
	})
}

// joinRepeatedStringParams returns raw with each repeated String parameter
// joined, and whether it changed anything.
func joinRepeatedStringParams(raw string) (string, bool) {
	if raw == "" {
		return raw, false
	}
	pairs := strings.Split(raw, "&")
	values := map[string][]string{}
	for _, p := range pairs {
		k, v, _ := strings.Cut(p, "=")
		key, err := url.QueryUnescape(k)
		if err != nil || !springStringParams[key] {
			continue
		}
		val, err := url.QueryUnescape(v)
		if err != nil {
			return raw, false
		}
		values[key] = append(values[key], val)
	}
	changed := false
	for _, vs := range values {
		if len(vs) > 1 {
			changed = true
		}
	}
	if !changed {
		return raw, false
	}
	out := make([]string, 0, len(pairs))
	done := map[string]bool{}
	for _, p := range pairs {
		k, _, _ := strings.Cut(p, "=")
		key, _ := url.QueryUnescape(k)
		vs := values[key]
		if len(vs) < 2 {
			out = append(out, p)
			continue
		}
		if !done[key] {
			done[key] = true
			out = append(out, url.QueryEscape(key)+"="+url.QueryEscape(strings.Join(vs, ",")))
		}
	}
	return strings.Join(out, "&"), true
}
